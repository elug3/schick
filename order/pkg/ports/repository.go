package ports

import (
	"context"
	"errors"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
)

var ErrNotFound = errors.New("not found")

type Repository interface {
	NextOrderID(ctx context.Context) (string, error)
	Save(ctx context.Context, order *domain.Order) error
	// SaveWithOutbox persists the order, optional idempotency row, and outbox events in one transaction.
	SaveWithOutbox(ctx context.Context, order *domain.Order, idem *IdempotencyRecord, events []OutboxEvent) error
	Get(ctx context.Context, id string) (*domain.Order, error)
	ListByCustomer(ctx context.Context, customerID string) ([]domain.Order, error)
	ListAll(ctx context.Context) ([]domain.Order, error)
	ListPendingPaymentExpired(ctx context.Context, now time.Time) ([]domain.Order, error)
	NextCheckoutSessionID(ctx context.Context) (string, error)
	SaveCheckoutSession(ctx context.Context, session *domain.CheckoutSession) error
	GetCheckoutSession(ctx context.Context, id string) (*domain.CheckoutSession, error)
	// CompleteCheckoutSessionIfOpen atomically marks an open session completed.
	// Returns false when the session is not open (already completed, expired, or missing).
	CompleteCheckoutSessionIfOpen(ctx context.Context, sessionID, orderID string, now time.Time) (bool, error)
	// CancelIfPendingExpired atomically cancels an order only when it is still pending and past payment_due_at.
	// Returns the canceled order and true when canceled; false when skipped (paid, already canceled, not expired).
	CancelIfPendingExpired(ctx context.Context, orderID string, now time.Time, events []OutboxEvent) (*domain.Order, bool, error)
	// CancelIfPaidForRefund atomically cancels a paid order only when it is still
	// paid and payment_id matches. Returns false when skipped (already canceled,
	// shipped, wrong payment, or missing).
	CancelIfPaidForRefund(ctx context.Context, orderID, paymentID string, now time.Time, events []OutboxEvent) (*domain.Order, bool, error)
	// SavePaidIfPending atomically persists a paid order only when it is still pending.
	// Returns true when saved; false when skipped (concurrent cancel or already paid).
	SavePaidIfPending(ctx context.Context, order *domain.Order, events []OutboxEvent) (bool, error)
	// SavePaidIfCanceled atomically persists a reinstated late payment when the order is still canceled.
	SavePaidIfCanceled(ctx context.Context, order *domain.Order, events []OutboxEvent) (bool, error)

	FindByIdempotencyKey(ctx context.Context, customerID, key string) (*IdempotencyRecord, error)
	ListPendingOutbox(ctx context.Context, limit int) ([]OutboxMessage, error)
	MarkOutboxPublished(ctx context.Context, id int64) error
	RecordOutboxAttempt(ctx context.Context, id int64, errMsg string) error
}
