package memory_test

import (
	"testing"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
	"github.com/elug3/dupli1/order/pkg/infra/memory"
	"github.com/elug3/dupli1/order/pkg/ports"
)

func seedPendingOrder(t *testing.T, repo *memory.Repository, id string, dueAt time.Time) *domain.Order {
	t.Helper()
	ctx := t.Context()
	now := dueAt
	order, err := domain.NewOrder(id, "cust-1", "res-1", []domain.OrderItem{
		{SKU: "BAG-1", SkuID: "01SKU", Quantity: 1, UnitPriceCents: 1000},
	}, "", 0, 0, now)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	order.PaymentDueAt = dueAt
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return order
}

func TestCancelIfPendingExpiredCancelsOverduePending(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	seedPendingOrder(t, repo, "ord-exp-1", dueAt)

	events := []ports.OutboxEvent{{AggregateID: "ord-exp-1", Subject: "order.canceled", Payload: []byte(`{}`)}}
	canceled, ok, err := repo.CancelIfPendingExpired(ctx, "ord-exp-1", now, events)
	if err != nil {
		t.Fatalf("CancelIfPendingExpired: %v", err)
	}
	if !ok || canceled == nil {
		t.Fatal("want canceled order")
	}
	if canceled.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}

	loaded, err := repo.Get(ctx, "ord-exp-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusCanceled {
		t.Fatalf("persisted status = %q, want canceled", loaded.Status)
	}
}

func TestCancelIfPendingExpiredSkipsPaidOrder(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	order := seedPendingOrder(t, repo, "ord-exp-2", now.Add(-time.Minute))
	order.Status = domain.StatusPaid
	order.UpdatedAt = now
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save paid: %v", err)
	}

	_, ok, err := repo.CancelIfPendingExpired(ctx, "ord-exp-2", now, nil)
	if err != nil {
		t.Fatalf("CancelIfPendingExpired: %v", err)
	}
	if ok {
		t.Fatal("expected no cancel on paid order")
	}

	loaded, err := repo.Get(ctx, "ord-exp-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", loaded.Status)
	}
}

func TestCancelIfPendingExpiredSkipsBeforeDue(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	seedPendingOrder(t, repo, "ord-exp-3", now.Add(time.Minute))

	_, ok, err := repo.CancelIfPendingExpired(ctx, "ord-exp-3", now, nil)
	if err != nil {
		t.Fatalf("CancelIfPendingExpired: %v", err)
	}
	if ok {
		t.Fatal("expected no cancel before payment_due_at")
	}
}

func TestCancelIfPaidForRefundCancelsMatchingPaidOrder(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	order := seedPendingOrder(t, repo, "ord-ref-1", now.Add(-time.Minute))
	if err := order.MarkPaid("pay-ord-ref-1", order.TotalCents, now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save paid: %v", err)
	}

	canceled, ok, err := repo.CancelIfPaidForRefund(ctx, "ord-ref-1", "pay-ord-ref-1", now, nil)
	if err != nil {
		t.Fatalf("CancelIfPaidForRefund: %v", err)
	}
	if !ok || canceled == nil {
		t.Fatal("want canceled order")
	}
	if canceled.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
}

func TestCancelIfPaidForRefundSkipsMismatchedPayment(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	order := seedPendingOrder(t, repo, "ord-ref-2", now.Add(-time.Minute))
	if err := order.MarkPaid("pay-ord-ref-2", order.TotalCents, now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save paid: %v", err)
	}

	_, ok, err := repo.CancelIfPaidForRefund(ctx, "ord-ref-2", "pay-other", now, nil)
	if err != nil {
		t.Fatalf("CancelIfPaidForRefund: %v", err)
	}
	if ok {
		t.Fatal("expected no cancel on mismatched payment_id")
	}
	loaded, err := repo.Get(ctx, "ord-ref-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", loaded.Status)
	}
}

func TestSavePaidIfPendingUpdatesOnlyPending(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	order := seedPendingOrder(t, repo, "ord-pay-1", now.Add(5*time.Minute))
	order.Status = domain.StatusPaid
	order.PaymentID = "pay-1"
	order.PaidAt = &now
	order.UpdatedAt = now

	saved, err := repo.SavePaidIfPending(ctx, order, nil)
	if err != nil {
		t.Fatalf("SavePaidIfPending: %v", err)
	}
	if !saved {
		t.Fatal("expected save on pending order")
	}

	loaded, err := repo.Get(ctx, "ord-pay-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusPaid || loaded.PaymentID != "pay-1" {
		t.Fatalf("loaded = %+v, want paid with payment id", loaded)
	}
}

func TestSavePaidIfPendingRejectsNonPending(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	order := seedPendingOrder(t, repo, "ord-pay-2", now.Add(5*time.Minute))
	order.Status = domain.StatusCanceled
	order.UpdatedAt = now
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save canceled: %v", err)
	}

	order.Status = domain.StatusPaid
	saved, err := repo.SavePaidIfPending(ctx, order, nil)
	if err != nil {
		t.Fatalf("SavePaidIfPending: %v", err)
	}
	if saved {
		t.Fatal("expected no save when order is not pending")
	}
}

func TestSavePaidIfCanceledReinstatesCanceledOrder(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	order := seedPendingOrder(t, repo, "ord-pay-3", now.Add(-time.Minute))
	order.Status = domain.StatusCanceled
	order.UpdatedAt = now
	if err := repo.Save(ctx, order); err != nil {
		t.Fatalf("Save canceled: %v", err)
	}

	order.Status = domain.StatusPaid
	order.PaymentID = "pay-late"
	order.PaidAt = &now
	saved, err := repo.SavePaidIfCanceled(ctx, order, nil)
	if err != nil {
		t.Fatalf("SavePaidIfCanceled: %v", err)
	}
	if !saved {
		t.Fatal("expected save on canceled order for late payment")
	}

	loaded, err := repo.Get(ctx, "ord-pay-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", loaded.Status)
	}
}

func TestSavePaidIfCanceledRejectsPending(t *testing.T) {
	ctx := t.Context()
	repo := memory.NewRepository()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	order := seedPendingOrder(t, repo, "ord-pay-4", now.Add(5*time.Minute))
	order.Status = domain.StatusPaid
	order.UpdatedAt = now

	saved, err := repo.SavePaidIfCanceled(ctx, order, nil)
	if err != nil {
		t.Fatalf("SavePaidIfCanceled: %v", err)
	}
	if saved {
		t.Fatal("expected no save via SavePaidIfCanceled when order is pending")
	}
}
