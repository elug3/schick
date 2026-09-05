package ports

import (
	"context"

	"github.com/elug3/dupli1/shared/pkg/events"
)

// PaymentSucceededSubject is the NATS subject payment publishes on success.
// Alias of the shared event — see shared/pkg/events.
const PaymentSucceededSubject = events.PaymentSucceeded

// PaymentSucceededEvent is an alias of the shared event payload — see shared/pkg/events.
type PaymentSucceededEvent = events.PaymentSucceededEvent

// PaymentCanceledSubject is the NATS subject payment publishes when a captured
// payment is canceled. Alias of the shared event — see shared/pkg/events.
const PaymentCanceledSubject = events.PaymentCanceled

// PaymentCanceledEvent is an alias of the shared event payload — see shared/pkg/events.
type PaymentCanceledEvent = events.PaymentCanceledEvent

// PaymentCallbackRejectedSubject is the NATS subject payment publishes when a PG
// callback the PG marked approved could not be applied. Alias — see shared/pkg/events.
const PaymentCallbackRejectedSubject = events.PaymentCallbackRejected

// PaymentCallbackRejectedEvent is an alias of the shared event payload — see shared/pkg/events.
type PaymentCallbackRejectedEvent = events.PaymentCallbackRejectedEvent

type EventPublisher interface {
	Publish(ctx context.Context, subject string, event any) error
}
