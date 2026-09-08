package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidOrder          = errors.New("invalid order")
	ErrInvalidTransition     = errors.New("invalid order status transition")
	ErrPaymentAmountMismatch = errors.New("payment amount does not match order total")
	ErrInvalidShipment       = errors.New("invalid shipment tracking")
)

const DefaultPaymentTTL = 5 * time.Minute

// ReasonVariantNotFound is returned when a line cannot be resolved to an
// active, sellable product variant.
const ReasonVariantNotFound = "variant_not_found"

type OrderStatus string

const (
	StatusPending   OrderStatus = "pending"
	StatusPaid      OrderStatus = "paid"
	StatusInTransit OrderStatus = "in_transit"
	StatusFulfilled OrderStatus = "fulfilled"
	StatusCanceled  OrderStatus = "canceled"
)

// UnavailableItem identifies a checkout/order line that cannot be purchased.
type UnavailableItem struct {
	SkuID  string `json:"sku_id,omitempty"`
	SKU    string `json:"sku,omitempty"`
	Reason string `json:"reason"`
}

type OrderItem struct {
	SkuID          string `json:"sku_id,omitempty"`
	SKU            string `json:"sku"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"` // whole KRW won
	// ProductName and ImageURL are captured at order creation from the product catalog.
	ProductName string `json:"product_name,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	// Available is false when the variant is no longer sellable (checkout session reads).
	Available *bool `json:"available,omitempty"`
}

type Order struct {
	ID            string      `json:"id"`
	CustomerID    string      `json:"customer_id"`
	ReservationID string      `json:"reservation_id"`
	Items         []OrderItem `json:"items"`
	Status        OrderStatus `json:"status"`
	CouponCode    string      `json:"coupon_code,omitempty"`
	SubtotalCents int64       `json:"subtotal_cents"`
	DiscountCents int64       `json:"discount_cents"`
	// ShippingFeeKRW is the delivery charge in whole KRW, captured at order
	// creation so a later config change never re-prices a placed order.
	ShippingFeeKRW  int64           `json:"shipping_fee_krw"`
	TotalCents      int64           `json:"total_cents"`
	RecipientName   string          `json:"recipient_name,omitempty"`
	RecipientPhone  string          `json:"recipient_phone,omitempty"`
	ShippingAddress ShippingAddress `json:"shipping_address,omitempty"`
	SourceAddressID string          `json:"source_address_id,omitempty"`
	PaymentID       string          `json:"payment_id,omitempty"`
	PaidAt          *time.Time      `json:"paid_at,omitempty"`
	PaymentDueAt    time.Time       `json:"payment_due_at"`
	ShippedBy       string          `json:"shipped_by,omitempty"`
	ShippedAt       *time.Time      `json:"shipped_at,omitempty"`
	Carrier         string          `json:"carrier,omitempty"`
	TrackingNumber  string          `json:"tracking_number,omitempty"`
	CarrierNote     string          `json:"carrier_note,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// NewOrder prices an order as subtotal - discount + shipping, all in whole KRW.
// shippingFeeKRW is passed in rather than read from config so the charge is
// snapshotted on the order: changing the configured fee must not alter what an
// already-placed order costs.
//
// The discount applies to goods only and is capped at the subtotal, so the total
// can never fall below the shipping fee — a 100%-off coupon still pays delivery.
func NewOrder(id, customerID, reservationID string, items []OrderItem, couponCode string, discountCents, shippingFeeKRW int64, now time.Time) (*Order, error) {
	id = strings.TrimSpace(id)
	customerID = strings.TrimSpace(customerID)
	reservationID = strings.TrimSpace(reservationID)
	if id == "" || customerID == "" || reservationID == "" {
		return nil, ErrInvalidOrder
	}

	copiedItems := make([]OrderItem, len(items))
	var subtotal int64
	for i, item := range items {
		item.SkuID = strings.TrimSpace(item.SkuID)
		item.SKU = strings.ToUpper(strings.TrimSpace(item.SKU))
		if (item.SKU == "" && item.SkuID == "") || item.Quantity <= 0 || item.UnitPriceCents < 0 {
			return nil, ErrInvalidOrder
		}
		subtotal += int64(item.Quantity) * item.UnitPriceCents
		copiedItems[i] = item
	}
	if len(copiedItems) == 0 {
		return nil, ErrInvalidOrder
	}
	if discountCents < 0 || discountCents > subtotal {
		return nil, ErrInvalidOrder
	}
	if shippingFeeKRW < 0 {
		return nil, ErrInvalidOrder
	}

	return &Order{
		ID:             id,
		CustomerID:     customerID,
		ReservationID:  reservationID,
		Items:          copiedItems,
		Status:         StatusPending,
		CouponCode:     strings.ToUpper(strings.TrimSpace(couponCode)),
		SubtotalCents:  subtotal,
		DiscountCents:  discountCents,
		ShippingFeeKRW: shippingFeeKRW,
		TotalCents:     subtotal - discountCents + shippingFeeKRW,
		PaymentDueAt:   now.Add(DefaultPaymentTTL),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func (o *Order) MarkPaid(paymentID string, amountCents int64, now time.Time) error {
	if o.Status != StatusPending {
		return ErrInvalidTransition
	}
	paymentID = strings.TrimSpace(paymentID)
	if paymentID == "" {
		return ErrInvalidOrder
	}
	if amountCents != o.TotalCents {
		return ErrPaymentAmountMismatch
	}
	o.Status = StatusPaid
	o.PaymentID = paymentID
	o.PaidAt = &now
	o.UpdatedAt = now
	return nil
}

func (o *Order) Ship(shippedBy string, tracking ShipmentTracking, now time.Time) error {
	if o.Status != StatusPaid {
		return ErrInvalidTransition
	}
	shippedBy = strings.TrimSpace(shippedBy)
	if shippedBy == "" {
		return ErrInvalidOrder
	}
	if tracking.Carrier == "" || tracking.TrackingNumber == "" {
		return fmt.Errorf("%w: carrier and tracking_number are required", ErrInvalidShipment)
	}
	if _, ok := ValidCarriers[tracking.Carrier]; !ok {
		return fmt.Errorf("%w: unknown carrier %q", ErrInvalidShipment, tracking.Carrier)
	}
	if tracking.Carrier == CarrierOther && strings.TrimSpace(tracking.CarrierNote) == "" {
		return fmt.Errorf("%w: carrier_note is required when carrier is other", ErrInvalidShipment)
	}
	o.Status = StatusInTransit
	o.ShippedBy = shippedBy
	o.ShippedAt = &now
	o.Carrier = tracking.Carrier
	o.TrackingNumber = tracking.TrackingNumber
	if tracking.Carrier == CarrierOther {
		o.CarrierNote = strings.TrimSpace(tracking.CarrierNote)
	} else {
		o.CarrierNote = ""
	}
	o.UpdatedAt = now
	return nil
}

func (o *Order) Cancel(now time.Time) error {
	if o.Status != StatusPending && o.Status != StatusPaid {
		return ErrInvalidTransition
	}
	o.Status = StatusCanceled
	o.UpdatedAt = now
	return nil
}

// ReinstateForLatePayment moves an auto-canceled pending order back to pending
// with a fresh stock reservation so a payment that completes after expiry can
// still be applied.
func (o *Order) ReinstateForLatePayment(reservationID string, now time.Time) error {
	if o.Status != StatusCanceled {
		return ErrInvalidTransition
	}
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" {
		return ErrInvalidOrder
	}
	o.Status = StatusPending
	o.ReservationID = reservationID
	o.PaymentDueAt = now.Add(DefaultPaymentTTL)
	o.UpdatedAt = now
	return nil
}

func (o *Order) Fulfill(now time.Time) error {
	if o.Status != StatusInTransit {
		return ErrInvalidTransition
	}
	o.Status = StatusFulfilled
	o.UpdatedAt = now
	return nil
}

func (o *Order) IsPaymentExpired(now time.Time) bool {
	return o.Status == StatusPending && now.After(o.PaymentDueAt)
}
