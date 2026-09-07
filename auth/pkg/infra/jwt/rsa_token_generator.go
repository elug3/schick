package jwt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/elug3/dupli1/auth/pkg/autherrors"
	"github.com/elug3/dupli1/auth/pkg/ports"
	"github.com/golang-jwt/jwt/v5"
)

// JWK represents a single JSON Web Key (RFC 7517).
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS represents a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// RSATokenGenerator implements ports.TokenGenerator using RS256.
type RSATokenGenerator struct {
	privateKey     *rsa.PrivateKey
	keyID          string
	expiryDuration time.Duration
	tokenType      string // "access" or "refresh"; omitted when empty
}

// NewRSATokenGenerator creates a token generator from an existing RSA key.
func NewRSATokenGenerator(key *rsa.PrivateKey, keyID string, expirySeconds int64) *RSATokenGenerator {
	return NewRSATokenGeneratorWithType(key, keyID, expirySeconds, "")
}

// NewRSATokenGeneratorWithType creates a token generator that stamps a type claim.
func NewRSATokenGeneratorWithType(key *rsa.PrivateKey, keyID string, expirySeconds int64, tokenType string) *RSATokenGenerator {
	if keyID == "" {
		keyID = "default"
	}
	return &RSATokenGenerator{
		privateKey:     key,
		keyID:          keyID,
		expiryDuration: time.Duration(expirySeconds) * time.Second,
		tokenType:      tokenType,
	}
}

// NewRSATokenGeneratorFromPEM parses a PEM-encoded RSA private key and creates a token generator.
// Supports PKCS#1 ("RSA PRIVATE KEY") and PKCS#8 ("PRIVATE KEY") formats.
func NewRSATokenGeneratorFromPEM(pemBytes []byte, keyID string, expirySeconds int64, tokenType string) (*RSATokenGenerator, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from private key")
	}

	var privateKey *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		var err error
		privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS1 RSA private key: %w", err)
		}
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS8 private key: %w", err)
		}
		var ok bool
		privateKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PEM contains %T, not an RSA private key", key)
		}
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %q", block.Type)
	}

	return NewRSATokenGeneratorWithType(privateKey, keyID, expirySeconds, tokenType), nil
}

// GenerateRSAKey creates a new RSA private key with the given bit size.
func GenerateRSAKey(bits int) (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, bits)
}

// Generate issues a signed RS256 JWT. Access tokens include permissions and
// email (when non-empty); refresh tokens include only sub, type, exp, iat, and jti.
func (g *RSATokenGenerator) Generate(ctx context.Context, userID string, userPermissions []string, email string) (string, error) {
	claims := buildMapClaims(userID, g.tokenType, time.Now().Add(g.expiryDuration), userPermissions, email)

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = g.keyID

	tokenString, err := token.SignedString(g.privateKey)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return tokenString, nil
}

// Validate verifies an RS256 JWT and returns the claims.
func (g *RSATokenGenerator) Validate(ctx context.Context, tokenString string) (ports.Claims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return &g.privateKey.PublicKey, nil
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

	if err := validateTokenType(mapClaims, g.tokenType); err != nil {
		return ports.Claims{}, autherrors.ErrInvalidToken
	}

	perms := claimsFromMap(mapClaims)

	return ports.Claims{UserID: userID, Permissions: perms}, nil
}

// PublicJWKS returns the JWKS document for the public key.
func (g *RSATokenGenerator) PublicJWKS() JWKS {
	pub := &g.privateKey.PublicKey
	return JWKS{
		Keys: []JWK{
			{
				Kty: "RSA",
				Use: "sig",
				Kid: g.keyID,
				Alg: "RS256",
				N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			},
		},
	}
}
