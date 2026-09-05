package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/infra/memory"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/elug3/dupli1/payment/pkg/service"
)

// cancelProvider records what the service asked the PG to cancel and replays a
// scripted outcome.
type cancelProvider struct {
	fakeCheckoutProvider
	calls  []ports.CancelPaymentInput
	result *ports.CancelPaymentResult
	err    error
}

func (p *cancelProvider) CancelPayment(_ context.Context, input ports.CancelPaymentInput) (*ports.CancelPaymentResult, error) {
	p.calls = append(p.calls, input)
	if p.err != nil {
		return nil, p.err
	}
	if p.result != nil {
		return p.result, nil
	}
	return &ports.CancelPaymentResult{CanceledAmountCents: input.AmountCents}, nil
}

// cancelEventPublisher captures payment.canceled events off the outbox drain.
type cancelEventPublisher struct {
	events []ports.PaymentCanceledEvent
}

func (p *cancelEventPublisher) Publish(_ context.Context, subject string, event any) error {
	if subject != ports.PaymentCanceledSubject {
		return nil
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var ev ports.PaymentCanceledEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return err
	}
	p.events = append(p.events, ev)
	return nil
}

// seedSucceededCardPayment creates a card payment and drives it to succeeded,
// as a NANO callback would, so it is ready to cancel.
func seedSucceededCardPayment(t *testing.T, repo ports.Repository, provider ports.CheckoutProvider, pub ports.EventPublisher, totalCents int64) (*service.Service, *domain.Payment) {
	t.Helper()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: totalCents,
		RecipientName: "윤라희", RecipientPhone: "01012345678",
	}}
	svc := service.New(repo, orders, provider, pub)

	payment, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	// Stand in for the verified NANO approval callback, which is what replaces
	// the placeholder ref with the provider's tranNo.
	payment.ProviderRef = "2409030071109"
	if err := repo.Save(t.Context(), payment); err != nil {
		t.Fatalf("persist provider ref: %v", err)
	}
	completed, err := svc.CompletePayment(t.Context(), payment.ID)
	if err != nil {
		t.Fatalf("CompletePayment: %v", err)
	}
	return svc, completed
}

func TestCancelPayment_FullCancelCallsProviderAndClosesPayment(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{}
	pub := &cancelEventPublisher{}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, pub, 70000)

	canceled, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: seeded.ID, Reason: "ops reject", CanceledBy: "mgr_1",
	})
	if err != nil {
		t.Fatalf("CancelPayment: %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.calls))
	}
	call := provider.calls[0]
	if call.AmountCents != 70000 {
		t.Fatalf("provider asked to cancel %d, want the full 70000", call.AmountCents)
	}
	if call.ProviderRef != "2409030071109" {
		t.Fatalf("provider ref = %q, want the NANO tranNo", call.ProviderRef)
	}
	if call.PaymentID != seeded.ID {
		t.Fatalf("compOrderNo = %q, want %q", call.PaymentID, seeded.ID)
	}
	if canceled.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}

	stored, err := repo.Get(t.Context(), seeded.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != domain.StatusCanceled || stored.CanceledAmountCents != 70000 {
		t.Fatalf("stored = %q / %d", stored.Status, stored.CanceledAmountCents)
	}
	if stored.CanceledBy != "mgr_1" || stored.CancelReason != "ops reject" {
		t.Fatalf("audit fields = %q / %q", stored.CanceledBy, stored.CancelReason)
	}

	if len(pub.events) != 1 {
		t.Fatalf("payment.canceled events = %d, want 1", len(pub.events))
	}
	ev := pub.events[0]
	if ev.OrderID != "ord_1" || ev.PaymentID != seeded.ID {
		t.Fatalf("event ids = %q / %q", ev.OrderID, ev.PaymentID)
	}
	if ev.AmountCents != 70000 || ev.RemainingCents != 0 {
		t.Fatalf("event amounts = %d / %d", ev.AmountCents, ev.RemainingCents)
	}
}

func TestCancelPayment_PartialLeavesRemainder(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{
		result: &ports.CancelPaymentResult{
			CanceledAmountCents: 20000, RemainingCents: 50000, RemainingKnown: true,
		},
	}
	pub := &cancelEventPublisher{}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, pub, 70000)

	canceled, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: seeded.ID, AmountCents: 20000,
	})
	if err != nil {
		t.Fatalf("CancelPayment: %v", err)
	}
	if canceled.Status != domain.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded after a partial cancel", canceled.Status)
	}
	if canceled.RemainingCancelableCents() != 50000 {
		t.Fatalf("remaining = %d, want 50000", canceled.RemainingCancelableCents())
	}
	if len(pub.events) != 1 || pub.events[0].RemainingCents != 50000 {
		t.Fatalf("event remaining = %+v", pub.events)
	}
}

// A PG that refuses must leave the payment exactly as it was — recording a
// refund that never happened is worse than failing the request.
func TestCancelPayment_ProviderRejectionLeavesPaymentUntouched(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{err: fmt.Errorf("%w: 취소 불가 거래", ports.ErrCancelRejected)}
	pub := &cancelEventPublisher{}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, pub, 70000)

	_, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{PaymentID: seeded.ID})
	if !errors.Is(err, ports.ErrCancelRejected) {
		t.Fatalf("err = %v, want ErrCancelRejected", err)
	}

	stored, err := repo.Get(t.Context(), seeded.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != domain.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded to survive a rejected cancel", stored.Status)
	}
	if stored.CanceledAmountCents != 0 || stored.CanceledAt != nil {
		t.Fatalf("rejected cancel left state: %d / %v", stored.CanceledAmountCents, stored.CanceledAt)
	}
	if len(pub.events) != 0 {
		t.Fatalf("rejected cancel must publish nothing, got %d", len(pub.events))
	}
}

// Bypass payments never reached a PG, so no provider call may be made; the
// matching refund happens out of band.
func TestCancelPayment_BypassSkipsProvider(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
	}}
	provider := &cancelProvider{err: errors.New("provider must not be called for bypass")}
	pub := &cancelEventPublisher{}
	svc := service.New(repo, orders, provider, pub)

	payment, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "mgr_1", BearerToken: "token",
		Method: domain.MethodBypass, CreatedBy: "mgr_1", AllowMethodBypass: true,
	})
	if err != nil {
		t.Fatalf("CreatePayment bypass: %v", err)
	}

	canceled, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: payment.ID, Reason: "refunded in person", CanceledBy: "mgr_1",
	})
	if err != nil {
		t.Fatalf("CancelPayment bypass: %v", err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("bypass cancel called the provider %d times", len(provider.calls))
	}
	if canceled.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
}

func TestCancelPayment_RejectsSecondFullCancel(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, &cancelEventPublisher{}, 70000)

	if _, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{PaymentID: seeded.ID}); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	_, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{PaymentID: seeded.ID})
	if !errors.Is(err, domain.ErrNotCancelable) {
		t.Fatalf("err = %v, want ErrNotCancelable", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider called %d times, want 1 — a closed payment must not reach the PG", len(provider.calls))
	}
}

// A retried partial cancel carrying the same Idempotency-Key must not refund
// twice; local state alone cannot tell it apart from a deliberate second refund.
func TestCancelPayment_IdempotencyKeyBlocksDoublePartialRefund(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, &cancelEventPublisher{}, 70000)

	first, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: seeded.ID, AmountCents: 20000, IdempotencyKey: "retry-1",
	})
	if err != nil {
		t.Fatalf("first partial: %v", err)
	}
	second, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: seeded.ID, AmountCents: 20000, IdempotencyKey: "retry-1",
	})
	if err != nil {
		t.Fatalf("retried partial: %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider called %d times, want 1 for a retried cancel", len(provider.calls))
	}
	if second.CanceledAmountCents != first.CanceledAmountCents {
		t.Fatalf("retry changed total: %d then %d", first.CanceledAmountCents, second.CanceledAmountCents)
	}
	if second.CanceledAmountCents != 20000 {
		t.Fatalf("canceled = %d, want 20000", second.CanceledAmountCents)
	}
}

// A distinct key is a deliberate second refund and must go through.
func TestCancelPayment_DifferentKeyRefundsAgain(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, &cancelEventPublisher{}, 70000)

	if _, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: seeded.ID, AmountCents: 20000, IdempotencyKey: "cancel-1",
	}); err != nil {
		t.Fatalf("first partial: %v", err)
	}
	got, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: seeded.ID, AmountCents: 30000, IdempotencyKey: "cancel-2",
	})
	if err != nil {
		t.Fatalf("second partial: %v", err)
	}
	if len(provider.calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(provider.calls))
	}
	if got.CanceledAmountCents != 50000 {
		t.Fatalf("canceled = %d, want 50000 cumulative", got.CanceledAmountCents)
	}
	if got.Status != domain.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded with 20000 remaining", got.Status)
	}
}

func TestCancelPayment_RejectsAmountOverRemaining(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, &cancelEventPublisher{}, 70000)

	_, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: seeded.ID, AmountCents: 70001,
	})
	if !errors.Is(err, domain.ErrCancelAmountInvalid) {
		t.Fatalf("err = %v, want ErrCancelAmountInvalid", err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("invalid amount must be rejected before the PG call, got %d calls", len(provider.calls))
	}
}

// When the PG reports a different remaining balance than our books expect, the
// provider is authoritative — the refund already happened and must be recorded.
func TestCancelPayment_ProviderRemainingWins(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{
		result: &ports.CancelPaymentResult{
			CanceledAmountCents: 20000, RemainingCents: 40000, RemainingKnown: true,
		},
	}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, &cancelEventPublisher{}, 70000)

	got, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{
		PaymentID: seeded.ID, AmountCents: 20000,
	})
	if err != nil {
		t.Fatalf("CancelPayment: %v", err)
	}
	if got.CanceledAmountCents != 30000 {
		t.Fatalf("canceled = %d, want 30000 derived from the provider's remainAmt", got.CanceledAmountCents)
	}
	if got.RemainingCancelableCents() != 40000 {
		t.Fatalf("remaining = %d, want 40000 to match the provider", got.RemainingCancelableCents())
	}
}

// If the PG confirms a cancel larger than our remaining balance, we still must
// record it rather than drop a refund that really happened.
func TestCancelPayment_ProviderOvershootIsClampedNotDropped(t *testing.T) {
	repo := memory.NewRepository()
	provider := &cancelProvider{
		result: &ports.CancelPaymentResult{CanceledAmountCents: 999999},
	}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, &cancelEventPublisher{}, 70000)

	got, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{PaymentID: seeded.ID})
	if err != nil {
		t.Fatalf("a confirmed cancel must be recorded, got %v", err)
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
	if got.CanceledAmountCents != 70000 {
		t.Fatalf("canceled = %d, want it clamped to the 70000 total", got.CanceledAmountCents)
	}
}

// Two in-flight full cancels must serialize before the PG call so the second
// sees the payment already closed and does not refund twice.
func TestCancelPayment_ConcurrentCancelsCallProviderOnce(t *testing.T) {
	repo := memory.NewRepository()
	provider := &blockingCancelProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc, seeded := seedSucceededCardPayment(t, repo, provider, &cancelEventPublisher{}, 70000)

	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{PaymentID: seeded.ID})
			errCh <- err
		}()
	}

	select {
	case <-provider.started:
	case <-t.Context().Done():
		t.Fatal("first cancel never reached the provider")
	}
	close(provider.release)
	wg.Wait()
	close(errCh)

	var succeeded, failed int
	for err := range errCh {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrNotCancelable):
			failed++
		default:
			t.Fatalf("unexpected cancel error: %v", err)
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Fatalf("outcomes: %d succeeded, %d not-cancelable; want 1 and 1", succeeded, failed)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider called %d times, want 1", got)
	}

	stored, err := repo.Get(t.Context(), seeded.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != domain.StatusCanceled || stored.CanceledAmountCents != 70000 {
		t.Fatalf("stored = %q / %d, want a single full cancel", stored.Status, stored.CanceledAmountCents)
	}
}

type blockingCancelProvider struct {
	fakeCheckoutProvider
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

func (p *blockingCancelProvider) CancelPayment(_ context.Context, input ports.CancelPaymentInput) (*ports.CancelPaymentResult, error) {
	n := p.calls.Add(1)
	if n == 1 {
		p.once.Do(func() { close(p.started) })
		<-p.release
	}
	return &ports.CancelPaymentResult{CanceledAmountCents: input.AmountCents}, nil
}

func TestCancelPayment_UnknownPayment(t *testing.T) {
	repo := memory.NewRepository()
	svc := service.New(repo, stubOrderClient{}, &cancelProvider{}, nil)
	_, err := svc.CancelPayment(t.Context(), service.CancelPaymentInput{PaymentID: "pay_missing"})
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCancelPayment_RequiresPaymentNotCancelable(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "윤라희", RecipientPhone: "01012345678",
	}}
	provider := &cancelProvider{}
	svc := service.New(repo, orders, provider, nil)
	payment, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	_, err = svc.CancelPayment(t.Context(), service.CancelPaymentInput{PaymentID: payment.ID})
	if !errors.Is(err, domain.ErrNotCancelable) {
		t.Fatalf("err = %v, want ErrNotCancelable for an unpaid checkout", err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("unpaid checkout must not reach the PG, got %d calls", len(provider.calls))
	}
}
