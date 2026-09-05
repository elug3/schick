// Package postgres implements ports.ProfileRepository against a PostgreSQL
// database, using database/sql + lib/pq (matching auth, the service this
// module was extracted from). Unlike auth's copy of this schema, the tables
// here carry no REFERENCES to a users table: profile owns no foreign key
// into auth's database, so account deletion is cleaned up via the
// shared/pkg/events.UserDeleted NATS event instead of an ON DELETE CASCADE.
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/elug3/dupli1/profile/pkg/domain"
	"github.com/elug3/dupli1/profile/pkg/ports"
)

// ProfileRepository implements ports.ProfileRepository using PostgreSQL.
type ProfileRepository struct {
	db *sql.DB
}

// NewProfileRepository creates a profile repository. Call Migrate to ensure
// the schema exists before use.
func NewProfileRepository(db *sql.DB) *ProfileRepository {
	return &ProfileRepository{db: db}
}

// Migrate creates/updates the profile service's schema. Safe to call on
// every startup.
func Migrate(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS id_sequences (
			name TEXT PRIMARY KEY,
			value BIGINT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS customer_profiles (
			user_id      TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			phone        TEXT NOT NULL DEFAULT '',
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS customer_addresses (
			id              TEXT PRIMARY KEY,
			user_id         TEXT NOT NULL,
			label           TEXT NOT NULL DEFAULT '',
			recipient_name  TEXT NOT NULL,
			recipient_phone TEXT NOT NULL,
			postal_code     TEXT NOT NULL,
			address_line1   TEXT NOT NULL,
			address_line2   TEXT NOT NULL DEFAULT '',
			city            TEXT NOT NULL,
			province        TEXT NOT NULL,
			pccc            TEXT NOT NULL DEFAULT '',
			is_default      BOOLEAN NOT NULL DEFAULT FALSE,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE customer_addresses ADD COLUMN IF NOT EXISTS pccc TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_customer_addresses_user_id ON customer_addresses (user_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_customer_addresses_one_default
			ON customer_addresses (user_id) WHERE is_default`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate profile schema: %w", err)
		}
	}
	return nil
}

func (r *ProfileRepository) GetProfile(ctx context.Context, userID string) (*domain.Profile, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT user_id, display_name, phone, created_at, updated_at
		FROM customer_profiles WHERE user_id = $1`, userID)
	var p domain.Profile
	err := row.Scan(&p.UserID, &p.DisplayName, &p.Phone, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	return &p, nil
}

func (r *ProfileRepository) UpsertProfile(ctx context.Context, profile *domain.Profile) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO customer_profiles (user_id, display_name, phone, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE
		  SET display_name = EXCLUDED.display_name,
		      phone = EXCLUDED.phone,
		      updated_at = EXCLUDED.updated_at`,
		profile.UserID, profile.DisplayName, profile.Phone, profile.CreatedAt, profile.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert profile: %w", err)
	}
	return nil
}

func (r *ProfileRepository) ListAddresses(ctx context.Context, userID string) ([]*domain.Address, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, label, recipient_name, recipient_phone, postal_code,
		       address_line1, address_line2, city, province, pccc, is_default, created_at, updated_at
		FROM customer_addresses
		WHERE user_id = $1
		ORDER BY is_default DESC, created_at ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list addresses: %w", err)
	}
	defer rows.Close()

	var out []*domain.Address
	for rows.Next() {
		a, err := scanAddress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *ProfileRepository) CountAddresses(ctx context.Context, userID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM customer_addresses WHERE user_id = $1`, userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count addresses: %w", err)
	}
	return count, nil
}

func (r *ProfileRepository) GetAddress(ctx context.Context, userID, addressID string) (*domain.Address, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, label, recipient_name, recipient_phone, postal_code,
		       address_line1, address_line2, city, province, pccc, is_default, created_at, updated_at
		FROM customer_addresses
		WHERE user_id = $1 AND id = $2`, userID, addressID)
	a, err := scanAddress(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get address: %w", err)
	}
	return a, nil
}

func (r *ProfileRepository) SaveAddress(ctx context.Context, address *domain.Address) error {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO customer_addresses (
			id, user_id, label, recipient_name, recipient_phone, postal_code,
			address_line1, address_line2, city, province, pccc, is_default, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE SET
			label = EXCLUDED.label,
			recipient_name = EXCLUDED.recipient_name,
			recipient_phone = EXCLUDED.recipient_phone,
			postal_code = EXCLUDED.postal_code,
			address_line1 = EXCLUDED.address_line1,
			address_line2 = EXCLUDED.address_line2,
			city = EXCLUDED.city,
			province = EXCLUDED.province,
			pccc = EXCLUDED.pccc,
			is_default = EXCLUDED.is_default,
			updated_at = EXCLUDED.updated_at
		WHERE customer_addresses.user_id = EXCLUDED.user_id`,
		address.ID, address.UserID, address.Label, address.RecipientName, address.RecipientPhone,
		address.PostalCode, address.AddressLine1, address.AddressLine2, address.City, address.Province,
		address.PCCC, address.IsDefault, address.CreatedAt, address.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("save address: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ports.ErrAddressNotFound
	}
	return nil
}

func (r *ProfileRepository) DeleteAddress(ctx context.Context, userID, addressID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM customer_addresses WHERE user_id = $1 AND id = $2`, userID, addressID,
	)
	if err != nil {
		return fmt.Errorf("delete address: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ports.ErrAddressNotFound
	}
	return nil
}

func (r *ProfileRepository) ClearDefaultAddresses(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE customer_addresses SET is_default = FALSE, updated_at = NOW() WHERE user_id = $1 AND is_default`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("clear default addresses: %w", err)
	}
	return nil
}

func (r *ProfileRepository) NextAddressID(ctx context.Context) (string, error) {
	var seq int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO id_sequences (name, value) VALUES ('address', 1)
		ON CONFLICT (name) DO UPDATE SET value = id_sequences.value + 1
		RETURNING value`).Scan(&seq)
	if err != nil {
		return "", fmt.Errorf("next address id: %w", err)
	}
	return fmt.Sprintf("addr_%06d", seq), nil
}

// DeleteUserData removes userID's profile and all saved addresses in a
// single transaction. There is no FK from customer_addresses/
// customer_profiles to a users table in this database, so this is the only
// cleanup path — it runs in response to shared/pkg/events.UserDeleted.
func (r *ProfileRepository) DeleteUserData(ctx context.Context, userID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete user data: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM customer_addresses WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete user addresses: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM customer_profiles WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete user profile: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete user data: commit: %w", err)
	}
	return nil
}

type addressScanner interface {
	Scan(dest ...any) error
}

func scanAddress(s addressScanner) (*domain.Address, error) {
	var a domain.Address
	err := s.Scan(
		&a.ID, &a.UserID, &a.Label, &a.RecipientName, &a.RecipientPhone, &a.PostalCode,
		&a.AddressLine1, &a.AddressLine2, &a.City, &a.Province, &a.PCCC, &a.IsDefault, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}
