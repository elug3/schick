package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/infra/checkout"
	"github.com/elug3/dupli1/payment/pkg/infra/memory"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/elug3/dupli1/payment/pkg/service"
)

// rejectionRecorder captures the ops alert without caring about any other subject.
type rejectionRecorder struct {
	events []ports.PaymentCallbackRejectedEvent
	fail   bool
}

func (r *rejectionRecorder) Publish(_ context.Context, subject string, event any) error {
	if r.fail {
		return errors.New("nats unavailable")
	}
	if subject == ports.PaymentCallbackRejectedSubject {
		if ev, ok := event.(ports.PaymentCallbackRejectedEvent); ok {
			r.events = append(r.events, ev)
		}
	}
	return nil
}

func nanoCallbackFixture(t *testing.T, pub ports.EventPublisher) (*service.Service, *domain.Payment) {
	t.Helper()
	repo := memory.NewRepository()
	orders := stubOrderClient{order: &ports.OrderSummary{
		ID: "ord_1", CustomerID: "cust_1", Status: "pending", TotalCents: 70000,
		RecipientName: "홍길동", RecipientPhone: "01012345678",
	}}
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
	return svc, created
}

// Every way of refusing a callback NANO already approved has to raise the alert:
// in each case a card may be charged with nothing on our side to show for it.
func TestHandleNanoResult_ApprovedRejectionAlerts(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(result *service.NanoResult, paymentID string)
		reason  service.NanoRejectReason
		wantIDs bool
	}{
		{
			name:   "unknown payment",
			mutate: func(r *service.NanoResult, _ string) { r.CompOrderNo = "pay_missing" },
			reason: service.NanoRejectUnknownPayment,
		},
		{
			name:   "shopcode not ours",
			mutate: func(r *service.NanoResult, _ string) { r.ShopCode = "999999" },
			reason: service.NanoRejectShopMismatch,
		},
		{
			name:    "amount disagrees",
			mutate:  func(r *service.NanoResult, _ string) { r.ReqPayAmt = "1" },
			reason:  service.NanoRejectAmountMismatch,
			wantIDs: true,
		},
		{
			name:    "hash does not verify",
			mutate:  func(r *service.NanoResult, _ string) { r.HashValue = "deadbeef" },
			reason:  service.NanoRejectVerifyFailed,
			wantIDs: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub := &rejectionRecorder{}
			svc, created := nanoCallbackFixture(t, pub)

			auth := nanoAuth("test-key")
			ts := "1725440123456"
			result := service.NanoResult{
				ResultCode: "0000", ShopCode: "240000005", CompOrderNo: created.ID,
				ReqPayAmt: "70000", TranNo: "2409030071109", Timestamp: ts,
				HashValue: checkout.NanoHash(auth.Ver, auth.LoginID, auth.ShopCode, "70000", ts, auth.APIKey),
				Source:    service.NanoSourceWebhook,
			}
			tc.mutate(&result, created.ID)

			_, err := svc.HandleNanoResult(t.Context(), auth, result)
			if err == nil {
				t.Fatal("callback was accepted")
			}
			// The JSON API paths key off the sentinel, so wrapping must not hide it.
			if !errors.Is(err, domain.ErrInvalidPayment) && !errors.Is(err, ports.ErrNotFound) {
				t.Fatalf("err = %v, want a wrapped ErrInvalidPayment or ErrNotFound", err)
			}
			rejection, ok := service.NanoRejection(err)
			if !ok {
				t.Fatalf("err = %v, carries no rejection detail", err)
			}
			if rejection.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", rejection.Reason, tc.reason)
			}
			if !rejection.Approved {
				t.Error("rejection must record that the PG approved")
			}

			if len(pub.events) != 1 {
				t.Fatalf("alerts = %d, want 1", len(pub.events))
			}
			alert := pub.events[0]
			if alert.Reason != string(tc.reason) {
				t.Errorf("alert reason = %q, want %q", alert.Reason, tc.reason)
			}
			if alert.Source != service.NanoSourceWebhook {
				t.Errorf("alert source = %q, want the source the handler set", alert.Source)
			}
			if alert.Detail == "" {
				t.Error("alert carries no human-readable detail")
			}
			if tc.wantIDs && (alert.PaymentID != created.ID || alert.OrderID != "ord_1") {
				t.Errorf("alert ids = %q/%q, want %q/ord_1", alert.PaymentID, alert.OrderID, created.ID)
			}
		})
	}
}

// A decline is an ordinary outcome; paging someone for each one would bury the
// alert that matters.
func TestHandleNanoResult_DeclineDoesNotAlert(t *testing.T) {
	pub := &rejectionRecorder{}
	svc, created := nanoCallbackFixture(t, pub)

	failed, err := svc.HandleNanoResult(t.Context(), nanoAuth("test-key"), service.NanoResult{
		ResultCode: "9999", ResultMsg: "user cancelled", ShopCode: "240000005",
		CompOrderNo: created.ID, ReqPayAmt: "70000",
	})
	if err != nil {
		t.Fatalf("HandleNanoResult: %v", err)
	}
	if failed.Status != domain.StatusFailed {
		t.Fatalf("status = %s, want failed", failed.Status)
	}
	if len(pub.events) != 0 {
		t.Fatalf("alerts = %d, want 0: %+v", len(pub.events), pub.events)
	}
}

// The alert is best-effort: it is published outside the outbox, and a NATS
// outage must not change what the caller is told about the callback.
func TestHandleNanoResult_AlertPublishFailureStillRejects(t *testing.T) {
	pub := &rejectionRecorder{fail: true}
	svc, created := nanoCallbackFixture(t, pub)

	auth := nanoAuth("test-key")
	_, err := svc.HandleNanoResult(t.Context(), auth, service.NanoResult{
		ResultCode: "0000", ShopCode: "240000005", CompOrderNo: created.ID,
		ReqPayAmt: "70000", Timestamp: "1725440123456", HashValue: "deadbeef",
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("err = %v, want ErrInvalidPayment", err)
	}
	rejection, ok := service.NanoRejection(err)
	if !ok || rejection.Reason != service.NanoRejectVerifyFailed {
		t.Fatalf("rejection = %+v, want verify_failed", rejection)
	}
}

// Service constructed without a publisher (the in-memory dev path) must not
// panic on the alert.
func TestHandleNanoResult_AlertWithoutPublisher(t *testing.T) {
	svc, created := nanoCallbackFixture(t, nil)

	_, err := svc.HandleNanoResult(t.Context(), nanoAuth("test-key"), service.NanoResult{
		ResultCode: "0000", ShopCode: "240000005", CompOrderNo: created.ID,
		ReqPayAmt: "70000", Timestamp: "1725440123456", HashValue: "deadbeef",
	})
	if !errors.Is(err, domain.ErrInvalidPayment) {
		t.Fatalf("err = %v, want ErrInvalidPayment", err)
	}
}
