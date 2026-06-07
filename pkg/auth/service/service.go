package service

import (
	"context"

	"github.com/elug3/schick/pkg/auth/autherrors"
	"github.com/elug3/schick/pkg/auth/domain"
	"github.com/elug3/schick/pkg/auth/ports"
)

type Service struct {
	userRepo ports.UserRepository
	tokenGen ports.TokenGenerator
}

// NewService creates a new auth Service with required dependencies.
func NewService(userRepo ports.UserRepository, tokenGen ports.TokenGenerator) *Service {
	return &Service{userRepo: userRepo, tokenGen: tokenGen}
}

// Register creates a new user (minimal signature).
func (s *Service) Register(ctx context.Context, email, password string) (*domain.User, error) {
	// TODO: add validation, hashing, uniqueness check
	u := domain.NewUser("", email, password)
	if err := s.userRepo.Save(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// Login validates credentials and returns a refresh token.
func (s *Service) Login(ctx context.Context, email, password string) (string, error) {
	u, err := s.userRepo.FindByEmail(ctx, email)
	if err != nil {
		return "", err
	}
	if u == nil {
		return "", autherrors.ErrInvalidCredentials
	}

	if !u.ValidatePassword(password) {
		return "", autherrors.ErrInvalidCredentials
	}

	token, err := s.tokenGen.Generate(ctx, u.ID)
	if err != nil {
		return "", err
	}

	return token, nil
}

// Logout handles logout logic (e.g., invalidate session). Signature accepts context.
func (s *Service) Logout(ctx context.Context, userID string) error {
	// TODO: implement session invalidation
	_ = userID
	return nil
}

// Refresh validates a refresh token and issues a new token.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (string, error) {
	// TODO: validate/refresh tokens
	userID, err := s.tokenGen.Validate(ctx, refreshToken)
	if err != nil {
		return "", err
	}

	return s.tokenGen.Generate(ctx, userID)
}
