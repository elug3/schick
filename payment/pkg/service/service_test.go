package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/infra/checkout"
	"github.com/elug3/dupli1/payment/pkg/infra/memory"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/elug3/dupli1/payment/pkg/service"
)

type stubOrderClient struct {
	order *ports.OrderSummary
}

func (s stubOrderClient) GetOrder(_ context.Context, _, _ string) (*ports.OrderSummary, error) {
	return s.order, nil
}

// fakeCheckoutProvider is a minimal working ports.CheckoutProvider stand-in for
// tests that don't care which real provider (NANO, …) is behind credit_card.
type fakeCheckoutProvider struct{}

func (fakeCheckoutProvider) CreateSession(_ context.Context, input ports.CheckoutSessionInput) (*ports.CheckoutSessionResult, error) {
	return &ports.CheckoutSessionResult{
		Provider:    "test",
		ProviderRef: "test_" + input.PaymentID,
		CheckoutURL: "http://localhost:8080/test-checkout/" + input.PaymentID,
	}, nil
}

// CancelPayment cancels the full requested amount and reports no remaining
// balance, standing in for a PG that accepts every cancel.
func (fakeCheckoutProvider) CancelPayment(_ context.Context, input ports.CancelPaymentInput) (*ports.CancelPaymentResult, error) {
	return &ports.CancelPaymentResult{
		CanceledAmountCents: input.AmountCents,
		ProviderRef:         input.ProviderRef,
	}, nil
}

type recordingPublisher struct {
	events []ports.PaymentSucceededEvent
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, event any) error {
	if subject == ports.PaymentSucceededSubject {
		ev, err := decodePaymentSucceededEvent(event)
		if err != nil {
			return err
		}
		p.events = append(p.events, ev)
	}
	return nil
}

// saveRaceRepo simulates concurrent checkout tabs: the first FindRequiresPaymentByOrderID
// misses a row another request just saved, then Save hits a unique open-payment constraint.
type saveRaceRepo struct {
	*memory.Repository
	hideOpenPaymentOnce bool
}

func (r *saveRaceRepo) FindRequiresPaymentByOrderID(ctx context.Context, orderID string) (*domain.Payment, error) {
	if r.hideOpenPaymentOnce {
		r.hideOpenPaymentOnce = false
		return nil, ports.ErrNotFound
	}
	return r.Repository.FindRequiresPaymentByOrderID(ctx, orderID)
}

func (r *saveRaceRepo) Save(ctx context.Context, payment *domain.Payment) error {
	if payment.Status == domain.StatusRequiresPayment {
		if existing, err := r.Repository.FindRequiresPaymentByOrderID(ctx, payment.OrderID); err == nil && existing.ID != payment.ID {
			return fmt.Errorf("duplicate open payment for order %s", payment.OrderID)
		}
	}
	return r.Repository.Save(ctx, payment)
}

func TestCreatePayment_ReusesOpenPaymentWhenSaveRaces(t *testing.T) {
	base := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	// Seed with a non-sequential id so NextPaymentID's pay_000001 collides on Save
	// (same-id overwrite would skip the unique-open-payment race path).
	winner, err := domain.NewPayment("pay_winner", "ord_1", "cust_1", 4200, domain.DefaultCurrency, "test", "dev_ref", "http://checkout/1", now)
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}
	if err := base.Save(t.Context(), winner); err != nil {
		t.Fatalf("seed payment: %v", err)
	}

	repo := &saveRaceRepo{Repository: base, hideOpenPaymentOnce: true}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	got, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment after save race: %v", err)
	}
	if got.ID != winner.ID {
		t.Fatalf("payment id = %q, want winner %q", got.ID, winner.ID)
	}
	if got.CheckoutURL != winner.CheckoutURL {
		t.Fatalf("checkout_url = %q, want %q", got.CheckoutURL, winner.CheckoutURL)
	}
}

func TestCreatePayment_ReusesNewestRequiresPaymentForOrder(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	older, err := domain.NewPayment("pay_old", "ord_1", "cust_1", 4200, domain.DefaultCurrency, "test", "dev_old", "http://checkout/old", time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewPayment older: %v", err)
	}
	newer, err := domain.NewPayment("pay_new", "ord_1", "cust_1", 4200, domain.DefaultCurrency, "test", "dev_new", "http://checkout/new", time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewPayment newer: %v", err)
	}
	if err := repo.Save(t.Context(), older); err != nil {
		t.Fatalf("save older: %v", err)
	}
	if err := repo.Save(t.Context(), newer); err != nil {
		t.Fatalf("save newer: %v", err)
	}

	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)
	got, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if got.ID != newer.ID {
		t.Fatalf("payment id = %q, want newest %q", got.ID, newer.ID)
	}
}

func TestCreatePayment_ReusesExistingRequiresPaymentForOrder(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	first, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("first CreatePayment: %v", err)
	}

	second, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("second CreatePayment: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second payment id = %q, want %q", second.ID, first.ID)
	}
}

func TestCreatePayment_ReusesExistingSucceededForOrder(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	first, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	succeeded, err := svc.CompletePayment(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("CompletePayment: %v", err)
	}
	if succeeded.Status != domain.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", succeeded.Status)
	}

	second, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("second CreatePayment: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second payment id = %q, want %q (double checkout after success)", second.ID, first.ID)
	}
	if second.Status != domain.StatusSucceeded {
		t.Fatalf("second status = %s, want succeeded", second.Status)
	}
}

func TestCreatePayment_ReusesBypassSucceededWhenCardRetried(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	bypass, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID:           "ord_1",
		CustomerID:        "manager_1",
		BearerToken:       "token",
		Method:            domain.MethodBypass,
		AllowMethodBypass: true,
	})
	if err != nil {
		t.Fatalf("bypass CreatePayment: %v", err)
	}

	card, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("card CreatePayment after bypass: %v", err)
	}
	if card.ID != bypass.ID {
		t.Fatalf("card payment id = %q, want bypass %q", card.ID, bypass.ID)
	}
}

func TestCreatePayment_CardCheckout(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	payment, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if payment.Status != domain.StatusRequiresPayment {
		t.Fatalf("status = %s", payment.Status)
	}
	if payment.Method != domain.MethodCreditCard {
		t.Fatalf("method = %s, want %s", payment.Method, domain.MethodCreditCard)
	}
	if payment.CheckoutURL == "" {
		t.Fatal("expected checkout URL")
	}
	if payment.Currency != domain.DefaultCurrency {
		t.Fatalf("currency = %q, want %q", payment.Currency, domain.DefaultCurrency)
	}
	if domain.DefaultCurrency != "krw" {
		t.Fatalf("DefaultCurrency = %q, want krw", domain.DefaultCurrency)
	}
}

func TestCreatePayment_BypassSucceedsAndPublishes(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
	}}
	pub := &recordingPublisher{}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, pub)

	payment, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID:           "ord_1",
		CustomerID:        "manager_1",
		BearerToken:       "token",
		Method:            domain.MethodBypass,
		Note:              "Cash at showroom",
		CreatedBy:         "manager_1",
		AllowMethodBypass: true,
	})
	if err != nil {
		t.Fatalf("CreatePayment bypass: %v", err)
	}
	if payment.Status != domain.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", payment.Status)
	}
	if payment.Method != domain.MethodBypass {
		t.Fatalf("method = %s", payment.Method)
	}
	if payment.Provider != domain.ProviderBypass {
		t.Fatalf("provider = %s", payment.Provider)
	}
	if payment.CheckoutURL != "" {
		t.Fatalf("checkout_url should be empty, got %q", payment.CheckoutURL)
	}
	if payment.CreatedBy != "manager_1" || payment.Note != "Cash at showroom" {
		t.Fatalf("audit fields: created_by=%q note=%q", payment.CreatedBy, payment.Note)
	}
	if payment.AmountCents != 70000 {
		t.Fatalf("amount = %d", payment.AmountCents)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d, want 1", len(pub.events))
	}
	if pub.events[0].OrderID != "ord_1" || pub.events[0].AmountCents != 70000 {
		t.Fatalf("unexpected event: %+v", pub.events[0])
	}
}

func TestCreatePayment_BypassForbiddenWithoutPermission(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	_, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", Method: domain.MethodBypass,
	})
	if !errors.Is(err, ports.ErrPaymentForbidden) {
		t.Fatalf("err = %v, want ErrPaymentForbidden", err)
	}
}

func TestCreatePayment_BitcoinUnavailable(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	_, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", Method: domain.MethodBitcoin,
	})
	if !errors.Is(err, ports.ErrMethodUnavailable) {
		t.Fatalf("err = %v, want ErrMethodUnavailable", err)
	}
}

func TestCreatePayment_CardUnavailableWhenNoProviderConfigured(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	svc := service.New(repo, orders, checkout.NewUnavailableProvider("no PG configured"), nil)

	_, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if !errors.Is(err, ports.ErrMethodUnavailable) {
		t.Fatalf("err = %v, want ErrMethodUnavailable", err)
	}
}

func TestCreatePayment_UnknownMethod(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	_, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", Method: "venmo",
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("err = %v, want ErrInvalidPayment", err)
	}
}

func TestCreatePayment_BypassSkipsCustomerABAC(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	payment, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID:           "ord_1",
		CustomerID:        "manager_other",
		Method:            domain.MethodBypass,
		AllowMethodBypass: true,
		CreatedBy:         "manager_other",
	})
	if err != nil {
		t.Fatalf("bypass for other customer order: %v", err)
	}
	if payment.CustomerID != "cust_1" {
		t.Fatalf("payment customer_id = %s, want cust_1", payment.CustomerID)
	}
}

func TestCompletePayment_PublishesEvent(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	pub := &recordingPublisher{}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, pub)

	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	paid, err := svc.CompletePayment(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("CompletePayment: %v", err)
	}
	if paid.Status != domain.StatusSucceeded {
		t.Fatalf("status = %s", paid.Status)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d", len(pub.events))
	}
	if pub.events[0].OrderID != "ord_1" || pub.events[0].PaymentID != created.ID {
		t.Fatalf("unexpected event: %+v", pub.events[0])
	}
}

func TestCreatePayment_RejectsNonPendingOrder(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "paid", TotalCents: 4200,
	}}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, nil)

	_, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

type failOncePublisher struct {
	failFirst bool
	events    []ports.PaymentSucceededEvent
}

func (p *failOncePublisher) Publish(_ context.Context, subject string, event any) error {
	if subject != ports.PaymentSucceededSubject {
		return nil
	}
	if p.failFirst {
		p.failFirst = false
		return fmt.Errorf("nats unavailable")
	}
	ev, err := decodePaymentSucceededEvent(event)
	if err != nil {
		return err
	}
	p.events = append(p.events, ev)
	return nil
}

// decodePaymentSucceededEvent accepts either shape a publisher may see: the
// outbox drainer publishes the persisted json.RawMessage as-is, while
// ReconcileSucceededPayments (outside the outbox) publishes the typed event
// directly.
func decodePaymentSucceededEvent(event any) (ports.PaymentSucceededEvent, error) {
	switch v := event.(type) {
	case ports.PaymentSucceededEvent:
		return v, nil
	case json.RawMessage:
		var ev ports.PaymentSucceededEvent
		if err := json.Unmarshal(v, &ev); err != nil {
			return ports.PaymentSucceededEvent{}, fmt.Errorf("decode event: %w", err)
		}
		return ev, nil
	default:
		return ports.PaymentSucceededEvent{}, fmt.Errorf("unexpected event type %T", event)
	}
}

type failAlwaysPublisher struct {
	calls int
}

func (p *failAlwaysPublisher) Publish(_ context.Context, subject string, event any) error {
	p.calls++
	return fmt.Errorf("nats unavailable")
}

func TestCompletePayment_SoftSucceedsWhenPublishFails(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	pub := &failAlwaysPublisher{}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, pub)

	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	paid, err := svc.CompletePayment(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("CompletePayment should soft-succeed: %v", err)
	}
	if paid.Status != domain.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", paid.Status)
	}
	if pub.calls < 1 {
		t.Fatal("expected publish attempt")
	}
	pending, err := repo.ListPendingOutbox(t.Context(), 10)
	if err != nil {
		t.Fatalf("ListPendingOutbox: %v", err)
	}
	if len(pending) != 1 || pending[0].Subject != ports.PaymentSucceededSubject {
		t.Fatalf("pending = %+v, want one payment.succeeded", pending)
	}
}

func TestCompletePayment_DrainOutboxAfterPublishFailure(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	failPub := &failAlwaysPublisher{}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, failPub)

	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if _, err := svc.CompletePayment(t.Context(), created.ID); err != nil {
		t.Fatalf("CompletePayment: %v", err)
	}

	okPub := &recordingPublisher{}
	svcOK := service.New(repo, orders, fakeCheckoutProvider{}, okPub)
	if err := svcOK.DrainOutbox(t.Context()); err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	if len(okPub.events) != 1 {
		t.Fatalf("events = %d, want 1", len(okPub.events))
	}
	pending, err := repo.ListPendingOutbox(t.Context(), 10)
	if err != nil {
		t.Fatalf("ListPendingOutbox: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(pending))
	}
}

func TestCompletePayment_RepublishesAfterPriorPublishFailure(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	pub := &failOncePublisher{failFirst: true}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, pub)

	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	// Soft-success: first complete persists succeeded + outbox even when publish fails.
	if _, err := svc.CompletePayment(t.Context(), created.ID); err != nil {
		t.Fatalf("CompletePayment soft-success: %v", err)
	}
	paid, err := repo.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get after failed publish: %v", err)
	}
	if paid.Status != domain.StatusSucceeded {
		t.Fatalf("status after failed publish = %s, want succeeded", paid.Status)
	}

	if _, err := svc.CompletePayment(t.Context(), created.ID); err != nil {
		t.Fatalf("retry CompletePayment: %v", err)
	}
	if len(pub.events) < 1 {
		t.Fatalf("events after retry = %d, want at least 1", len(pub.events))
	}
}

func TestReconcileSucceededPaymentsRepublishes(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 4200,
	}}
	pub := &recordingPublisher{}
	svc := service.New(repo, orders, fakeCheckoutProvider{}, pub)

	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if _, err := svc.CompletePayment(t.Context(), created.ID); err != nil {
		t.Fatalf("CompletePayment: %v", err)
	}
	pub.events = nil

	if err := svc.ReconcileSucceededPayments(t.Context(), time.Hour); err != nil {
		t.Fatalf("ReconcileSucceededPayments: %v", err)
	}
	if len(pub.events) != 1 || pub.events[0].PaymentID != created.ID {
		t.Fatalf("reconcile events = %+v", pub.events)
	}
}

func TestCreatePayment_NanoCheckout(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "윤라희", RecipientPhone: "010-4112-5167",
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		BaseURL: "https://dev3.nanopay.co.kr", Ver: "240000005", ShopCode: "240000005",
		LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)

	payment, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if payment.Provider != domain.ProviderNano {
		t.Fatalf("provider = %s, want nano", payment.Provider)
	}
	if payment.PayerName != "윤라희" || payment.PayerPhone != "010-4112-5167" {
		t.Fatalf("payer = %q / %q", payment.PayerName, payment.PayerPhone)
	}
	if payment.CheckoutURL != "http://localhost:8080/api/v1/payments/"+payment.ID+"/nano/checkout" {
		t.Fatalf("checkout_url = %s", payment.CheckoutURL)
	}
}

func TestCreatePayment_NanoRequiresRecipient(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)
	_, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("err = %v, want ErrInvalidPayment", err)
	}
}

func TestHandleNanoResult_Success(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	pub := &recordingPublisher{}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, pub)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	auth := nanoAuth("test-key")
	ts := "1725440123456"
	paid, err := svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", ShopCode: "240000005", CompOrderNo: created.ID,
		ReqPayAmt: "70000", TranNo: "2409030071109",
		Timestamp: ts,
		HashValue: checkout.NanoHash(auth.Ver, auth.LoginID, auth.ShopCode, "70000", ts, auth.APIKey),
	})
	if err != nil {
		t.Fatalf("HandleNanoResult: %v", err)
	}
	if paid.Status != domain.StatusSucceeded {
		t.Fatalf("status = %s", paid.Status)
	}
	if paid.ProviderRef != "2409030071109" {
		t.Fatalf("provider_ref = %s", paid.ProviderRef)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d", len(pub.events))
	}
}

func TestHandleNanoResult_UnsignedReturnMACSucceeds(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	auth := nanoAuth("test-key")
	ts := fmt.Sprintf("%d", time.Now().UTC().UnixMilli())
	paid, err := svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", ShopCode: "240000005", CompOrderNo: created.ID,
		ReqPayAmt: "70000", TranNo: "tn_unsigned",
		ReturnTS:  ts,
		ReturnMAC: checkout.NanoReturnMAC(auth.Ver, auth.LoginID, auth.ShopCode, created.ID, "70000", ts, auth.APIKey),
	})
	if err != nil {
		t.Fatalf("HandleNanoResult: %v", err)
	}
	if paid.Status != domain.StatusSucceeded {
		t.Fatalf("status = %s", paid.Status)
	}
	if paid.ProviderRef != "tn_unsigned" {
		t.Fatalf("provider_ref = %s", paid.ProviderRef)
	}
}

func TestHandleNanoResult_ReturnMACDoesNotCrossPayments(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	auth := nanoAuth("test-key")
	ts := fmt.Sprintf("%d", time.Now().UTC().UnixMilli())
	mac := checkout.NanoReturnMAC(auth.Ver, auth.LoginID, auth.ShopCode, "pay_other", "70000", ts, auth.APIKey)
	_, err = svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", ShopCode: "240000005", CompOrderNo: created.ID,
		ReqPayAmt: "70000", ReturnTS: ts, ReturnMAC: mac,
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("err = %v, want ErrInvalidPayment", err)
	}
	got, err := svc.GetPayment(t.Context(), created.ID, "cust_1")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if got.Status == domain.StatusSucceeded {
		t.Fatal("MAC for another payment must not mark this one succeeded")
	}
}

func TestHandleNanoResult_AmountMismatch(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	auth := nanoAuth("test-key")
	ts := "1725440123456"
	_, err = svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", ShopCode: "240000005", CompOrderNo: created.ID, ReqPayAmt: "1",
		Timestamp: ts,
		HashValue: checkout.NanoHash(auth.Ver, auth.LoginID, auth.ShopCode, "1", ts, auth.APIKey),
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("err = %v, want ErrInvalidPayment", err)
	}
}

func TestHandleNanoResult_FailureMarksFailed(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	paid, err := svc.HandleNanoResult(t.Context(), nanoAuth("test-key"), service.NanoResult{
		ResultCode: "9999", ShopCode: "240000005", CompOrderNo: created.ID, ReqPayAmt: "70000",
	})
	if err != nil {
		t.Fatalf("HandleNanoResult: %v", err)
	}
	if paid.Status != domain.StatusFailed {
		t.Fatalf("status = %s, want failed", paid.Status)
	}
}

func TestHandleNanoResult_ForgedSuccessMissingFieldsRejected(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	auth := nanoAuth("test-key")

	// Minimal forgery: resultCode + payment id only (shopcode/amount omitted).
	_, err = svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", CompOrderNo: created.ID,
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("minimal forge err = %v, want ErrInvalidPayment", err)
	}

	// Full field forgery without valid hashValue.
	_, err = svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", ShopCode: "240000005", CompOrderNo: created.ID,
		ReqPayAmt: "70000", Timestamp: "1725440123456", HashValue: "deadbeef",
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("bad hash err = %v, want ErrInvalidPayment", err)
	}

	got, err := svc.GetPayment(t.Context(), created.ID, "cust_1")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if got.Status == domain.StatusSucceeded {
		t.Fatalf("forged callback must not mark payment succeeded")
	}
}

func TestHandleNanoResult_MissingShopCodeRejected(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	auth := nanoAuth("test-key")
	ts := "1725440123456"
	_, err = svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", CompOrderNo: created.ID, ReqPayAmt: "70000",
		Timestamp: ts,
		HashValue: checkout.NanoHash(auth.Ver, auth.LoginID, auth.ShopCode, "70000", ts, auth.APIKey),
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("err = %v, want ErrInvalidPayment", err)
	}
}

func TestHandleNanoResult_ShopCodeMismatch(t *testing.T) {
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key", PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, orders, nano, nil)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord_1", CustomerID: "cust_1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	auth := nanoAuth("test-key")
	_, err = svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", ShopCode: "wrong-shop", CompOrderNo: created.ID, ReqPayAmt: "70000",
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("err = %v, want ErrInvalidPayment", err)
	}
}

func nanoAuth(apiKey string) service.NanoCallbackAuth {
	return service.NanoCallbackAuth{
		Ver:      "240000005",
		LoginID:  "shoptest",
		ShopCode: "240000005",
		APIKey:   apiKey,
	}
}
