package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/handler"
	"github.com/elug3/dupli1/payment/pkg/infra/checkout"
	"github.com/elug3/dupli1/payment/pkg/infra/memory"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/elug3/dupli1/payment/pkg/service"
	"github.com/elug3/dupli1/shared/pkg/authjwt"
	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/golang-jwt/jwt/v5"
)

type stubOrderClient struct{}

func (s stubOrderClient) GetOrder(_ context.Context, _, _ string) (*ports.OrderSummary, error) {
	return &ports.OrderSummary{ID: "ord-1", CustomerID: "u-1", Status: "pending", TotalCents: 1000}, nil
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

func makeToken(t *testing.T, secret, userID string, perms []string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":         userID,
		"permissions": perms,
		"exp":         time.Now().Add(time.Hour).Unix(),
		"iat":         time.Now().Unix(),
		"type":        "access",
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSettingsDoesNotRequireAuth(t *testing.T) {
	repo := memory.NewRepository()
	svc := service.New(repo, stubOrderClient{}, fakeCheckoutProvider{}, nil)
	h := handler.New(svc, authjwt.NewHMACValidator("test-secret"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, path := range []string{"/settings", "/api/v1/payments/settings"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if body["service"] != "payment" {
			t.Fatalf("%s service = %v, want payment", path, body["service"])
		}
	}
}

// TestSimulateSuccessRouteRemoved guards against reintroducing the dev-simulate
// checkout path: local/manual testing now goes through method=bypass instead.
func TestSimulateSuccessRouteRemoved(t *testing.T) {
	repo := memory.NewRepository()
	svc := service.New(repo, stubOrderClient{}, fakeCheckoutProvider{}, nil)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord-1", CustomerID: "u-1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	h := handler.New(svc, authjwt.NewHMACValidator("test-secret"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/payments/"+created.ID+"/simulate-success", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRequireAuthFailsClosedWithoutValidator(t *testing.T) {
	repo := memory.NewRepository()
	svc := service.New(repo, stubOrderClient{}, fakeCheckoutProvider{}, nil)
	h := handler.New(svc, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader([]byte(`{"order_id":"ord-1"}`)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestCreatePayment_BypassRequiresPermission(t *testing.T) {
	const secret = "test-secret"
	repo := memory.NewRepository()
	pub := &recordingPublisher{}
	svc := service.New(repo, stubOrderClient{}, fakeCheckoutProvider{}, pub)
	h := handler.New(svc, authjwt.NewHMACValidator(secret))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body, _ := json.Marshal(map[string]string{
		"order_id": "ord-1",
		"method":   "bypass",
		"note":     "cash",
	})

	// Customer token — forbidden
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+makeToken(t, secret, "u-1", nil))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("customer bypass status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}

	// Manager with payment.bypass — succeeded
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+makeToken(t, secret, "mgr-1", []string{permissions.PaymentBypass}))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("manager bypass status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var payment domain.Payment
	if err := json.NewDecoder(rec.Body).Decode(&payment); err != nil {
		t.Fatal(err)
	}
	if payment.Status != domain.StatusSucceeded || payment.Method != domain.MethodBypass {
		t.Fatalf("payment = %+v", payment)
	}
	if payment.CreatedBy != "mgr-1" || payment.Note != "cash" {
		t.Fatalf("audit: created_by=%q note=%q", payment.CreatedBy, payment.Note)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d", len(pub.events))
	}
}

func TestCreatePayment_BitcoinNotImplemented(t *testing.T) {
	const secret = "test-secret"
	repo := memory.NewRepository()
	svc := service.New(repo, stubOrderClient{}, fakeCheckoutProvider{}, nil)
	h := handler.New(svc, authjwt.NewHMACValidator(secret))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body, _ := json.Marshal(map[string]string{"order_id": "ord-1", "method": "bitcoin"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+makeToken(t, secret, "u-1", nil))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body: %s", rec.Code, rec.Body.String())
	}
}

type recordingPublisher struct {
	events []ports.PaymentSucceededEvent
	// rejected captures payment.callback_rejected, which the service publishes
	// directly (not through the outbox) because no state change accompanies it.
	rejected []ports.PaymentCallbackRejectedEvent
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, event any) error {
	if subject == ports.PaymentCallbackRejectedSubject {
		ev, ok := event.(ports.PaymentCallbackRejectedEvent)
		if !ok {
			return fmt.Errorf("unexpected event type %T", event)
		}
		p.rejected = append(p.rejected, ev)
		return nil
	}
	if subject == ports.PaymentSucceededSubject {
		// The outbox drainer republishes the persisted json.RawMessage as-is.
		raw, ok := event.(json.RawMessage)
		if !ok {
			return fmt.Errorf("unexpected event type %T", event)
		}
		var ev ports.PaymentSucceededEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		p.events = append(p.events, ev)
	}
	return nil
}

type nanoOrderClient struct{}

func (s nanoOrderClient) GetOrder(_ context.Context, _, _ string) (*ports.OrderSummary, error) {
	return &ports.OrderSummary{
		ID: "ord-1", CustomerID: "u-1", Status: "pending", TotalCents: 1000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}, nil
}

func TestNanoReturn_SucceedsAndRedirects(t *testing.T) {
	repo := memory.NewRepository()
	pub := &recordingPublisher{}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key",
		PublicBaseURL: "http://localhost:8080",
		SuccessURL:    "http://localhost:5173/checkout/confirmation",
		FailureURL:    "http://localhost:5173/checkout",
	})
	svc := service.New(repo, nanoOrderClient{}, nano, pub)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord-1", CustomerID: "u-1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	h := handler.New(svc, authjwt.NewHMACValidator("test-secret")).WithNano(nano)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := "1725440123456"
	hash := checkout.NanoHash("240000005", "shoptest", "240000005", "1000", ts, "test-key")
	form := "resultCode=0000&shopcode=240000005&compOrderNo=" + created.ID +
		"&reqPayAmt=1000&tranNo=tn_1&timestamp=" + ts + "&hashValue=" + hash
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/nano/return", bytes.NewBufferString(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc == "" || !bytes.Contains([]byte(loc), []byte("order_id=ord-1")) {
		t.Fatalf("Location = %q", loc)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d", len(pub.events))
	}
}

func TestNanoReturn_RejectsForgedMinimalCallback(t *testing.T) {
	repo := memory.NewRepository()
	pub := &recordingPublisher{}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key",
		PublicBaseURL: "http://localhost:8080",
		SuccessURL:    "http://localhost:5173/checkout/confirmation",
	})
	svc := service.New(repo, nanoOrderClient{}, nano, pub)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord-1", CustomerID: "u-1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	h := handler.New(svc, authjwt.NewHMACValidator("test-secret")).WithNano(nano)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	form := "resultCode=0000&compOrderNo=" + created.ID
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/nano/return", bytes.NewBufferString(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// The callback is refused, so the shopper must not land on the confirmation
	// page and no payment.succeeded may escape...
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "/checkout/confirmation") {
		t.Fatalf("forged callback must not redirect as success: %q", loc)
	}
	if len(pub.events) != 0 {
		t.Fatalf("events = %d, want 0", len(pub.events))
	}
	// ...and with no failure URL configured the refusal is still a page, never a
	// JSON error body in the shopper's browser (elug3/dupli1#232).
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html; body: %s", ct, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "다시 결제하지") {
		t.Fatalf("page must tell the shopper not to pay again; body: %s", rec.Body.String())
	}
	stored, err := repo.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status == domain.StatusSucceeded {
		t.Fatalf("forged callback marked the payment succeeded")
	}
}

func TestNanoWebhook_SucceedsAndReturnsResultCode00(t *testing.T) {
	repo := memory.NewRepository()
	pub := &recordingPublisher{}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key",
		PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, nanoOrderClient{}, nano, pub)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord-1", CustomerID: "u-1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	h := handler.New(svc, authjwt.NewHMACValidator("test-secret")).WithNano(nano)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := "1725440123456"
	hash := checkout.NanoHash("240000005", "shoptest", "240000005", "1000", ts, "test-key")
	form := "resultCode=0000&shopcode=240000005&compOrderNo=" + created.ID +
		"&reqPayAmt=1000&tranNo=tn_1&timestamp=" + ts + "&hashValue=" + hash
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhooks/nano", bytes.NewBufferString(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["resultCode"] != "00" {
		t.Fatalf("resultCode = %q, want 00", body["resultCode"])
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d, want 1", len(pub.events))
	}
}

func TestNanoWebhook_RejectsForgedCallback(t *testing.T) {
	repo := memory.NewRepository()
	pub := &recordingPublisher{}
	nano := checkout.NewNanoProvider(checkout.NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key",
		PublicBaseURL: "http://localhost:8080",
	})
	svc := service.New(repo, nanoOrderClient{}, nano, pub)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord-1", CustomerID: "u-1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	h := handler.New(svc, authjwt.NewHMACValidator("test-secret")).WithNano(nano)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	form := "resultCode=0000&compOrderNo=" + created.ID
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhooks/nano", bytes.NewBufferString(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("forged webhook must not succeed, body=%s", rec.Body.String())
	}
	if len(pub.events) != 0 {
		t.Fatalf("events = %d, want 0", len(pub.events))
	}
}

// nanoTestStack wires a NANO-enabled payment service over an in-memory repo and
// returns the pieces the return-path tests assert on.
func nanoTestStack(t *testing.T, cfg checkout.NanoConfig) (*http.ServeMux, *memory.Repository, *recordingPublisher, *domain.Payment) {
	t.Helper()
	repo := memory.NewRepository()
	pub := &recordingPublisher{}
	nano := checkout.NewNanoProvider(cfg)
	svc := service.New(repo, nanoOrderClient{}, nano, pub)
	created, err := svc.CreatePayment(t.Context(), service.CreatePaymentInput{
		OrderID: "ord-1", CustomerID: "u-1", BearerToken: "token",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	mux := http.NewServeMux()
	handler.New(svc, authjwt.NewHMACValidator("test-secret")).WithNano(nano).RegisterRoutes(mux)
	return mux, repo, pub, created
}

func nanoStorefrontConfig() checkout.NanoConfig {
	return checkout.NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key",
		PublicBaseURL: "http://localhost:8080",
		SuccessURL:    "http://localhost:5173/checkout/confirmation",
		FailureURL:    "http://localhost:5173/checkout",
	}
}

func postNanoForm(t *testing.T, mux *http.ServeMux, path, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// An approval dupli1 cannot verify is the dangerous case: NANO says the card was
// charged and we refuse to record it. The shopper must land on the storefront
// with a reason it reads as "do not pay again", and ops must hear about it.
func TestNanoReturn_ApprovedButUnverifiedNeverInvitesRetry(t *testing.T) {
	mux, repo, pub, created := nanoTestStack(t, nanoStorefrontConfig())

	rec := postNanoForm(t, mux, "/api/v1/payments/nano/return",
		"resultCode=0000&shopcode=240000005&compOrderNo="+created.ID+
			"&reqPayAmt=1000&tranNo=tn_1&timestamp=1725440123456&hashValue=deadbeef")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "http://localhost:5173/checkout?") {
		t.Fatalf("Location = %q, want the storefront failure page", loc)
	}
	got, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if reason := got.Query().Get("error"); reason != "verify_failed" {
		t.Fatalf("error = %q, want verify_failed", reason)
	}
	if id := got.Query().Get("payment_id"); id != created.ID {
		t.Fatalf("payment_id = %q, want %q", id, created.ID)
	}

	stored, err := repo.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status == domain.StatusSucceeded {
		t.Fatalf("unverified callback marked the payment succeeded")
	}
	if len(pub.events) != 0 {
		t.Fatalf("payment.succeeded events = %d, want 0", len(pub.events))
	}

	if len(pub.rejected) != 1 {
		t.Fatalf("callback_rejected events = %d, want 1", len(pub.rejected))
	}
	alert := pub.rejected[0]
	if alert.Reason != string(service.NanoRejectVerifyFailed) {
		t.Errorf("alert reason = %q, want verify_failed", alert.Reason)
	}
	if alert.Source != service.NanoSourceReturn {
		t.Errorf("alert source = %q, want return", alert.Source)
	}
	if alert.PaymentID != created.ID || alert.OrderID != "ord-1" {
		t.Errorf("alert ids = %q/%q, want %q/ord-1", alert.PaymentID, alert.OrderID, created.ID)
	}
	if alert.ResultCode != "0000" {
		t.Errorf("alert result_code = %q, want 0000", alert.ResultCode)
	}
	if alert.ExpectedCents != 1000 || alert.ReportedAmount != "1000" {
		t.Errorf("alert amounts = %d/%q, want 1000/\"1000\"", alert.ExpectedCents, alert.ReportedAmount)
	}
}

// A genuine decline is safe to retry, and must not page anyone.
func TestNanoReturn_DeclineRedirectsAsRetryable(t *testing.T) {
	mux, _, pub, created := nanoTestStack(t, nanoStorefrontConfig())

	rec := postNanoForm(t, mux, "/api/v1/payments/nano/return",
		"resultCode=9999&resultMsg=user+cancelled&shopcode=240000005&compOrderNo="+created.ID+"&reqPayAmt=1000")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", rec.Code, rec.Body.String())
	}
	got, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if reason := got.Query().Get("error"); reason != "declined" {
		t.Fatalf("error = %q, want declined", reason)
	}
	if len(pub.rejected) != 0 {
		t.Fatalf("a decline must not raise an ops alert: %+v", pub.rejected)
	}
}

// Point 1 of the issue, stated as a property: no shape of failed return renders
// JSON into a browser.
func TestNanoReturn_NeverRendersJSONToTheShopper(t *testing.T) {
	cases := map[string]string{
		"unparseable":       "",
		"unknown payment":   "resultCode=0000&shopcode=240000005&compOrderNo=pay_missing&reqPayAmt=1000",
		"wrong shopcode":    "resultCode=0000&shopcode=999&compOrderNo=%s&reqPayAmt=1000",
		"amount mismatch":   "resultCode=0000&shopcode=240000005&compOrderNo=%s&reqPayAmt=999999",
		"unverifiable hash": "resultCode=0000&shopcode=240000005&compOrderNo=%s&reqPayAmt=1000&timestamp=1&hashValue=nope",
		"declined":          "resultCode=9999&shopcode=240000005&compOrderNo=%s&reqPayAmt=1000",
	}
	// Both with a storefront failure page configured and without one: the
	// property must hold on the code, not on the deployment's env vars.
	configs := map[string]checkout.NanoConfig{"with failure url": nanoStorefrontConfig()}
	bare := nanoStorefrontConfig()
	bare.FailureURL = ""
	configs["without failure url"] = bare

	for cfgName, cfg := range configs {
		for name, form := range cases {
			t.Run(cfgName+"/"+name, func(t *testing.T) {
				mux, _, _, created := nanoTestStack(t, cfg)
				if strings.Contains(form, "%s") {
					form = fmt.Sprintf(form, created.ID)
				}
				rec := postNanoForm(t, mux, "/api/v1/payments/nano/return", form)

				if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "application/json") {
					t.Fatalf("shopper received JSON (%s): %s", ct, rec.Body.String())
				}
				if rec.Code >= 400 {
					t.Fatalf("status = %d, want a redirect or a page; body: %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

// The webhook is server-to-server: NANO reads the status code, so it keeps the
// JSON contract. Only the alert is shared with the browser path.
func TestNanoWebhook_KeepsJSONAndAlertsOnApprovedRejection(t *testing.T) {
	mux, _, pub, created := nanoTestStack(t, nanoStorefrontConfig())

	rec := postNanoForm(t, mux, "/api/v1/payments/webhooks/nano",
		"resultCode=0000&shopcode=240000005&compOrderNo="+created.ID+
			"&reqPayAmt=1000&timestamp=1725440123456&hashValue=deadbeef")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.rejected) != 1 {
		t.Fatalf("callback_rejected events = %d, want 1", len(pub.rejected))
	}
	if src := pub.rejected[0].Source; src != service.NanoSourceWebhook {
		t.Fatalf("alert source = %q, want webhook", src)
	}
}
