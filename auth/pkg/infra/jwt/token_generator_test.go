package jwt_test

import (
	"testing"

	jwtinfra "github.com/elug3/dupli1/auth/pkg/infra/jwt"
	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/golang-jwt/jwt/v5"
)

func TestRoundtrip_UserIDAndPermissionsPreserved(t *testing.T) {
	gen := jwtinfra.NewTokenGenerator("test-secret", 3600)
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

func TestGenerate_AccessTokenIncludesEmail(t *testing.T) {
	gen := jwtinfra.NewTokenGenerator("test-secret", 3600)
	token, err := gen.Generate(t.Context(), "user-1", []string{permissions.UserCreate}, "buyer@dupli1.com")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	parsed, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte("test-secret"), nil
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	mapClaims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("claims are not MapClaims")
	}
	if got, _ := mapClaims["email"].(string); got != "buyer@dupli1.com" {
		t.Fatalf("email claim = %q", got)
	}

	refresh := jwtinfra.NewTokenGeneratorWithType("test-secret", 3600, "refresh")
	rt, err := refresh.Generate(t.Context(), "user-1", nil, "buyer@dupli1.com")
	if err != nil {
		t.Fatalf("Generate refresh: %v", err)
	}
	parsedRT, err := jwt.Parse(rt, func(token *jwt.Token) (interface{}, error) {
		return []byte("test-secret"), nil
	})
	if err != nil {
		t.Fatalf("Parse refresh: %v", err)
	}
	rtClaims := parsedRT.Claims.(jwt.MapClaims)
	if _, ok := rtClaims["email"]; ok {
		t.Fatalf("refresh token must not carry email: %v", rtClaims["email"])
	}
}

func TestGenerate_IncludesPermissionsClaim(t *testing.T) {
	gen := jwtinfra.NewTokenGenerator("test-secret", 3600)
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
		t.Fatalf("Permissions = %v, want admin wildcard", claims.Permissions)
	}
}

func TestGenerate_EmptyPermissionsForCustomer(t *testing.T) {
	gen := jwtinfra.NewTokenGenerator("test-secret", 3600)
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

func TestRefreshToken_OmitsPermissions(t *testing.T) {
	gen := jwtinfra.NewTokenGeneratorWithType("test-secret", 3600, "refresh")
	ctx := t.Context()

	token, err := gen.Generate(ctx, "user-1", []string{permissions.All}, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	claims, err := gen.Validate(ctx, token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(claims.Permissions) != 0 {
		t.Fatalf("refresh Permissions = %v, want empty", claims.Permissions)
	}
}

func TestValidate_WrongSecretReturnsError(t *testing.T) {
	gen := jwtinfra.NewTokenGenerator("secret-A", 3600)
	ctx := t.Context()

	token, _ := gen.Generate(ctx, "user-1", []string{permissions.UserCreate}, "")

	other := jwtinfra.NewTokenGenerator("secret-B", 3600)
	if _, err := other.Validate(ctx, token); err == nil {
		t.Fatal("expected error with wrong secret, got nil")
	}
}

func TestValidate_ExpiredTokenReturnsError(t *testing.T) {
	gen := jwtinfra.NewTokenGenerator("test-secret", -1)
	ctx := t.Context()

	token, err := gen.Generate(ctx, "user-1", []string{permissions.UserCreate}, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if _, err := gen.Validate(ctx, token); err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestValidate_RejectsWrongTokenType(t *testing.T) {
	access := jwtinfra.NewTokenGeneratorWithType("test-secret", 3600, "access")
	refresh := jwtinfra.NewTokenGeneratorWithType("test-secret", 3600, "refresh")
	ctx := t.Context()

	refreshToken, err := refresh.Generate(ctx, "user-1", nil, "")
	if err != nil {
		t.Fatalf("Generate refresh: %v", err)
	}
	if _, err := access.Validate(ctx, refreshToken); err == nil {
		t.Fatal("expected access validator to reject refresh token, got nil")
	}

	accessToken, err := access.Generate(ctx, "user-1", []string{permissions.UserCreate}, "")
	if err != nil {
		t.Fatalf("Generate access: %v", err)
	}
	if _, err := refresh.Validate(ctx, accessToken); err == nil {
		t.Fatal("expected refresh validator to reject access token, got nil")
	}
}

func TestValidate_TamperedTokenReturnsError(t *testing.T) {
	gen := jwtinfra.NewTokenGenerator("test-secret", 3600)
	ctx := t.Context()

	token, _ := gen.Generate(ctx, "user-1", []string{permissions.UserCreate}, "")

	if _, err := gen.Validate(ctx, token+"tampered"); err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
}

func TestGenerate_TokensIssuedInSameSecondAreUnique(t *testing.T) {
	// Refresh rotation stores sessions keyed by the full token string. Without
	// a unique jti claim, two tokens minted for the same user in the same
	// second would be byte-identical and rotation would delete the only session.
	gen := jwtinfra.NewTokenGeneratorWithType("test-secret", 3600, "refresh")
	ctx := t.Context()

	t1, err := gen.Generate(ctx, "user-1", nil, "")
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	t2, err := gen.Generate(ctx, "user-1", nil, "")
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if t1 == t2 {
		t.Fatalf("refresh tokens are identical: %q", t1)
	}
}
