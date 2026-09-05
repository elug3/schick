package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/ports"
)

type outboxEntry struct {
	msg       ports.OutboxMessage
	published bool
}

type Repository struct {
	mu           sync.RWMutex
	payments     map[string]*domain.Payment
	byKey        map[string]string
	outbox       map[int64]*outboxEntry
	nextID       int
	nextOutboxID int64
}

func NewRepository() *Repository {
	return &Repository{
		payments: make(map[string]*domain.Payment),
		byKey:    make(map[string]string),
		outbox:   make(map[int64]*outboxEntry),
	}
}

func (r *Repository) NextPaymentID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	return fmt.Sprintf("pay_%06d", r.nextID), nil
}

func (r *Repository) Save(ctx context.Context, payment *domain.Payment) error {
	return r.SaveWithOutbox(ctx, payment, nil)
}

func (r *Repository) SaveWithOutbox(ctx context.Context, payment *domain.Payment, events []ports.OutboxEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.putLocked(payment, events)
	return nil
}

func (r *Repository) MutateLocked(ctx context.Context, id string, mutate func(*domain.Payment) ([]ports.OutboxEvent, error)) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.payments[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	working := *stored
	events, err := mutate(&working)
	if err != nil {
		return nil, err
	}
	r.putLocked(&working, events)
	out := working
	return &out, nil
}

func (r *Repository) putLocked(payment *domain.Payment, events []ports.OutboxEvent) {
	copied := *payment
	r.payments[payment.ID] = &copied
	if payment.IdempotencyKey != "" {
		r.byKey[payment.IdempotencyKey] = payment.ID
	}
	now := time.Now().UTC()
	for _, ev := range events {
		r.nextOutboxID++
		id := r.nextOutboxID
		r.outbox[id] = &outboxEntry{
			msg: ports.OutboxMessage{
				ID:          id,
				AggregateID: ev.AggregateID,
				Subject:     ev.Subject,
				Payload:     append([]byte(nil), ev.Payload...),
				CreatedAt:   now,
			},
		}
	}
}

func (r *Repository) Get(ctx context.Context, id string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.payments[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	copied := *p
	return &copied, nil
}

func (r *Repository) GetByProviderRef(ctx context.Context, providerRef string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.payments {
		if p.ProviderRef == providerRef {
			copied := *p
			return &copied, nil
		}
	}
	return nil, ports.ErrNotFound
}

func (r *Repository) FindByIdempotencyKey(ctx context.Context, key string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byKey[key]
	if !ok {
		return nil, ports.ErrNotFound
	}
	copied := *r.payments[id]
	return &copied, nil
}

func (r *Repository) FindRequiresPaymentByOrderID(ctx context.Context, orderID string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := findLatestPaymentByOrderAndStatus(r.payments, orderID, domain.StatusRequiresPayment)
	if p == nil {
		return nil, ports.ErrNotFound
	}
	return p, nil
}

func (r *Repository) FindSucceededByOrderID(ctx context.Context, orderID string) (*domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := findLatestPaymentByOrderAndStatus(r.payments, orderID, domain.StatusSucceeded)
	if p == nil {
		return nil, ports.ErrNotFound
	}
	return p, nil
}

func findLatestPaymentByOrderAndStatus(payments map[string]*domain.Payment, orderID string, status domain.PaymentStatus) *domain.Payment {
	var latest *domain.Payment
	for _, p := range payments {
		if p.OrderID != orderID || p.Status != status {
			continue
		}
		if latest == nil || p.CreatedAt.After(latest.CreatedAt) {
			copied := *p
			latest = &copied
		}
	}
	if latest == nil {
		return nil
	}
	return latest
}

func (r *Repository) ListSucceededSince(ctx context.Context, since time.Time, limit int) ([]domain.Payment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.Payment, 0, limit)
	for _, p := range r.payments {
		if p.Status != domain.StatusSucceeded {
			continue
		}
		if p.UpdatedAt.Before(since) {
			continue
		}
		out = append(out, *p)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *Repository) ListPendingOutbox(ctx context.Context, limit int) ([]ports.OutboxMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ports.OutboxMessage, 0, limit)
	for _, e := range r.outbox {
		if e.published {
			continue
		}
		msg := e.msg
		msg.Payload = append([]byte(nil), e.msg.Payload...)
		out = append(out, msg)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *Repository) MarkOutboxPublished(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.outbox[id]
	if !ok {
		return ports.ErrNotFound
	}
	e.published = true
	e.msg.LastError = ""
	return nil
}

func (r *Repository) RecordOutboxAttempt(ctx context.Context, id int64, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.outbox[id]
	if !ok {
		return ports.ErrNotFound
	}
	e.msg.Attempts++
	e.msg.LastError = errMsg
	return nil
}
