// Package outbox implements the drain/retry loop for the transactional
// outbox pattern used by order, payment, and auth: an event row is written in the
// same DB transaction as the state change, then a Drainer publishes it to
// the broker and marks it published (or records the failed attempt so the
// next drain retries it).
//
// Each service keeps its own outbox table, schema, and SQL behind a Store
// implementation (order_outbox, payment_outbox, ...) — only the drain/retry
// loop itself is shared here.
package outbox

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// Event is enqueued in the same transaction as a state-changing write.
type Event struct {
	AggregateID string
	Subject     string
	Payload     []byte // JSON bytes matching the published event shape
}

// Message is a persisted outbox row awaiting (or completing) publish.
type Message struct {
	ID          int64
	AggregateID string
	Subject     string
	Payload     []byte
	CreatedAt   time.Time
	Attempts    int
	LastError   string
}

// Store persists and retrieves outbox rows. Implemented per service against
// that service's own table — only the drain/retry logic below is shared,
// not the schema.
type Store interface {
	ListPendingOutbox(ctx context.Context, limit int) ([]Message, error)
	MarkOutboxPublished(ctx context.Context, id int64) error
	RecordOutboxAttempt(ctx context.Context, id int64, errMsg string) error
}

// Publisher publishes one event to the broker (NATS in practice).
type Publisher interface {
	Publish(ctx context.Context, subject string, event any) error
}

// Drainer publishes pending outbox messages for one service.
type Drainer struct {
	store     Store
	publisher Publisher // nil in dev/test when no broker is configured
	logPrefix string
}

// NewDrainer builds a Drainer. logPrefix identifies the owning service in
// drain-failure log lines, e.g. "order outbox drain".
func NewDrainer(store Store, publisher Publisher, logPrefix string) *Drainer {
	return &Drainer{store: store, publisher: publisher, logPrefix: logPrefix}
}

// Drain publishes pending outbox messages once. Failures are recorded on the
// row and retried on the next call — never returned as fatal to the caller
// of a write path (soft-success: the write already committed).
func (d *Drainer) Drain(ctx context.Context) error {
	if d.publisher == nil {
		// No broker configured (dev/test): mark pending rows published so
		// they do not accumulate.
		msgs, err := d.store.ListPendingOutbox(ctx, 100)
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			if err := d.store.MarkOutboxPublished(ctx, msg.ID); err != nil {
				return err
			}
		}
		return nil
	}

	msgs, err := d.store.ListPendingOutbox(ctx, 50)
	if err != nil {
		return err
	}
	var firstErr error
	for _, msg := range msgs {
		// Publish exactly the bytes that were persisted — no re-encoding —
		// so what got written is guaranteed to be what gets published.
		if err := d.publisher.Publish(ctx, msg.Subject, json.RawMessage(msg.Payload)); err != nil {
			_ = d.store.RecordOutboxAttempt(ctx, msg.ID, err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := d.store.MarkOutboxPublished(ctx, msg.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// StartWorker runs Drain on a ticker until ctx is done.
func (d *Drainer) StartWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := d.Drain(ctx); err != nil {
					log.Printf("%s: %v", d.logPrefix, err)
				}
			}
		}
	}()
}

// TryDrain runs Drain once, best-effort: failures are logged, not returned.
// Intended for the soft-success call right after a write commits.
func (d *Drainer) TryDrain(ctx context.Context) {
	if err := d.Drain(ctx); err != nil {
		log.Printf("%s: %v", d.logPrefix, err)
	}
}
