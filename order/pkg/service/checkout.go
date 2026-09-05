package service

import (
	"context"
	"errors"
	"strings"

	"github.com/elug3/dupli1/order/pkg/domain"
	"github.com/elug3/dupli1/order/pkg/ports"
)

type CreateCheckoutSessionInput struct {
	CustomerID string
}

type CompleteCheckoutResult struct {
	Session *domain.CheckoutSession `json:"session"`
	Order   *domain.Order           `json:"order"`
}

func (s *Service) CreateCheckoutSession(ctx context.Context, input CreateCheckoutSessionInput) (*domain.CheckoutSession, error) {
	sessionID, err := s.repo.NextCheckoutSessionID(ctx)
	if err != nil {
		return nil, err
	}

	session, err := domain.NewCheckoutSession(sessionID, input.CustomerID, s.now(), s.checkoutTTL, s.shippingFeeCents)
	if err != nil {
		return nil, err
	}
	if err := s.repo.SaveCheckoutSession(ctx, session); err != nil {
		return nil, err
	}
	return cloneCheckoutSession(session), nil
}

func (s *Service) GetCheckoutSession(ctx context.Context, id string) (*domain.CheckoutSession, error) {
	session, err := s.repo.GetCheckoutSession(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if err := session.EnsureOpen(s.now()); err != nil && !errors.Is(err, domain.ErrSessionNotOpen) {
		_ = s.repo.SaveCheckoutSession(ctx, session)
		return nil, err
	}
	return s.annotateCheckoutSession(ctx, cloneCheckoutSession(session)), nil
}

func (s *Service) SetCheckoutItems(ctx context.Context, sessionID string, items []domain.OrderItem) (*domain.CheckoutSession, error) {
	session, err := s.getOpenCheckoutSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	priced, err := s.priceItems(ctx, items)
	if err != nil {
		return nil, err
	}
	if err := session.SetItems(priced, s.now()); err != nil {
		return nil, err
	}
	return s.saveCheckoutSession(ctx, session)
}

func (s *Service) UpsertCheckoutItem(ctx context.Context, sessionID string, item domain.OrderItem) (*domain.CheckoutSession, error) {
	session, err := s.getOpenCheckoutSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	priced, err := s.priceItems(ctx, []domain.OrderItem{item})
	if err != nil {
		return nil, err
	}
	if err := session.UpsertItem(priced[0], s.now()); err != nil {
		return nil, err
	}
	return s.saveCheckoutSession(ctx, session)
}

func (s *Service) RemoveCheckoutItem(ctx context.Context, sessionID, sku string) (*domain.CheckoutSession, error) {
	session, err := s.getOpenCheckoutSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := session.RemoveItem(sku, s.now()); err != nil {
		return nil, err
	}
	return s.saveCheckoutSession(ctx, session)
}

func (s *Service) RemoveCheckoutItemBySkuID(ctx context.Context, sessionID, skuID string) (*domain.CheckoutSession, error) {
	session, err := s.getOpenCheckoutSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := session.RemoveItemBySkuID(skuID, s.now()); err != nil {
		return nil, err
	}
	return s.saveCheckoutSession(ctx, session)
}

func (s *Service) ApplyCheckoutCoupon(ctx context.Context, sessionID, code string) (*domain.CheckoutSession, error) {
	if s.couponClient == nil {
		return nil, ports.ErrCouponUnavailable
	}

	session, err := s.getOpenCheckoutSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if len(session.Items) == 0 {
		return nil, domain.ErrEmptyCheckout
	}

	coupon, err := s.couponClient.Redeem(ctx, code)
	if err != nil {
		return nil, err
	}
	if err := session.ApplyCoupon(coupon.Code, coupon.DiscountFraction, s.now()); err != nil {
		return nil, err
	}
	return s.saveCheckoutSession(ctx, session)
}

func (s *Service) CompleteCheckout(ctx context.Context, sessionID string, input CompleteCheckoutInput) (*CompleteCheckoutResult, error) {
	session, err := s.getOpenCheckoutSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if len(session.Items) == 0 {
		return nil, domain.ErrEmptyCheckout
	}

	snapshot, err := applyCompleteCheckoutInput(input)
	if err != nil {
		return nil, err
	}

	pricedItems, err := s.priceItems(ctx, session.Items)
	if err != nil {
		return nil, err
	}

	discountCents := int64(0)
	couponCode := session.CouponCode
	if couponCode != "" {
		if s.couponClient == nil {
			return nil, ports.ErrCouponUnavailable
		}
		coupon, err := s.couponClient.Redeem(ctx, couponCode)
		if err != nil {
			return nil, err
		}
		couponCode = coupon.Code
		var subtotal int64
		for _, item := range pricedItems {
			subtotal += int64(item.Quantity) * item.UnitPriceCents
		}
		discountCents = int64(float64(subtotal) * coupon.DiscountFraction)
	}

	shippingFee := session.ShippingFeeCents
	order, err := s.CreateOrder(ctx, CreateOrderInput{
		CustomerID:       session.CustomerID,
		Items:            pricedItems,
		CouponCode:       couponCode,
		DiscountCents:    discountCents,
		RecipientName:    snapshot.RecipientName,
		RecipientPhone:   snapshot.RecipientPhone,
		ShippingAddress:  snapshot.ShippingAddress,
		SourceAddressID:  snapshot.SourceAddressID,
		ShippingFeeCents: &shippingFee,
	})
	if err != nil {
		return nil, err
	}
	now := s.now()
	claimed, err := s.repo.CompleteCheckoutSessionIfOpen(ctx, session.ID, order.ID, now)
	if err != nil {
		_, _ = s.CancelOrder(ctx, order.ID)
		return nil, err
	}
	if !claimed {
		_, _ = s.CancelOrder(ctx, order.ID)
		return nil, domain.ErrSessionNotOpen
	}
	session.Status = domain.CheckoutStatusCompleted
	session.OrderID = order.ID
	session.UpdatedAt = now

	return &CompleteCheckoutResult{
		Session: cloneCheckoutSession(session),
		Order:   order,
	}, nil
}

func (s *Service) getOpenCheckoutSession(ctx context.Context, sessionID string) (*domain.CheckoutSession, error) {
	session, err := s.repo.GetCheckoutSession(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	if err := session.EnsureOpen(s.now()); err != nil {
		_ = s.repo.SaveCheckoutSession(ctx, session)
		return nil, err
	}
	return session, nil
}

func (s *Service) saveCheckoutSession(ctx context.Context, session *domain.CheckoutSession) (*domain.CheckoutSession, error) {
	if err := s.repo.SaveCheckoutSession(ctx, session); err != nil {
		return nil, err
	}
	return s.annotateCheckoutSession(ctx, cloneCheckoutSession(session)), nil
}

// annotateCheckoutSession re-checks each stored line against the product catalog
// so the storefront can surface unavailable_items before complete.
func (s *Service) annotateCheckoutSession(ctx context.Context, session *domain.CheckoutSession) *domain.CheckoutSession {
	if session == nil || len(session.Items) == 0 || s.product == nil {
		return session
	}
	unavailable := make([]domain.UnavailableItem, 0)
	for i, item := range session.Items {
		_, err := s.resolveVariant(ctx, item)
		if errors.Is(err, ports.ErrVariantNotFound) {
			available := false
			session.Items[i].Available = &available
			unavailable = append(unavailable, unavailableFromOrderItem(item))
		}
	}
	if len(unavailable) > 0 {
		session.UnavailableItems = unavailable
	} else {
		session.UnavailableItems = nil
	}
	return session
}

func cloneCheckoutSession(session *domain.CheckoutSession) *domain.CheckoutSession {
	if session == nil {
		return nil
	}
	copied := *session
	copied.Items = cloneOrderItems(session.Items)
	if session.UnavailableItems != nil {
		copied.UnavailableItems = append([]domain.UnavailableItem(nil), session.UnavailableItems...)
	}
	return &copied
}

func cloneOrderItems(items []domain.OrderItem) []domain.OrderItem {
	copied := make([]domain.OrderItem, len(items))
	copy(copied, items)
	return copied
}
