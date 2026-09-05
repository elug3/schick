package events_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/elug3/dupli1/shared/pkg/events"
)

func TestSubjectConstants(t *testing.T) {
	// Cross-service NATS wiring depends on these exact strings; a typo breaks
	// the publisher/subscriber pair silently (messages never delivered).
	cases := map[string]string{
		"OrderCreated":      events.OrderCreated,
		"OrderStatusUpdate": events.OrderStatusUpdate,
		"OrderPaid":         events.OrderPaid,
		"PaymentSucceeded":  events.PaymentSucceeded,
		"PaymentCanceled":   events.PaymentCanceled,
		"ProductCreated":    events.ProductCreated,
		"ProductUpdated":    events.ProductUpdated,
		"ProductDeleted":    events.ProductDeleted,
		"ProductImage":      events.ProductImage,
		"UserDeleted":       events.UserDeleted,
	}
	for name, subject := range cases {
		if subject == "" {
			t.Fatalf("%s subject is empty", name)
		}
		if strings.Contains(subject, " ") {
			t.Fatalf("%s subject %q contains whitespace", name, subject)
		}
	}
}

func TestOrderJSONRoundTrip(t *testing.T) {
	occurred := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	created := occurred.Add(-5 * time.Minute)
	orig := events.Order{
		EventType:     events.OrderCreated,
		OrderID:       "ord_01",
		CustomerID:    "cust_01",
		Status:        "pending",
		SubtotalCents: 120000,
		DiscountCents: 5000,
		TotalCents:    115000,
		Items: []events.OrderItem{
			{SkuID: "01HXYZ", SKU: "PRADA_GALLERIA_BLK_M", Quantity: 1, UnitPriceCents: 115000},
		},
		CreatedAt: created,
		Occurred:  occurred,
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("Unmarshal wire: %v", err)
	}
	for _, key := range []string{"order_id", "customer_id", "subtotal_cents", "discount_cents", "total_cents", "created_at", "occurred_at"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("missing snake_case field %q in %s", key, string(raw))
		}
	}
	items, ok := wire["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want one element", wire["items"])
	}
	item := items[0].(map[string]any)
	if item["sku_id"] != "01HXYZ" || item["unit_price_cents"] != float64(115000) {
		t.Fatalf("item wire = %v", item)
	}

	var decoded events.Order
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal Order: %v", err)
	}
	if decoded.OrderID != orig.OrderID || decoded.TotalCents != orig.TotalCents {
		t.Fatalf("decoded = %+v, want %+v", decoded, orig)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].SkuID != "01HXYZ" {
		t.Fatalf("decoded items = %+v", decoded.Items)
	}
}

func TestPaymentSucceededEventJSONRoundTrip(t *testing.T) {
	orig := events.PaymentSucceededEvent{
		EventType:   events.PaymentSucceeded,
		OrderID:     "ord_pay",
		PaymentID:   "pay_nano_1",
		AmountCents: 99000,
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"event_type", "order_id", "payment_id", "amount_cents"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("missing %q in %s", key, string(raw))
		}
	}

	var decoded events.PaymentSucceededEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.OrderID != orig.OrderID || decoded.PaymentID != orig.PaymentID || decoded.AmountCents != orig.AmountCents {
		t.Fatalf("decoded = %+v, want %+v", decoded, orig)
	}
}

func TestPaymentCanceledEventJSONRoundTrip(t *testing.T) {
	orig := events.PaymentCanceledEvent{
		EventType:      events.PaymentCanceled,
		OrderID:        "ord_pay",
		PaymentID:      "pay_nano_1",
		AmountCents:    70000,
		RemainingCents: 0,
		Reason:         "ops reject",
		CanceledBy:     "mgr_1",
		Occurred:       time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"event_type", "order_id", "payment_id", "amount_cents", "remaining_cents"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("missing %q in %s", key, string(raw))
		}
	}

	var decoded events.PaymentCanceledEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.OrderID != orig.OrderID || decoded.PaymentID != orig.PaymentID || decoded.AmountCents != orig.AmountCents {
		t.Fatalf("decoded = %+v, want %+v", decoded, orig)
	}
	if decoded.RemainingCents != 0 || !decoded.RemainingSpecified() {
		t.Fatalf("remaining = %d specified=%t, want 0 / true", decoded.RemainingCents, decoded.RemainingSpecified())
	}
}

func TestPaymentCanceledEvent_OmittedRemainingCentsIsNotSpecified(t *testing.T) {
	raw := []byte(`{"event_type":"payment.canceled","order_id":"ord_1","payment_id":"pay_1","amount_cents":1000}`)
	var decoded events.PaymentCanceledEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.RemainingSpecified() {
		t.Fatal("omitted remaining_cents must not count as specified")
	}
	if decoded.RemainingCents != 0 {
		t.Fatalf("omitted remaining unmarshals to %d, want 0", decoded.RemainingCents)
	}
}

func TestPaymentCanceledEvent_ExplicitZeroRemainingIsSpecified(t *testing.T) {
	raw := []byte(`{"event_type":"payment.canceled","order_id":"ord_1","payment_id":"pay_1","amount_cents":1000,"remaining_cents":0}`)
	var decoded events.PaymentCanceledEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !decoded.RemainingSpecified() || decoded.RemainingCents != 0 {
		t.Fatalf("explicit 0 remaining: specified=%t remaining=%d", decoded.RemainingSpecified(), decoded.RemainingCents)
	}
}

func TestProductJSONRoundTrip(t *testing.T) {
	occurred := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	orig := events.Product{
		EventType: events.ProductUpdated,
		ProductID: "prod_01",
		SKU:       "PRADA_GALLERIA_BLK_M",
		Name:      "Prada Galleria",
		Brand:     "Prada",
		Category:  "handbag",
		Status:    "active",
		Price:     1200000,
		ImageURL:  "https://images.dupli1.com/x.jpg",
		Occurred:  occurred,
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"product_id", "image_url", "occurred_at"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("missing %q in %s", key, string(raw))
		}
	}

	var decoded events.Product
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.ProductID != orig.ProductID || decoded.ImageURL != orig.ImageURL || decoded.Price != orig.Price {
		t.Fatalf("decoded = %+v, want %+v", decoded, orig)
	}
}

func TestUserDeletedEventJSONRoundTrip(t *testing.T) {
	occurred := time.Date(2026, 9, 4, 3, 50, 0, 0, time.UTC)
	orig := events.UserDeletedEvent{
		EventType: events.UserDeleted,
		UserID:    "user_01",
		Occurred:  occurred,
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"event_type", "user_id", "occurred_at"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("missing %q in %s", key, string(raw))
		}
	}

	var decoded events.UserDeletedEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.UserID != orig.UserID || decoded.EventType != orig.EventType {
		t.Fatalf("decoded = %+v, want %+v", decoded, orig)
	}
	if decoded.Occurred.IsZero() {
		t.Fatal("occurred_at must round-trip")
	}
}
