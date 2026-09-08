package domain_test

import (
	"testing"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
)

func feeTestItems() []domain.OrderItem {
	return []domain.OrderItem{
		{SKU: "BAG-001", Quantity: 1, UnitPriceCents: 250000},
	}
}

func TestNewOrder_AddsShippingFeeToTotal(t *testing.T) {
	now := time.Now().UTC()
	order, err := domain.NewOrder("ord-1", "cust-1", "res-1", feeTestItems(), "", 0, 3000, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if order.SubtotalCents != 250000 {
		t.Fatalf("subtotal = %d, want 250000", order.SubtotalCents)
	}
	if order.ShippingFeeKRW != 3000 {
		t.Fatalf("shipping = %d, want 3000", order.ShippingFeeKRW)
	}
	if order.TotalCents != 253000 {
		t.Fatalf("total = %d, want subtotal + shipping = 253000", order.TotalCents)
	}
}

func TestNewOrder_ShippingAppliesAfterDiscount(t *testing.T) {
	now := time.Now().UTC()
	order, err := domain.NewOrder("ord-1", "cust-1", "res-1", feeTestItems(), "SAVE10", 25000, 3000, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	// 250000 - 25000 + 3000
	if order.TotalCents != 228000 {
		t.Fatalf("total = %d, want 228000 (subtotal - discount + shipping)", order.TotalCents)
	}
}

// A coupon discounts goods, never delivery: a 100%-off order still owes the
// shipping fee.
func TestNewOrder_FullDiscountStillPaysShipping(t *testing.T) {
	now := time.Now().UTC()
	order, err := domain.NewOrder("ord-1", "cust-1", "res-1", feeTestItems(), "FREE", 250000, 3000, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if order.TotalCents != 3000 {
		t.Fatalf("total = %d, want 3000 — the shipping fee survives a full discount", order.TotalCents)
	}
	if order.TotalCents < order.ShippingFeeKRW {
		t.Fatal("total must never fall below the shipping fee")
	}
}

func TestNewOrder_ZeroShippingKeepsOldPricing(t *testing.T) {
	now := time.Now().UTC()
	order, err := domain.NewOrder("ord-1", "cust-1", "res-1", feeTestItems(), "", 25000, 0, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if order.TotalCents != 225000 {
		t.Fatalf("total = %d, want 225000 with free delivery", order.TotalCents)
	}
	if order.ShippingFeeKRW != 0 {
		t.Fatalf("shipping = %d, want 0", order.ShippingFeeKRW)
	}
}

func TestNewOrder_RejectsNegativeShippingFee(t *testing.T) {
	now := time.Now().UTC()
	if _, err := domain.NewOrder("ord-1", "cust-1", "res-1", feeTestItems(), "", 0, -1, now); err != domain.ErrInvalidOrder {
		t.Fatalf("err = %v, want ErrInvalidOrder", err)
	}
}

// The payment service charges order.TotalCents and MarkPaid compares against
// the same field, so a shipped-fee order must reconcile at the inclusive total
// and reject the goods-only amount.
func TestMarkPaid_RequiresTotalIncludingShipping(t *testing.T) {
	now := time.Now().UTC()
	order, err := domain.NewOrder("ord-1", "cust-1", "res-1", feeTestItems(), "", 0, 3000, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if err := order.MarkPaid("pay-1", 250000, now); err != domain.ErrPaymentAmountMismatch {
		t.Fatalf("paying the goods-only amount: err = %v, want ErrPaymentAmountMismatch", err)
	}
	if err := order.MarkPaid("pay-1", 253000, now); err != nil {
		t.Fatalf("paying the inclusive total: %v", err)
	}
	if order.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", order.Status)
	}
}

func TestCheckoutSession_EmptySessionQuotesNoShipping(t *testing.T) {
	now := time.Now().UTC()
	session, err := domain.NewCheckoutSession("cs_1", "cust-1", now, time.Hour, 3000)
	if err != nil {
		t.Fatalf("NewCheckoutSession: %v", err)
	}
	if session.TotalCents != 0 {
		t.Fatalf("empty session total = %d, want 0 — an empty cart owes no delivery", session.TotalCents)
	}
	if session.ShippingFeeKRW != 3000 {
		t.Fatalf("quoted fee = %d, want the configured 3000 retained on the session", session.ShippingFeeKRW)
	}
}

func TestCheckoutSession_TotalIncludesShippingOnceItemsExist(t *testing.T) {
	now := time.Now().UTC()
	session, err := domain.NewCheckoutSession("cs_1", "cust-1", now, time.Hour, 3000)
	if err != nil {
		t.Fatalf("NewCheckoutSession: %v", err)
	}
	if err := session.UpsertItem(domain.OrderItem{SKU: "BAG-001", Quantity: 1, UnitPriceCents: 250000}, now); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	if session.SubtotalCents != 250000 {
		t.Fatalf("subtotal = %d, want 250000", session.SubtotalCents)
	}
	if session.TotalCents != 253000 {
		t.Fatalf("total = %d, want 253000", session.TotalCents)
	}
}

// Emptying the cart must drop the delivery charge again, or the customer is
// left looking at a bare shipping fee as their total.
func TestCheckoutSession_RemovingLastItemDropsShipping(t *testing.T) {
	now := time.Now().UTC()
	session, err := domain.NewCheckoutSession("cs_1", "cust-1", now, time.Hour, 3000)
	if err != nil {
		t.Fatalf("NewCheckoutSession: %v", err)
	}
	item := domain.OrderItem{SKU: "BAG-001", Quantity: 1, UnitPriceCents: 250000}
	if err := session.UpsertItem(item, now); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	if err := session.RemoveItem("BAG-001", now); err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	if session.TotalCents != 0 {
		t.Fatalf("emptied session total = %d, want 0", session.TotalCents)
	}
}

func TestCheckoutSession_RejectsNegativeShippingFee(t *testing.T) {
	now := time.Now().UTC()
	if _, err := domain.NewCheckoutSession("cs_1", "cust-1", now, time.Hour, -1); err != domain.ErrInvalidCheckoutSession {
		t.Fatalf("err = %v, want ErrInvalidCheckoutSession", err)
	}
}
