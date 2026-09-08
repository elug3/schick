package pg

import (
	"testing"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
)

func shippingFeeRepo(t *testing.T, schema string) *Repository {
	t.Helper()
	pool := freshSchema(t, requireDSN(t), schema)
	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

func TestMigrateAddsShippingFeeColumns(t *testing.T) {
	const schema = "order_shipping_fee_migrate_test"
	repo := shippingFeeRepo(t, schema)

	for _, table := range []string{"orders", "checkout_sessions"} {
		var count int
		if err := repo.pool.QueryRow(t.Context(), `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = 'shipping_fee_krw'
		`, schema, table).Scan(&count); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s.shipping_fee_krw column count = %d, want 1", table, count)
		}
	}
}

// Every read path has its own SELECT column list; a shipping fee that survives
// Get but not ListAll would show different totals in different views.
func TestOrderShippingFeeSurvivesEveryReadPath(t *testing.T) {
	repo := shippingFeeRepo(t, "order_shipping_fee_roundtrip_test")
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)

	order, err := domain.NewOrder("ord-fee-1", "cust-1", "res-1", []domain.OrderItem{{
		SkuID: "sku-1", SKU: "BAG-001", Quantity: 1, UnitPriceCents: 250000,
	}}, "", 0, 3000, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.Get(ctx, "ord-fee-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ShippingFeeKRW != 3000 || got.TotalCents != 253000 {
		t.Fatalf("Get: shipping = %d total = %d, want 3000 / 253000", got.ShippingFeeKRW, got.TotalCents)
	}

	byCustomer, err := repo.ListByCustomer(ctx, "cust-1")
	if err != nil {
		t.Fatalf("ListByCustomer: %v", err)
	}
	if len(byCustomer) != 1 || byCustomer[0].ShippingFeeKRW != 3000 {
		t.Fatalf("ListByCustomer shipping = %+v, want 3000", byCustomer)
	}

	all, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 || all[0].ShippingFeeKRW != 3000 {
		t.Fatalf("ListAll shipping = %+v, want 3000", all)
	}

	// The expiry sweep reads its own column list too.
	expired, err := repo.ListPendingPaymentExpired(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListPendingPaymentExpired: %v", err)
	}
	if len(expired) != 1 || expired[0].ShippingFeeKRW != 3000 {
		t.Fatalf("ListPendingPaymentExpired shipping = %+v, want 3000", expired)
	}
}

func TestCheckoutSessionShippingFeeRoundTrip(t *testing.T) {
	repo := shippingFeeRepo(t, "order_shipping_fee_session_test")
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)

	session, err := domain.NewCheckoutSession("cs-fee-1", "cust-1", now, time.Hour, 3000)
	if err != nil {
		t.Fatalf("NewCheckoutSession: %v", err)
	}
	if err := session.UpsertItem(domain.OrderItem{
		SkuID: "sku-1", SKU: "BAG-001", Quantity: 1, UnitPriceCents: 250000,
	}, now); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	if err := repo.SaveCheckoutSession(ctx, session); err != nil {
		t.Fatalf("SaveCheckoutSession: %v", err)
	}

	got, err := repo.GetCheckoutSession(ctx, "cs-fee-1")
	if err != nil {
		t.Fatalf("GetCheckoutSession: %v", err)
	}
	if got.ShippingFeeKRW != 3000 {
		t.Fatalf("shipping = %d, want 3000", got.ShippingFeeKRW)
	}
	if got.TotalCents != 253000 {
		t.Fatalf("total = %d, want 253000", got.TotalCents)
	}
}

// The deployment path: an existing orders table predating the column, holding
// rows. Those orders were placed with free delivery and must stay that way.
func TestMigrateOverPreShippingFeeSchemaKeepsRowsIntact(t *testing.T) {
	dsn := requireDSN(t)
	pool := freshSchema(t, dsn, "order_shipping_fee_upgrade_test")
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := pool.Exec(ctx, `
		CREATE TABLE orders (
			id TEXT PRIMARY KEY,
			customer_id TEXT NOT NULL,
			reservation_id TEXT NOT NULL,
			status TEXT NOT NULL,
			coupon_code TEXT NOT NULL DEFAULT '',
			subtotal_cents BIGINT NOT NULL,
			discount_cents BIGINT NOT NULL,
			total_cents BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`); err != nil {
		t.Fatalf("create pre-fee orders table: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (id, customer_id, reservation_id, status, subtotal_cents,
		                    discount_cents, total_cents, created_at, updated_at)
		VALUES ('ord-legacy', 'cust-1', 'res-legacy', 'pending', 250000, 0, 250000, $1, $1)
	`, now); err != nil {
		t.Fatalf("seed legacy order: %v", err)
	}

	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate over pre-fee schema: %v", err)
	}

	got, err := repo.Get(ctx, "ord-legacy")
	if err != nil {
		t.Fatalf("read legacy order after migrate: %v", err)
	}
	if got.ShippingFeeKRW != 0 {
		t.Fatalf("legacy shipping = %d, want 0", got.ShippingFeeKRW)
	}
	if got.TotalCents != 250000 {
		t.Fatalf("legacy total = %d, want 250000 — the migration must not re-price it", got.TotalCents)
	}
}

// Databases that already stored the fee under shipping_fee_cents must keep
// those snapshotted values after the column is renamed.
func TestMigrateRenamesShippingFeeCentsColumn(t *testing.T) {
	dsn := requireDSN(t)
	pool := freshSchema(t, dsn, "order_shipping_fee_rename_test")
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := pool.Exec(ctx, `
		CREATE TABLE orders (
			id TEXT PRIMARY KEY,
			customer_id TEXT NOT NULL,
			reservation_id TEXT NOT NULL,
			status TEXT NOT NULL,
			coupon_code TEXT NOT NULL DEFAULT '',
			subtotal_cents BIGINT NOT NULL,
			discount_cents BIGINT NOT NULL,
			shipping_fee_cents BIGINT NOT NULL DEFAULT 0,
			total_cents BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`); err != nil {
		t.Fatalf("create cents-era orders table: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (id, customer_id, reservation_id, status, subtotal_cents,
		                    discount_cents, shipping_fee_cents, total_cents, created_at, updated_at)
		VALUES ('ord-cents', 'cust-1', 'res-cents', 'pending', 250000, 0, 3000, 253000, $1, $1)
	`, now); err != nil {
		t.Fatalf("seed cents-era order: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE checkout_sessions (
			id TEXT PRIMARY KEY,
			customer_id TEXT NOT NULL,
			status TEXT NOT NULL,
			coupon_code TEXT NOT NULL DEFAULT '',
			subtotal_cents BIGINT NOT NULL DEFAULT 0,
			discount_cents BIGINT NOT NULL DEFAULT 0,
			shipping_fee_cents BIGINT NOT NULL DEFAULT 0,
			total_cents BIGINT NOT NULL DEFAULT 0,
			order_id TEXT NOT NULL DEFAULT '',
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`); err != nil {
		t.Fatalf("create cents-era checkout_sessions table: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO checkout_sessions (id, customer_id, status, shipping_fee_cents, total_cents,
		                               expires_at, created_at, updated_at)
		VALUES ('cs-cents', 'cust-1', 'open', 3000, 3000, $1, $1, $1)
	`, now.Add(time.Hour)); err != nil {
		t.Fatalf("seed cents-era session: %v", err)
	}

	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate rename: %v", err)
	}

	for _, table := range []string{"orders", "checkout_sessions"} {
		var krw, cents int
		if err := repo.pool.QueryRow(ctx, `
			SELECT
				count(*) FILTER (WHERE column_name = 'shipping_fee_krw'),
				count(*) FILTER (WHERE column_name = 'shipping_fee_cents')
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1
		`, table).Scan(&krw, &cents); err != nil {
			t.Fatalf("inspect %s columns: %v", table, err)
		}
		if krw != 1 || cents != 0 {
			t.Fatalf("%s columns: shipping_fee_krw=%d shipping_fee_cents=%d, want 1 / 0", table, krw, cents)
		}
	}

	got, err := repo.Get(ctx, "ord-cents")
	if err != nil {
		t.Fatalf("Get after rename: %v", err)
	}
	if got.ShippingFeeKRW != 3000 || got.TotalCents != 253000 {
		t.Fatalf("renamed order shipping = %d total = %d, want 3000 / 253000", got.ShippingFeeKRW, got.TotalCents)
	}

	session, err := repo.GetCheckoutSession(ctx, "cs-cents")
	if err != nil {
		t.Fatalf("GetCheckoutSession after rename: %v", err)
	}
	if session.ShippingFeeKRW != 3000 {
		t.Fatalf("renamed session shipping = %d, want 3000", session.ShippingFeeKRW)
	}
}
