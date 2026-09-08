package domain

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidCheckoutSession = errors.New("invalid checkout session")
	ErrSessionNotOpen         = errors.New("checkout session is not open")
	ErrSessionExpired         = errors.New("checkout session has expired")
	ErrEmptyCheckout          = errors.New("checkout session has no items")
)

type CheckoutSessionStatus string

const (
	CheckoutStatusOpen      CheckoutSessionStatus = "open"
	CheckoutStatusCompleted CheckoutSessionStatus = "completed"
	CheckoutStatusExpired   CheckoutSessionStatus = "expired"
)

const DefaultCheckoutTTL = 30 * time.Minute

type CheckoutSession struct {
	ID               string                `json:"id"`
	CustomerID       string                `json:"customer_id"`
	Items            []OrderItem           `json:"items"`
	UnavailableItems []UnavailableItem     `json:"unavailable_items,omitempty"`
	Status           CheckoutSessionStatus `json:"status"`
	CouponCode       string                `json:"coupon_code,omitempty"`
	SubtotalCents    int64                 `json:"subtotal_cents"`
	DiscountCents    int64                 `json:"discount_cents"`
	// ShippingFeeKRW is the delivery charge quoted for this session, in whole
	// KRW. It is fixed when the session opens so a mid-session config change
	// cannot move the price the customer was shown.
	ShippingFeeKRW int64     `json:"shipping_fee_krw"`
	TotalCents     int64     `json:"total_cents"`
	OrderID        string    `json:"order_id,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// NewCheckoutSession opens a session quoting shippingFeeKRW for delivery. The
// fee is stored on the session so every later recalculation reuses the quote the
// customer was first shown, rather than re-reading a config value that may have
// changed mid-checkout.
func NewCheckoutSession(id, customerID string, now time.Time, ttl time.Duration, shippingFeeKRW int64) (*CheckoutSession, error) {
	id = strings.TrimSpace(id)
	customerID = strings.TrimSpace(customerID)
	if id == "" || customerID == "" {
		return nil, ErrInvalidCheckoutSession
	}
	if ttl <= 0 {
		ttl = DefaultCheckoutTTL
	}
	if shippingFeeKRW < 0 {
		return nil, ErrInvalidCheckoutSession
	}

	return &CheckoutSession{
		ID:             id,
		CustomerID:     customerID,
		Items:          []OrderItem{},
		Status:         CheckoutStatusOpen,
		ShippingFeeKRW: shippingFeeKRW,
		ExpiresAt:      now.Add(ttl),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func (s *CheckoutSession) EnsureOpen(now time.Time) error {
	if s.Status == CheckoutStatusCompleted {
		return ErrSessionNotOpen
	}
	if s.Status == CheckoutStatusExpired || now.After(s.ExpiresAt) {
		s.Status = CheckoutStatusExpired
		return ErrSessionExpired
	}
	return nil
}

func (s *CheckoutSession) SetItems(items []OrderItem, now time.Time) error {
	if err := s.EnsureOpen(now); err != nil {
		return err
	}

	copied := make([]OrderItem, len(items))
	for i, item := range items {
		item.SkuID = strings.TrimSpace(item.SkuID)
		item.SKU = strings.ToUpper(strings.TrimSpace(item.SKU))
		if (item.SKU == "" && item.SkuID == "") || item.Quantity <= 0 || item.UnitPriceCents < 0 {
			return ErrInvalidCheckoutSession
		}
		copied[i] = item
	}

	s.Items = copied
	s.recalculateTotals()
	s.UpdatedAt = now
	return nil
}

func (s *CheckoutSession) UpsertItem(item OrderItem, now time.Time) error {
	if err := s.EnsureOpen(now); err != nil {
		return err
	}

	item.SkuID = strings.TrimSpace(item.SkuID)
	item.SKU = strings.ToUpper(strings.TrimSpace(item.SKU))
	if (item.SKU == "" && item.SkuID == "") || item.Quantity <= 0 || item.UnitPriceCents < 0 {
		return ErrInvalidCheckoutSession
	}

	for i, existing := range s.Items {
		if sameItem(existing, item) {
			s.Items[i] = item
			s.recalculateTotals()
			s.UpdatedAt = now
			return nil
		}
	}

	s.Items = append(s.Items, item)
	s.recalculateTotals()
	s.UpdatedAt = now
	return nil
}

func (s *CheckoutSession) RemoveItem(sku string, now time.Time) error {
	if err := s.EnsureOpen(now); err != nil {
		return err
	}

	sku = strings.ToUpper(strings.TrimSpace(sku))
	if sku == "" {
		return ErrInvalidCheckoutSession
	}

	filtered := s.Items[:0]
	for _, item := range s.Items {
		if item.SKU != sku {
			filtered = append(filtered, item)
		}
	}
	s.Items = filtered
	s.recalculateTotals()
	s.UpdatedAt = now
	return nil
}

func (s *CheckoutSession) RemoveItemBySkuID(skuID string, now time.Time) error {
	if err := s.EnsureOpen(now); err != nil {
		return err
	}

	skuID = strings.TrimSpace(skuID)
	if skuID == "" {
		return ErrInvalidCheckoutSession
	}

	filtered := s.Items[:0]
	for _, item := range s.Items {
		if item.SkuID != skuID {
			filtered = append(filtered, item)
		}
	}
	s.Items = filtered
	s.recalculateTotals()
	s.UpdatedAt = now
	return nil
}

// sameItem reports whether two line items refer to the same variant. It
// matches by SkuID when both sides have one populated, otherwise falls back
// to the human SKU — this lets old (sku-only) and new (skuId-aware) callers
// interoperate on the same checkout session.
func sameItem(a, b OrderItem) bool {
	if a.SkuID != "" && b.SkuID != "" {
		return a.SkuID == b.SkuID
	}
	return a.SKU == b.SKU
}

func (s *CheckoutSession) ApplyCoupon(code string, discountFraction float64, now time.Time) error {
	if err := s.EnsureOpen(now); err != nil {
		return err
	}

	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" || discountFraction <= 0 || discountFraction >= 1 {
		return ErrInvalidCheckoutSession
	}

	s.CouponCode = code
	s.recalculateTotalsWithDiscount(discountFraction)
	s.UpdatedAt = now
	return nil
}

func (s *CheckoutSession) ClearCoupon(now time.Time) error {
	if err := s.EnsureOpen(now); err != nil {
		return err
	}

	s.CouponCode = ""
	s.recalculateTotals()
	s.UpdatedAt = now
	return nil
}

func (s *CheckoutSession) Complete(orderID string, now time.Time) error {
	if err := s.EnsureOpen(now); err != nil {
		return err
	}
	if len(s.Items) == 0 {
		return ErrEmptyCheckout
	}

	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return ErrInvalidCheckoutSession
	}

	s.Status = CheckoutStatusCompleted
	s.OrderID = orderID
	s.UpdatedAt = now
	return nil
}

func (s *CheckoutSession) recalculateTotals() {
	s.recalculateTotalsWithDiscount(0)
}

func (s *CheckoutSession) recalculateTotalsWithDiscount(discountFraction float64) {
	var subtotal int64
	for _, item := range s.Items {
		subtotal += int64(item.Quantity) * item.UnitPriceCents
	}

	s.SubtotalCents = subtotal
	if discountFraction > 0 && s.CouponCode != "" {
		s.DiscountCents = int64(float64(subtotal) * discountFraction)
	} else {
		s.DiscountCents = 0
	}
	s.TotalCents = subtotal - s.DiscountCents + s.shippingFeeForTotal()
}

// shippingFeeForTotal is the delivery charge to include in the session total.
// An empty cart owes nothing to ship, so a session with no items quotes zero
// rather than showing a bare delivery charge as its total.
func (s *CheckoutSession) shippingFeeForTotal() int64 {
	if len(s.Items) == 0 {
		return 0
	}
	return s.ShippingFeeKRW
}
