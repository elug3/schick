package service_test

import (
	"encoding/json"
	"testing"

	"github.com/elug3/dupli1/order/pkg/domain"
	"github.com/elug3/dupli1/order/pkg/infra/memory"
	"github.com/elug3/dupli1/order/pkg/service"
	"github.com/elug3/dupli1/shared/pkg/events"
)

func TestCreateOrder_AppliesConfiguredShippingFee(t *testing.T) {
	ctx := t.Context()
	svc := service.New(memory.NewRepository(), &fakeStock{}).
		WithProduct(&fakeProduct{defaultCents: 250000}).
		WithShippingFee(3000)

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.ShippingFeeCents != 3000 {
		t.Fatalf("shipping = %d, want the configured 3000", order.ShippingFeeCents)
	}
	if order.TotalCents != 253000 {
		t.Fatalf("total = %d, want 253000", order.TotalCents)
	}
}

// A Service built without WithShippingFee must not invent a delivery charge.
// This is the zero value of the service layer, not the deployment default —
// bootstrap always passes the configured fee (30,000 KRW unless overridden).
func TestCreateOrder_UnconfiguredServiceChargesNothing(t *testing.T) {
	ctx := t.Context()
	svc := service.New(memory.NewRepository(), &fakeStock{}).
		WithProduct(&fakeProduct{defaultCents: 250000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.ShippingFeeCents != 0 || order.TotalCents != 250000 {
		t.Fatalf("shipping = %d, total = %d; want 0 / 250000", order.ShippingFeeCents, order.TotalCents)
	}
}

// A misconfigured negative fee must be ignored rather than making orders
// cheaper than their goods.
func TestWithShippingFee_IgnoresNegative(t *testing.T) {
	ctx := t.Context()
	svc := service.New(memory.NewRepository(), &fakeStock{}).
		WithProduct(&fakeProduct{defaultCents: 250000}).
		WithShippingFee(-500)

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.ShippingFeeCents != 0 {
		t.Fatalf("shipping = %d, want a negative fee ignored", order.ShippingFeeCents)
	}
	if order.TotalCents != 250000 {
		t.Fatalf("total = %d, want 250000", order.TotalCents)
	}
}

// The fee is snapshotted on the order, so re-reading it after the configured
// fee changes must still show what the customer agreed to pay.
func TestCreateOrder_ShippingFeeIsSnapshotted(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	svc := service.New(repo, &fakeStock{}).
		WithProduct(&fakeProduct{defaultCents: 250000}).
		WithShippingFee(3000)

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	svc.WithShippingFee(9000) // fee raised after the order was placed

	reloaded, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if reloaded.ShippingFeeCents != 3000 || reloaded.TotalCents != 253000 {
		t.Fatalf("reloaded shipping = %d total = %d; a config change must not re-price a placed order",
			reloaded.ShippingFeeCents, reloaded.TotalCents)
	}
}

// Notification and any future subscriber need the fee on the wire to reconcile
// the total they are shown.
func TestCreateOrder_PublishesShippingFeeInEvent(t *testing.T) {
	ctx := t.Context()
	publisher := &recordedPublisher{}
	svc := service.New(memory.NewRepository(), &fakeStock{}, publisher).
		WithProduct(&fakeProduct{defaultCents: 250000}).
		WithShippingFee(3000)

	if _, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	}); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if len(publisher.events) == 0 {
		t.Fatal("no event published")
	}
	raw, err := json.Marshal(publisher.events[0])
	if err != nil {
		t.Fatal(err)
	}
	var ev events.Order
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ShippingFeeCents != 3000 {
		t.Fatalf("event shipping = %d, want 3000", ev.ShippingFeeCents)
	}
	if ev.SubtotalCents+ev.ShippingFeeCents-ev.DiscountCents != ev.TotalCents {
		t.Fatalf("event totals do not reconcile: %+v", ev)
	}
}

// Completing a session must charge the fee quoted when it opened, even if the
// configured amount changed mid-checkout.
func TestCompleteCheckout_UsesSessionQuotedShippingFee(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	svc := service.NewWithCheckout(repo, &fakeStock{reservationID: "res-fee"}, nil, 0).
		WithProduct(&fakeProduct{defaultCents: 250000}).
		WithShippingFee(3000)

	session, err := svc.CreateCheckoutSession(ctx, service.CreateCheckoutSessionInput{CustomerID: "customer-1"})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if _, err := svc.UpsertCheckoutItem(ctx, session.ID, domain.OrderItem{SKU: "bag-1", Quantity: 1}); err != nil {
		t.Fatalf("UpsertCheckoutItem: %v", err)
	}

	svc.WithShippingFee(9000)

	result, err := svc.CompleteCheckout(ctx, session.ID, testCompleteCheckoutInput())
	if err != nil {
		t.Fatalf("CompleteCheckout: %v", err)
	}
	if result.Order.ShippingFeeCents != 3000 {
		t.Fatalf("order shipping = %d, want the session-quoted 3000", result.Order.ShippingFeeCents)
	}
	if result.Order.TotalCents != 253000 {
		t.Fatalf("order total = %d, want 253000", result.Order.TotalCents)
	}
}
