package authjwt

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

type contextKey struct{}

// Claims holds the verified identity extracted from a JWT.
type Claims struct {
	UserID      string
	Email       string
	Permissions []string
}

// HasPermission reports whether any of the given permissions is granted.
func (c Claims) HasPermission(perms ...string) bool {
	return permissions.HasAny(c.Permissions, perms...)
}

// WithClaims stores claims in the context.
func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// FromContext retrieves claims from the context. Returns false if not present.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(contextKey{}).(Claims)
	return c, ok
}

// AccessTokenValidator validates Bearer access tokens.
type AccessTokenValidator interface {
	ValidateAccessToken(token string) (Claims, error)
}

type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

// JWKSValidator validates RS256 access tokens using keys from a JWKS endpoint.
type JWKSValidator struct {
	url    string
	client *http.Client
	mu     sync.RWMutex
	keys   map[string]*rsa.PublicKey
	sf     singleflight.Group
}

// NewJWKSValidator creates a validator that loads signing keys from url.
func NewJWKSValidator(url string) *JWKSValidator {
	return &JWKSValidator{
		url: url,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		keys: make(map[string]*rsa.PublicKey),
	}
}

// ValidateAccessToken verifies signature, expiry, and access token type.
func (v *JWKSValidator) ValidateAccessToken(tokenString string) (Claims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		kid, _ := token.Header["kid"].(string)
		key, err := v.publicKey(kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	})
	if err != nil || token == nil || !token.Valid {
		return Claims{}, fmt.Errorf("invalid token")
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, fmt.Errorf("invalid token claims")
	}
	return claimsFromMap(mapClaims)
}

func (v *JWKSValidator) publicKey(kid string) (*rsa.PublicKey, error) {
	if key, ok := v.cachedKey(kid); ok {
		return key, nil
	}

	if err := v.refreshKeys(); err != nil {
		return nil, err
	}

	if key, ok := v.cachedKey(kid); ok {
		return key, nil
	}
	return nil, fmt.Errorf("signing key %q not found", kid)
}

func (v *JWKSValidator) cachedKey(kid string) (*rsa.PublicKey, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if key, ok := v.keys[kid]; ok && key != nil {
		return key, true
	}
	if kid == "" && len(v.keys) == 1 {
		for _, key := range v.keys {
			return key, true
		}
	}
	return nil, false
}

func (v *JWKSValidator) refreshKeys() error {
	_, err, _ := v.sf.Do(v.url, func() (any, error) {
		return nil, v.fetchAndStoreKeys()
	})
	return err
}

func (v *JWKSValidator) fetchAndStoreKeys() error {
	req, err := http.NewRequest(http.MethodGet, v.url, nil)
	if err != nil {
		return err
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch JWKS: status %d", resp.StatusCode)
	}

	var doc jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, entry := range doc.Keys {
		if !strings.EqualFold(entry.Kty, "RSA") || entry.N == "" || entry.E == "" {
			continue
		}
		pub, err := parseRSAPublicKey(entry.N, entry.E)
		if err != nil {
			continue
		}
		kid := entry.Kid
		if kid == "" {
			kid = "default"
		}
		keys[kid] = pub
	}
	if len(keys) == 0 {
		return fmt.Errorf("JWKS contains no usable RSA keys")
	}

	v.mu.Lock()
	v.keys = keys
	v.mu.Unlock()
	return nil
}

func parseRSAPublicKey(nEnc, eEnc string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nEnc)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eEnc)
	if err != nil {
		return nil, err
	}

	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	return &rsa.PublicKey{N: n, E: e}, nil
}

// HMACValidator validates HS256 access tokens with a shared secret.
type HMACValidator struct {
	secret []byte
}

// NewHMACValidator creates a validator for legacy/dev HMAC tokens.
func NewHMACValidator(secret string) *HMACValidator {
	return &HMACValidator{secret: []byte(secret)}
}

// ValidateAccessToken verifies signature, expiry, and access token type.
func (v *HMACValidator) ValidateAccessToken(tokenString string) (Claims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return v.secret, nil
	})
	if err != nil || token == nil || !token.Valid {
		return Claims{}, fmt.Errorf("invalid token")
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, fmt.Errorf("invalid token claims")
	}
	return claimsFromMap(mapClaims)
}

func claimsFromMap(mapClaims jwt.MapClaims) (Claims, error) {
	if tokenType, _ := mapClaims["type"].(string); tokenType != "access" {
		return Claims{}, fmt.Errorf("access token required")
	}
	userID, err := extractSubject(mapClaims)
	if err != nil {
		return Claims{}, err
	}
	rawPerms := extractStringSlice(mapClaims, "permissions")
	email, _ := mapClaims["email"].(string)
	return Claims{
		UserID:      userID,
		Email:       strings.TrimSpace(email),
		Permissions: permissions.Dedupe(rawPerms),
	}, nil
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

func extractStringSlice(claims jwt.MapClaims, key string) []string {
	raw, ok := claims[key]
	if !ok {
		return []string{}
	}
	switch v := raw.(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return []string{}
}

// NewAccessTokenValidator returns JWKS validation when url is set, otherwise HMAC.
func NewAccessTokenValidator(jwksURL, hmacSecret string) (AccessTokenValidator, error) {
	if jwksURL != "" {
		return NewJWKSValidator(jwksURL), nil
	}
	if hmacSecret == "" {
		return nil, fmt.Errorf("AUTH_JWKS_URL or JWT_SECRET is required")
	}
	return NewHMACValidator(hmacSecret), nil
}
