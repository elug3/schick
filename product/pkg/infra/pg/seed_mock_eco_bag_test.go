package pg

import (
	"testing"

	"github.com/jackc/pgx/v4/pgxpool"
)

func TestSeedMockEcoBagIdempotent(t *testing.T) {
	dsn := requireProductDSN(t)
	pool := freshProductSchema(t, dsn, "seed_mock_eco_bag_test")
	ctx := t.Context()

	store := &ProductSearchStore{pool: pool}
	if err := store.migrate(); err != nil {
		t.Fatalf("product migrate: %v", err)
	}
	stock, err := NewInventoryStore(pool)
	if err != nil {
		t.Fatalf("inventory migrate: %v", err)
	}

	if err := store.SeedMockEcoBag(ctx, stock); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	id, sku, price, qty := mockEcoBagRow(t, pool)
	if price != mockEcoBagPrice {
		t.Fatalf("price = %v, want %v", price, mockEcoBagPrice)
	}
	if sku != "DUP_ECO01_BLK_OS" {
		t.Fatalf("sku = %q, want DUP_ECO01_BLK_OS", sku)
	}
	if qty != mockEcoBagStock {
		t.Fatalf("stock = %d, want %d", qty, mockEcoBagStock)
	}

	if _, err := pool.Exec(ctx, `UPDATE products SET price = 999 WHERE id = $1`, id); err != nil {
		t.Fatalf("bump price: %v", err)
	}
	if err := store.SeedMockEcoBag(ctx, stock); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	id2, sku2, price2, qty2 := mockEcoBagRow(t, pool)
	if id2 != id {
		t.Fatalf("product id changed: %q -> %q", id, id2)
	}
	if sku2 != sku {
		t.Fatalf("sku changed: %q -> %q", sku, sku2)
	}
	if price2 != mockEcoBagPrice {
		t.Fatalf("resumed price = %v, want %v", price2, mockEcoBagPrice)
	}
	if qty2 != mockEcoBagStock {
		t.Fatalf("resumed stock = %d, want %d", qty2, mockEcoBagStock)
	}
}

func mockEcoBagRow(t *testing.T, pool *pgxpool.Pool) (id, sku string, price float64, qty int) {
	t.Helper()
	err := pool.QueryRow(t.Context(), `
		SELECT p.id, v.sku, p.price, s.quantity
		  FROM products p
		  JOIN product_variants v ON v.product_id = p.id
		  JOIN stock_items s ON s.sku_id = v.sku_id
		 WHERE p.brand_code = $1 AND p.style_code = $2
	`, mockEcoBagBrandCode, mockEcoBagStyleCode).Scan(&id, &sku, &price, &qty)
	if err != nil {
		t.Fatalf("lookup mock eco-bag: %v", err)
	}
	return id, sku, price, qty
}

func freshProductSchema(t *testing.T, dsn, schema string) *pgxpool.Pool {
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
		cleanup, err := pgxpool.Connect(ctx, withPostgresSSLMode(dsn))
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	return pool
}
