package service_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/elug3/dupli1/notification/pkg/service"
)

func dispatchCallbackRejected(t *testing.T, chatID string, payload map[string]any) *recordedNotifier {
	t.Helper()
	notifier := &recordedNotifier{}
	dispatcher := service.NewDispatcher(notifier, service.DispatcherConfig{
		OrderChatID:  chatID,
		ManageWebURL: "https://manage.dupli1.com",
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.HandleForTest(t.Context(), service.SubjectPaymentCallbackRejected, raw); err != nil {
		t.Fatalf("HandleForTest: %v", err)
	}
	return notifier
}

// A PG approval that dupli1 refuses moves nothing: no order is paid and no
// payment row changes, so this alert is the only signal a human ever gets that
// a card may have been charged for nothing (elug3/dupli1#232).
func TestDispatcher_CallbackRejectedAlert(t *testing.T) {
	n := dispatchCallbackRejected(t, "-100123", map[string]any{
		"event_type": "payment.callback_rejected",
		"provider":   "nano", "source": "return",
		"payment_id": "pay_000023", "order_id": "ORD-023",
		"reason": "verify_failed", "result_code": "0000",
		"expected_cents": 31004, "reported_amount": "31004",
		"tran_no": "260905001496",
		"detail":  "callback hashValue did not verify",
	})

	if n.chatID != "-100123" {
		t.Fatalf("chat id = %q", n.chatID)
	}
	for _, want := range []string{
		"approved by PG but rejected",
		"charged twice",
		"ORD-023",
		"pay_000023",
		"callback hashValue did not verify",
		"₩31,004",
		"260905001496",
		"nano return callback",
		"https://manage.dupli1.com",
	} {
		if !strings.Contains(n.message, want) {
			t.Fatalf("message missing %q:\n%s", want, n.message)
		}
	}
}

// The callback may have failed precisely because it named no payment we hold.
// The alert still has to go out, without printing empty fields.
func TestDispatcher_CallbackRejectedWithoutIdentifiedPayment(t *testing.T) {
	n := dispatchCallbackRejected(t, "-100123", map[string]any{
		"event_type": "payment.callback_rejected",
		"provider":   "nano", "source": "webhook",
		"reason": "unknown_payment", "result_code": "0000",
	})

	if n.message == "" {
		t.Fatal("no alert sent")
	}
	for _, absent := range []string{"Order:", "Payment:", "Expected:", "PG reported:", "PG transaction:"} {
		if strings.Contains(n.message, absent) {
			t.Errorf("unset field rendered as %q:\n%s", absent, n.message)
		}
	}
	// The reason stands in when no human-readable detail was published.
	if !strings.Contains(n.message, "unknown_payment") {
		t.Errorf("message missing the reason:\n%s", n.message)
	}
	if !strings.Contains(n.message, "nano webhook callback") {
		t.Errorf("message missing the source:\n%s", n.message)
	}
}

// A missing chat id must not turn a swallowed alert into a failed NATS handler
// that redelivers forever.
func TestDispatcher_CallbackRejectedWithoutChatIsNotAnError(t *testing.T) {
	n := dispatchCallbackRejected(t, "", map[string]any{
		"event_type": "payment.callback_rejected",
		"provider":   "nano", "source": "return", "reason": "verify_failed",
	})
	if n.message != "" {
		t.Fatalf("message sent with no chat configured: %s", n.message)
	}
}
