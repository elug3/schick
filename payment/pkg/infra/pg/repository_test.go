package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/jackc/pgx/v4/pgxpool"
)

// requireDSN returns the test database DSN, skipping when Postgres is not available.
func requireDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set; skipping postgres integration test")
	}
	return dsn
}

// freshSchema gives the test an empty schema so migrate() runs the way it does
// against a brand-new database. search_path is set on the pool config rather than
// with a SET statement, so it holds for every connection the pool opens.
func freshSchema(t *testing.T, dsn, schema string) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()

	admin, err := pgxpool.Connect(ctx, withPostgresSSLMode(dsn))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
		`CREATE SCHEMA ` + schema,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("prepare schema (%s): %v", stmt, err)
		}
	}

	cfg, err := pgxpool.ParseConfig(withPostgresSSLMode(dsn))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect with search_path: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		// t.Context() is canceled just before cleanups run, so reconnecting with
		// it always fails and the schema is never dropped — the leak accumulates
		// one schema per test per run in the shared dev database. Use a fresh
		// context, and report failures instead of swallowing them.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgxpool.Connect(cleanupCtx, withPostgresSSLMode(dsn))
		if err != nil {
			t.Errorf("cleanup: connect to drop schema %s: %v", schema, err)
			return
		}
		defer conn.Close()
		if _, err := conn.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Errorf("cleanup: drop schema %s: %v", schema, err)
		}
	})
	return pool
}

func migratedRepo(t *testing.T, schema string) *Repository {
	t.Helper()
	pool := freshSchema(t, requireDSN(t), schema)
	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

// succeededPayment stores a card payment that has been captured, ready to cancel.
func succeededPayment(t *testing.T, repo *Repository, id string, amountCents int64) *domain.Payment {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p, err := domain.NewPayment(id, "ord_"+id, "cust_1", amountCents,
		domain.DefaultCurrency, domain.ProviderNano, "2409030071109", "", now)
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}
	p.MarkSucceeded(now)
	if err := repo.Save(t.Context(), p); err != nil {
		t.Fatalf("save payment: %v", err)
	}
	return p
}

func TestMigrateAddsCancelColumns(t *testing.T) {
	const schema = "payment_migrate_cancel_test"
	repo := migratedRepo(t, schema)

	for _, column := range []string{
		"canceled_amount_cents", "canceled_at", "cancel_reason",
		"canceled_by", "cancel_idempotency_key",
	} {
		var count int
		// Scoped to this test's schema — every other test schema also holds a
		// payments table, so an unscoped count would match all of them.
		if err := repo.pool.QueryRow(t.Context(), `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = 'payments' AND column_name = $2
		`, schema, column).Scan(&count); err != nil {
			t.Fatalf("inspect %s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("payments.%s column count = %d, want 1", column, count)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	repo := migratedRepo(t, "payment_migrate_idempotent_test")
	if err := repo.migrate(); err != nil {
		t.Fatalf("second migrate returned error: %v", err)
	}
	if err := repo.migrate(); err != nil {
		t.Fatalf("third migrate returned error: %v", err)
	}
}

// The real deployment path: an existing payments table predating the cancel
// columns, holding rows. The additive migration must leave those rows readable,
// with the new columns defaulting rather than erroring on NULL.
func TestMigrateOverPreCancelSchemaKeepsExistingRowsReadable(t *testing.T) {
	dsn := requireDSN(t)
	pool := freshSchema(t, dsn, "payment_migrate_upgrade_test")
	ctx := t.Context()

	// The payments table exactly as it stood before the cancel work.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE payments (
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
			updated_at      TIMESTAMPTZ NOT NULL,
			payer_name      TEXT,
			payer_phone     TEXT,
			payer_email     TEXT
		)`); err != nil {
		t.Fatalf("create pre-cancel table: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO payments (
			id, order_id, customer_id, amount_cents, currency, status, method,
			provider, provider_ref, expires_at, created_at, updated_at
		) VALUES ('pay_legacy', 'ord_legacy', 'cust_1', 70000, 'krw', 'succeeded',
		          'credit_card', 'nano', '2409030071109', $1, $1, $1)
	`, now); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate over pre-cancel schema: %v", err)
	}

	got, err := repo.Get(ctx, "pay_legacy")
	if err != nil {
		t.Fatalf("read legacy row after migrate: %v", err)
	}
	if got.Status != domain.StatusSucceeded || got.AmountCents != 70000 {
		t.Fatalf("legacy row = %q / %d", got.Status, got.AmountCents)
	}
	if got.CanceledAmountCents != 0 {
		t.Fatalf("canceled_amount_cents = %d, want 0 for a legacy row", got.CanceledAmountCents)
	}
	if got.CanceledAt != nil {
		t.Fatalf("canceled_at = %v, want nil for a legacy row", got.CanceledAt)
	}
	if got.CancelReason != "" || got.CanceledBy != "" || got.CancelIdempotencyKey != "" {
		t.Fatalf("cancel text fields should be empty: %q / %q / %q",
			got.CancelReason, got.CanceledBy, got.CancelIdempotencyKey)
	}
	// A legacy row must still be cancelable — that is the whole point of the upgrade.
	if !got.Cancelable() {
		t.Fatal("legacy succeeded row must be cancelable after migrate")
	}
}

func TestSaveAndLoadFullCancel(t *testing.T) {
	repo := migratedRepo(t, "payment_full_cancel_test")
	ctx := t.Context()
	p := succeededPayment(t, repo, "pay_000001", 70000)

	canceledAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := p.ApplyCancel(70000, "ops reject", "mgr_1", canceledAt); err != nil {
		t.Fatalf("ApplyCancel: %v", err)
	}
	p.CancelIdempotencyKey = "cancel-key-1"
	if err := repo.Save(ctx, p); err != nil {
		t.Fatalf("save cancel: %v", err)
	}

	got, err := repo.Get(ctx, "pay_000001")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
	if got.CanceledAmountCents != 70000 {
		t.Fatalf("canceled_amount_cents = %d, want 70000", got.CanceledAmountCents)
	}
	if got.CancelReason != "ops reject" || got.CanceledBy != "mgr_1" {
		t.Fatalf("audit = %q / %q", got.CancelReason, got.CanceledBy)
	}
	if got.CancelIdempotencyKey != "cancel-key-1" {
		t.Fatalf("cancel key = %q", got.CancelIdempotencyKey)
	}
	if got.CanceledAt == nil || !got.CanceledAt.Equal(canceledAt) {
		t.Fatalf("canceled_at = %v, want %v", got.CanceledAt, canceledAt)
	}
	if got.RemainingCancelableCents() != 0 {
		t.Fatalf("remaining = %d, want 0", got.RemainingCancelableCents())
	}
}

// A partial cancel keeps the payment succeeded, so it must survive the round
// trip with its reduced balance intact and stay cancelable.
func TestSaveAndLoadPartialCancel(t *testing.T) {
	repo := migratedRepo(t, "payment_partial_cancel_test")
	ctx := t.Context()
	p := succeededPayment(t, repo, "pay_000002", 70000)

	if err := p.ApplyCancel(20000, "partial", "mgr_1", time.Now().UTC()); err != nil {
		t.Fatalf("ApplyCancel: %v", err)
	}
	if err := repo.Save(ctx, p); err != nil {
		t.Fatalf("save partial cancel: %v", err)
	}

	got, err := repo.Get(ctx, "pay_000002")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != domain.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded after a partial cancel", got.Status)
	}
	if got.CanceledAmountCents != 20000 {
		t.Fatalf("canceled = %d, want 20000", got.CanceledAmountCents)
	}
	if got.RemainingCancelableCents() != 50000 {
		t.Fatalf("remaining = %d, want 50000", got.RemainingCancelableCents())
	}
	if !got.Cancelable() {
		t.Fatal("partially canceled payment must stay cancelable")
	}
}

// Cancels accumulate across saves, which is what a second partial refund does.
func TestPartialCancelsAccumulateAcrossSaves(t *testing.T) {
	repo := migratedRepo(t, "payment_cancel_accumulate_test")
	ctx := t.Context()
	p := succeededPayment(t, repo, "pay_000003", 70000)

	if err := p.ApplyCancel(20000, "first", "mgr_1", time.Now().UTC()); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if err := repo.Save(ctx, p); err != nil {
		t.Fatalf("save first: %v", err)
	}
	reloaded, err := repo.Get(ctx, "pay_000003")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := reloaded.ApplyCancel(50000, "second", "mgr_2", time.Now().UTC()); err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if err := repo.Save(ctx, reloaded); err != nil {
		t.Fatalf("save second: %v", err)
	}

	got, err := repo.Get(ctx, "pay_000003")
	if err != nil {
		t.Fatalf("final reload: %v", err)
	}
	if got.CanceledAmountCents != 70000 {
		t.Fatalf("canceled = %d, want 70000 cumulative", got.CanceledAmountCents)
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled once fully refunded", got.Status)
	}
}

// The status CHECK constraint has always listed 'canceled'; now that a real code
// path writes it, confirm the database actually accepts it.
func TestCanceledStatusPassesCheckConstraint(t *testing.T) {
	repo := migratedRepo(t, "payment_cancel_constraint_test")
	ctx := t.Context()
	p := succeededPayment(t, repo, "pay_000004", 1000)

	if err := p.ApplyCancel(1000, "", "", time.Now().UTC()); err != nil {
		t.Fatalf("ApplyCancel: %v", err)
	}
	if err := repo.Save(ctx, p); err != nil {
		t.Fatalf("the payments_status_check constraint rejected 'canceled': %v", err)
	}

	var status string
	if err := repo.pool.QueryRow(ctx, `SELECT status FROM payments WHERE id = 'pay_000004'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "canceled" {
		t.Fatalf("stored status = %q, want canceled", status)
	}
}

// A full cancel frees the one-succeeded-per-order unique index, so the row must
// not collide with itself on the way out of succeeded.
func TestFullCancelReleasesSucceededUniqueIndex(t *testing.T) {
	repo := migratedRepo(t, "payment_cancel_unique_test")
	ctx := t.Context()
	p := succeededPayment(t, repo, "pay_000005", 5000)

	if err := p.ApplyCancel(5000, "", "", time.Now().UTC()); err != nil {
		t.Fatalf("ApplyCancel: %v", err)
	}
	if err := repo.Save(ctx, p); err != nil {
		t.Fatalf("save cancel: %v", err)
	}
	if _, err := repo.FindSucceededByOrderID(ctx, p.OrderID); err == nil {
		t.Fatal("a fully canceled payment must not read as the order's succeeded payment")
	}
}

// SaveWithOutbox is the path CancelPayment actually uses; the payment write and
// the payment.canceled row must land together.
func TestSaveWithOutboxPersistsCancelAndEvent(t *testing.T) {
	repo := migratedRepo(t, "payment_cancel_outbox_test")
	ctx := t.Context()
	p := succeededPayment(t, repo, "pay_000006", 9000)

	if err := p.ApplyCancel(9000, "ops reject", "mgr_1", time.Now().UTC()); err != nil {
		t.Fatalf("ApplyCancel: %v", err)
	}
	if err := repo.SaveWithOutbox(ctx, p, []ports.OutboxEvent{{
		AggregateID: p.ID,
		Subject:     ports.PaymentCanceledSubject,
		Payload:     []byte(`{"event_type":"payment.canceled","payment_id":"pay_000006"}`),
	}}); err != nil {
		t.Fatalf("SaveWithOutbox: %v", err)
	}

	got, err := repo.Get(ctx, "pay_000006")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}

	pending, err := repo.ListPendingOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingOutbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending outbox rows = %d, want 1", len(pending))
	}
	if pending[0].Subject != ports.PaymentCanceledSubject {
		t.Fatalf("subject = %q, want %q", pending[0].Subject, ports.PaymentCanceledSubject)
	}
}

// payer_* columns were added for NANO compOrderMem (PR #244). A round-trip must
// survive Save/Get — nanoCheckout reads them when building the PG request.
func TestPayerFieldsRoundTrip(t *testing.T) {
	repo := migratedRepo(t, "payment_payer_fields_test")
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p, err := domain.NewPayment("pay_payer", "ord_payer", "cust_1", 70000,
		domain.DefaultCurrency, domain.ProviderNano, "2409030071109",
		"http://localhost:8080/api/v1/payments/pay_payer/nano/checkout", now)
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}
	p.PayerName = "홍길동"
	p.PayerPhone = "01012345678"
	p.PayerEmail = "buyer@dupli1.com"
	if err := repo.Save(ctx, p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.Get(ctx, "pay_payer")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PayerName != "홍길동" || got.PayerPhone != "01012345678" || got.PayerEmail != "buyer@dupli1.com" {
		t.Fatalf("payer fields = %q / %q / %q", got.PayerName, got.PayerPhone, got.PayerEmail)
	}
}
