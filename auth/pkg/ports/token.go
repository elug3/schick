package ports

import "context"

// Claims holds the verified identity extracted from a token.
type Claims struct {
	UserID      string
	Permissions []string
}

// TokenGenerator defines the interface for token generation and validation.
type TokenGenerator interface {
	Generate(ctx context.Context, userID string, permissions []string, email string) (string, error)
	Validate(ctx context.Context, token string) (Claims, error)
}
