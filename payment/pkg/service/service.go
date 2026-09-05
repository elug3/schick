package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/infra/checkout"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/elug3/dupli1/shared/pkg/outbox"
)

type Service struct {
	repo          ports.Repository
	orders        ports.OrderClient
	checkout      ports.CheckoutProvider
	events        ports.EventPublisher
	outboxDrainer *outbox.Drainer
	now           func() time.Time
}

func New(repo ports.Repository, orders ports.OrderClient, checkout ports.CheckoutProvider, events ports.EventPublisher) *Service {
	return &Service{
		repo:          repo,
		orders:        orders,
		checkout:      checkout,
		events:        events,
		outboxDrainer: outbox.NewDrainer(repo, events, "payment outbox drain"),
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

type CreatePaymentInput struct {
	OrderID        string
	CustomerID     string
	BearerToken    string
	IdempotencyKey string
	Method         string
	Note           string
	CreatedBy      string
	// BypassABAC skips customer ownership check (payment.create / admin / *).
	BypassABAC bool
	// AllowMethodBypass permits method=bypass (payment.bypass / admin / *).
	AllowMethodBypass bool
}

func (s *Service) CreatePayment(ctx context.Context, input CreatePaymentInput) (*domain.Payment, error) {
	if input.IdempotencyKey != "" {
		if existing, err := s.repo.FindByIdempotencyKey(ctx, input.IdempotencyKey); err == nil {
			// Succeeded payments (e.g. Bypass) re-enqueue so a prior save+failed-publish
			// retry still notifies order.
			if existing.Status == domain.StatusSucceeded {
				return s.CompletePayment(ctx, existing.ID)
			}
			return existing, nil
		} else if !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
	}

	method, err := domain.NormalizeMethod(input.Method)
	if err != nil {
		return nil, err
	}
	switch method {
	case domain.MethodBitcoin:
		return nil, ports.ErrMethodUnavailable
	case domain.MethodBypass:
		return s.createBypassPayment(ctx, input)
	default:
		return s.createCardPayment(ctx, input)
	}
}

func (s *Service) createCardPayment(ctx context.Context, input CreatePaymentInput) (*domain.Payment, error) {
	order, err := s.loadPendingOrder(ctx, input, false)
	if err != nil {
		return nil, err
	}
	if existing, err := s.reuseOpenPaymentForOrder(ctx, order.ID); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	paymentID, err := s.repo.NextPaymentID(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	goodsName := "Dupli1 " + order.ID
	session, err := s.checkout.CreateSession(ctx, ports.CheckoutSessionInput{
		OrderID:     order.ID,
		PaymentID:   paymentID,
		AmountCents: order.TotalCents,
		Currency:    domain.DefaultCurrency,
		CustomerID:  order.CustomerID,
		OrderName:   order.RecipientName,
		OrderTel:    order.RecipientPhone,
		GoodsName:   goodsName,
	})
	if err != nil {
		return nil, err
	}

	payment, err := domain.NewPayment(paymentID, order.ID, order.CustomerID, order.TotalCents, domain.DefaultCurrency, session.Provider, session.ProviderRef, session.CheckoutURL, now)
	if err != nil {
		return nil, err
	}
	payment.Method = domain.MethodCreditCard
	payment.PayerName = strings.TrimSpace(order.RecipientName)
	payment.PayerPhone = strings.TrimSpace(order.RecipientPhone)
	if input.CreatedBy != "" {
		payment.CreatedBy = input.CreatedBy
	}
	payment.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if err := s.repo.Save(ctx, payment); err != nil {
		if existing, reuseErr := s.reuseOpenPaymentForOrder(ctx, order.ID); reuseErr != nil {
			return nil, reuseErr
		} else if existing != nil {
			return existing, nil
		}
		return nil, err
	}
	return payment, nil
}

func (s *Service) createBypassPayment(ctx context.Context, input CreatePaymentInput) (*domain.Payment, error) {
	if !input.AllowMethodBypass {
		return nil, ports.ErrPaymentForbidden
	}

	order, err := s.loadPendingOrder(ctx, input, true)
	if err != nil {
		return nil, err
	}
	if existing, err := s.reuseOpenPaymentForOrder(ctx, order.ID); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	paymentID, err := s.repo.NextPaymentID(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	providerRef := "bypass_" + paymentID
	payment, err := domain.NewPayment(paymentID, order.ID, order.CustomerID, order.TotalCents, domain.DefaultCurrency, domain.ProviderBypass, providerRef, "", now)
	if err != nil {
		return nil, err
	}
	payment.Method = domain.MethodBypass
	payment.CreatedBy = strings.TrimSpace(input.CreatedBy)
	payment.Note = strings.TrimSpace(input.Note)
	payment.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	payment.MarkSucceeded(now)

	if err := s.persistSucceeded(ctx, payment); err != nil {
		return nil, err
	}
	return payment, nil
}

// reuseOpenPaymentForOrder returns an existing checkout when a pending order
// already has requires_payment or succeeded (order not yet marked paid).
func (s *Service) reuseOpenPaymentForOrder(ctx context.Context, orderID string) (*domain.Payment, error) {
	existing, err := s.repo.FindRequiresPaymentByOrderID(ctx, orderID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ports.ErrNotFound) {
		return nil, err
	}
	succeeded, err := s.repo.FindSucceededByOrderID(ctx, orderID)
	if err == nil {
		return succeeded, nil
	}
	if !errors.Is(err, ports.ErrNotFound) {
		return nil, err
	}
	return nil, nil
}

// loadPendingOrder fetches the order and enforces pending + ownership rules.
// skipABAC forces ownership skip (used for Bypass, which is manager-only).
func (s *Service) loadPendingOrder(ctx context.Context, input CreatePaymentInput, skipABAC bool) (*ports.OrderSummary, error) {
	order, err := s.orders.GetOrder(ctx, input.BearerToken, input.OrderID)
	if err != nil {
		return nil, err
	}
	if !skipABAC && !input.BypassABAC && order.CustomerID != input.CustomerID {
		return nil, ports.ErrOrderForbidden
	}
	if order.Status != "pending" {
		return nil, ports.ErrOrderNotPending
	}
	return order, nil
}

func (s *Service) GetPayment(ctx context.Context, paymentID, customerID string) (*domain.Payment, error) {
	payment, err := s.repo.Get(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	if customerID != "" && payment.CustomerID != customerID {
		return nil, ports.ErrOrderForbidden
	}
	return payment, nil
}

func (s *Service) CompletePayment(ctx context.Context, paymentID string) (*domain.Payment, error) {
	payment, err := s.repo.Get(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	if payment.Status != domain.StatusSucceeded {
		payment.MarkSucceeded(s.now())
	}
	if err := s.persistSucceeded(ctx, payment); err != nil {
		return nil, err
	}
	return payment, nil
}

// NanoCallbackAuth holds merchant credentials used to authenticate NANO callbacks.
// ShopCode and APIKey must be set; Ver/LoginID are required to verify hashValue.
type NanoCallbackAuth struct {
	Ver      string
	LoginID  string
	ShopCode string
	APIKey   string
}

// NanoResult is the approval payload from NANO receiveUrl / webhook.
// Success (resultCode=0000) is accepted only after shopcode, amount, and hashValue checks.
type NanoResult struct {
	ResultCode  string `json:"resultCode"`
	ResultMsg   string `json:"resultMsg"`
	ShopCode    string `json:"shopcode"`
	CompOrderNo string `json:"compOrderNo"` // our payment id
	ReqPayAmt   string `json:"reqPayAmt"`
	TranNo      string `json:"tranNo"`
	PayWay      string `json:"payWay"`
	Timestamp   string `json:"timestamp"`
	HashValue   string `json:"hashValue"`
}

// HandleNanoResult marks a NANO card payment succeeded or failed after PG callback.
//
// Fail-closed rules:
//   - shopcode must be present and match merchant config (always)
//   - reqPayAmt must be present and match the payment amount (always)
//   - on resultCode=0000, hashValue must verify against API_KEY (see checkout.VerifyNanoCallbackHash)
//
// Browser landing without a valid signed callback does not mark paid.
func (s *Service) HandleNanoResult(ctx context.Context, auth NanoCallbackAuth, result NanoResult) (*domain.Payment, error) {
	paymentID := strings.TrimSpace(result.CompOrderNo)
	if paymentID == "" {
		return nil, domain.ErrInvalidPayment
	}
	shop := strings.TrimSpace(result.ShopCode)
	wantShop := strings.TrimSpace(auth.ShopCode)
	if wantShop == "" || shop == "" || shop != wantShop {
		return nil, domain.ErrInvalidPayment
	}
	payment, err := s.repo.Get(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	if payment.Provider != domain.ProviderNano && !strings.HasPrefix(payment.ProviderRef, "nano_") {
		return nil, domain.ErrInvalidPayment
	}
	amt := strings.TrimSpace(result.ReqPayAmt)
	if amt == "" || amt != fmt.Sprintf("%d", payment.AmountCents) {
		return nil, domain.ErrInvalidPayment
	}
	if strings.TrimSpace(result.ResultCode) != "0000" {
		if payment.Status == domain.StatusSucceeded {
			return payment, nil
		}
		payment.MarkFailed(s.now())
		if err := s.repo.Save(ctx, payment); err != nil {
			return nil, err
		}
		return payment, nil
	}
	if strings.TrimSpace(auth.APIKey) == "" ||
		!checkout.VerifyNanoCallbackHash(checkout.NanoConfig{
			Ver:      auth.Ver,
			LoginID:  auth.LoginID,
			ShopCode: auth.ShopCode,
			APIKey:   auth.APIKey,
		}, shop, amt, result.Timestamp, result.HashValue) {
		return nil, domain.ErrInvalidPayment
	}
	if tran := strings.TrimSpace(result.TranNo); tran != "" {
		payment.ProviderRef = tran
	}
	if payment.Status != domain.StatusSucceeded {
		payment.MarkSucceeded(s.now())
	}
	if err := s.persistSucceeded(ctx, payment); err != nil {
		return nil, err
	}
	return payment, nil
}

// persistSucceeded saves the payment and enqueues payment.succeeded in one transaction,
// then best-effort drains the outbox (soft-success: save wins even if NATS is down).
func (s *Service) persistSucceeded(ctx context.Context, payment *domain.Payment) error {
	events, err := s.paymentSucceededOutbox(payment)
	if err != nil {
		return err
	}
	if err := s.repo.SaveWithOutbox(ctx, payment, events); err != nil {
		return err
	}
	s.tryDrainOutbox(ctx)
	return nil
}

func (s *Service) paymentSucceededOutbox(payment *domain.Payment) ([]ports.OutboxEvent, error) {
	payload, err := json.Marshal(ports.PaymentSucceededEvent{
		EventType:   ports.PaymentSucceededSubject,
		OrderID:     payment.OrderID,
		PaymentID:   payment.ID,
		AmountCents: payment.AmountCents,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal payment.succeeded: %w", err)
	}
	return []ports.OutboxEvent{{
		AggregateID: payment.ID,
		Subject:     ports.PaymentSucceededSubject,
		Payload:     payload,
	}}, nil
}

// StartOutboxWorker periodically publishes pending outbox rows.
func (s *Service) StartOutboxWorker(ctx context.Context, interval time.Duration) {
	s.outboxDrainer.StartWorker(ctx, interval)
}

// StartReconcileWorker re-publishes recent succeeded payments so order can catch
// up if a prior Core NATS delivery was lost after publish (MarkOrderPaid is idempotent).
func (s *Service) StartReconcileWorker(ctx context.Context, interval, lookback time.Duration) {
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	if lookback <= 0 {
		lookback = 2 * time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.ReconcileSucceededPayments(ctx, lookback); err != nil {
					log.Printf("payment succeed reconcile: %v", err)
				}
			}
		}
	}()
}

func (s *Service) tryDrainOutbox(ctx context.Context) {
	s.outboxDrainer.TryDrain(ctx)
}

// DrainOutbox publishes pending outbox messages. Failures are recorded and retried later.
func (s *Service) DrainOutbox(ctx context.Context) error {
	return s.outboxDrainer.Drain(ctx)
}

// ReconcileSucceededPayments republishes payment.succeeded for recent succeeded rows.
func (s *Service) ReconcileSucceededPayments(ctx context.Context, lookback time.Duration) error {
	if s.events == nil {
		return nil
	}
	if lookback <= 0 {
		lookback = 2 * time.Hour
	}
	payments, err := s.repo.ListSucceededSince(ctx, s.now().Add(-lookback), 100)
	if err != nil {
		return err
	}
	var firstErr error
	for i := range payments {
		p := payments[i]
		if err := s.events.Publish(ctx, ports.PaymentSucceededSubject, ports.PaymentSucceededEvent{
			EventType:   ports.PaymentSucceededSubject,
			OrderID:     p.OrderID,
			PaymentID:   p.ID,
			AmountCents: p.AmountCents,
		}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CancelPaymentInput requests a cancel (refund) of a captured payment.
type CancelPaymentInput struct {
	PaymentID string
	// AmountCents is the amount to cancel. Zero means the full remaining
	// balance, which is the common ops case (order rejected after payment).
	AmountCents int64
	Reason      string
	CanceledBy  string
	// IdempotencyKey makes a retry of this exact cancel a no-op. Strongly
	// recommended for partial cancels, where local state alone cannot tell a
	// duplicate submit apart from a deliberate second refund.
	IdempotencyKey string
}

// CancelPayment cancels a captured payment at the PG and records the result.
//
// Concurrent cancels serialize on MutateLocked (Postgres FOR UPDATE / in-memory
// mutex) so two in-flight requests cannot both pass ValidateCancel and both
// call the PG. The PG call still happens before any local mutation commits, so
// a rejected or unreachable PG leaves the payment exactly as it was. The
// reverse order would risk marking money refunded that was never returned.
//
// If the process dies after the PG accepts a cancel and before the local
// write commits, a retry can refund again. That crash window is retained so a
// rejected PG never records a refund that did not happen.
//
// Bypass payments never went through a PG (a manager recorded them by hand), so
// they are canceled locally and the matching refund is made out of band.
func (s *Service) CancelPayment(ctx context.Context, input CancelPaymentInput) (*domain.Payment, error) {
	paymentID := strings.TrimSpace(input.PaymentID)
	if paymentID == "" {
		return nil, domain.ErrInvalidPayment
	}
	key := strings.TrimSpace(input.IdempotencyKey)

	payment, err := s.repo.MutateLocked(ctx, paymentID, func(payment *domain.Payment) ([]ports.OutboxEvent, error) {
		// A retry of the cancel we already applied returns the current state
		// rather than refunding twice.
		if key != "" && payment.CancelIdempotencyKey == key {
			return nil, nil
		}

		requested := input.AmountCents
		if requested == 0 {
			requested = payment.RemainingCancelableCents()
		}
		// Validate before spending a PG round trip.
		if err := payment.ValidateCancel(requested); err != nil {
			return nil, err
		}
		remainingBefore := payment.RemainingCancelableCents()
		amount := requested

		if payment.Method != domain.MethodBypass {
			result, err := s.checkout.CancelPayment(ctx, ports.CancelPaymentInput{
				ProviderRef: payment.ProviderRef,
				PaymentID:   payment.ID,
				AmountCents: requested,
				Currency:    payment.Currency,
			})
			if err != nil {
				return nil, err
			}
			// Past this point the provider has confirmed a cancel, so the
			// refund is real and must be recorded. Any disagreement with our
			// own accounting is reconciled toward the provider and clamped
			// into a recordable range — never discarded, which would leave
			// the books showing money we no longer hold.
			amount = reconcileCanceledAmount(payment.ID, result, requested, remainingBefore)
		}

		if err := payment.ApplyCancel(amount, input.Reason, input.CanceledBy, s.now()); err != nil {
			return nil, err
		}
		// Keep the previous key when this cancel carried none, so an unkeyed
		// cancel cannot clear a keyed one and let it replay.
		if key != "" {
			payment.CancelIdempotencyKey = key
		}
		return s.paymentCanceledOutbox(payment, amount)
	})
	if err != nil {
		return nil, err
	}
	s.tryDrainOutbox(ctx)
	return payment, nil
}

// reconcileCanceledAmount decides how much to record as canceled after the
// provider has accepted a cancel. The provider is authoritative: its reported
// cancelAmt wins, and its remainAmt (when given) wins over that, since remainAmt
// reflects the balance the provider will enforce on the next cancel. The result
// is clamped to [1, remainingBefore] so it is always recordable.
func reconcileCanceledAmount(paymentID string, result *ports.CancelPaymentResult, requested, remainingBefore int64) int64 {
	amount := requested
	if result == nil {
		return amount
	}
	if result.CanceledAmountCents > 0 {
		amount = result.CanceledAmountCents
	}
	if result.RemainingKnown {
		amount = remainingBefore - result.RemainingCents
	}
	if amount != requested {
		log.Printf(
			"cancel payment %s: provider canceled %d (remaining_known=%t remaining=%d), requested %d",
			paymentID, amount, result.RemainingKnown, result.RemainingCents, requested,
		)
	}
	if amount < 1 {
		log.Printf("cancel payment %s: provider reported non-positive cancel %d; recording 1", paymentID, amount)
		amount = 1
	}
	if amount > remainingBefore {
		log.Printf(
			"cancel payment %s: provider canceled %d beyond remaining %d; recording %d",
			paymentID, amount, remainingBefore, remainingBefore,
		)
		amount = remainingBefore
	}
	return amount
}

func (s *Service) paymentCanceledOutbox(payment *domain.Payment, amountCents int64) ([]ports.OutboxEvent, error) {
	payload, err := json.Marshal(ports.PaymentCanceledEvent{
		EventType:      ports.PaymentCanceledSubject,
		OrderID:        payment.OrderID,
		PaymentID:      payment.ID,
		AmountCents:    amountCents,
		RemainingCents: payment.RemainingCancelableCents(),
		Reason:         payment.CancelReason,
		CanceledBy:     payment.CanceledBy,
		Occurred:       s.now(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal payment.canceled: %w", err)
	}
	return []ports.OutboxEvent{{
		AggregateID: payment.ID,
		Subject:     ports.PaymentCanceledSubject,
		Payload:     payload,
	}}, nil
}
