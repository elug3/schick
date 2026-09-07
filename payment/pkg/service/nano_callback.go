package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/ports"
)

// NanoRejectReason says why a NANO callback was not applied. It is a diagnostic
// for logs and ops alerts — the shopper-facing wording is chosen by the handler,
// which deliberately collapses these into a much smaller, safer vocabulary.
type NanoRejectReason string

const (
	// NanoRejectUnknownPayment — the callback named no payment we hold.
	NanoRejectUnknownPayment NanoRejectReason = "unknown_payment"
	// NanoRejectShopMismatch — shopcode absent or not ours.
	NanoRejectShopMismatch NanoRejectReason = "shop_mismatch"
	// NanoRejectNotNano — the payment exists but was not taken through NANO.
	NanoRejectNotNano NanoRejectReason = "not_nano_payment"
	// NanoRejectAmountMismatch — reqPayAmt disagrees with the amount we hold.
	NanoRejectAmountMismatch NanoRejectReason = "amount_mismatch"
	// NanoRejectVerifyFailed — resultCode was 0000 but hashValue did not verify.
	NanoRejectVerifyFailed NanoRejectReason = "verify_failed"
	// NanoRejectLookupFailed — the payment could not be read (storage error).
	NanoRejectLookupFailed NanoRejectReason = "lookup_failed"
	// NanoRejectPersistFailed — a verified approval could not be recorded. The
	// worst case: the card is charged, the hash checked out, and the order is
	// still not paid.
	NanoRejectPersistFailed NanoRejectReason = "persist_failed"
)

// Callback sources, recorded on the alert so ops can tell a browser return from
// a server-to-server webhook.
const (
	NanoSourceReturn  = "return"
	NanoSourceWebhook = "webhook"
)

// nanoApprovedCode is NANO's "approved" result code. Any other value is a decline.
const nanoApprovedCode = "0000"

// NanoCallbackError is a callback that dupli1 refused to apply.
//
// It wraps the underlying sentinel (domain.ErrInvalidPayment, ports.ErrNotFound,
// …) so errors.Is keeps working and the JSON API paths keep their status codes;
// the added fields let the browser-return handler pick a safe redirect instead of
// having to re-derive the cause.
type NanoCallbackError struct {
	Reason NanoRejectReason
	// Approved reports whether the PG said the card was charged. When true the
	// shopper must never be invited to pay again (elug3/dupli1#232).
	Approved  bool
	PaymentID string
	OrderID   string
	err       error
}

func (e *NanoCallbackError) Error() string {
	return fmt.Sprintf("nano callback rejected (%s): %v", e.Reason, e.err)
}

func (e *NanoCallbackError) Unwrap() error { return e.err }

// nanoReject builds the rejection, emitting an ops alert first when the PG had
// already approved — that is the case where money may be gone with no paid order.
func (s *Service) nanoReject(ctx context.Context, result NanoResult, reason NanoRejectReason, payment *domain.Payment, err error) error {
	rejection := &NanoCallbackError{
		Reason:   reason,
		Approved: NanoApproved(result),
		err:      err,
	}
	if payment != nil {
		rejection.PaymentID = payment.ID
		rejection.OrderID = payment.OrderID
	}
	if rejection.PaymentID == "" {
		rejection.PaymentID = strings.TrimSpace(result.CompOrderNo)
	}
	if rejection.Approved {
		s.alertCallbackRejected(ctx, result, rejection, payment)
	} else {
		log.Printf("payment: nano callback ignored: reason=%s payment=%s result_code=%s",
			reason, rejection.PaymentID, strings.TrimSpace(result.ResultCode))
	}
	return rejection
}

// alertCallbackRejected publishes payment.callback_rejected for a human to chase.
//
// Best-effort by design: the callback is already being refused, and losing the
// alert must not turn into a second failure mode. A publish error is logged at
// the same volume as the rejection itself so the record survives locally.
func (s *Service) alertCallbackRejected(ctx context.Context, result NanoResult, rejection *NanoCallbackError, payment *domain.Payment) {
	event := ports.PaymentCallbackRejectedEvent{
		EventType:      ports.PaymentCallbackRejectedSubject,
		Provider:       string(domain.ProviderNano),
		Source:         callbackSource(result.Source),
		PaymentID:      rejection.PaymentID,
		OrderID:        rejection.OrderID,
		Reason:         string(rejection.Reason),
		ResultCode:     strings.TrimSpace(result.ResultCode),
		ReportedAmount: strings.TrimSpace(result.ReqPayAmt),
		TranNo:         strings.TrimSpace(result.TranNo),
		Detail:         nanoRejectDetail(rejection.Reason),
		Occurred:       s.now(),
	}
	if payment != nil {
		event.ExpectedCents = payment.AmountCents
	}

	log.Printf("payment: ALERT nano callback approved but rejected: reason=%s source=%s payment=%s order=%s reported_amount=%s expected_cents=%d tran=%s",
		event.Reason, event.Source, event.PaymentID, event.OrderID, event.ReportedAmount, event.ExpectedCents, event.TranNo)

	if s.events == nil {
		return
	}
	if err := s.events.Publish(ctx, ports.PaymentCallbackRejectedSubject, event); err != nil {
		log.Printf("payment: publish %s for payment %s failed: %v",
			ports.PaymentCallbackRejectedSubject, event.PaymentID, err)
	}
}

// NanoApproved reports whether NANO said the payment went through. Exported
// because the browser-return handler must key its shopper-facing wording off the
// PG's own verdict, not off whichever error our checks produced.
func NanoApproved(result NanoResult) bool {
	return strings.TrimSpace(result.ResultCode) == nanoApprovedCode
}

func callbackSource(source string) string {
	if s := strings.TrimSpace(source); s != "" {
		return s
	}
	return NanoSourceReturn
}

// nanoRejectDetail is the one-line explanation carried into the ops alert.
func nanoRejectDetail(reason NanoRejectReason) string {
	switch reason {
	case NanoRejectUnknownPayment:
		return "callback named a payment dupli1 does not hold"
	case NanoRejectShopMismatch:
		return "shopcode missing or not ours"
	case NanoRejectNotNano:
		return "payment was not taken through NANO"
	case NanoRejectAmountMismatch:
		return "approved amount disagrees with the amount on file"
	case NanoRejectVerifyFailed:
		return "callback hashValue / receiveUrl MAC did not verify"
	case NanoRejectLookupFailed:
		return "payment lookup failed"
	case NanoRejectPersistFailed:
		return "verified approval could not be saved"
	default:
		return string(reason)
	}
}

// NanoRejection extracts the rejection detail from an error returned by
// HandleNanoResult, if it is one.
func NanoRejection(err error) (*NanoCallbackError, bool) {
	var rejection *NanoCallbackError
	if errors.As(err, &rejection) {
		return rejection, true
	}
	return nil, false
}
