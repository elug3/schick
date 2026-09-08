// Package events defines the NATS subject names and payload shapes shared
// between a publishing service and its subscriber(s): order publishes
// order.* (notification subscribes), payment publishes payment.succeeded
// (order subscribes) and payment.callback_rejected (notification
// subscribes), product publishes product.* (notification
// subscribes), and auth publishes user.deleted (profile subscribes, to
// cascade-delete saved profile/address data). Each subject has exactly one
// publisher and one or more subscribers that must agree on the exact string
// and payload fields, so both are defined once here rather than redeclared
// per service.
package events

import (
	"encoding/json"
	"time"
)

// Subject names published over NATS.
const (
	OrderCreated      = "order.created"
	OrderStatusUpdate = "order.status_updated"
	OrderPaid         = "order.paid"
	PaymentSucceeded  = "payment.succeeded"
	PaymentCanceled   = "payment.canceled"
	// PaymentCallbackRejected fires when a PG callback the PG itself marked
	// approved could not be applied — the money may be gone with no paid order.
	PaymentCallbackRejected = "payment.callback_rejected"
	ProductCreated          = "product.created"
	ProductUpdated          = "product.updated"
	ProductDeleted          = "product.deleted"
	ProductImage            = "product.image_uploaded"
	UserDeleted             = "user.deleted"
)

// OrderItem is one line of an Order event payload.
type OrderItem struct {
	SkuID          string `json:"sku_id,omitempty"`
	SKU            string `json:"sku"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

// Order is the payload for OrderCreated, OrderStatusUpdate, and OrderPaid —
// published by order, consumed by notification.
type Order struct {
	EventType     string `json:"event_type"`
	OrderID       string `json:"order_id"`
	CustomerID    string `json:"customer_id"`
	Status        string `json:"status"`
	SubtotalCents int64  `json:"subtotal_cents"`
	DiscountCents int64  `json:"discount_cents"`
	// ShippingFeeKRW is the delivery charge included in TotalCents, in whole
	// KRW. Zero for orders placed before shipping fees existed, and for any
	// deployment running with delivery free.
	ShippingFeeKRW int64       `json:"shipping_fee_krw"`
	TotalCents     int64       `json:"total_cents"`
	Items          []OrderItem `json:"items"`
	CreatedAt      time.Time   `json:"created_at"`
	Occurred       time.Time   `json:"occurred_at"`
}

// Product is the payload for ProductCreated, ProductUpdated,
// ProductDeleted, and ProductImage — published by product, consumed by
// notification. Also reused (with a service-local subject) for product's
// unconsumed variant_created/updated/deleted events.
type Product struct {
	EventType string    `json:"event_type"`
	ProductID string    `json:"product_id"`
	SKU       string    `json:"sku,omitempty"`
	Name      string    `json:"name"`
	Brand     string    `json:"brand"`
	Category  string    `json:"category"`
	Status    string    `json:"status"`
	Price     float64   `json:"price"`
	ImageURL  string    `json:"image_url,omitempty"`
	Occurred  time.Time `json:"occurred_at"`
}

// PaymentSucceededEvent is the payload for PaymentSucceeded — published by
// payment, consumed by order.
type PaymentSucceededEvent struct {
	EventType   string `json:"event_type"`
	OrderID     string `json:"order_id"`
	PaymentID   string `json:"payment_id"`
	AmountCents int64  `json:"amount_cents"`
}

// PaymentCanceledEvent is the payload for PaymentCanceled — published by
// payment when a succeeded payment is canceled (fully or partially) at the PG.
// AmountCents is the amount canceled by this event; RemainingCents is what is
// still captured afterwards (0 on a full cancel). Order cancels a still-paid
// order on a full refund (remaining_cents == 0) when payment_id matches;
// notification alerts ops. A missing remaining_cents must not be treated as 0.
type PaymentCanceledEvent struct {
	EventType      string    `json:"event_type"`
	OrderID        string    `json:"order_id"`
	PaymentID      string    `json:"payment_id"`
	AmountCents    int64     `json:"amount_cents"`
	RemainingCents int64     `json:"remaining_cents"`
	Reason         string    `json:"reason,omitempty"`
	CanceledBy     string    `json:"canceled_by,omitempty"`
	Occurred       time.Time `json:"occurred_at"`
	remainingSet   bool      `json:"-"`
}

// RemainingSpecified reports whether remaining_cents was present in the JSON
// payload. encoding/json treats a missing int64 as 0, which order would
// otherwise interpret as a full refund.
func (e PaymentCanceledEvent) RemainingSpecified() bool {
	return e.remainingSet
}

func (e *PaymentCanceledEvent) UnmarshalJSON(data []byte) error {
	type wire struct {
		EventType      string    `json:"event_type"`
		OrderID        string    `json:"order_id"`
		PaymentID      string    `json:"payment_id"`
		AmountCents    int64     `json:"amount_cents"`
		RemainingCents *int64    `json:"remaining_cents"`
		Reason         string    `json:"reason"`
		CanceledBy     string    `json:"canceled_by"`
		Occurred       time.Time `json:"occurred_at"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	e.EventType = w.EventType
	e.OrderID = w.OrderID
	e.PaymentID = w.PaymentID
	e.AmountCents = w.AmountCents
	e.Reason = w.Reason
	e.CanceledBy = w.CanceledBy
	e.Occurred = w.Occurred
	if w.RemainingCents != nil {
		e.RemainingCents = *w.RemainingCents
		e.remainingSet = true
	}
	return nil
}

// PaymentCallbackRejectedEvent is the payload for PaymentCallbackRejected —
// published by payment, consumed by notification.
//
// It is emitted only when the PG reported approval (NANO resultCode 0000) and
// dupli1 still refused to mark the payment succeeded. That combination means a
// card was probably charged with no paid order behind it, so it needs a human,
// not a retry. Ordinary declines publish nothing.
//
// Every field is best-effort: a callback can be rejected precisely because it
// failed to identify a payment, so PaymentID/OrderID may be empty.
type PaymentCallbackRejectedEvent struct {
	EventType string `json:"event_type"`
	// Provider is the PG that sent the callback ("nano").
	Provider string `json:"provider"`
	// Source is which endpoint received it: "return" (shopper's browser) or
	// "webhook" (server-to-server).
	Source    string `json:"source"`
	PaymentID string `json:"payment_id,omitempty"`
	OrderID   string `json:"order_id,omitempty"`
	// Reason is the internal rejection cause — see payment's nanoReject* values.
	Reason string `json:"reason"`
	// ResultCode is the PG's own result code, retained verbatim.
	ResultCode string `json:"result_code,omitempty"`
	// ExpectedCents is the amount dupli1 holds for the payment, in whole KRW;
	// ReportedAmount is what the PG sent, unparsed, so a malformed value survives
	// into the alert instead of being flattened to 0.
	ExpectedCents  int64  `json:"expected_cents,omitempty"`
	ReportedAmount string `json:"reported_amount,omitempty"`
	TranNo         string `json:"tran_no,omitempty"`
	// Detail is a short human-readable note for the alert (never a secret).
	Detail   string    `json:"detail,omitempty"`
	Occurred time.Time `json:"occurred_at"`
}

// UserDeletedEvent is the payload for UserDeleted — published by auth,
// consumed by profile (which owns no foreign key to auth's users table and
// must clean up saved profile/address data itself).
type UserDeletedEvent struct {
	EventType string    `json:"event_type"`
	UserID    string    `json:"user_id"`
	Occurred  time.Time `json:"occurred_at"`
}
