package pg

import (
	"testing"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
)

// CancelIfPaidForRefund is the atomic guard that keeps a concurrent ship from
// being last-write-wins undone when payment.canceled arrives.
func TestCancelIfPaidForRefundCancelsMatchingPaidOrder(t *testing.T) {
	dsn := requireDSN(t)
	pool := freshSchema(t, dsn, "order_cancel_refund_match_test")
	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := t.Context()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	order, err := domain.NewOrder("ord-ref-pg-1", "cust-1", "res-1", []domain.OrderItem{{
		SkuID: "sku-1", SKU: "BAG-001", Quantity: 1, UnitPriceCents: 1000,
	}}, "", 0, 0, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if err := order.MarkPaid("pay-ord-ref-pg-1", order.TotalCents, now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save: %v", err)
	}

	canceled, ok, err := repo.CancelIfPaidForRefund(ctx, "ord-ref-pg-1", "pay-ord-ref-pg-1", now, nil)
	if err != nil {
		t.Fatalf("CancelIfPaidForRefund: %v", err)
	}
	if !ok || canceled == nil {
		t.Fatal("want canceled order")
	}
	if canceled.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}

	loaded, err := repo.Get(ctx, "ord-ref-pg-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusCanceled {
		t.Fatalf("persisted status = %q, want canceled", loaded.Status)
	}
}

func TestCancelIfPaidForRefundSkipsMismatchedPayment(t *testing.T) {
	dsn := requireDSN(t)
	pool := freshSchema(t, dsn, "order_cancel_refund_mismatch_test")
	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := t.Context()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	order, err := domain.NewOrder("ord-ref-pg-2", "cust-1", "res-2", []domain.OrderItem{{
		SkuID: "sku-2", SKU: "BAG-002", Quantity: 1, UnitPriceCents: 1000,
	}}, "", 0, 0, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if err := order.MarkPaid("pay-ord-ref-pg-2", order.TotalCents, now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, ok, err := repo.CancelIfPaidForRefund(ctx, "ord-ref-pg-2", "pay-other", now, nil)
	if err != nil {
		t.Fatalf("CancelIfPaidForRefund: %v", err)
	}
	if ok {
		t.Fatal("expected no cancel on mismatched payment_id")
	}

	loaded, err := repo.Get(ctx, "ord-ref-pg-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", loaded.Status)
	}
}

func TestCancelIfPaidForRefundIsIdempotent(t *testing.T) {
	dsn := requireDSN(t)
	pool := freshSchema(t, dsn, "order_cancel_refund_idempotent_test")
	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := t.Context()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	order, err := domain.NewOrder("ord-ref-pg-3", "cust-1", "res-3", []domain.OrderItem{{
		SkuID: "sku-3", SKU: "BAG-003", Quantity: 1, UnitPriceCents: 1000,
	}}, "", 0, 0, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if err := order.MarkPaid("pay-ord-ref-pg-3", order.TotalCents, now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, ok, err := repo.CancelIfPaidForRefund(ctx, "ord-ref-pg-3", "pay-ord-ref-pg-3", now, nil); err != nil || !ok {
		t.Fatalf("first cancel: ok=%v err=%v", ok, err)
	}
	_, ok, err := repo.CancelIfPaidForRefund(ctx, "ord-ref-pg-3", "pay-ord-ref-pg-3", now, nil)
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if ok {
		t.Fatal("replay must not cancel again")
	}
}
