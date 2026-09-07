package authjwt

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/golang-jwt/jwt/v5"
)

func TestHMACValidatorRequiresAccessType(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "user-1",
		"type": "access",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	v := NewHMACValidator("test-secret")
	claims, err := v.ValidateAccessToken(signed)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if claims.UserID != "user-1" {
		t.Fatalf("UserID = %q, want user-1", claims.UserID)
	}
}

func TestHMACValidatorExtractsEmail(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "user-1",
		"email": " buyer@dupli1.com ",
		"type":  "access",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	v := NewHMACValidator("test-secret")
	claims, err := v.ValidateAccessToken(signed)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if claims.Email != "buyer@dupli1.com" {
		t.Fatalf("Email = %q, want trimmed buyer@dupli1.com", claims.Email)
	}
}

func TestHMACValidatorRejectsRefreshType(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "user-1",
		"type": "refresh",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	v := NewHMACValidator("test-secret")
	if _, err := v.ValidateAccessToken(signed); err == nil {
		t.Fatal("expected refresh token to be rejected")
	}
}

func TestHMACValidatorReadsPermissionsClaim(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         "user-2",
		"type":        "access",
		"permissions": []string{permissions.ProductAll, permissions.CouponAll},
		"exp":         time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	v := NewHMACValidator("test-secret")
	claims, err := v.ValidateAccessToken(signed)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if !claims.HasPermission(permissions.ProductCreate) {
		t.Fatal("expected product.create from product.* wildcard")
	}
	if claims.HasPermission(permissions.OrderShip) {
		t.Fatal("did not expect order.ship")
	}
}

func TestHMACValidatorReadsOrderManagerPermissions(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         "user-1",
		"type":        "access",
		"permissions": permissions.ExpandLegacyRoles([]string{permissions.RoleOrderManager}),
		"exp":         time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	v := NewHMACValidator("test-secret")
	claims, err := v.ValidateAccessToken(signed)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if !claims.HasPermission(permissions.OrderShip) {
		t.Fatal("expected order.ship")
	}
}

func TestHMACValidatorIgnoresLegacyRolesClaim(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         "user-3",
		"type":        "access",
		"permissions": []string{permissions.CouponRead},
		"roles":       []string{"product_manager"},
		"exp":         time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	v := NewHMACValidator("test-secret")
	claims, err := v.ValidateAccessToken(signed)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if !claims.HasPermission(permissions.CouponRead) {
		t.Fatal("expected coupon.read from permissions claim")
	}
	if claims.HasPermission(permissions.ProductCreate) {
		t.Fatal("legacy roles claim must not grant permissions")
	}
}

func TestExtractStringSliceHandlesStringSliceClaim(t *testing.T) {
	claims := jwt.MapClaims{
		"permissions": []string{"coupon.read"},
	}
	perms := extractStringSlice(claims, "permissions")
	if len(perms) != 1 || perms[0] != "coupon.read" {
		t.Fatalf("permissions = %v, want [coupon.read]", perms)
	}
}

func signRS256AccessToken(t *testing.T, key *rsa.PrivateKey, kid, userID string, perms []string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":         userID,
		"permissions": perms,
		"type":        "access",
		"exp":         time.Now().Add(time.Hour).Unix(),
		"iat":         time.Now().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return signed
}

func publicJWKS(key *rsa.PrivateKey, kid string) []byte {
	pub := &key.PublicKey
	doc := map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"kid": kid,
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
	b, _ := json.Marshal(doc)
	return b
}

func TestJWKSValidatorReadsAdminPermissions(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	const kid = "test-kid"
	jwks := publicJWKS(key, kid)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	}))
	defer srv.Close()

	validator := NewJWKSValidator(srv.URL)
	token := signRS256AccessToken(t, key, kid, "admin-1", permissions.ExpandLegacyRoles([]string{permissions.RoleAdmin}))

	claims, err := validator.ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if !claims.HasPermission(permissions.ProductCreate) {
		t.Fatalf("admin should grant product.create, got %v", claims.Permissions)
	}
	if !claims.HasPermission(permissions.CouponRead) {
		t.Fatalf("admin should grant coupon.read, got %v", claims.Permissions)
	}
}

func TestJWKSValidatorSingleflightCoalescesRefresh(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	const kid = "sf-kid"
	jwks := publicJWKS(key, kid)

	var hits atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		once.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	}))
	defer srv.Close()

	validator := NewJWKSValidator(srv.URL)
	token := signRS256AccessToken(t, key, kid, "user-sf", []string{permissions.ProductRead})

	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := validator.ValidateAccessToken(token)
			errs <- err
		}()
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("JWKS handler was never hit")
	}
	// Give siblings time to join the singleflight before the first fetch returns.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("ValidateAccessToken: %v", err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("JWKS fetches = %d, want 1 (singleflight)", got)
	}
}

func TestNewAccessTokenValidatorRequiresSecretOrJWKS(t *testing.T) {
	if _, err := NewAccessTokenValidator("", ""); err == nil {
		t.Fatal("expected error when neither JWKS nor HMAC secret is set")
	}
}
