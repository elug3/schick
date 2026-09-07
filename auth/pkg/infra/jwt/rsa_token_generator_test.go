// Package jwt_test covers both the HMAC token generator (token_generator_test.go)
// and the RS256/JWKS token generator (this file).
package jwt_test

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"

	jwtinfra "github.com/elug3/dupli1/auth/pkg/infra/jwt"
	"github.com/elug3/dupli1/shared/pkg/permissions"
)

// testRSAKey is generated once per test binary run (2048-bit is fast enough).
var testRSAKey *rsa.PrivateKey

func init() {
	var err error
	testRSAKey, err = jwtinfra.GenerateRSAKey(2048)
	if err != nil {
		panic("failed to generate test RSA key: " + err.Error())
	}
}

func TestRSA_RoundtripPreservesClaims(t *testing.T) {
	gen := jwtinfra.NewRSATokenGenerator(testRSAKey, "test-kid", 3600)
	ctx := t.Context()

	token, err := gen.Generate(ctx, "user-1", []string{permissions.UserCreate}, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	claims, err := gen.Validate(ctx, token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.UserID != "user-1" {
		t.Fatalf("UserID = %q, want user-1", claims.UserID)
	}
	if len(claims.Permissions) != 1 || claims.Permissions[0] != permissions.UserCreate {
		t.Fatalf("Permissions = %v, want [%s]", claims.Permissions, permissions.UserCreate)
	}
}

func TestRSA_AdminPermissionsPreserved(t *testing.T) {
	gen := jwtinfra.NewRSATokenGenerator(testRSAKey, "kid", 3600)
	ctx := t.Context()

	perms := permissions.ExpandLegacyRoles([]string{permissions.RoleAdmin})
	token, err := gen.Generate(ctx, "user-2", perms, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	claims, err := gen.Validate(ctx, token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !permissions.Has(claims.Permissions, permissions.AdminAll) {
		t.Fatalf("Permissions = %v", claims.Permissions)
	}
}

func TestRSA_EmptyPermissionsForCustomer(t *testing.T) {
	gen := jwtinfra.NewRSATokenGenerator(testRSAKey, "kid", 3600)
	ctx := t.Context()

	token, err := gen.Generate(ctx, "user-3", []string{}, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	claims, err := gen.Validate(ctx, token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(claims.Permissions) != 0 {
		t.Fatalf("Permissions = %v, want empty", claims.Permissions)
	}
}

func TestRSA_ExpiredTokenReturnsError(t *testing.T) {
	gen := jwtinfra.NewRSATokenGenerator(testRSAKey, "kid", -1)
	ctx := t.Context()

	token, err := gen.Generate(ctx, "user-1", []string{}, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if _, err := gen.Validate(ctx, token); err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestRSA_WrongKeyReturnsError(t *testing.T) {
	otherKey, err := jwtinfra.GenerateRSAKey(2048)
	if err != nil {
		t.Fatalf("GenerateRSAKey: %v", err)
	}

	signer := jwtinfra.NewRSATokenGenerator(testRSAKey, "kid", 3600)
	verifier := jwtinfra.NewRSATokenGenerator(otherKey, "kid", 3600)
	ctx := t.Context()

	token, _ := signer.Generate(ctx, "user-1", []string{}, "")
	if _, err := verifier.Validate(ctx, token); err == nil {
		t.Fatal("expected error when validating with wrong key, got nil")
	}
}

func TestRSA_Validate_RejectsWrongTokenType(t *testing.T) {
	access := jwtinfra.NewRSATokenGeneratorWithType(testRSAKey, "kid", 3600, "access")
	refresh := jwtinfra.NewRSATokenGeneratorWithType(testRSAKey, "kid", 3600, "refresh")
	ctx := t.Context()

	refreshToken, err := refresh.Generate(ctx, "user-1", nil, "")
	if err != nil {
		t.Fatalf("Generate refresh: %v", err)
	}
	if _, err := access.Validate(ctx, refreshToken); err == nil {
		t.Fatal("expected access validator to reject refresh token, got nil")
	}
}

func TestRSA_TamperedTokenReturnsError(t *testing.T) {
	gen := jwtinfra.NewRSATokenGenerator(testRSAKey, "kid", 3600)
	ctx := t.Context()

	token, _ := gen.Generate(ctx, "user-1", []string{}, "")
	if _, err := gen.Validate(ctx, token+"tampered"); err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
}

func TestRSA_DefaultKeyID(t *testing.T) {
	gen := jwtinfra.NewRSATokenGenerator(testRSAKey, "", 3600)
	jwks := gen.PublicJWKS()
	if jwks.Keys[0].Kid != "default" {
		t.Fatalf("Kid = %q, want default", jwks.Keys[0].Kid)
	}
}

func TestRSA_PublicJWKS_FieldsCorrect(t *testing.T) {
	gen := jwtinfra.NewRSATokenGenerator(testRSAKey, "my-key", 3600)
	jwks := gen.PublicJWKS()

	if len(jwks.Keys) != 1 {
		t.Fatalf("len(Keys) = %d, want 1", len(jwks.Keys))
	}
	k := jwks.Keys[0]
	if k.Kty != "RSA" {
		t.Errorf("Kty = %q, want RSA", k.Kty)
	}
	if k.Use != "sig" {
		t.Errorf("Use = %q, want sig", k.Use)
	}
	if k.Alg != "RS256" {
		t.Errorf("Alg = %q, want RS256", k.Alg)
	}
	if k.Kid != "my-key" {
		t.Errorf("Kid = %q, want my-key", k.Kid)
	}
	if k.N == "" {
		t.Error("N is empty")
	}
	if k.E == "" {
		t.Error("E is empty")
	}
}

func TestRSA_PublicJWKS_NandEMatchKey(t *testing.T) {
	gen := jwtinfra.NewRSATokenGenerator(testRSAKey, "kid", 3600)
	k := gen.PublicJWKS().Keys[0]

	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		t.Fatalf("decode N: %v", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		t.Fatalf("decode E: %v", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())

	if n.Cmp(testRSAKey.PublicKey.N) != 0 {
		t.Error("N in JWKS does not match public key modulus")
	}
	if e != testRSAKey.PublicKey.E {
		t.Errorf("E in JWKS = %d, want %d", e, testRSAKey.PublicKey.E)
	}
}

func TestRSA_NewFromPEM_PKCS1(t *testing.T) {
	pemBytes := encodePKCS1(t, testRSAKey)
	gen, err := jwtinfra.NewRSATokenGeneratorFromPEM(pemBytes, "kid", 3600, "")
	if err != nil {
		t.Fatalf("NewRSATokenGeneratorFromPEM PKCS1: %v", err)
	}

	ctx := t.Context()
	token, err := gen.Generate(ctx, "user-1", []string{permissions.All}, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	claims, err := gen.Validate(ctx, token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.UserID != "user-1" {
		t.Fatalf("UserID = %q, want user-1", claims.UserID)
	}
}

func TestRSA_NewFromPEM_PKCS8(t *testing.T) {
	pemBytes := encodePKCS8(t, testRSAKey)
	gen, err := jwtinfra.NewRSATokenGeneratorFromPEM(pemBytes, "kid", 3600, "")
	if err != nil {
		t.Fatalf("NewRSATokenGeneratorFromPEM PKCS8: %v", err)
	}

	ctx := t.Context()
	token, err := gen.Generate(ctx, "user-2", []string{}, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := gen.Validate(ctx, token); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRSA_NewFromPEM_InvalidPEM(t *testing.T) {
	if _, err := jwtinfra.NewRSATokenGeneratorFromPEM([]byte("not-a-pem"), "kid", 3600, ""); err == nil {
		t.Fatal("expected error for invalid PEM, got nil")
	}
}

func TestRSA_NewFromPEM_WrongKeyType(t *testing.T) {
	// EC key PEM — not an RSA key
	ecPEM := []byte(`-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgevZzL1gdAFr88hD2
cV4nQeiws4tLf2HxcDsGAQb4EaKhRANCAARxy5u39/yqw2tI98mV/Kato6xNnSM5
0fXxMDnbYMCISHpYjx7BKV9lMoaRxVVBHBbRtCGDfSMgCCVBvH9IAJQ
-----END PRIVATE KEY-----`)
	_, err := jwtinfra.NewRSATokenGeneratorFromPEM(ecPEM, "kid", 3600, "")
	if err == nil {
		t.Fatal("expected error for non-RSA key, got nil")
	}
}

func TestRSA_GenerateRSAKey(t *testing.T) {
	key, err := jwtinfra.GenerateRSAKey(2048)
	if err != nil {
		t.Fatalf("GenerateRSAKey: %v", err)
	}
	if key == nil {
		t.Fatal("GenerateRSAKey returned nil key")
	}
	if key.N.BitLen() != 2048 {
		t.Fatalf("key size = %d bits, want 2048", key.N.BitLen())
	}
}

func encodePKCS1(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func encodePKCS8(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
}

// Ensure GenerateRSAKey is actually random across calls.
func TestRSA_GenerateRSAKey_Unique(t *testing.T) {
	key1, err := jwtinfra.GenerateRSAKey(2048)
	if err != nil {
		t.Fatalf("GenerateRSAKey: %v", err)
	}
	key2, err := jwtinfra.GenerateRSAKey(2048)
	if err != nil {
		t.Fatalf("GenerateRSAKey: %v", err)
	}
	if key1.N.Cmp(key2.N) == 0 {
		t.Fatal("two independently generated keys have the same modulus")
	}
}
