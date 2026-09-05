package ports

import (
	"context"
	"errors"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
)

var ErrNotFound = errors.New("not found")

type Repository interface {
	NextPaymentID(ctx context.Context) (string, error)
	Save(ctx context.Context, payment *domain.Payment) error
	// SaveWithOutbox persists the payment and outbox events in one transaction.
	SaveWithOutbox(ctx context.Context, payment *domain.Payment, events []OutboxEvent) error
	// MutateLocked loads the payment, holds a row/mutex lock across mutate, and
	// persists the result with any outbox events in the same critical section.
	// Concurrent cancels serialize here so two in-flight requests cannot both
	// pass ValidateCancel and both call the payment gateway.
	MutateLocked(ctx context.Context, id string, mutate func(*domain.Payment) ([]OutboxEvent, error)) (*domain.Payment, error)
	Get(ctx context.Context, id string) (*domain.Payment, error)
	GetByProviderRef(ctx context.Context, providerRef string) (*domain.Payment, error)
	FindByIdempotencyKey(ctx context.Context, key string) (*domain.Payment, error)
	// FindRequiresPaymentByOrderID returns the newest open checkout for an order.
	FindRequiresPaymentByOrderID(ctx context.Context, orderID string) (*domain.Payment, error)
	// FindSucceededByOrderID returns the newest succeeded payment for an order.
	FindSucceededByOrderID(ctx context.Context, orderID string) (*domain.Payment, error)
	ListSucceededSince(ctx context.Context, since time.Time, limit int) ([]domain.Payment, error)

	ListPendingOutbox(ctx context.Context, limit int) ([]OutboxMessage, error)
	MarkOutboxPublished(ctx context.Context, id int64) error
	RecordOutboxAttempt(ctx context.Context, id int64, errMsg string) error
}
