package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/lib/pq"
)

// MigrateSchema ensures the auth service database schema is up to date.
func MigrateSchema(ctx context.Context, db *sql.DB) error {
	return migrateSchema(ctx, db)
}

func migrateSchema(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id                    TEXT PRIMARY KEY,
			email                 TEXT UNIQUE NOT NULL,
			password              TEXT NOT NULL,
			permissions           TEXT[]    NOT NULL DEFAULT '{}',
			is_active             BOOLEAN   NOT NULL DEFAULT TRUE,
			locked_at             TIMESTAMPTZ,
			failed_login_attempts INT       NOT NULL DEFAULT 0,
			account_type          TEXT      NOT NULL DEFAULT 'customer'
		)`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS permissions TEXT[] NOT NULL DEFAULT '{}'`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT TRUE`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS failed_login_attempts INT NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS account_type TEXT NOT NULL DEFAULT 'customer'`,
		`CREATE TABLE IF NOT EXISTS auth_outbox (
			id BIGSERIAL PRIMARY KEY,
			aggregate_id TEXT NOT NULL,
			subject TEXT NOT NULL,
			payload JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			published_at TIMESTAMPTZ,
			attempts INT NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_outbox_pending ON auth_outbox (created_at) WHERE published_at IS NULL`,
	}

	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate schema: %w", err)
		}
	}

	if err := renameRolesColumn(ctx, db); err != nil {
		return err
	}
	if err := expandLegacyPermissionValues(ctx, db); err != nil {
		return err
	}
	if err := normalizeUserEmailCase(ctx, db); err != nil {
		return err
	}
	if err := createEmailLowerUniqueIndex(ctx, db); err != nil {
		return err
	}

	backfill := []string{
		// Rename legacy account_type "admin" → "manager" (admin is a permission tier, not an account type).
		`UPDATE users SET account_type = 'manager' WHERE account_type = 'admin'`,
		`UPDATE users SET account_type = 'manager'
		 WHERE account_type = 'customer'
		   AND (permissions && ARRAY['owner','admin','user_manager','*','admin.*'])`,
		`UPDATE users SET account_type = 'service'
		 WHERE account_type = 'customer'
		   AND (permissions && ARRAY['customer_registrar','order_manager','user.create'])`,
	}
	for _, stmt := range backfill {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate schema: %w", err)
		}
	}

	// customer_profiles / customer_addresses now live in the profile service
	// DB. Existing auth DBs may still have orphan copies from before the
	// cutover; leave them untouched here (manual drop after verified migrate).

	return nil
}

func renameRolesColumn(ctx context.Context, db *sql.DB) error {
	var rolesExists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'users'
			  AND column_name = 'roles'
		)`).Scan(&rolesExists)
	if err != nil {
		return fmt.Errorf("migrate roles column: %w", err)
	}
	if !rolesExists {
		return nil
	}

	var permissionsExists bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'users'
			  AND column_name = 'permissions'
		)`).Scan(&permissionsExists)
	if err != nil {
		return fmt.Errorf("migrate permissions column: %w", err)
	}

	if permissionsExists {
		if _, err := db.ExecContext(ctx, `
			UPDATE users
			   SET permissions = roles
			 WHERE cardinality(permissions) = 0
			   AND cardinality(roles) > 0`); err != nil {
			return fmt.Errorf("copy roles to permissions: %w", err)
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE users DROP COLUMN roles`); err != nil {
			return fmt.Errorf("drop roles column: %w", err)
		}
		return nil
	}

	if _, err := db.ExecContext(ctx, `ALTER TABLE users RENAME COLUMN roles TO permissions`); err != nil {
		return fmt.Errorf("rename roles to permissions: %w", err)
	}
	return nil
}

func expandLegacyPermissionValues(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id, permissions FROM users`)
	if err != nil {
		return fmt.Errorf("list users for permission expansion: %w", err)
	}
	defer rows.Close()

	type update struct {
		id    string
		perms []string
	}
	var pending []update

	for rows.Next() {
		var id string
		var stored pq.StringArray
		if err := rows.Scan(&id, &stored); err != nil {
			return fmt.Errorf("scan user permissions: %w", err)
		}
		perms := []string(stored)
		if !permissions.NeedsExpansion(perms) {
			continue
		}
		pending = append(pending, update{
			id:    id,
			perms: permissions.ExpandLegacyRoles(perms),
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list users for permission expansion: %w", err)
	}

	for _, item := range pending {
		if _, err := db.ExecContext(ctx,
			`UPDATE users SET permissions = $1 WHERE id = $2`,
			pq.Array(item.perms), item.id,
		); err != nil {
			return fmt.Errorf("expand permissions for %s: %w", item.id, err)
		}
	}
	return nil
}

// normalizeUserEmailCase lowercases every stored email that isn't already
// lowercase, so "Alice@x.com" and "alice@x.com" can no longer coexist as
// distinct accounts (app code now normalizes on write via
// domain.NormalizeEmail, and FindByEmail matches case-insensitively — this
// backfills rows written before that). Row-by-row so a genuine pre-existing
// case collision (two accounts already differing only by case) can't abort
// the whole migration: that one row is left as-is, logged, and needs manual
// review rather than a silent automatic pick of which account wins.
func normalizeUserEmailCase(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id, email FROM users WHERE email <> LOWER(email)`)
	if err != nil {
		return fmt.Errorf("list users for email normalization: %w", err)
	}

	type pendingUpdate struct{ id, email string }
	var updates []pendingUpdate
	for rows.Next() {
		var u pendingUpdate
		if err := rows.Scan(&u.id, &u.email); err != nil {
			rows.Close()
			return fmt.Errorf("scan user email: %w", err)
		}
		updates = append(updates, u)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list users for email normalization: %w", err)
	}

	for _, u := range updates {
		_, err := db.ExecContext(ctx, `UPDATE users SET email = LOWER(email) WHERE id = $1`, u.id)
		if err != nil {
			if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
				log.Printf("auth: user %s email %q collides case-insensitively with an existing account; left unnormalized, needs manual review", u.id, u.email)
				continue
			}
			return fmt.Errorf("normalize email case for %s: %w", u.id, err)
		}
	}
	return nil
}

// createEmailLowerUniqueIndex adds a case-insensitive unique index on email,
// the DB-level backstop for domain.NormalizeEmail so a future write that
// bypasses the application layer can't reintroduce a case-duplicate account.
// Skips (and logs) if normalizeUserEmailCase left an unresolved collision —
// index creation would otherwise fail and block every future startup until a
// human resolves the duplicate.
func createEmailLowerUniqueIndex(ctx context.Context, db *sql.DB) error {
	var collisions int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT LOWER(email) FROM users GROUP BY LOWER(email) HAVING COUNT(*) > 1
		) dupes`).Scan(&collisions)
	if err != nil {
		return fmt.Errorf("check email case collisions: %w", err)
	}
	if collisions > 0 {
		log.Printf("auth: %d email(s) still collide case-insensitively; skipping ux_users_email_lower until resolved manually", collisions)
		return nil
	}

	if _, err := db.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS ux_users_email_lower ON users (LOWER(email))`,
	); err != nil {
		return fmt.Errorf("create email lower unique index: %w", err)
	}
	return nil
}
