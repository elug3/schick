package handler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/elug3/dupli1/payment/pkg/service"
)

// The one rule that must not erode: if NANO said it approved the card, nothing
// that went wrong afterwards may be described to the shopper as an ordinary
// decline, because the storefront turns that into a "try again" button and the
// retry would be a second charge (elug3/dupli1#232).
func TestNanoReturnReason_ApprovedIsNeverRetryable(t *testing.T) {
	approved := service.NanoResult{ResultCode: "0000"}
	reasons := []service.NanoRejectReason{
		service.NanoRejectUnknownPayment,
		service.NanoRejectShopMismatch,
		service.NanoRejectNotNano,
		service.NanoRejectAmountMismatch,
		service.NanoRejectVerifyFailed,
		service.NanoRejectLookupFailed,
		service.NanoRejectPersistFailed,
	}
	errs := []error{
		// An error carrying no rejection detail at all — the case a future code
		// path added without thinking about this rule would produce.
		errors.New("boom"),
		fmt.Errorf("wrapped: %w", ports.ErrNotFound),
		domain.ErrInvalidPayment,
	}
	for _, reason := range reasons {
		errs = append(errs, &service.NanoCallbackError{Reason: reason, Approved: true, PaymentID: "pay_1", OrderID: "ord_1"})
	}

	for _, err := range errs {
		got := nanoReturnReason(approved, err)
		if !nanoReturnUnconfirmedReasons[got] {
			t.Errorf("nanoReturnReason(approved, %v) = %q, which the storefront reads as retryable", err, got)
		}
	}
}

func TestNanoReturnReason_DeclineIsRetryable(t *testing.T) {
	for _, code := range []string{"9999", "", "0001", "  "} {
		result := service.NanoResult{ResultCode: code}
		if got := nanoReturnReason(result, domain.ErrInvalidPayment); got != nanoReturnDeclined {
			t.Errorf("resultCode %q: reason = %q, want %q", code, got, nanoReturnDeclined)
		}
	}
}

func TestNanoReturnReason_NamesTheAmountMismatch(t *testing.T) {
	// Worth distinguishing: the shopper can be told the charged amount did not
	// match, which is a different support conversation from an unverifiable hash.
	err := &service.NanoCallbackError{Reason: service.NanoRejectAmountMismatch, Approved: true, PaymentID: "pay_1", OrderID: "ord_1"}
	if got := nanoReturnReason(service.NanoResult{ResultCode: "0000"}, err); got != nanoReturnAmountMismatch {
		t.Fatalf("reason = %q, want %q", got, nanoReturnAmountMismatch)
	}
}

// The reasons that suppress the retry button are a cross-repo contract with
// dupli1-web's UNCONFIRMED_REASONS. Adding one here without adding it there
// silently re-opens the bug, so the set is pinned.
func TestNanoReturnUnconfirmedReasonsMatchStorefront(t *testing.T) {
	want := map[string]bool{
		"verify_failed":       true,
		"verification_failed": true,
		"invalid_payment":     true,
		"amount_mismatch":     true,
	}
	if len(nanoReturnUnconfirmedReasons) != len(want) {
		t.Fatalf("unconfirmed reasons = %v, want %v", nanoReturnUnconfirmedReasons, want)
	}
	for reason := range want {
		if !nanoReturnUnconfirmedReasons[reason] {
			t.Errorf("%q missing from the unconfirmed set", reason)
		}
	}
	// The retryable reasons must stay out of it.
	for _, reason := range []string{nanoReturnDeclined, nanoReturnInvalidPayload} {
		if nanoReturnUnconfirmedReasons[reason] {
			t.Errorf("%q must not suppress the retry button", reason)
		}
	}
}

func TestAppendNanoReturnQuery(t *testing.T) {
	cases := []struct {
		name                       string
		base                       string
		orderID, paymentID, reason string
		want                       string
	}{
		{
			name: "all fields", base: "http://shop/checkout",
			orderID: "ord_1", paymentID: "pay_1", reason: "verify_failed",
			want: "http://shop/checkout?error=verify_failed&order_id=ord_1&payment_id=pay_1",
		},
		{
			name: "empty values are omitted rather than blank", base: "http://shop/checkout",
			paymentID: "pay_1", reason: "verify_failed",
			want: "http://shop/checkout?error=verify_failed&payment_id=pay_1",
		},
		{
			name: "no reason on the success path", base: "http://shop/confirmation",
			orderID: "ord_1", paymentID: "pay_1",
			want: "http://shop/confirmation?order_id=ord_1&payment_id=pay_1",
		},
		{
			name: "existing query is preserved", base: "http://shop/checkout?step=pay",
			orderID: "ord_1", reason: "declined",
			want: "http://shop/checkout?error=declined&order_id=ord_1&step=pay",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendNanoReturnQuery(tc.base, tc.orderID, tc.paymentID, tc.reason); got != tc.want {
				t.Fatalf("appendNanoReturnQuery = %q, want %q", got, tc.want)
			}
		})
	}
}

// A bridge failure means the card window never opened, so it must stay retryable.
func TestNanoReturnCheckoutFailedIsRetryable(t *testing.T) {
	if nanoReturnUnconfirmedReasons[nanoReturnCheckoutFailed] {
		t.Fatalf("%q must not suppress the retry button — nothing was charged", nanoReturnCheckoutFailed)
	}
}
