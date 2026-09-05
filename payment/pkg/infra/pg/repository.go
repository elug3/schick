package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(connString string) (*Repository, error) {
	connString = withPostgresSSLMode(connString)
	// Pool connect at process start; no request context available.
	pool, err := pgxpool.Connect(context.Background(), connString)
	if err != nil {
		return nil, fmt.Errorf("connect payment database: %w", err)
	}

	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		pool.Close()
		return nil, err
	}
	return repo, nil
}

func (r *Repository) Close() {
	if r.pool != nil {
		r.pool.Close()
	}
}

func (r *Repository) migrate() error {
	// Startup schema migration; no request-scoped context to propagate.
	ctx := context.Background()
	stmts := []string{
		`CREATE SEQUENCE IF NOT EXISTS payment_id_seq`,
		`CREATE TABLE IF NOT EXISTS payments (
			id              TEXT PRIMARY KEY,
			order_id        TEXT NOT NULL,
			customer_id     TEXT NOT NULL,
			amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
			currency        TEXT NOT NULL,
			status          TEXT NOT NULL,
			method          TEXT NOT NULL DEFAULT 'credit_card',
			provider        TEXT NOT NULL,
			provider_ref    TEXT NOT NULL,
			checkout_url    TEXT,
			created_by      TEXT,
			note            TEXT,
			idempotency_key TEXT UNIQUE,
			expires_at      TIMESTAMPTZ NOT NULL,
			created_at      TIMESTAMPTZ NOT NULL,
			updated_at      TIMESTAMPTZ NOT NULL
		)`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS method TEXT NOT NULL DEFAULT 'credit_card'`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS created_by TEXT`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS note TEXT`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS payer_name TEXT`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS payer_phone TEXT`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS payer_email TEXT`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS canceled_amount_cents BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS canceled_at TIMESTAMPTZ`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS cancel_reason TEXT`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS canceled_by TEXT`,
		`ALTER TABLE payments ADD COLUMN IF NOT EXISTS cancel_idempotency_key TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_payments_provider_ref ON payments(provider_ref)`,
		`CREATE INDEX IF NOT EXISTS idx_payments_order_id ON payments(order_id)`,
		`CREATE INDEX IF NOT EXISTS idx_payments_customer_id ON payments(customer_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_one_open_per_order ON payments(order_id) WHERE status = 'requires_payment'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_one_succeeded_per_order ON payments(order_id) WHERE status = 'succeeded'`,
		`CREATE INDEX IF NOT EXISTS idx_payments_succeeded_updated ON payments(updated_at) WHERE status = 'succeeded'`,
		`CREATE TABLE IF NOT EXISTS payment_outbox (
			id BIGSERIAL PRIMARY KEY,
			aggregate_id TEXT NOT NULL,
			subject TEXT NOT NULL,
			payload JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			published_at TIMESTAMPTZ,
			attempts INT NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_payment_outbox_pending ON payment_outbox (created_at) WHERE published_at IS NULL`,
	}
	for _, stmt := range stmts {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate payment schema: %w", err)
		}
	}

	// status is otherwise plain TEXT with validity enforced only in Go
	// (domain.PaymentStatus); this CHECK makes an invalid value fail loudly at
	// write time instead of silently corrupting state. Postgres has no ADD
	// CONSTRAINT IF NOT EXISTS, so existence is checked against pg_constraint first.
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname = 'payments_status_check')`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check payments_status_check: %w", err)
	}
	if !exists {
		if _, err := r.pool.Exec(ctx, `
			ALTER TABLE payments ADD CONSTRAINT payments_status_check
			CHECK (status IN ('requires_payment', 'succeeded', 'failed', 'canceled', 'expired'))
		`); err != nil {
			return fmt.Errorf("add payments_status_check: %w", err)
		}
	}
	return nil
}

func (r *Repository) NextPaymentID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var id string
	err := r.pool.QueryRow(ctx,
		`SELECT 'pay_' || LPAD(nextval('payment_id_seq')::text, 6, '0')`,
	).Scan(&id)
	return id, err
}

func (r *Repository) Save(ctx context.Context, payment *domain.Payment) error {
	return r.SaveWithOutbox(ctx, payment, nil)
}

func (r *Repository) SaveWithOutbox(ctx context.Context, payment *domain.Payment, events []ports.OutboxEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := persistPaymentTx(ctx, tx, payment, events); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) MutateLocked(ctx context.Context, id string, mutate func(*domain.Payment) ([]ports.OutboxEvent, error)) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, paymentSelect+` WHERE id = $1 FOR UPDATE`, id)
	payment, err := scanPayment(row)
	if err != nil {
		return nil, err
	}
	events, err := mutate(payment)
	if err != nil {
		return nil, err
	}
	if err := persistPaymentTx(ctx, tx, payment, events); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return payment, nil
}

func persistPaymentTx(ctx context.Context, tx pgx.Tx, payment *domain.Payment, events []ports.OutboxEvent) error {
	var idempotencyKey any
	if payment.IdempotencyKey != "" {
		idempotencyKey = payment.IdempotencyKey
	}
	method := payment.Method
	if method == "" {
		method = domain.MethodCreditCard
	}
	var createdBy any
	if payment.CreatedBy != "" {
		createdBy = payment.CreatedBy
	}
	var note any
	if payment.Note != "" {
		note = payment.Note
	}
	var canceledAt any
	if payment.CanceledAt != nil {
		canceledAt = *payment.CanceledAt
	}
	var cancelReason, canceledByUser, cancelKey any
	if payment.CancelReason != "" {
		cancelReason = payment.CancelReason
	}
	if payment.CanceledBy != "" {
		canceledByUser = payment.CanceledBy
	}
	if payment.CancelIdempotencyKey != "" {
		cancelKey = payment.CancelIdempotencyKey
	}
	var payerName, payerPhone, payerEmail any
	if payment.PayerName != "" {
		payerName = payment.PayerName
	}
	if payment.PayerPhone != "" {
		payerPhone = payment.PayerPhone
	}
	if payment.PayerEmail != "" {
		payerEmail = payment.PayerEmail
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO payments (
			id, order_id, customer_id, amount_cents, currency, status, method,
			provider, provider_ref, checkout_url, created_by, note,
			payer_name, payer_phone, payer_email, idempotency_key,
			expires_at, created_at, updated_at,
			canceled_amount_cents, canceled_at, cancel_reason, canceled_by,
			cancel_idempotency_key
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			provider_ref = EXCLUDED.provider_ref,
			checkout_url = EXCLUDED.checkout_url,
			method = EXCLUDED.method,
			created_by = EXCLUDED.created_by,
			note = EXCLUDED.note,
			payer_name = EXCLUDED.payer_name,
			payer_phone = EXCLUDED.payer_phone,
			payer_email = EXCLUDED.payer_email,
			updated_at = EXCLUDED.updated_at,
			canceled_amount_cents = EXCLUDED.canceled_amount_cents,
			canceled_at = EXCLUDED.canceled_at,
			cancel_reason = EXCLUDED.cancel_reason,
			canceled_by = EXCLUDED.canceled_by,
			cancel_idempotency_key = EXCLUDED.cancel_idempotency_key
	`, payment.ID, payment.OrderID, payment.CustomerID, payment.AmountCents, payment.Currency,
		string(payment.Status), method, payment.Provider, payment.ProviderRef, payment.CheckoutURL,
		createdBy, note, payerName, payerPhone, payerEmail, idempotencyKey,
		payment.ExpiresAt, payment.CreatedAt, payment.UpdatedAt,
		payment.CanceledAmountCents, canceledAt, cancelReason, canceledByUser, cancelKey)
	if err != nil {
		return err
	}

	for _, ev := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_outbox (aggregate_id, subject, payload)
			VALUES ($1, $2, $3)
		`, ev.AggregateID, ev.Subject, ev.Payload); err != nil {
			return fmt.Errorf("enqueue outbox: %w", err)
		}
	}
	return nil
}

func (r *Repository) ListSucceededSince(ctx context.Context, since time.Time, limit int) ([]domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, paymentSelect+`
		WHERE status = 'succeeded' AND updated_at >= $1
		ORDER BY updated_at ASC
		LIMIT $2
	`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *Repository) ListPendingOutbox(ctx context.Context, limit int) ([]ports.OutboxMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, aggregate_id, subject, payload, created_at, attempts, last_error
		FROM payment_outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ports.OutboxMessage
	for rows.Next() {
		var m ports.OutboxMessage
		if err := rows.Scan(&m.ID, &m.AggregateID, &m.Subject, &m.Payload, &m.CreatedAt, &m.Attempts, &m.LastError); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repository) MarkOutboxPublished(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE payment_outbox SET published_at = NOW(), last_error = '' WHERE id = $1
	`, id)
	return err
}

func (r *Repository) RecordOutboxAttempt(ctx context.Context, id int64, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE payment_outbox
		SET attempts = attempts + 1, last_error = $2
		WHERE id = $1
	`, id, errMsg)
	return err
}

func (r *Repository) Get(ctx context.Context, id string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, paymentSelect+` WHERE id = $1`, id)
	return scanPayment(row)
}

func (r *Repository) GetByProviderRef(ctx context.Context, providerRef string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, paymentSelect+` WHERE provider_ref = $1`, providerRef)
	return scanPayment(row)
}

func (r *Repository) FindByIdempotencyKey(ctx context.Context, key string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, paymentSelect+` WHERE idempotency_key = $1`, key)
	return scanPayment(row)
}

func (r *Repository) FindRequiresPaymentByOrderID(ctx context.Context, orderID string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, paymentSelect+`
		WHERE order_id = $1 AND status = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, orderID, string(domain.StatusRequiresPayment))
	return scanPayment(row)
}

func (r *Repository) FindSucceededByOrderID(ctx context.Context, orderID string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, paymentSelect+`
		WHERE order_id = $1 AND status = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, orderID, string(domain.StatusSucceeded))
	return scanPayment(row)
}

const paymentSelect = `
	SELECT id, order_id, customer_id, amount_cents, currency, status,
	       COALESCE(method, 'credit_card'),
	       provider, provider_ref, COALESCE(checkout_url, ''),
	       COALESCE(created_by, ''), COALESCE(note, ''),
	       COALESCE(payer_name, ''), COALESCE(payer_phone, ''), COALESCE(payer_email, ''),
	       COALESCE(idempotency_key, ''),
	       expires_at, created_at, updated_at,
	       COALESCE(canceled_amount_cents, 0), canceled_at,
	       COALESCE(cancel_reason, ''), COALESCE(canceled_by, ''),
	       COALESCE(cancel_idempotency_key, '')
	FROM payments`

func scanPayment(row pgx.Row) (*domain.Payment, error) {
	var p domain.Payment
	var status string
	var idempotencyKey string
	err := row.Scan(
		&p.ID, &p.OrderID, &p.CustomerID, &p.AmountCents, &p.Currency, &status,
		&p.Method, &p.Provider, &p.ProviderRef, &p.CheckoutURL,
		&p.CreatedBy, &p.Note, &p.PayerName, &p.PayerPhone, &p.PayerEmail,
		&idempotencyKey, &p.ExpiresAt, &p.CreatedAt, &p.UpdatedAt,
		&p.CanceledAmountCents, &p.CanceledAt,
		&p.CancelReason, &p.CanceledBy, &p.CancelIdempotencyKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Status = domain.PaymentStatus(status)
	p.IdempotencyKey = idempotencyKey
	return &p, nil
}

var _ ports.Repository = (*Repository)(nil)
