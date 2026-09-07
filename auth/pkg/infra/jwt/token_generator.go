package jwt

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/elug3/dupli1/auth/pkg/autherrors"
	"github.com/elug3/dupli1/auth/pkg/ports"
	"github.com/golang-jwt/jwt/v5"
)

// TokenGenerator implements ports.TokenGenerator using JWT.
type TokenGenerator struct {
	secret         string
	expiryDuration time.Duration
	tokenType      string
}

// NewTokenGenerator creates a new JWT token generator.
func NewTokenGenerator(secret string, expirySeconds int64) *TokenGenerator {
	return NewTokenGeneratorWithType(secret, expirySeconds, "access")
}

// NewTokenGeneratorWithType creates a JWT token generator that stamps a type claim.
func NewTokenGeneratorWithType(secret string, expirySeconds int64, tokenType string) *TokenGenerator {
	return &TokenGenerator{
		secret:         secret,
		expiryDuration: time.Duration(expirySeconds) * time.Second,
		tokenType:      tokenType,
	}
}

// Generate generates a JWT token. Access tokens include permissions and email
// (when non-empty); refresh tokens include only sub, type, exp, iat, and jti.
func (tg *TokenGenerator) Generate(ctx context.Context, userID string, userPermissions []string, email string) (string, error) {
	claims := buildMapClaims(userID, tg.tokenType, time.Now().Add(tg.expiryDuration), userPermissions, email)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(tg.secret))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	return tokenString, nil
}

// Validate validates a JWT token and returns the claims.
// Returns autherrors.ErrTokenExpired when the token has expired,
// and autherrors.ErrInvalidToken for any other validation failure.
func (tg *TokenGenerator) Validate(ctx context.Context, tokenString string) (ports.Claims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(tg.secret), nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return ports.Claims{}, autherrors.ErrTokenExpired
		}
		return ports.Claims{}, autherrors.ErrInvalidToken
	}

	if !token.Valid {
		return ports.Claims{}, autherrors.ErrInvalidToken
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return ports.Claims{}, autherrors.ErrInvalidToken
	}

	userID, err := extractSubject(mapClaims)
	if err != nil {
		return ports.Claims{}, autherrors.ErrInvalidToken
	}

	if err := validateTokenType(mapClaims, tg.tokenType); err != nil {
		return ports.Claims{}, autherrors.ErrInvalidToken
	}

	perms := claimsFromMap(mapClaims)

	return ports.Claims{UserID: userID, Permissions: perms}, nil
}

func validateTokenType(claims jwt.MapClaims, expected string) error {
	if expected == "" {
		return nil
	}
	raw, ok := claims["type"]
	if !ok {
		return fmt.Errorf("token type claim missing")
	}
	typ, ok := raw.(string)
	if !ok || typ != expected {
		return fmt.Errorf("unexpected token type %q, want %q", typ, expected)
	}
	return nil
}

func extractSubject(claims jwt.MapClaims) (string, error) {
	if sub, ok := claims["sub"]; ok {
		if s, ok := sub.(string); ok && s != "" {
			return s, nil
		}
	}
	if uid, ok := claims["user_id"]; ok {
		if s, ok := uid.(string); ok && s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("subject claim missing")
}
