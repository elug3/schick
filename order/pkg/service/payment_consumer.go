package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/elug3/dupli1/order/pkg/domain"
	"github.com/elug3/dupli1/order/pkg/ports"
	"github.com/elug3/dupli1/shared/pkg/events"
)

// RegisterPaymentConsumer subscribes to payment.succeeded and marks orders paid.
func (s *Service) RegisterPaymentConsumer(ctx context.Context, subscriber ports.EventSubscriber) error {
	return subscriber.Subscribe(ctx, paymentSucceededSubject, s.handlePaymentSucceeded)
}

func (s *Service) handlePaymentSucceeded(ctx context.Context, _ string, payload []byte) error {
	var event events.PaymentSucceededEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode payment.succeeded: %w", err)
	}
	if event.OrderID == "" || event.PaymentID == "" {
		return fmt.Errorf("payment.succeeded missing order_id or payment_id")
	}
	_, err := s.MarkOrderPaid(ctx, event.OrderID, event.PaymentID, event.AmountCents)
	if err != nil {
		return fmt.Errorf("mark order paid order_id=%s payment_id=%s: %w", event.OrderID, event.PaymentID, err)
	}
	return nil
}

// StartPendingExpiryWorker cancels unpaid pending orders past payment_due_at.
func (s *Service) StartPendingExpiryWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.expirePendingOrders(ctx); err != nil {
					log.Printf("expire pending orders: %v", err)
				}
			}
		}
	}()
}

func (s *Service) expirePendingOrders(ctx context.Context) error {
	orders, err := s.repo.ListPendingPaymentExpired(ctx, s.now())
	if err != nil {
		return err
	}
	for _, order := range orders {
		if err := s.cancelExpiredPendingOrder(ctx, order.ID); err != nil {
			log.Printf("cancel expired order %s: %v", order.ID, err)
		}
	}
	return nil
}

// cancelExpiredPendingOrder cancels an unpaid pending order past payment_due_at.
// Uses an atomic status guard so a payment that completes concurrently cannot be undone.
func (s *Service) cancelExpiredPendingOrder(ctx context.Context, orderID string) error {
	now := s.now()
	order, err := s.repo.Get(ctx, orderID)
	if err != nil {
		return err
	}
	if order.Status != domain.StatusPending || !order.IsPaymentExpired(now) {
		return nil
	}

	cancelled := cloneOrder(order)
	if err := cancelled.Cancel(now); err != nil {
		return err
	}
	events, err := s.outboxEvents(cancelled, orderUpdatedSubject)
	if err != nil {
		return err
	}

	canceledOrder, didCancel, err := s.repo.CancelIfPendingExpired(ctx, orderID, now, events)
	if err != nil || !didCancel {
		return err
	}
	s.tryDrainOutbox(ctx)
	if err := s.releaseReservationForCancel(ctx, canceledOrder.ReservationID); err != nil {
		log.Printf("cancel expired order %s: release reservation %s: %v", orderID, canceledOrder.ReservationID, err)
	}
	return nil
}

// RegisterPaymentCanceledConsumer subscribes to payment.canceled so a refunded
// order stops looking shippable.
//
// Without it the two services each knew half the story: payment moved to
// canceled while the order stayed paid, which left it in the fulfillment queue
// and passing Ship's status check. Shipping then committed the reservation and
// sent goods for an order that had already been refunded.
func (s *Service) RegisterPaymentCanceledConsumer(ctx context.Context, subscriber ports.EventSubscriber) error {
	return subscriber.Subscribe(ctx, paymentCanceledSubject, s.handlePaymentCanceled)
}

func (s *Service) handlePaymentCanceled(ctx context.Context, _ string, payload []byte) error {
	var event events.PaymentCanceledEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode payment.canceled: %w", err)
	}
	if event.OrderID == "" || event.PaymentID == "" {
		log.Printf("payment.canceled missing order_id or payment_id: skip")
		return nil
	}
	if !event.RemainingSpecified() {
		log.Printf("payment.canceled missing remaining_cents (order %s payment %s): skip", event.OrderID, event.PaymentID)
		return nil
	}
	return s.CancelOrderForRefund(ctx, event.OrderID, event.PaymentID, event.RemainingCents)
}

// CancelOrderForRefund cancels an order whose payment was fully refunded.
//
// Only a full refund of the order's own payment cancels: a partial one
// (remainingCents > 0) leaves money still owed on goods the customer has not
// been made whole for, and silently cancelling that is worse than leaving it
// for a human. A payload whose payment_id does not match the order is ignored
// so a spoofed or mis-routed event cannot cancel someone else's paid order.
// Likewise an order already shipped — the goods are gone, so this is a return,
// which the system has no concept of yet. Pending orders are left alone: they
// were never captured. All of those cases are logged and left alone.
//
// The status change is atomic (paid + matching payment_id), so a concurrent
// ship cannot be undone by last-write-wins.
//
// Idempotent: a replayed event finds the order already canceled and no-ops,
// matching how MarkOrderPaid tolerates redelivery.
func (s *Service) CancelOrderForRefund(ctx context.Context, orderID, paymentID string, remainingCents int64) error {
	orderID = strings.TrimSpace(orderID)
	paymentID = strings.TrimSpace(paymentID)

	if remainingCents > 0 {
		log.Printf(
			"payment.canceled: order %s partially refunded (payment %s, %d still captured); leaving for manual review",
			orderID, paymentID, remainingCents,
		)
		return nil
	}

	order, err := s.repo.Get(ctx, orderID)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			log.Printf("payment.canceled: unknown order %s (payment %s)", orderID, paymentID)
			return nil
		}
		return err
	}

	if paymentID == "" || order.PaymentID != paymentID {
		log.Printf(
			"payment.canceled: order %s payment_id %q does not match event payment %q; skip",
			order.ID, order.PaymentID, paymentID,
		)
		return nil
	}

	switch order.Status {
	case domain.StatusCanceled:
		return nil // replay, or already canceled by an operator
	case domain.StatusInTransit, domain.StatusFulfilled:
		log.Printf(
			"payment.canceled: order %s was refunded (payment %s) but is already %s; "+
				"goods have shipped, so this needs a return rather than a cancel",
			order.ID, paymentID, order.Status,
		)
		return nil
	case domain.StatusPaid:
		// continue
	default:
		log.Printf(
			"payment.canceled: order %s is %s (payment %s); only paid orders cancel on a full refund",
			order.ID, order.Status, paymentID,
		)
		return nil
	}

	now := s.now()
	cancelled := cloneOrder(order)
	if err := cancelled.Cancel(now); err != nil {
		return fmt.Errorf("cancel refunded order %s: %w", order.ID, err)
	}
	events, err := s.outboxEvents(cancelled, orderUpdatedSubject)
	if err != nil {
		return err
	}

	canceledOrder, didCancel, err := s.repo.CancelIfPaidForRefund(ctx, order.ID, paymentID, now, events)
	if err != nil {
		return fmt.Errorf("save refunded order %s: %w", order.ID, err)
	}
	if !didCancel {
		log.Printf("payment.canceled: order %s was not still paid with payment %s; skip", order.ID, paymentID)
		return nil
	}
	s.tryDrainOutbox(ctx)
	if err := s.releaseReservationForCancel(ctx, canceledOrder.ReservationID); err != nil {
		log.Printf("payment.canceled: order %s release reservation %s: %v", canceledOrder.ID, canceledOrder.ReservationID, err)
	}
	log.Printf("payment.canceled: order %s canceled after full refund (payment %s)", canceledOrder.ID, paymentID)
	return nil
}
