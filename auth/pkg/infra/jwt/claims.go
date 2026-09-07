package jwt

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/golang-jwt/jwt/v5"
)

func buildMapClaims(userID string, tokenType string, expiry time.Time, userPermissions []string, email string) jwt.MapClaims {
	claims := jwt.MapClaims{
		"sub": userID,
		"exp": expiry.Unix(),
		"iat": time.Now().Unix(),
		// jti makes every issued token unique even when the same user is
		// issued two tokens within the same second (same sub/type/iat/exp
		// would otherwise sign to the byte-identical JWT). Refresh-token
		// rotation depends on this: the session store is keyed by the token
		// string, so a collision would make "issue the replacement" and
		// "delete the original" collapse into deleting the only session.
		"jti": newJTI(),
	}
	if tokenType != "" {
		claims["type"] = tokenType
	}
	if tokenType != "refresh" {
		claims["permissions"] = permissions.Dedupe(userPermissions)
		if e := strings.TrimSpace(email); e != "" {
			claims["email"] = e
		}
	}
	return claims
}

func newJTI() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func claimsFromMap(mapClaims jwt.MapClaims) []string {
	return extractStringSlice(mapClaims, "permissions")
}

func extractStringSlice(mapClaims jwt.MapClaims, key string) []string {
	raw, ok := mapClaims[key]
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
