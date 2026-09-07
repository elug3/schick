package pg

import (
	"testing"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
)

// ShipIfPaid is the atomic guard that keeps a concurrent refund cancel from
// being last-write-wins undone when an admin ships at the same moment.
func TestShipIfPaidUpdatesPaidOrder(t *testing.T) {
	dsn := requireDSN(t)
	pool := freshSchema(t, dsn, "order_ship_if_paid_success_test")
	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := t.Context()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	order, err := domain.NewOrder("ord-ship-pg-1", "cust-1", "res-ship-1", []domain.OrderItem{{
		SkuID: "sku-1", SKU: "BAG-001", Quantity: 1, UnitPriceCents: 1000,
	}}, "", 0, 0, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if err := order.MarkPaid("pay-ship-pg-1", order.TotalCents, now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save: %v", err)
	}

	toShip := *order
	toShip.Status = domain.StatusInTransit
	toShip.ShippedBy = "manager-1"
	toShip.ShippedAt = &now
	toShip.Carrier = domain.CarrierCJ
	toShip.TrackingNumber = "123456789012"
	toShip.UpdatedAt = now

	saved, err := repo.ShipIfPaid(ctx, &toShip, nil)
	if err != nil {
		t.Fatalf("ShipIfPaid: %v", err)
	}
	if !saved {
		t.Fatal("expected ShipIfPaid to persist in_transit")
	}

	loaded, err := repo.Get(ctx, "ord-ship-pg-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusInTransit {
		t.Fatalf("status = %q, want in_transit", loaded.Status)
	}
	if loaded.ShippedBy != "manager-1" || loaded.TrackingNumber != "123456789012" {
		t.Fatalf("ship metadata = by %q tracking %q", loaded.ShippedBy, loaded.TrackingNumber)
	}
}

func TestShipIfPaidSkipsCanceledOrder(t *testing.T) {
	dsn := requireDSN(t)
	pool := freshSchema(t, dsn, "order_ship_if_paid_canceled_test")
	repo := &Repository{pool: pool}
	if err := repo.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := t.Context()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	order, err := domain.NewOrder("ord-ship-pg-2", "cust-1", "res-ship-2", []domain.OrderItem{{
		SkuID: "sku-2", SKU: "BAG-002", Quantity: 1, UnitPriceCents: 1000,
	}}, "", 0, 0, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if err := order.MarkPaid("pay-ship-pg-2", order.TotalCents, now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, ok, err := repo.CancelIfPaidForRefund(ctx, "ord-ship-pg-2", "pay-ship-pg-2", now, nil)
	if err != nil {
		t.Fatalf("CancelIfPaidForRefund: %v", err)
	}
	if !ok {
		t.Fatal("expected refund cancel")
	}

	beforeShip, err := repo.Get(ctx, "ord-ship-pg-2")
	if err != nil {
		t.Fatalf("Get before ship: %v", err)
	}
	toShip := *beforeShip
	toShip.Status = domain.StatusInTransit
	toShip.ShippedBy = "manager-1"
	toShip.ShippedAt = &now
	toShip.Carrier = domain.CarrierCJ
	toShip.TrackingNumber = "123456789012"
	toShip.UpdatedAt = now

	saved, err := repo.ShipIfPaid(ctx, &toShip, nil)
	if err != nil {
		t.Fatalf("ShipIfPaid: %v", err)
	}
	if saved {
		t.Fatal("expected ShipIfPaid to skip canceled order")
	}

	loaded, err := repo.Get(ctx, "ord-ship-pg-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", loaded.Status)
	}
}
