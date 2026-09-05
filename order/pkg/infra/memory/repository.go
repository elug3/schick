package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
	"github.com/elug3/dupli1/order/pkg/ports"
)

type idempotencyEntry struct {
	orderID     string
	requestHash string
}

type outboxEntry struct {
	msg       ports.OutboxMessage
	published bool
}

type Repository struct {
	mu           sync.RWMutex
	orders       map[string]*domain.Order
	sessions     map[string]*domain.CheckoutSession
	idempotency  map[string]idempotencyEntry // key: customerID+"\x00"+idemKey
	outbox       map[int64]*outboxEntry
	nextID       int
	nextOutboxID int64
}

func NewRepository() *Repository {
	return &Repository{
		orders:      make(map[string]*domain.Order),
		sessions:    make(map[string]*domain.CheckoutSession),
		idempotency: make(map[string]idempotencyEntry),
		outbox:      make(map[int64]*outboxEntry),
	}
}

func idemKey(customerID, key string) string {
	return customerID + "\x00" + key
}

func (r *Repository) NextOrderID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	return fmt.Sprintf("ord_%06d", r.nextID), nil
}

func (r *Repository) NextCheckoutSessionID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	return fmt.Sprintf("cs_%06d", r.nextID), nil
}

func (r *Repository) Save(ctx context.Context, order *domain.Order) error {
	return r.SaveWithOutbox(ctx, order, nil, nil)
}

func (r *Repository) SaveWithOutbox(ctx context.Context, order *domain.Order, idem *ports.IdempotencyRecord, events []ports.OutboxEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if idem != nil && idem.Key != "" {
		k := idemKey(idem.CustomerID, idem.Key)
		if existing, ok := r.idempotency[k]; ok {
			if existing.requestHash != idem.RequestHash {
				return ports.ErrIdempotencyConflict
			}
			if existing.orderID != order.ID {
				// Same key already claimed by another order (concurrent create).
				return ports.ErrIdempotencyConflict
			}
		} else {
			r.idempotency[k] = idempotencyEntry{orderID: order.ID, requestHash: idem.RequestHash}
		}
	}

	r.orders[order.ID] = cloneOrder(order)
	now := time.Now().UTC()
	for _, ev := range events {
		r.nextOutboxID++
		id := r.nextOutboxID
		payload := append([]byte(nil), ev.Payload...)
		r.outbox[id] = &outboxEntry{
			msg: ports.OutboxMessage{
				ID:          id,
				AggregateID: ev.AggregateID,
				Subject:     ev.Subject,
				Payload:     payload,
				CreatedAt:   now,
			},
		}
	}
	return nil
}

func (r *Repository) FindByIdempotencyKey(ctx context.Context, customerID, key string) (*ports.IdempotencyRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	customerID = strings.TrimSpace(customerID)
	key = strings.TrimSpace(key)
	if customerID == "" || key == "" {
		return nil, ports.ErrNotFound
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.idempotency[idemKey(customerID, key)]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return &ports.IdempotencyRecord{
		Key:         key,
		CustomerID:  customerID,
		OrderID:     entry.orderID,
		RequestHash: entry.requestHash,
	}, nil
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

func (r *Repository) Get(ctx context.Context, id string) (*domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	order, ok := r.orders[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return cloneOrder(order), nil
}

func (r *Repository) ListByCustomer(ctx context.Context, customerID string) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var orders []domain.Order
	for _, order := range r.orders {
		if order.CustomerID == customerID {
			orders = append(orders, *cloneOrder(order))
		}
	}
	return orders, nil
}

func (r *Repository) ListAll(ctx context.Context) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var orders []domain.Order
	for _, order := range r.orders {
		orders = append(orders, *cloneOrder(order))
	}
	return orders, nil
}

func (r *Repository) ListPendingPaymentExpired(ctx context.Context, now time.Time) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var orders []domain.Order
	for _, order := range r.orders {
		if order.Status == domain.StatusPending && now.After(order.PaymentDueAt) {
			orders = append(orders, *cloneOrder(order))
		}
	}
	return orders, nil
}

func (r *Repository) SaveCheckoutSession(ctx context.Context, session *domain.CheckoutSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.sessions[session.ID] = cloneCheckoutSession(session)
	return nil
}

func (r *Repository) CompleteCheckoutSessionIfOpen(ctx context.Context, sessionID, orderID string, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.sessions[sessionID]
	if !ok {
		return false, nil
	}
	if session.Status != domain.CheckoutStatusOpen {
		return false, nil
	}
	if now.After(session.ExpiresAt) {
		session.Status = domain.CheckoutStatusExpired
		r.sessions[sessionID] = session
		return false, nil
	}

	session.Status = domain.CheckoutStatusCompleted
	session.OrderID = orderID
	session.UpdatedAt = now
	r.sessions[sessionID] = session
	return true, nil
}

func (r *Repository) CancelIfPendingExpired(ctx context.Context, orderID string, now time.Time, events []ports.OutboxEvent) (*domain.Order, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	order, ok := r.orders[orderID]
	if !ok {
		return nil, false, nil
	}
	if order.Status != domain.StatusPending || !now.After(order.PaymentDueAt) {
		return nil, false, nil
	}

	order.Status = domain.StatusCanceled
	order.UpdatedAt = now
	r.orders[orderID] = cloneOrder(order)

	for _, ev := range events {
		r.nextOutboxID++
		id := r.nextOutboxID
		payload := append([]byte(nil), ev.Payload...)
		r.outbox[id] = &outboxEntry{
			msg: ports.OutboxMessage{
				ID:          id,
				AggregateID: ev.AggregateID,
				Subject:     ev.Subject,
				Payload:     payload,
				CreatedAt:   now,
			},
		}
	}

	return cloneOrder(order), true, nil
}

func (r *Repository) CancelIfPaidForRefund(ctx context.Context, orderID, paymentID string, now time.Time, events []ports.OutboxEvent) (*domain.Order, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	order, ok := r.orders[orderID]
	if !ok {
		return nil, false, nil
	}
	if order.Status != domain.StatusPaid {
		return cloneOrder(order), false, nil
	}
	if paymentID == "" || order.PaymentID != paymentID {
		return cloneOrder(order), false, nil
	}

	order.Status = domain.StatusCanceled
	order.UpdatedAt = now
	r.orders[orderID] = cloneOrder(order)

	for _, ev := range events {
		r.nextOutboxID++
		id := r.nextOutboxID
		payload := append([]byte(nil), ev.Payload...)
		r.outbox[id] = &outboxEntry{
			msg: ports.OutboxMessage{
				ID:          id,
				AggregateID: ev.AggregateID,
				Subject:     ev.Subject,
				Payload:     payload,
				CreatedAt:   now,
			},
		}
	}

	return cloneOrder(order), true, nil
}

func (r *Repository) SavePaidIfPending(ctx context.Context, order *domain.Order, events []ports.OutboxEvent) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.orders[order.ID]
	if !ok {
		return false, nil
	}
	if existing.Status != domain.StatusPending {
		return false, nil
	}

	r.orders[order.ID] = cloneOrder(order)

	for _, ev := range events {
		r.nextOutboxID++
		id := r.nextOutboxID
		payload := append([]byte(nil), ev.Payload...)
		r.outbox[id] = &outboxEntry{
			msg: ports.OutboxMessage{
				ID:          id,
				AggregateID: ev.AggregateID,
				Subject:     ev.Subject,
				Payload:     payload,
				CreatedAt:   order.UpdatedAt,
			},
		}
	}

	return true, nil
}

func (r *Repository) SavePaidIfCanceled(ctx context.Context, order *domain.Order, events []ports.OutboxEvent) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.orders[order.ID]
	if !ok {
		return false, nil
	}
	if existing.Status != domain.StatusCanceled {
		return false, nil
	}

	r.orders[order.ID] = cloneOrder(order)

	for _, ev := range events {
		r.nextOutboxID++
		id := r.nextOutboxID
		payload := append([]byte(nil), ev.Payload...)
		r.outbox[id] = &outboxEntry{
			msg: ports.OutboxMessage{
				ID:          id,
				AggregateID: ev.AggregateID,
				Subject:     ev.Subject,
				Payload:     payload,
				CreatedAt:   order.UpdatedAt,
			},
		}
	}

	return true, nil
}

func (r *Repository) GetCheckoutSession(ctx context.Context, id string) (*domain.CheckoutSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	session, ok := r.sessions[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return cloneCheckoutSession(session), nil
}

func cloneOrder(order *domain.Order) *domain.Order {
	copied := *order
	copied.Items = make([]domain.OrderItem, len(order.Items))
	copy(copied.Items, order.Items)
	copied.ShippingAddress = order.ShippingAddress
	return &copied
}

func cloneCheckoutSession(session *domain.CheckoutSession) *domain.CheckoutSession {
	copied := *session
	copied.Items = make([]domain.OrderItem, len(session.Items))
	copy(copied.Items, session.Items)
	return &copied
}
