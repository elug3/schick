package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/elug3/dupli1/profile/pkg/domain"
	"github.com/elug3/dupli1/profile/pkg/infra/postgres"
	"github.com/elug3/dupli1/profile/pkg/ports"
	"github.com/elug3/dupli1/shared/pkg/pgsslmode"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func requirePostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping postgres integration test")
	}
	dsn = pgsslmode.WithSSLMode(dsn)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := db.PingContext(t.Context()); err != nil {
		_ = db.Close()
		t.Fatalf("ping postgres: %v", err)
	}

	schema := "profile_test_" + uuid.New().String()[:8]
	if _, err := db.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		_ = db.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = db.Close()
	})
	if _, err := db.ExecContext(t.Context(), `SET search_path TO `+schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if err := postgres.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// SaveAddress must refuse to reassign an existing address id to another user.
func TestSaveAddressRefusesCrossUserOverwrite(t *testing.T) {
	db := requirePostgresDB(t)
	repo := postgres.NewProfileRepository(db)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)

	original := &domain.Address{
		ID: "addr_000001", UserID: "user-a", Label: "home",
		RecipientName: "A", RecipientPhone: "01011112222",
		PostalCode: "06000", AddressLine1: "line", City: "서울", Province: "서울",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.SaveAddress(ctx, original); err != nil {
		t.Fatalf("SaveAddress original: %v", err)
	}

	hijack := *original
	hijack.UserID = "user-b"
	hijack.Label = "stolen"
	if err := repo.SaveAddress(ctx, &hijack); err != ports.ErrAddressNotFound {
		t.Fatalf("cross-user overwrite err = %v, want ErrAddressNotFound", err)
	}

	got, err := repo.GetAddress(ctx, "user-a", "addr_000001")
	if err != nil || got == nil {
		t.Fatalf("GetAddress: %v %v", got, err)
	}
	if got.Label != "home" || got.UserID != "user-a" {
		t.Fatalf("original address mutated: %+v", got)
	}

	// Sanity: the attacker cannot read the victim's address under their own user id.
	missing, err := repo.GetAddress(ctx, "user-b", "addr_000001")
	if err != nil {
		t.Fatalf("GetAddress attacker: %v", err)
	}
	if missing != nil {
		t.Fatalf("attacker must not see victim address, got %+v", missing)
	}
}

func TestSaveAddressAllowsSameUserUpdate(t *testing.T) {
	db := requirePostgresDB(t)
	repo := postgres.NewProfileRepository(db)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)

	addr := &domain.Address{
		ID: fmt.Sprintf("addr_%s", uuid.New().String()[:8]), UserID: "user-a", Label: "home",
		RecipientName: "A", RecipientPhone: "01011112222",
		PostalCode: "06000", AddressLine1: "line", City: "서울", Province: "서울",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.SaveAddress(ctx, addr); err != nil {
		t.Fatalf("SaveAddress: %v", err)
	}

	addr.Label = "office"
	addr.UpdatedAt = now.Add(time.Minute)
	if err := repo.SaveAddress(ctx, addr); err != nil {
		t.Fatalf("SaveAddress update: %v", err)
	}

	got, err := repo.GetAddress(ctx, "user-a", addr.ID)
	if err != nil || got == nil {
		t.Fatalf("GetAddress: %v %v", got, err)
	}
	if got.Label != "office" {
		t.Fatalf("label = %q, want office", got.Label)
	}
}
