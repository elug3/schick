package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/elug3/dupli1/auth/pkg/autherrors"
	"github.com/elug3/dupli1/auth/pkg/domain"
	memoryinfra "github.com/elug3/dupli1/auth/pkg/infra/memory"
	"github.com/elug3/dupli1/auth/pkg/ports"
	"github.com/elug3/dupli1/shared/pkg/permissions"
)

type fakeUserRepository struct {
	saved *domain.User
}

func (r *fakeUserRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	return nil, nil
}

func (r *fakeUserRepository) FindByID(ctx context.Context, id string) (*domain.User, error) {
	return nil, nil
}

func (r *fakeUserRepository) Save(ctx context.Context, u *domain.User) error {
	u.ID = "user-123"
	r.saved = u
	return nil
}

func (r *fakeUserRepository) Delete(ctx context.Context, id string) error {
	return nil
}

func (r *fakeUserRepository) ListAll(ctx context.Context) ([]*domain.User, error) {
	return nil, nil
}

type fakeTokenGenerator struct{}

func (g fakeTokenGenerator) Generate(ctx context.Context, userID string, userPermissions []string, email string) (string, error) {
	return "token", nil
}

func (g fakeTokenGenerator) Validate(ctx context.Context, token string) (ports.Claims, error) {
	return ports.Claims{UserID: "user-123"}, nil
}

type capturingTokenGenerator struct {
	capturedUserID      string
	capturedPermissions []string
}

func (g *capturingTokenGenerator) Generate(ctx context.Context, userID string, userPermissions []string, email string) (string, error) {
	g.capturedUserID = userID
	g.capturedPermissions = append([]string(nil), userPermissions...)
	return "token", nil
}

func (g *capturingTokenGenerator) Validate(ctx context.Context, token string) (ports.Claims, error) {
	return ports.Claims{UserID: g.capturedUserID}, nil
}

type stubUserRepository struct {
	user *domain.User
}

func (r *stubUserRepository) FindByEmail(_ context.Context, _ string) (*domain.User, error) {
	return r.user, nil
}

func (r *stubUserRepository) FindByID(_ context.Context, _ string) (*domain.User, error) {
	return r.user, nil
}

func (r *stubUserRepository) Save(_ context.Context, u *domain.User) error {
	if u.ID == "" {
		u.ID = "user-999"
	}
	return nil
}

func (r *stubUserRepository) Delete(_ context.Context, _ string) error { return nil }

func (r *stubUserRepository) ListAll(_ context.Context) ([]*domain.User, error) { return nil, nil }

type recordedEventPublisher struct {
	subject string
	event   any
}

func (p *recordedEventPublisher) Publish(ctx context.Context, subject string, event any) error {
	p.subject = subject
	p.event = event
	return nil
}

func TestRegisterPublishesUserRegisteredEvent(t *testing.T) {
	repo := &fakeUserRepository{}
	publisher := &recordedEventPublisher{}
	svc := NewService(repo, fakeTokenGenerator{}, WithEventPublisher(publisher))

	user, err := svc.Register(t.Context(), "customer@example.com", "supersecret", domain.AccountTypeCustomer)
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if repo.saved != user {
		t.Fatalf("Register did not save returned user")
	}
	if publisher.subject != userRegisteredSubject {
		t.Fatalf("published subject = %q, want %q", publisher.subject, userRegisteredSubject)
	}

	event, ok := publisher.event.(userRegisteredEvent)
	if !ok {
		t.Fatalf("published event type = %T, want userRegisteredEvent", publisher.event)
	}
	if event.UserID != "user-123" {
		t.Fatalf("event.UserID = %q, want user-123", event.UserID)
	}
	if event.Email != "customer@example.com" {
		t.Fatalf("event.Email = %q, want customer@example.com", event.Email)
	}
	if event.AccountType != domain.AccountTypeCustomer {
		t.Fatalf("event.AccountType = %q, want %q", event.AccountType, domain.AccountTypeCustomer)
	}
	if event.EventType != userRegisteredSubject {
		t.Fatalf("event.EventType = %q, want %q", event.EventType, userRegisteredSubject)
	}
	if event.Occurred.IsZero() {
		t.Fatalf("event.Occurred is zero")
	}

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal event returned error: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("Unmarshal event returned error: %v", err)
	}
	if _, ok := fields["password"]; ok {
		t.Fatalf("user registered event includes password")
	}
}

func TestLogin_RefreshTokenOmitsPermissions(t *testing.T) {
	user, _ := domain.NewUser("u-1", "user@example.com", "pass", domain.AccountTypeService,
		permissions.OrderShip, permissions.OrderStatusUpdate)
	repo := &stubUserRepository{user: user}
	gen := &capturingTokenGenerator{}
	svc := NewService(repo, gen)

	if _, err := svc.Login(t.Context(), "user@example.com", "pass"); err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
	if gen.capturedUserID != "u-1" {
		t.Fatalf("Generate userID = %q, want u-1", gen.capturedUserID)
	}
	if len(gen.capturedPermissions) != 0 {
		t.Fatalf("refresh Generate permissions = %v, want nil/empty", gen.capturedPermissions)
	}
}

func TestRefresh_FetchesFreshPermissionsFromDB(t *testing.T) {
	user, _ := domain.NewUser("u-2", "user@example.com", "pass", domain.AccountTypeManager,
		permissions.ExpandLegacyRoles([]string{permissions.RoleAdmin})...)
	repo := &stubUserRepository{user: user}
	gen := &capturingTokenGenerator{}
	svc := NewService(repo, gen)

	gen.capturedUserID = "u-2"

	if _, _, err := svc.Refresh(t.Context(), "any-token"); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if !permissions.Has(gen.capturedPermissions, permissions.AdminAll) {
		t.Fatalf("Generate permissions = %v, want admin wildcard", gen.capturedPermissions)
	}
}

type failingEventPublisher struct{}

func (p failingEventPublisher) Publish(context.Context, string, any) error {
	return errors.New("nats unavailable")
}

type deleteTrackingUserRepository struct {
	fakeUserRepository
	deleted []string
}

func (r *deleteTrackingUserRepository) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

func TestRegisterSucceedsWhenEventPublishFails(t *testing.T) {
	repo := &deleteTrackingUserRepository{}
	svc := NewService(repo, fakeTokenGenerator{}, WithEventPublisher(failingEventPublisher{}))

	user, err := svc.Register(t.Context(), "customer@example.com", "supersecret", domain.AccountTypeCustomer)
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if user == nil || user.ID != "user-123" {
		t.Fatalf("Register returned user = %+v, want saved user", user)
	}
	if repo.saved == nil {
		t.Fatal("Register did not save the user")
	}
	if len(repo.deleted) != 0 {
		t.Fatalf("Register deleted users %v after publish failure, want none", repo.deleted)
	}
}

func TestRegisterRejectsInvalidAccountType(t *testing.T) {
	svc := NewService(&fakeUserRepository{}, fakeTokenGenerator{})

	if _, err := svc.Register(t.Context(), "customer@example.com", "supersecret", "staff"); !errors.Is(err, autherrors.ErrInvalidAccountType) {
		t.Fatalf("got %v, want ErrInvalidAccountType", err)
	}
}

func TestRegisterRejectsAdminAccountType(t *testing.T) {
	svc := NewService(&fakeUserRepository{}, fakeTokenGenerator{})

	if _, err := svc.Register(t.Context(), "ops@example.com", "supersecret", "admin"); !errors.Is(err, autherrors.ErrInvalidAccountType) {
		t.Fatalf("Register(admin) error = %v, want ErrInvalidAccountType (admin is a permission, not account_type)", err)
	}
}

func TestRegisterAssignsEmptyPermissionsForCustomer(t *testing.T) {
	repo := &fakeUserRepository{}
	svc := NewService(repo, fakeTokenGenerator{})

	user, err := svc.Register(t.Context(), "customer@example.com", "supersecret", domain.AccountTypeCustomer)
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if len(user.Permissions) != 0 {
		t.Fatalf("Register permissions = %v, want empty", user.Permissions)
	}
	if user.AccountType != domain.AccountTypeCustomer {
		t.Fatalf("Register account_type = %q, want %q", user.AccountType, domain.AccountTypeCustomer)
	}
}

type mutableUserRepository struct {
	user *domain.User
}

func (r *mutableUserRepository) FindByEmail(_ context.Context, _ string) (*domain.User, error) {
	return r.user, nil
}

func (r *mutableUserRepository) FindByID(_ context.Context, _ string) (*domain.User, error) {
	return r.user, nil
}

func (r *mutableUserRepository) Save(_ context.Context, u *domain.User) error {
	r.user = u
	return nil
}

func (r *mutableUserRepository) Delete(_ context.Context, _ string) error { return nil }

func (r *mutableUserRepository) ListAll(_ context.Context) ([]*domain.User, error) { return nil, nil }

func TestLogin_UnknownEmailReturnsInvalidCredentials(t *testing.T) {
	repo := &fakeUserRepository{} // FindByEmail returns nil, nil: no such account
	svc := NewService(repo, fakeTokenGenerator{})

	if _, err := svc.Login(context.Background(), "nobody@example.com", "whatever"); !errors.Is(err, autherrors.ErrInvalidCredentials) {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestLogin_LocksAccountAfterMaxFailedAttempts(t *testing.T) {
	user, _ := domain.NewUser("u-lock", "locked@example.com", "correct-pass", domain.AccountTypeCustomer)
	repo := &mutableUserRepository{user: user}
	svc := NewService(repo, fakeTokenGenerator{})

	for i := 0; i < maxFailedAttempts; i++ {
		if _, err := svc.Login(t.Context(), "locked@example.com", "wrong"); err == nil {
			t.Fatalf("attempt %d: expected error", i+1)
		}
	}
	if !repo.user.IsLocked() {
		t.Fatal("account should be locked after max failed attempts")
	}

	if _, err := svc.Login(t.Context(), "locked@example.com", "correct-pass"); !errors.Is(err, autherrors.ErrAccountLocked) {
		t.Fatalf("locked login: got %v, want ErrAccountLocked", err)
	}
}

func TestLogin_DoesNotLockAdminOrOwner(t *testing.T) {
	cases := []struct {
		name string
		user *domain.User
	}{
		{
			name: "owner",
			user: mustUser(t, "owner@dupli1.com", "correct-pass", domain.AccountTypeManager, permissions.All),
		},
		{
			name: "admin",
			user: mustUser(t, "admin@dupli1.com", "correct-pass", domain.AccountTypeManager, permissions.AdminAll),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Stale lock from before the policy change must not block login.
			now := time.Now()
			tc.user.LockedAt = &now
			tc.user.FailedLoginAttempts = maxFailedAttempts
			repo := &mutableUserRepository{user: tc.user}
			svc := NewService(repo, fakeTokenGenerator{})

			for i := 0; i < maxFailedAttempts+2; i++ {
				_, err := svc.Login(t.Context(), tc.user.Email, "wrong")
				if !errors.Is(err, autherrors.ErrInvalidCredentials) {
					t.Fatalf("attempt %d: got %v, want ErrInvalidCredentials", i+1, err)
				}
			}
			if repo.user.IsLocked() || repo.user.LockedAt != nil {
				t.Fatal("admin/owner must not be locked after failed attempts")
			}

			token, err := svc.Login(t.Context(), tc.user.Email, "correct-pass")
			if err != nil {
				t.Fatalf("correct password: %v", err)
			}
			if token == "" {
				t.Fatal("expected refresh token")
			}
			if repo.user.LockedAt != nil || repo.user.FailedLoginAttempts != 0 {
				t.Fatalf("successful login should clear lock state: locked_at=%v attempts=%d",
					repo.user.LockedAt, repo.user.FailedLoginAttempts)
			}
		})
	}
}

func mustUser(t *testing.T, email, password, accountType string, perms ...string) *domain.User {
	t.Helper()
	u, err := domain.NewUser("u-"+email, email, password, accountType, perms...)
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	return u
}

func TestLogin_RejectsDeactivatedAccount(t *testing.T) {
	user, _ := domain.NewUser("u-off", "off@example.com", "pass", domain.AccountTypeCustomer)
	user.SetActive(false)
	repo := &stubUserRepository{user: user}
	svc := NewService(repo, fakeTokenGenerator{})

	if _, err := svc.Login(t.Context(), "off@example.com", "pass"); !errors.Is(err, autherrors.ErrAccountDeactivated) {
		t.Fatalf("got %v, want ErrAccountDeactivated", err)
	}
}

func TestLogin_LockExpiresAndResetsAttempts(t *testing.T) {
	user, _ := domain.NewUser("u-lock2", "stale@example.com", "correct-pass", domain.AccountTypeCustomer)
	past := time.Now().Add(-domain.AccountLockDuration - time.Minute)
	user.LockedAt = &past
	user.FailedLoginAttempts = maxFailedAttempts
	repo := &mutableUserRepository{user: user}
	svc := NewService(repo, fakeTokenGenerator{})

	token, err := svc.Login(context.Background(), "stale@example.com", "correct-pass")
	if err != nil {
		t.Fatalf("expected expired lock to allow login, got %v", err)
	}
	if token == "" {
		t.Fatal("expected refresh token")
	}
	if repo.user.LockedAt != nil || repo.user.FailedLoginAttempts != 0 {
		t.Fatalf("expired lock should be cleared: locked_at=%v attempts=%d", repo.user.LockedAt, repo.user.FailedLoginAttempts)
	}
}

func TestLogin_LockExpiresButAttemptCanStillFail(t *testing.T) {
	user, _ := domain.NewUser("u-lock3", "stale2@example.com", "correct-pass", domain.AccountTypeCustomer)
	past := time.Now().Add(-domain.AccountLockDuration - time.Minute)
	user.LockedAt = &past
	user.FailedLoginAttempts = maxFailedAttempts
	repo := &mutableUserRepository{user: user}
	svc := NewService(repo, fakeTokenGenerator{})

	if _, err := svc.Login(context.Background(), "stale2@example.com", "wrong"); !errors.Is(err, autherrors.ErrInvalidCredentials) {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
	if repo.user.IsLocked() {
		t.Fatal("a single fresh failed attempt after expiry must not re-lock the account")
	}
	if repo.user.FailedLoginAttempts != 1 {
		t.Fatalf("failed attempts = %d, want 1 (counted fresh, not carried over)", repo.user.FailedLoginAttempts)
	}
}

func TestGetMe_RejectsDeactivatedAccount(t *testing.T) {
	user, _ := domain.NewUser("u-off", "off@example.com", "pass", domain.AccountTypeCustomer)
	user.SetActive(false)
	repo := &stubUserRepository{user: user}
	svc := NewService(repo, fakeTokenGenerator{})

	if _, err := svc.GetMe(context.Background(), "access-token"); !errors.Is(err, autherrors.ErrAccountDeactivated) {
		t.Fatalf("got %v, want ErrAccountDeactivated", err)
	}
}

func TestGetMe_RejectsLockedAccount(t *testing.T) {
	user, _ := domain.NewUser("u-locked", "locked-getme@example.com", "pass", domain.AccountTypeCustomer)
	user.Lock()
	repo := &stubUserRepository{user: user}
	svc := NewService(repo, fakeTokenGenerator{})

	if _, err := svc.GetMe(context.Background(), "access-token"); !errors.Is(err, autherrors.ErrAccountLocked) {
		t.Fatalf("got %v, want ErrAccountLocked", err)
	}
}

func TestRefresh_RejectsDeactivatedAccount(t *testing.T) {
	user, _ := domain.NewUser("u-off", "off@example.com", "pass", domain.AccountTypeCustomer)
	user.SetActive(false)
	repo := &stubUserRepository{user: user}
	gen := &capturingTokenGenerator{capturedUserID: "u-off"}
	svc := NewService(repo, fakeTokenGenerator{}, WithRefreshTokenGen(gen, time.Hour))

	if _, _, err := svc.Refresh(t.Context(), "refresh-token"); !errors.Is(err, autherrors.ErrAccountDeactivated) {
		t.Fatalf("got %v, want ErrAccountDeactivated", err)
	}
}

type memorySessionStore struct {
	entries map[string]string
}

func (s *memorySessionStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	if s.entries == nil {
		s.entries = make(map[string]string)
	}
	s.entries[key] = value
	return nil
}

func (s *memorySessionStore) Get(_ context.Context, key string) (string, error) {
	if s.entries == nil {
		return "", ports.ErrSessionNotFound
	}
	v, ok := s.entries[key]
	if !ok {
		return "", ports.ErrSessionNotFound
	}
	return v, nil
}

func (s *memorySessionStore) Delete(_ context.Context, key string) error {
	if s.entries != nil {
		delete(s.entries, key)
	}
	return nil
}

func (s *memorySessionStore) Rotate(_ context.Context, oldKey, newKey, value string, _ time.Duration) error {
	if s.entries == nil {
		return ports.ErrSessionNotFound
	}
	if _, ok := s.entries[oldKey]; !ok {
		return ports.ErrSessionNotFound
	}
	s.entries[newKey] = value
	delete(s.entries, oldKey)
	return nil
}

func TestLogout_RevokesRefreshSession(t *testing.T) {
	user, _ := domain.NewUser("u-1", "user@example.com", "pass", domain.AccountTypeCustomer)
	repo := &stubUserRepository{user: user}
	refreshGen := &capturingTokenGenerator{}
	accessGen := fakeTokenGenerator{}
	sessions := &memorySessionStore{}
	svc := NewService(
		repo,
		accessGen,
		WithRefreshTokenGen(refreshGen, time.Hour),
		WithSessionStore(sessions),
	)

	refreshToken, err := svc.Login(t.Context(), "user@example.com", "pass")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, ok := sessions.entries[refreshToken]; !ok {
		t.Fatal("refresh token was not stored in session store")
	}

	if err := svc.Logout(t.Context(), refreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, ok := sessions.entries[refreshToken]; ok {
		t.Fatal("refresh token should be removed after logout")
	}

	if _, _, err := svc.Refresh(t.Context(), refreshToken); !errors.Is(err, autherrors.ErrInvalidToken) {
		t.Fatalf("Refresh after logout: got %v, want ErrInvalidToken", err)
	}
}

func TestRefresh_RotatesRefreshTokenAndInvalidatesTheOldOne(t *testing.T) {
	user, _ := domain.NewUser("u-1", "user@example.com", "pass", domain.AccountTypeCustomer)
	repo := &stubUserRepository{user: user}
	sessions := &memorySessionStore{}
	svc := NewService(
		repo,
		fakeTokenGenerator{},
		WithRefreshTokenGen(sequentialTokenGenerator{}, time.Hour),
		WithSessionStore(sessions),
	)

	refreshToken, err := svc.Login(context.Background(), "user@example.com", "pass")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	_, rotated, err := svc.Refresh(context.Background(), refreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if rotated == "" || rotated == refreshToken {
		t.Fatalf("expected a new, different refresh token; got %q (original %q)", rotated, refreshToken)
	}
	if _, ok := sessions.entries[refreshToken]; ok {
		t.Fatal("original refresh token should be invalidated after rotation")
	}
	if _, ok := sessions.entries[rotated]; !ok {
		t.Fatal("rotated refresh token should be stored")
	}

	if _, _, err := svc.Refresh(context.Background(), refreshToken); !errors.Is(err, autherrors.ErrInvalidToken) {
		t.Fatalf("reusing the rotated-away token: got %v, want ErrInvalidToken", err)
	}

	if _, _, err := svc.Refresh(context.Background(), rotated); err != nil {
		t.Fatalf("refreshing with the rotated token should still work: %v", err)
	}
}

func TestRefresh_ConcurrentRefreshOnlyOneSucceeds(t *testing.T) {
	user, _ := domain.NewUser("u-1", "user@example.com", "pass", domain.AccountTypeCustomer)
	repo := &stubUserRepository{user: user}
	sessions := memoryinfra.NewSessionStore()
	svc := NewService(
		repo,
		fakeTokenGenerator{},
		WithRefreshTokenGen(sequentialTokenGenerator{}, time.Hour),
		WithSessionStore(sessions),
	)

	refreshToken, err := svc.Login(context.Background(), "user@example.com", "pass")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	const attempts = 8
	type result struct {
		rotated string
		err     error
	}
	results := make([]result, attempts)
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := range attempts {
		go func(idx int) {
			defer wg.Done()
			_, rotated, err := svc.Refresh(context.Background(), refreshToken)
			results[idx] = result{rotated: rotated, err: err}
		}(i)
	}
	wg.Wait()

	successes := 0
	invalids := 0
	rotatedTokens := make(map[string]struct{})
	for _, r := range results {
		switch {
		case r.err == nil:
			successes++
			if r.rotated != "" {
				rotatedTokens[r.rotated] = struct{}{}
			}
		case errors.Is(r.err, autherrors.ErrInvalidToken):
			invalids++
		default:
			t.Fatalf("unexpected refresh error: %v", r.err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 successful concurrent refresh, got %d (invalid=%d)", successes, invalids)
	}
	if invalids != attempts-1 {
		t.Fatalf("expected %d ErrInvalidToken responses, got %d", attempts-1, invalids)
	}
	if len(rotatedTokens) != 1 {
		t.Fatalf("expected one distinct rotated token, got %d", len(rotatedTokens))
	}
}

// sequentialTokenGenerator returns a distinct token string on every Generate
// call, unlike capturingTokenGenerator/fakeTokenGenerator which always return
// the same fixed string. Rotation tests need each issued refresh token to be
// a distinct session-store key.
type sequentialTokenGenerator struct{}

func (sequentialTokenGenerator) Generate(_ context.Context, userID string, _ []string, _ string) (string, error) {
	return userID + "-" + newID(), nil
}

func (sequentialTokenGenerator) Validate(_ context.Context, token string) (ports.Claims, error) {
	return ports.Claims{UserID: "u-1"}, nil
}

func TestSetUserPermissionsRejectsInvalidPermission(t *testing.T) {
	user, _ := domain.NewUser("u-1", "user@example.com", "pass", domain.AccountTypeCustomer)
	repo := &stubUserRepository{user: user}
	svc := NewService(repo, fakeTokenGenerator{})

	_, err := svc.SetUserPermissions(t.Context(), "u-1", []string{"not.valid.permission"}, "")
	if !errors.Is(err, autherrors.ErrInvalidPermission) {
		t.Fatalf("got %v, want ErrInvalidPermission", err)
	}
}
