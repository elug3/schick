package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
	"github.com/elug3/dupli1/order/pkg/ports"
	"github.com/jackc/pgtype"
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
		return nil, fmt.Errorf("connect order database: %w", err)
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
		`CREATE TABLE IF NOT EXISTS id_sequences (
			name TEXT PRIMARY KEY,
			value BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS orders (
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
		)`,
		`CREATE INDEX IF NOT EXISTS idx_orders_customer_id ON orders(customer_id)`,
		`CREATE TABLE IF NOT EXISTS order_items (
			order_id TEXT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
			sku TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			unit_price_cents BIGINT NOT NULL,
			PRIMARY KEY (order_id, sku)
		)`,
		`CREATE TABLE IF NOT EXISTS checkout_sessions (
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
		)`,
		`CREATE TABLE IF NOT EXISTS checkout_session_items (
			session_id TEXT NOT NULL REFERENCES checkout_sessions(id) ON DELETE CASCADE,
			sku TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			unit_price_cents BIGINT NOT NULL,
			PRIMARY KEY (session_id, sku)
		)`,
		`CREATE TABLE IF NOT EXISTS order_idempotency_keys (
			customer_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			order_id TEXT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
			request_hash TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (customer_id, idempotency_key)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_order_idempotency_order_id ON order_idempotency_keys(order_id)`,
		`CREATE INDEX IF NOT EXISTS idx_order_idempotency_created_at ON order_idempotency_keys(created_at)`,
		`CREATE TABLE IF NOT EXISTS order_outbox (
			id BIGSERIAL PRIMARY KEY,
			aggregate_id TEXT NOT NULL,
			subject TEXT NOT NULL,
			payload JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			published_at TIMESTAMPTZ,
			attempts INT NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_order_outbox_pending ON order_outbox (created_at) WHERE published_at IS NULL`,
	}
	for _, stmt := range stmts {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate order schema: %w", err)
		}
	}

	// Status columns are otherwise plain TEXT with validity enforced only in
	// Go (domain.OrderStatus / domain.CheckoutSessionStatus); these CHECK
	// constraints make an invalid value fail loudly at write time instead of
	// silently corrupting state that every downstream query assumes is clean.
	// Postgres has no ADD CONSTRAINT IF NOT EXISTS, so existence is checked
	// against pg_constraint first (same pattern as product's SKU-master FKs).
	checks := []struct{ name, sql string }{
		{"orders_status_check", `
			ALTER TABLE orders ADD CONSTRAINT orders_status_check
			CHECK (status IN ('pending', 'paid', 'in_transit', 'fulfilled', 'canceled'))`},
		{"checkout_sessions_status_check", `
			ALTER TABLE checkout_sessions ADD CONSTRAINT checkout_sessions_status_check
			CHECK (status IN ('open', 'completed', 'expired'))`},
	}
	for _, c := range checks {
		if err := r.addConstraintIfMissing(ctx, c.name, c.sql); err != nil {
			return fmt.Errorf("migrate order schema: %w", err)
		}
	}
	alterStmts := []string{
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS shipping_fee_cents BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE checkout_sessions ADD COLUMN IF NOT EXISTS shipping_fee_cents BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS paid_at TIMESTAMPTZ`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_due_at TIMESTAMPTZ`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS shipped_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS shipped_at TIMESTAMPTZ`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS recipient_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS recipient_phone TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS shipping_address JSONB NOT NULL DEFAULT '{}'`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS source_address_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS carrier TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS tracking_number TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS carrier_note TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE order_items ADD COLUMN IF NOT EXISTS sku_id TEXT`,
		`ALTER TABLE order_items ADD COLUMN IF NOT EXISTS product_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE order_items ADD COLUMN IF NOT EXISTS image_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE checkout_session_items ADD COLUMN IF NOT EXISTS sku_id TEXT`,
	}
	for _, stmt := range alterStmts {
		_, _ = r.pool.Exec(ctx, stmt)
	}
	_, _ = r.pool.Exec(ctx, `UPDATE orders SET payment_due_at = created_at + INTERVAL '5 minutes' WHERE payment_due_at IS NULL`)

	// Indexes over columns the ALTERs above add. Creating these alongside the base
	// tables would fail on a fresh database, where orders has no payment_due_at yet.
	postAlterStmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_orders_pending_payment_due_at ON orders(payment_due_at) WHERE status = 'pending'`,
	}
	for _, stmt := range postAlterStmts {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate order schema: %w", err)
		}
	}

	if err := r.promoteSkuIDPrimaryKey(ctx, "order_items", "order_id"); err != nil {
		return err
	}
	if err := r.promoteSkuIDPrimaryKey(ctx, "checkout_session_items", "session_id"); err != nil {
		return err
	}
	return nil
}

// promoteSkuIDPrimaryKey swaps table's primary key from (parentCol, sku) to
// (parentCol, sku_id), unlike cart's ephemeral data this table holds
// permanent historical/financial records, so rows can't simply be purged.
// Promotion is gated on every row already having a sku_id — cmd/backfill-sku-id
// resolves historical sku strings via product's API first. Until that's
// complete, this is a no-op on every startup (logged once, not an error) and
// the table stays on its legacy (parentCol, sku) key.
func (r *Repository) promoteSkuIDPrimaryKey(ctx context.Context, table, parentCol string) error {
	var pkColumns []string
	rows, err := r.pool.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey, a.attnum)
	`, table)
	if err != nil {
		return fmt.Errorf("check %s primary key: %w", table, err)
	}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			rows.Close()
			return fmt.Errorf("check %s primary key: %w", table, err)
		}
		pkColumns = append(pkColumns, col)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check %s primary key: %w", table, err)
	}
	if len(pkColumns) == 2 && pkColumns[1] == "sku_id" {
		return nil
	}

	var remaining int
	if err := r.pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE sku_id IS NULL`, table)).Scan(&remaining); err != nil {
		return fmt.Errorf("count unresolved %s rows: %w", table, err)
	}
	if remaining > 0 {
		log.Printf("order: %s has %d row(s) with no sku_id yet; run cmd/backfill-sku-id, then restart to promote the primary key (staying on legacy (%s, sku) key for now)",
			table, remaining, parentCol)
		return nil
	}

	if _, err := r.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN sku_id SET NOT NULL`, table)); err != nil {
		return fmt.Errorf("set %s.sku_id not null: %w", table, err)
	}
	if _, err := r.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT %s_pkey`, table, table)); err != nil {
		return fmt.Errorf("drop legacy %s pkey: %w", table, err)
	}
	if _, err := r.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ADD PRIMARY KEY (%s, sku_id)`, table, parentCol)); err != nil {
		return fmt.Errorf("promote %s primary key: %w", table, err)
	}
	log.Printf("order: promoted %s primary key to (%s, sku_id)", table, parentCol)
	return nil
}

// addConstraintIfMissing adds a named constraint via sql if it doesn't
// already exist. Postgres has no ADD CONSTRAINT IF NOT EXISTS, so existence
// is checked against pg_constraint first — safe to call on every startup.
func (r *Repository) addConstraintIfMissing(ctx context.Context, name, sql string) error {
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname = $1)`, name,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check constraint %s: %w", name, err)
	}
	if exists {
		return nil
	}
	if _, err := r.pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("add constraint %s: %w", name, err)
	}
	return nil
}

func (r *Repository) NextOrderID(ctx context.Context) (string, error) {
	return r.nextID(ctx, "order", "ord")
}

func (r *Repository) NextCheckoutSessionID(ctx context.Context) (string, error) {
	return r.nextID(ctx, "checkout_session", "cs")
}

func (r *Repository) nextID(ctx context.Context, name, prefix string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var seq int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO id_sequences (name, value) VALUES ($1, 1)
		ON CONFLICT (name) DO UPDATE SET value = id_sequences.value + 1
		RETURNING value
	`, name).Scan(&seq)
	if err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return fmt.Sprintf("%s_%06d", prefix, seq), nil
}

func (r *Repository) Save(ctx context.Context, order *domain.Order) error {
	return r.SaveWithOutbox(ctx, order, nil, nil)
}

func (r *Repository) SaveWithOutbox(ctx context.Context, order *domain.Order, idem *ports.IdempotencyRecord, events []ports.OutboxEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	shippingJSON, err := json.Marshal(order.ShippingAddress)
	if err != nil {
		return fmt.Errorf("marshal shipping address: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO orders (
			id, customer_id, reservation_id, status, coupon_code,
			subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			recipient_name, recipient_phone, shipping_address, source_address_id,
			payment_id, paid_at, payment_due_at, shipped_by, shipped_at, carrier, tracking_number, carrier_note,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
		ON CONFLICT (id) DO UPDATE SET
			customer_id = EXCLUDED.customer_id,
			reservation_id = EXCLUDED.reservation_id,
			status = EXCLUDED.status,
			coupon_code = EXCLUDED.coupon_code,
			subtotal_cents = EXCLUDED.subtotal_cents,
			discount_cents = EXCLUDED.discount_cents,
			shipping_fee_cents = EXCLUDED.shipping_fee_cents,
			total_cents = EXCLUDED.total_cents,
			recipient_name = EXCLUDED.recipient_name,
			recipient_phone = EXCLUDED.recipient_phone,
			shipping_address = EXCLUDED.shipping_address,
			source_address_id = EXCLUDED.source_address_id,
			payment_id = EXCLUDED.payment_id,
			paid_at = EXCLUDED.paid_at,
			payment_due_at = EXCLUDED.payment_due_at,
			shipped_by = EXCLUDED.shipped_by,
			shipped_at = EXCLUDED.shipped_at,
			carrier = EXCLUDED.carrier,
			tracking_number = EXCLUDED.tracking_number,
			carrier_note = EXCLUDED.carrier_note,
			updated_at = EXCLUDED.updated_at
	`, order.ID, order.CustomerID, order.ReservationID, order.Status, order.CouponCode,
		order.SubtotalCents, order.DiscountCents, order.ShippingFeeCents, order.TotalCents,
		order.RecipientName, order.RecipientPhone, shippingJSON, order.SourceAddressID,
		order.PaymentID, order.PaidAt, order.PaymentDueAt, order.ShippedBy, order.ShippedAt,
		order.Carrier, order.TrackingNumber, order.CarrierNote,
		order.CreatedAt, order.UpdatedAt)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM order_items WHERE order_id = $1`, order.ID); err != nil {
		return err
	}
	for _, item := range order.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (order_id, sku, sku_id, quantity, unit_price_cents, product_name, image_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, order.ID, item.SKU, nullIfEmpty(item.SkuID), item.Quantity, item.UnitPriceCents, item.ProductName, item.ImageURL); err != nil {
			return err
		}
	}

	if idem != nil && idem.Key != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_idempotency_keys (customer_id, idempotency_key, order_id, request_hash)
			VALUES ($1, $2, $3, $4)
		`, idem.CustomerID, idem.Key, idem.OrderID, idem.RequestHash); err != nil {
			return fmt.Errorf("save idempotency key: %w", err)
		}
	}

	for _, ev := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_outbox (aggregate_id, subject, payload)
			VALUES ($1, $2, $3)
		`, ev.AggregateID, ev.Subject, ev.Payload); err != nil {
			return fmt.Errorf("enqueue outbox: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (r *Repository) FindByIdempotencyKey(ctx context.Context, customerID, key string) (*ports.IdempotencyRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	customerID = strings.TrimSpace(customerID)
	key = strings.TrimSpace(key)
	if customerID == "" || key == "" {
		return nil, ports.ErrNotFound
	}
	var rec ports.IdempotencyRecord
	err := r.pool.QueryRow(ctx, `
		SELECT customer_id, idempotency_key, order_id, request_hash
		FROM order_idempotency_keys
		WHERE customer_id = $1 AND idempotency_key = $2
	`, customerID, key).Scan(&rec.CustomerID, &rec.Key, &rec.OrderID, &rec.RequestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
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
		FROM order_outbox
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
		UPDATE order_outbox SET published_at = NOW(), last_error = '' WHERE id = $1
	`, id)
	return err
}

func (r *Repository) RecordOutboxAttempt(ctx context.Context, id int64, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE order_outbox
		SET attempts = attempts + 1, last_error = $2
		WHERE id = $1
	`, id, errMsg)
	return err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *Repository) Get(ctx context.Context, id string) (*domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var order domain.Order
	var paidAt, shippedAt *time.Time
	var shippingJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, customer_id, reservation_id, status, coupon_code,
			subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			recipient_name, recipient_phone, shipping_address, source_address_id,
			payment_id, paid_at, payment_due_at, shipped_by, shipped_at, carrier, tracking_number, carrier_note,
			created_at, updated_at
		FROM orders WHERE id = $1
	`, id).Scan(
		&order.ID, &order.CustomerID, &order.ReservationID, &order.Status, &order.CouponCode,
		&order.SubtotalCents, &order.DiscountCents, &order.ShippingFeeCents, &order.TotalCents,
		&order.RecipientName, &order.RecipientPhone, &shippingJSON, &order.SourceAddressID,
		&order.PaymentID, &paidAt, &order.PaymentDueAt, &order.ShippedBy, &shippedAt,
		&order.Carrier, &order.TrackingNumber, &order.CarrierNote,
		&order.CreatedAt, &order.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := decodeShippingJSON(shippingJSON, &order.ShippingAddress); err != nil {
		return nil, err
	}
	order.PaidAt = paidAt
	order.ShippedAt = shippedAt

	items, err := r.loadOrderItems(ctx, id)
	if err != nil {
		return nil, err
	}
	order.Items = items
	return &order, nil
}

func (r *Repository) loadOrderItems(ctx context.Context, orderID string) ([]domain.OrderItem, error) {
	byOrder, err := r.loadOrderItemsBatch(ctx, []string{orderID})
	if err != nil {
		return nil, err
	}
	return byOrder[orderID], nil
}

func (r *Repository) loadOrderItemsBatch(ctx context.Context, orderIDs []string) (map[string][]domain.OrderItem, error) {
	out := make(map[string][]domain.OrderItem, len(orderIDs))
	if len(orderIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT order_id, sku, COALESCE(sku_id, ''), quantity, unit_price_cents,
			COALESCE(product_name, ''), COALESCE(image_url, '')
		FROM order_items
		WHERE order_id = ANY($1)
		ORDER BY order_id, sku
	`, toTextArray(orderIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var orderID string
		var item domain.OrderItem
		if err := rows.Scan(&orderID, &item.SKU, &item.SkuID, &item.Quantity, &item.UnitPriceCents, &item.ProductName, &item.ImageURL); err != nil {
			return nil, err
		}
		out[orderID] = append(out[orderID], item)
	}
	return out, rows.Err()
}

func toTextArray(ss []string) pgtype.TextArray {
	if len(ss) == 0 {
		return pgtype.TextArray{Status: pgtype.Present}
	}
	elements := make([]pgtype.Text, len(ss))
	for i, s := range ss {
		elements[i] = pgtype.Text{String: s, Status: pgtype.Present}
	}
	return pgtype.TextArray{
		Elements:   elements,
		Dimensions: []pgtype.ArrayDimension{{Length: int32(len(ss)), LowerBound: 1}},
		Status:     pgtype.Present,
	}
}

func (r *Repository) ListByCustomer(ctx context.Context, customerID string) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, customer_id, reservation_id, status, coupon_code,
			subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			recipient_name, recipient_phone, shipping_address, source_address_id,
			payment_id, paid_at, payment_due_at, shipped_by, shipped_at, carrier, tracking_number, carrier_note,
			created_at, updated_at
		FROM orders WHERE customer_id = $1 ORDER BY created_at DESC
	`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []domain.Order
	var ids []string
	for rows.Next() {
		var order domain.Order
		var paidAt, shippedAt *time.Time
		var shippingJSON []byte
		if err := rows.Scan(
			&order.ID, &order.CustomerID, &order.ReservationID, &order.Status, &order.CouponCode,
			&order.SubtotalCents, &order.DiscountCents, &order.ShippingFeeCents, &order.TotalCents,
			&order.RecipientName, &order.RecipientPhone, &shippingJSON, &order.SourceAddressID,
			&order.PaymentID, &paidAt, &order.PaymentDueAt, &order.ShippedBy, &shippedAt,
			&order.Carrier, &order.TrackingNumber, &order.CarrierNote,
			&order.CreatedAt, &order.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := decodeShippingJSON(shippingJSON, &order.ShippingAddress); err != nil {
			return nil, err
		}
		order.PaidAt = paidAt
		order.ShippedAt = shippedAt
		orders = append(orders, order)
		ids = append(ids, order.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	itemsByOrder, err := r.loadOrderItemsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range orders {
		orders[i].Items = itemsByOrder[orders[i].ID]
	}
	return orders, nil
}

func (r *Repository) ListAll(ctx context.Context) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, customer_id, reservation_id, status, coupon_code,
			subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			recipient_name, recipient_phone, shipping_address, source_address_id,
			payment_id, paid_at, payment_due_at, shipped_by, shipped_at, carrier, tracking_number, carrier_note,
			created_at, updated_at
		FROM orders ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []domain.Order
	var ids []string
	for rows.Next() {
		var order domain.Order
		var paidAt, shippedAt *time.Time
		var shippingJSON []byte
		if err := rows.Scan(
			&order.ID, &order.CustomerID, &order.ReservationID, &order.Status, &order.CouponCode,
			&order.SubtotalCents, &order.DiscountCents, &order.ShippingFeeCents, &order.TotalCents,
			&order.RecipientName, &order.RecipientPhone, &shippingJSON, &order.SourceAddressID,
			&order.PaymentID, &paidAt, &order.PaymentDueAt, &order.ShippedBy, &shippedAt,
			&order.Carrier, &order.TrackingNumber, &order.CarrierNote,
			&order.CreatedAt, &order.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := decodeShippingJSON(shippingJSON, &order.ShippingAddress); err != nil {
			return nil, err
		}
		order.PaidAt = paidAt
		order.ShippedAt = shippedAt
		orders = append(orders, order)
		ids = append(ids, order.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	itemsByOrder, err := r.loadOrderItemsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range orders {
		orders[i].Items = itemsByOrder[orders[i].ID]
	}
	return orders, nil
}

func (r *Repository) ListPendingPaymentExpired(ctx context.Context, now time.Time) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, customer_id, reservation_id, status, coupon_code,
			subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			recipient_name, recipient_phone, shipping_address, source_address_id,
			payment_id, paid_at, payment_due_at, shipped_by, shipped_at, carrier, tracking_number, carrier_note,
			created_at, updated_at
		FROM orders
		WHERE status = $1 AND payment_due_at < $2
	`, domain.StatusPending, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []domain.Order
	var ids []string
	for rows.Next() {
		var order domain.Order
		var paidAt, shippedAt *time.Time
		var shippingJSON []byte
		if err := rows.Scan(
			&order.ID, &order.CustomerID, &order.ReservationID, &order.Status, &order.CouponCode,
			&order.SubtotalCents, &order.DiscountCents, &order.ShippingFeeCents, &order.TotalCents,
			&order.RecipientName, &order.RecipientPhone, &shippingJSON, &order.SourceAddressID,
			&order.PaymentID, &paidAt, &order.PaymentDueAt, &order.ShippedBy, &shippedAt,
			&order.Carrier, &order.TrackingNumber, &order.CarrierNote,
			&order.CreatedAt, &order.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := decodeShippingJSON(shippingJSON, &order.ShippingAddress); err != nil {
			return nil, err
		}
		order.PaidAt = paidAt
		order.ShippedAt = shippedAt
		orders = append(orders, order)
		ids = append(ids, order.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	itemsByOrder, err := r.loadOrderItemsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range orders {
		orders[i].Items = itemsByOrder[orders[i].ID]
	}
	return orders, nil
}

func (r *Repository) SaveCheckoutSession(ctx context.Context, session *domain.CheckoutSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO checkout_sessions (
			id, customer_id, status, coupon_code, subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			order_id, expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO UPDATE SET
			customer_id = EXCLUDED.customer_id,
			status = EXCLUDED.status,
			coupon_code = EXCLUDED.coupon_code,
			subtotal_cents = EXCLUDED.subtotal_cents,
			discount_cents = EXCLUDED.discount_cents,
			shipping_fee_cents = EXCLUDED.shipping_fee_cents,
			total_cents = EXCLUDED.total_cents,
			order_id = EXCLUDED.order_id,
			expires_at = EXCLUDED.expires_at,
			updated_at = EXCLUDED.updated_at
	`, session.ID, session.CustomerID, session.Status, session.CouponCode,
		session.SubtotalCents, session.DiscountCents, session.ShippingFeeCents, session.TotalCents,
		session.OrderID, session.ExpiresAt, session.CreatedAt, session.UpdatedAt)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM checkout_session_items WHERE session_id = $1`, session.ID); err != nil {
		return err
	}
	for _, item := range session.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO checkout_session_items (session_id, sku, sku_id, quantity, unit_price_cents)
			VALUES ($1, $2, $3, $4, $5)
		`, session.ID, item.SKU, nullIfEmpty(item.SkuID), item.Quantity, item.UnitPriceCents); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) CompleteCheckoutSessionIfOpen(ctx context.Context, sessionID, orderID string, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE checkout_sessions
		SET status = $3, order_id = $4, updated_at = $5
		WHERE id = $1 AND status = $2
	`, sessionID, domain.CheckoutStatusOpen, domain.CheckoutStatusCompleted, orderID, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) CancelIfPendingExpired(ctx context.Context, orderID string, now time.Time, events []ports.OutboxEvent) (*domain.Order, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE orders
		SET status = $3, updated_at = $4
		WHERE id = $1 AND status = $2 AND payment_due_at < $4
	`, orderID, domain.StatusPending, domain.StatusCanceled, now)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 0 {
		return nil, false, nil
	}

	var order domain.Order
	var paidAt, shippedAt *time.Time
	var shippingJSON []byte
	err = tx.QueryRow(ctx, `
		SELECT id, customer_id, reservation_id, status, coupon_code,
			subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			recipient_name, recipient_phone, shipping_address, source_address_id,
			payment_id, paid_at, payment_due_at, shipped_by, shipped_at, carrier, tracking_number, carrier_note,
			created_at, updated_at
		FROM orders WHERE id = $1
	`, orderID).Scan(
		&order.ID, &order.CustomerID, &order.ReservationID, &order.Status, &order.CouponCode,
		&order.SubtotalCents, &order.DiscountCents, &order.ShippingFeeCents, &order.TotalCents,
		&order.RecipientName, &order.RecipientPhone, &shippingJSON, &order.SourceAddressID,
		&order.PaymentID, &paidAt, &order.PaymentDueAt, &order.ShippedBy, &shippedAt,
		&order.Carrier, &order.TrackingNumber, &order.CarrierNote,
		&order.CreatedAt, &order.UpdatedAt,
	)
	if err != nil {
		return nil, false, err
	}
	if err := decodeShippingJSON(shippingJSON, &order.ShippingAddress); err != nil {
		return nil, false, err
	}
	order.PaidAt = paidAt
	order.ShippedAt = shippedAt

	rows, err := tx.Query(ctx, `
		SELECT sku, COALESCE(sku_id, ''), quantity, unit_price_cents
		FROM order_items WHERE order_id = $1 ORDER BY sku
	`, orderID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.OrderItem
		if err := rows.Scan(&item.SKU, &item.SkuID, &item.Quantity, &item.UnitPriceCents); err != nil {
			return nil, false, err
		}
		order.Items = append(order.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	for _, ev := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_outbox (aggregate_id, subject, payload)
			VALUES ($1, $2, $3)
		`, ev.AggregateID, ev.Subject, ev.Payload); err != nil {
			return nil, false, fmt.Errorf("enqueue outbox: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &order, true, nil
}

func (r *Repository) CancelIfPaidForRefund(ctx context.Context, orderID, paymentID string, now time.Time, events []ports.OutboxEvent) (*domain.Order, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE orders
		SET status = $4, updated_at = $5
		WHERE id = $1 AND status = $2 AND payment_id = $3
	`, orderID, domain.StatusPaid, paymentID, domain.StatusCanceled, now)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 0 {
		return nil, false, nil
	}

	var order domain.Order
	var paidAt, shippedAt *time.Time
	var shippingJSON []byte
	err = tx.QueryRow(ctx, `
		SELECT id, customer_id, reservation_id, status, coupon_code,
			subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			recipient_name, recipient_phone, shipping_address, source_address_id,
			payment_id, paid_at, payment_due_at, shipped_by, shipped_at, carrier, tracking_number, carrier_note,
			created_at, updated_at
		FROM orders WHERE id = $1
	`, orderID).Scan(
		&order.ID, &order.CustomerID, &order.ReservationID, &order.Status, &order.CouponCode,
		&order.SubtotalCents, &order.DiscountCents, &order.ShippingFeeCents, &order.TotalCents,
		&order.RecipientName, &order.RecipientPhone, &shippingJSON, &order.SourceAddressID,
		&order.PaymentID, &paidAt, &order.PaymentDueAt, &order.ShippedBy, &shippedAt,
		&order.Carrier, &order.TrackingNumber, &order.CarrierNote,
		&order.CreatedAt, &order.UpdatedAt,
	)
	if err != nil {
		return nil, false, err
	}
	if err := decodeShippingJSON(shippingJSON, &order.ShippingAddress); err != nil {
		return nil, false, err
	}
	order.PaidAt = paidAt
	order.ShippedAt = shippedAt

	rows, err := tx.Query(ctx, `
		SELECT sku, COALESCE(sku_id, ''), quantity, unit_price_cents
		FROM order_items WHERE order_id = $1 ORDER BY sku
	`, orderID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.OrderItem
		if err := rows.Scan(&item.SKU, &item.SkuID, &item.Quantity, &item.UnitPriceCents); err != nil {
			return nil, false, err
		}
		order.Items = append(order.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	for _, ev := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_outbox (aggregate_id, subject, payload)
			VALUES ($1, $2, $3)
		`, ev.AggregateID, ev.Subject, ev.Payload); err != nil {
			return nil, false, fmt.Errorf("enqueue outbox: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &order, true, nil
}

func (r *Repository) SavePaidIfPending(ctx context.Context, order *domain.Order, events []ports.OutboxEvent) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE orders SET
			status = $2,
			payment_id = $3,
			paid_at = $4,
			reservation_id = $5,
			updated_at = $6
		WHERE id = $1 AND status = $7
	`, order.ID, domain.StatusPaid, order.PaymentID, order.PaidAt, order.ReservationID, order.UpdatedAt, domain.StatusPending)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	for _, ev := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_outbox (aggregate_id, subject, payload)
			VALUES ($1, $2, $3)
		`, ev.AggregateID, ev.Subject, ev.Payload); err != nil {
			return false, fmt.Errorf("enqueue outbox: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) SavePaidIfCanceled(ctx context.Context, order *domain.Order, events []ports.OutboxEvent) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE orders SET
			status = $2,
			payment_id = $3,
			paid_at = $4,
			reservation_id = $5,
			payment_due_at = $6,
			updated_at = $7
		WHERE id = $1 AND status = $8
	`, order.ID, domain.StatusPaid, order.PaymentID, order.PaidAt, order.ReservationID, order.PaymentDueAt, order.UpdatedAt, domain.StatusCanceled)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	for _, ev := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_outbox (aggregate_id, subject, payload)
			VALUES ($1, $2, $3)
		`, ev.AggregateID, ev.Subject, ev.Payload); err != nil {
			return false, fmt.Errorf("enqueue outbox: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) GetCheckoutSession(ctx context.Context, id string) (*domain.CheckoutSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var session domain.CheckoutSession
	err := r.pool.QueryRow(ctx, `
		SELECT id, customer_id, status, coupon_code, subtotal_cents, discount_cents, shipping_fee_cents, total_cents,
			order_id, expires_at, created_at, updated_at
		FROM checkout_sessions WHERE id = $1
	`, id).Scan(
		&session.ID, &session.CustomerID, &session.Status, &session.CouponCode,
		&session.SubtotalCents, &session.DiscountCents, &session.ShippingFeeCents, &session.TotalCents,
		&session.OrderID, &session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT sku, COALESCE(sku_id, ''), quantity, unit_price_cents FROM checkout_session_items WHERE session_id = $1 ORDER BY sku
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var item domain.OrderItem
		if err := rows.Scan(&item.SKU, &item.SkuID, &item.Quantity, &item.UnitPriceCents); err != nil {
			return nil, err
		}
		session.Items = append(session.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &session, nil
}

func decodeShippingJSON(raw []byte, addr *domain.ShippingAddress) error {
	if len(raw) == 0 {
		*addr = domain.ShippingAddress{}
		return nil
	}
	if err := json.Unmarshal(raw, addr); err != nil {
		return fmt.Errorf("decode shipping address: %w", err)
	}
	return nil
}

var _ ports.Repository = (*Repository)(nil)
