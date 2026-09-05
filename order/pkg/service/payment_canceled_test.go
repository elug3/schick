package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
	"github.com/elug3/dupli1/order/pkg/infra/memory"
	"github.com/elug3/dupli1/order/pkg/ports"
	"github.com/elug3/dupli1/order/pkg/service"
)

// recordingSubscriber captures the subject and handler so a payload can be fed
// through the real consumer path.
type recordingSubscriber struct {
	subject string
	handler ports.MessageHandler
}

func (r *recordingSubscriber) Subscribe(_ context.Context, subject string, handler ports.MessageHandler) error {
	r.subject = subject
	r.handler = handler
	return nil
}

func (r *recordingSubscriber) Close() {}

func paidOrderForRefund(t *testing.T, repo *memory.Repository, id string) *domain.Order {
	t.Helper()
	now := time.Now().UTC()
	order, err := domain.NewOrder(id, "cust-1", "res-"+id, []domain.OrderItem{
		{SKU: "BAG-1", Quantity: 1, UnitPriceCents: 250000},
	}, "", 0, 30000, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if err := order.MarkPaid("pay-"+id, order.TotalCents, now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := repo.Save(t.Context(), order); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return order
}

// A full refund must take the order out of the fulfilment queue. Left paid, it
// still passes Ship's status check and the goods go out for money already
// returned.
func TestPaymentCanceled_FullRefundCancelsOrderAndReleasesStock(t *testing.T) {
	repo := memory.NewRepository()
	stock := &fakeStock{}
	svc := service.New(repo, stock)
	order := paidOrderForRefund(t, repo, "ord_refund_1")

	if err := svc.CancelOrderForRefund(t.Context(), order.ID, "pay-ord_refund_1", 0); err != nil {
		t.Fatalf("CancelOrderForRefund: %v", err)
	}

	got, err := repo.Get(t.Context(), order.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
	if stock.released != order.ReservationID {
		t.Fatalf("released = %q, want the order's reservation %q", stock.released, order.ReservationID)
	}
	if err := got.Ship("mgr-1", testShipTracking(), time.Now().UTC()); err == nil {
		t.Fatal("a refunded order must no longer be shippable")
	}
}

// Partial refunds leave money owed on goods; cancelling silently is worse than
// leaving it for a human.
func TestPaymentCanceled_PartialRefundLeavesOrderAlone(t *testing.T) {
	repo := memory.NewRepository()
	stock := &fakeStock{}
	svc := service.New(repo, stock)
	order := paidOrderForRefund(t, repo, "ord_refund_2")

	if err := svc.CancelOrderForRefund(t.Context(), order.ID, "pay-x", 50000); err != nil {
		t.Fatalf("CancelOrderForRefund: %v", err)
	}

	got, _ := repo.Get(t.Context(), order.ID)
	if got.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid to survive a partial refund", got.Status)
	}
	if stock.released != "" {
		t.Fatalf("partial refund must not release stock, released %q", stock.released)
	}
}

// Once goods have shipped a refund is a return, which the system cannot model;
// flipping the status would misreport where the goods are.
func TestPaymentCanceled_ShippedOrderIsLeftForReturnHandling(t *testing.T) {
	for _, status := range []domain.OrderStatus{domain.StatusInTransit, domain.StatusFulfilled} {
		repo := memory.NewRepository()
		stock := &fakeStock{}
		svc := service.New(repo, stock)
		order := paidOrderForRefund(t, repo, "ord_shipped")
		order.Status = status
		if err := repo.Save(t.Context(), order); err != nil {
			t.Fatalf("Save: %v", err)
		}

		if err := svc.CancelOrderForRefund(t.Context(), order.ID, "pay-ord_shipped", 0); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		got, _ := repo.Get(t.Context(), order.ID)
		if got.Status != status {
			t.Fatalf("status = %q, want %q left untouched", got.Status, status)
		}
		if stock.released != "" {
			t.Fatalf("%s: must not release committed stock", status)
		}
	}
}

// NATS redelivers; a replay must not error or double-release.
func TestPaymentCanceled_ReplayIsANoOp(t *testing.T) {
	repo := memory.NewRepository()
	svc := service.New(repo, &fakeStock{})
	order := paidOrderForRefund(t, repo, "ord_refund_3")

	for i := 0; i < 3; i++ {
		if err := svc.CancelOrderForRefund(t.Context(), order.ID, "pay-ord_refund_3", 0); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
	got, _ := repo.Get(t.Context(), order.ID)
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
}

// An event for an order this service does not know must not wedge the
// subscription in a redelivery loop.
func TestPaymentCanceled_UnknownOrderDoesNotError(t *testing.T) {
	svc := service.New(memory.NewRepository(), &fakeStock{})
	if err := svc.CancelOrderForRefund(t.Context(), "ord_missing", "pay-x", 0); err != nil {
		t.Fatalf("unknown order should be tolerated, got %v", err)
	}
}

// End to end over the subscriber, since only this path parses the payload.
func TestPaymentCanceled_ConsumerHandlesPublishedEvent(t *testing.T) {
	repo := memory.NewRepository()
	stock := &fakeStock{}
	svc := service.New(repo, stock)
	order := paidOrderForRefund(t, repo, "ord_refund_4")

	sub := &recordingSubscriber{}
	if err := svc.RegisterPaymentCanceledConsumer(t.Context(), sub); err != nil {
		t.Fatalf("register: %v", err)
	}
	if sub.subject != "payment.canceled" {
		t.Fatalf("subscribed to %q, want payment.canceled", sub.subject)
	}

	payload, _ := json.Marshal(map[string]any{
		"event_type": "payment.canceled", "order_id": order.ID,
		"payment_id": "pay-ord_refund_4", "amount_cents": order.TotalCents, "remaining_cents": 0,
	})
	if err := sub.handler(t.Context(), "payment.canceled", payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	got, _ := repo.Get(t.Context(), order.ID)
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
}

// A payload for a different payment must not cancel this order — order IDs are
// sequential and a spoofed event would otherwise refund the wrong capture.
func TestPaymentCanceled_MismatchedPaymentIDIsIgnored(t *testing.T) {
	repo := memory.NewRepository()
	stock := &fakeStock{}
	svc := service.New(repo, stock)
	order := paidOrderForRefund(t, repo, "ord_refund_mismatch")

	if err := svc.CancelOrderForRefund(t.Context(), order.ID, "pay-someone-else", 0); err != nil {
		t.Fatalf("CancelOrderForRefund: %v", err)
	}
	got, _ := repo.Get(t.Context(), order.ID)
	if got.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid to survive a mismatched payment_id", got.Status)
	}
	if stock.released != "" {
		t.Fatalf("mismatched payment must not release stock, released %q", stock.released)
	}
}

// Pending orders were never captured; cancelling them on payment.canceled would
// drop unpaid reservations that the expiry worker already owns.
func TestPaymentCanceled_PendingOrderIsLeftAlone(t *testing.T) {
	repo := memory.NewRepository()
	stock := &fakeStock{}
	svc := service.New(repo, stock)
	now := time.Now().UTC()
	order, err := domain.NewOrder("ord_pending_refund", "cust-1", "res-pending", []domain.OrderItem{
		{SKU: "BAG-1", Quantity: 1, UnitPriceCents: 250000},
	}, "", 0, 30000, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	order.PaymentID = "pay-ord_pending_refund"
	if err := repo.Save(t.Context(), order); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := svc.CancelOrderForRefund(t.Context(), order.ID, "pay-ord_pending_refund", 0); err != nil {
		t.Fatalf("CancelOrderForRefund: %v", err)
	}
	got, _ := repo.Get(t.Context(), order.ID)
	if got.Status != domain.StatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
	if stock.released != "" {
		t.Fatalf("pending order must not release stock on payment.canceled, released %q", stock.released)
	}
}

// encoding/json treats a missing remaining_cents as 0, which would look like a
// full refund. The consumer must skip that payload instead of cancelling.
func TestPaymentCanceled_OmittedRemainingCentsDoesNotCancel(t *testing.T) {
	repo := memory.NewRepository()
	stock := &fakeStock{}
	svc := service.New(repo, stock)
	order := paidOrderForRefund(t, repo, "ord_refund_omit")

	sub := &recordingSubscriber{}
	if err := svc.RegisterPaymentCanceledConsumer(t.Context(), sub); err != nil {
		t.Fatalf("register: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"event_type": "payment.canceled", "order_id": order.ID,
		"payment_id": "pay-ord_refund_omit", "amount_cents": order.TotalCents,
	})
	if err := sub.handler(t.Context(), "payment.canceled", payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	got, _ := repo.Get(t.Context(), order.ID)
	if got.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid when remaining_cents is omitted", got.Status)
	}
	if stock.released != "" {
		t.Fatalf("omitted remaining_cents must not release stock, released %q", stock.released)
	}
}
