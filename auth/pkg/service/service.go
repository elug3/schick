package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/elug3/dupli1/auth/pkg/autherrors"
	"github.com/elug3/dupli1/auth/pkg/domain"
	"github.com/elug3/dupli1/auth/pkg/ports"
	"github.com/elug3/dupli1/shared/pkg/events"
	"github.com/elug3/dupli1/shared/pkg/outbox"
	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/rs/zerolog"
)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

const (
	userRegisteredSubject = "user.registered"
	maxFailedAttempts     = 5
)

// Service holds all auth business logic.
type Service struct {
	userRepo           ports.UserRepository
	tokenGen           ports.TokenGenerator
	refreshTokenGen    ports.TokenGenerator
	sessionStore       ports.SessionStore
	refreshTokenExpiry time.Duration
	eventPublisher     ports.EventPublisher
	outboxDrainer      *outbox.Drainer
	logger             zerolog.Logger
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithRefreshTokenGen sets the token generator and expiry used for refresh tokens.
func WithRefreshTokenGen(gen ports.TokenGenerator, expiry time.Duration) ServiceOption {
	return func(s *Service) {
		s.refreshTokenGen = gen
		s.refreshTokenExpiry = expiry
	}
}

// WithSessionStore enables refresh-token revocation via a persistent store.
func WithSessionStore(store ports.SessionStore) ServiceOption {
	return func(s *Service) {
		s.sessionStore = store
	}
}

// WithEventPublisher sets the integration-event publisher.
func WithEventPublisher(pub ports.EventPublisher) ServiceOption {
	return func(s *Service) {
		s.eventPublisher = pub
	}
}

// WithOutboxDrainer publishes auth_outbox rows (user.deleted) after a
// transactional delete. Production always passes this; tests without a
// Postgres repo omit it and use the in-process publisher instead.
func WithOutboxDrainer(d *outbox.Drainer) ServiceOption {
	return func(s *Service) {
		s.outboxDrainer = d
	}
}

// WithLogger sets the logger used for background/best-effort failures.
func WithLogger(logger zerolog.Logger) ServiceOption {
	return func(s *Service) {
		s.logger = logger
	}
}

// NewService creates a new auth Service.
func NewService(userRepo ports.UserRepository, tokenGen ports.TokenGenerator, opts ...ServiceOption) *Service {
	s := &Service{userRepo: userRepo, tokenGen: tokenGen}
	for _, o := range opts {
		o(s)
	}
	if s.refreshTokenGen == nil {
		s.refreshTokenGen = tokenGen
	}
	return s
}

// StartOutboxWorker periodically publishes pending auth_outbox rows.
func (s *Service) StartOutboxWorker(ctx context.Context, interval time.Duration) {
	if s.outboxDrainer == nil {
		return
	}
	s.outboxDrainer.StartWorker(ctx, interval)
}

type userDeletedOutbox interface {
	DeleteAndEnqueue(ctx context.Context, userID, subject string, payload []byte) error
}

type userRegisteredEvent struct {
	EventType   string    `json:"event_type"`
	UserID      string    `json:"user_id"`
	Email       string    `json:"email"`
	AccountType string    `json:"account_type"`
	Occurred    time.Time `json:"occurred_at"`
}

// Register creates a new user. Permissions default to empty for storefront customers.
func (s *Service) Register(ctx context.Context, email, password, accountType string, userPermissions ...string) (*domain.User, error) {
	if !strings.Contains(email, "@") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return nil, autherrors.ErrInvalidEmail
	}
	if len(password) < 8 {
		return nil, autherrors.ErrWeakPassword
	}
	if accountType == "" {
		accountType = domain.DefaultAccountType
	}
	// Do not NormalizeAccountType here — "admin" must be rejected (use manager).
	if !domain.ValidAccountType(accountType) {
		return nil, autherrors.ErrInvalidAccountType
	}
	perms := permissions.Dedupe(userPermissions)
	u, err := domain.NewUser(newID(), email, password, accountType, perms...)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	if err := s.userRepo.Save(ctx, u); err != nil {
		return nil, fmt.Errorf("save user: %w", err)
	}
	// The account is already durable and usable at this point. user.registered has no
	// critical consumer, so a broker outage must not undo a successful registration.
	if err := s.publishUserRegistered(ctx, u); err != nil {
		s.logger.Error().
			Str("event", "user_registered_publish_failed").
			Str("user_id", u.ID).
			Err(err).
			Msg("registration succeeded but user.registered publish failed")
	}
	return u, nil
}

// Login validates credentials, tracks failed attempts, and returns a refresh token.
func (s *Service) Login(ctx context.Context, email, password string) (string, error) {
	u, err := s.userRepo.FindByEmail(ctx, email)
	if err != nil {
		return "", fmt.Errorf("find user: %w", err)
	}
	if u == nil {
		// Run a bcrypt comparison anyway so this path takes about as long as
		// the wrong-password path below — otherwise the timing difference
		// reveals whether the email is registered.
		domain.ValidateDummyPassword(password)
		return "", autherrors.ErrInvalidCredentials
	}

	if u.IsLocked() {
		return "", autherrors.ErrAccountLocked
	}
	// A lock past AccountLockDuration no longer reports as locked above, but
	// the stale LockedAt/FailedLoginAttempts still need clearing so this
	// attempt is scored fresh rather than as an extension of the old streak.
	if u.LockedAt != nil {
		u.Unlock()
		_ = s.userRepo.Save(ctx, u)
	}

	if !u.IsActive {
		return "", autherrors.ErrAccountDeactivated
	}

	if !u.ValidatePassword(password) {
		// Admin / owner accounts are never locked out (IsLockExempt).
		if u.IsLockExempt() {
			if u.LockedAt != nil {
				u.LockedAt = nil
				_ = s.userRepo.Save(ctx, u)
			}
		} else {
			u.FailedLoginAttempts++
			if u.FailedLoginAttempts >= maxFailedAttempts {
				u.Lock()
			}
			_ = s.userRepo.Save(ctx, u)
		}
		return "", autherrors.ErrInvalidCredentials
	}

	if u.FailedLoginAttempts > 0 || u.LockedAt != nil {
		u.Unlock()
		_ = s.userRepo.Save(ctx, u)
	}

	token, err := s.refreshTokenGen.Generate(ctx, u.ID, nil)
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	if s.sessionStore != nil {
		if err := s.sessionStore.Set(ctx, token, u.ID, s.refreshTokenExpiry); err != nil {
			return "", fmt.Errorf("store session: %w", err)
		}
	}

	return token, nil
}

// Logout revokes a refresh token from the session store.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if s.sessionStore == nil {
		return nil
	}
	return s.sessionStore.Delete(ctx, refreshToken)
}

// Refresh validates a refresh token and issues a new short-lived access
// token. When a session store is backing refresh tokens, the refresh token
// itself is rotated: the caller gets a new one back and the one it presented
// stops working immediately, so a leaked-then-superseded refresh token can't
// be replayed indefinitely — reuse of an already-rotated token now fails
// with ErrInvalidToken instead of silently minting another access token.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (accessToken string, rotatedRefreshToken string, err error) {
	claims, err := s.refreshTokenGen.Validate(ctx, refreshToken)
	if err != nil {
		return "", "", err
	}

	if s.sessionStore != nil {
		if _, err := s.sessionStore.Get(ctx, refreshToken); err != nil {
			if errors.Is(err, ports.ErrSessionNotFound) {
				return "", "", autherrors.ErrInvalidToken
			}
			return "", "", fmt.Errorf("session store: %w", err)
		}
	}

	u, err := s.userRepo.FindByID(ctx, claims.UserID)
	if err != nil {
		return "", "", fmt.Errorf("find user: %w", err)
	}
	if u == nil {
		return "", "", autherrors.ErrUserNotFound
	}
	if !u.IsActive {
		return "", "", autherrors.ErrAccountDeactivated
	}
	if u.IsLocked() {
		return "", "", autherrors.ErrAccountLocked
	}

	newAccessToken, err := s.tokenGen.Generate(ctx, u.ID, u.Permissions)
	if err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}

	rotatedRefreshToken = refreshToken
	if s.sessionStore != nil {
		newRefreshToken, err := s.refreshTokenGen.Generate(ctx, u.ID, nil)
		if err != nil {
			return "", "", fmt.Errorf("generate refresh token: %w", err)
		}
		if err := s.sessionStore.Rotate(ctx, refreshToken, newRefreshToken, u.ID, s.refreshTokenExpiry); err != nil {
			if errors.Is(err, ports.ErrSessionNotFound) {
				return "", "", autherrors.ErrInvalidToken
			}
			return "", "", fmt.Errorf("rotate session: %w", err)
		}
		rotatedRefreshToken = newRefreshToken
	}

	return newAccessToken, rotatedRefreshToken, nil
}

// GetMe validates an access token and returns the authenticated user.
// Every RequireAuth-protected request goes through this, so a deactivated or
// locked account stops working immediately instead of only on its next
// login/refresh — its already-issued access tokens are otherwise still good
// for a full TokenExpiry window.
func (s *Service) GetMe(ctx context.Context, accessToken string) (*domain.User, error) {
	claims, err := s.tokenGen.Validate(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	u, err := s.userRepo.FindByID(ctx, claims.UserID)
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	if u == nil {
		return nil, autherrors.ErrUserNotFound
	}
	if !u.IsActive {
		return nil, autherrors.ErrAccountDeactivated
	}
	if u.IsLocked() {
		return nil, autherrors.ErrAccountLocked
	}

	return u, nil
}

// FindUserByID returns a user by ID.
func (s *Service) FindUserByID(ctx context.Context, userID string) (*domain.User, error) {
	u, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	if u == nil {
		return nil, autherrors.ErrUserNotFound
	}
	return u, nil
}

// HasOwner reports whether an owner account (* permission) already exists.
func (s *Service) HasOwner(ctx context.Context) (bool, error) {
	users, err := s.userRepo.ListAll(ctx)
	if err != nil {
		return false, fmt.Errorf("list users: %w", err)
	}
	for _, u := range users {
		if permissions.Has(u.Permissions, permissions.All) {
			return true, nil
		}
	}
	return false, nil
}

// SetUserPermissions replaces the permission list for the given user.
func (s *Service) SetUserPermissions(ctx context.Context, userID string, perms []string, accountType string) (*domain.User, error) {
	if err := permissions.Validate(perms); err != nil {
		return nil, autherrors.ErrInvalidPermission
	}
	u, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	if u == nil {
		return nil, autherrors.ErrUserNotFound
	}
	if accountType != "" {
		// Reject legacy "admin" — admin is a permission tier, not account_type.
		if !domain.ValidAccountType(accountType) {
			return nil, autherrors.ErrInvalidAccountType
		}
		u.AccountType = accountType
	}
	u.SetPermissions(perms)
	if err := s.userRepo.Save(ctx, u); err != nil {
		return nil, fmt.Errorf("save user: %w", err)
	}
	return u, nil
}

// UpdateUserPassword hashes newPassword and persists it for the given user.
func (s *Service) UpdateUserPassword(ctx context.Context, userID, newPassword string) error {
	if len(newPassword) < 8 {
		return autherrors.ErrWeakPassword
	}
	u, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if u == nil {
		return autherrors.ErrUserNotFound
	}
	if err := u.UpdatePassword(newPassword); err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.userRepo.Save(ctx, u)
}

// SetUserStatus sets the active/inactive status for the given user.
func (s *Service) SetUserStatus(ctx context.Context, userID string, isActive bool) (*domain.User, error) {
	u, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	if u == nil {
		return nil, autherrors.ErrUserNotFound
	}
	u.SetActive(isActive)
	if err := s.userRepo.Save(ctx, u); err != nil {
		return nil, fmt.Errorf("save user: %w", err)
	}
	return u, nil
}

// ListUsers returns all users. The caller is responsible for authorization.
func (s *Service) ListUsers(ctx context.Context) ([]*domain.User, error) {
	users, err := s.userRepo.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// DeleteUser permanently removes a user and records user.deleted so
// downstream services (profile) can drop owned PII.
//
// When the repo supports a transactional outbox (Postgres), the user row and
// the outbox event are written in one transaction: the account is gone if and
// only if the event is durable. The drain worker then publishes to NATS.
// Without that (tests / in-memory), the event is published first and the row
// is deleted only after the broker accepts it — a publish failure leaves the
// account in place rather than orphaning profile PII forever.
func (s *Service) DeleteUser(ctx context.Context, userID string) error {
	u, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if u == nil {
		return autherrors.ErrUserNotFound
	}

	payload, err := json.Marshal(events.UserDeletedEvent{
		EventType: events.UserDeleted,
		UserID:    userID,
		Occurred:  time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("marshal user.deleted: %w", err)
	}

	if d, ok := s.userRepo.(userDeletedOutbox); ok {
		if err := d.DeleteAndEnqueue(ctx, userID, events.UserDeleted, payload); err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		if s.outboxDrainer != nil {
			s.outboxDrainer.TryDrain(ctx)
		}
		return nil
	}

	if err := s.publishUserDeleted(ctx, userID); err != nil {
		return fmt.Errorf("publish user.deleted: %w", err)
	}
	if err := s.userRepo.Delete(ctx, userID); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

func (s *Service) publishUserRegistered(ctx context.Context, u *domain.User) error {
	if s.eventPublisher == nil {
		return nil
	}

	return s.eventPublisher.Publish(ctx, userRegisteredSubject, userRegisteredEvent{
		EventType:   userRegisteredSubject,
		UserID:      u.ID,
		Email:       u.Email,
		AccountType: u.AccountType,
		Occurred:    time.Now().UTC(),
	})
}

func (s *Service) publishUserDeleted(ctx context.Context, userID string) error {
	if s.eventPublisher == nil {
		return nil
	}
	return s.eventPublisher.Publish(ctx, events.UserDeleted, events.UserDeletedEvent{
		EventType: events.UserDeleted,
		UserID:    userID,
		Occurred:  time.Now().UTC(),
	})
}
