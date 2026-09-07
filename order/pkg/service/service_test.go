package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/elug3/dupli1/order/pkg/domain"
	"github.com/elug3/dupli1/order/pkg/infra/memory"
	"github.com/elug3/dupli1/order/pkg/ports"
	"github.com/elug3/dupli1/order/pkg/service"
)

type fakeStock struct {
	reservedItems []ports.StockItem
	reservationID string
	committed     string
	released      string
	commitErr     error
}


func testShipTracking() domain.ShipmentTracking {
	return domain.ShipmentTracking{Carrier: domain.CarrierCJ, TrackingNumber: "123456789012"}
}

func (f *fakeStock) Reserve(ctx context.Context, orderID string, items []ports.StockItem) (string, error) {
	f.reservedItems = append([]ports.StockItem(nil), items...)
	if f.reservationID == "" {
		f.reservationID = "res-1"
	}
	return f.reservationID, nil
}

func (f *fakeStock) CommitReservation(ctx context.Context, reservationID string) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	if f.released == reservationID {
		return ports.ErrReservationAlreadyReleased
	}
	if f.committed != "" && f.committed == reservationID {
		return ports.ErrReservationAlreadyCommitted
	}
	f.committed = reservationID
	return nil
}

func (f *fakeStock) ReleaseReservation(ctx context.Context, reservationID string) error {
	f.released = reservationID
	return nil
}

type recordedPublisher struct {
	subjects []string
	events   []any
}

func (p *recordedPublisher) Publish(ctx context.Context, subject string, event any) error {
	p.subjects = append(p.subjects, subject)
	p.events = append(p.events, event)
	return nil
}

type failingPublisher struct {
	calls int
}

func (p *failingPublisher) Publish(ctx context.Context, subject string, event any) error {
	p.calls++
	return errors.New("nats unavailable")
}

type countingStock struct {
	fakeStock
	reserveCalls int
}

func (f *countingStock) Reserve(ctx context.Context, orderID string, items []ports.StockItem) (string, error) {
	f.reserveCalls++
	return f.fakeStock.Reserve(ctx, orderID, items)
}

// fakeProduct resolves catalog prices; client UnitPriceCents is ignored by the service.
type fakeProduct struct {
	defaultCents int64
	byKey        map[string]*ports.VariantInfo
	// strictMissing makes unknown keys return ErrVariantNotFound when byKey is set.
	strictMissing bool
}

func (f *fakeProduct) GetVariant(_ context.Context, sku string) (*ports.VariantInfo, error) {
	return f.lookup(strings.ToUpper(strings.TrimSpace(sku)), true)
}

func (f *fakeProduct) GetVariantBySkuID(_ context.Context, skuID string) (*ports.VariantInfo, error) {
	return f.lookup(strings.TrimSpace(skuID), false)
}

func (f *fakeProduct) lookup(key string, asSKU bool) (*ports.VariantInfo, error) {
	if f.byKey != nil {
		if v, ok := f.byKey[key]; ok {
			cp := *v
			return &cp, nil
		}
		if v, ok := f.byKey[strings.ToUpper(key)]; ok {
			cp := *v
			return &cp, nil
		}
		if f.strictMissing {
			return nil, ports.ErrVariantNotFound
		}
	}
	cents := f.defaultCents
	if cents == 0 {
		cents = 1000
	}
	if asSKU {
		sku := strings.ToUpper(key)
		return &ports.VariantInfo{SkuID: "ID-" + sku, SKU: sku, UnitPriceCents: cents}, nil
	}
	return &ports.VariantInfo{SkuID: key, SKU: strings.ToUpper(key), UnitPriceCents: cents}, nil
}

func newSvc(stock ports.StockClient, product *fakeProduct, publisher ...ports.EventPublisher) *service.Service {
	if product == nil {
		product = &fakeProduct{}
	}
	return service.New(memory.NewRepository(), stock, publisher...).WithProduct(product)
}

func TestCreateOrderReservesStockAndPublishesEvent(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	publisher := &recordedPublisher{}
	svc := newSvc(stock, &fakeProduct{defaultCents: 1250}, publisher)

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items: []domain.OrderItem{
			{SKU: "shoe-1", Quantity: 2, UnitPriceCents: 1}, // client price ignored
		},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}

	if order.Status != domain.StatusPending {
		t.Fatalf("order status = %q, want pending", order.Status)
	}
	if order.TotalCents != 2500 {
		t.Fatalf("total = %d, want 2500 from catalog (not client 1)", order.TotalCents)
	}
	if order.PaymentDueAt.IsZero() {
		t.Fatal("payment_due_at should be set")
	}
	if publisher.subjects[0] != "order.created" {
		t.Fatalf("published subject = %q, want order.created", publisher.subjects[0])
	}
}

func TestCreateOrderIgnoresClientUnitPrice(t *testing.T) {
	ctx := t.Context()
	svc := newSvc(&fakeStock{}, &fakeProduct{defaultCents: 2890000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "BAG-1", Quantity: 1, UnitPriceCents: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.Items[0].UnitPriceCents != 2890000 || order.TotalCents != 2890000 {
		t.Fatalf("priced = %+v total=%d, want catalog 2890000", order.Items[0], order.TotalCents)
	}
}

func TestCreateOrderCapturesProductNameAndImageURL(t *testing.T) {
	ctx := t.Context()
	product := &fakeProduct{
		byKey: map[string]*ports.VariantInfo{
			"BAG-001": {
				SkuID:          "sku-bag-1",
				SKU:            "BAG-001",
				UnitPriceCents: 50000,
				ProductName:    "Prada Galleria",
				ImageURL:       "https://cdn.example/bag.jpg",
			},
		},
		strictMissing: true,
	}
	svc := newSvc(&fakeStock{}, product)

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "BAG-001", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.Items[0].ProductName != "Prada Galleria" {
		t.Fatalf("ProductName = %q, want Prada Galleria", order.Items[0].ProductName)
	}
	if order.Items[0].ImageURL != "https://cdn.example/bag.jpg" {
		t.Fatalf("ImageURL = %q, want catalog image", order.Items[0].ImageURL)
	}

	loaded, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if loaded.Items[0].ProductName != "Prada Galleria" || loaded.Items[0].ImageURL != "https://cdn.example/bag.jpg" {
		t.Fatalf("persisted snapshot lost on reload: %+v", loaded.Items[0])
	}
}

func TestMarkOrderPaidThenShipCommitsStock(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}

	order, err = svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents)
	if err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}
	if order.Status != domain.StatusPaid {
		t.Fatalf("order status = %q, want paid", order.Status)
	}
	if stock.committed != "" {
		t.Fatal("stock should not commit on paid")
	}

	order, err = svc.ShipOrder(ctx, order.ID, "manager-1", testShipTracking())
	if err != nil {
		t.Fatalf("ShipOrder returned error: %v", err)
	}
	if order.Status != domain.StatusInTransit {
		t.Fatalf("order status = %q, want in_transit", order.Status)
	}
	if stock.committed != "res-123" {
		t.Fatalf("committed reservation = %q, want res-123", stock.committed)
	}
}

// Payment republishes payment.succeeded for two hours, so the consumer sees replays
// after the order has already shipped or been fulfilled.
func TestMarkOrderPaidReplayAfterShipIsNoOp(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}

	for _, status := range []struct {
		name    string
		advance func() error
		want    domain.OrderStatus
	}{
		{"in_transit", func() error { _, err := svc.ShipOrder(ctx, order.ID, "manager-1", testShipTracking()); return err }, domain.StatusInTransit},
		{"fulfilled", func() error { _, err := svc.FulfillOrder(ctx, order.ID); return err }, domain.StatusFulfilled},
	} {
		if err := status.advance(); err != nil {
			t.Fatalf("advance to %s: %v", status.name, err)
		}
		replayed, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents)
		if err != nil {
			t.Fatalf("replayed payment.succeeded while %s returned error: %v", status.name, err)
		}
		if replayed.Status != status.want {
			t.Fatalf("status after replay = %q, want %q", replayed.Status, status.want)
		}
	}
}

func TestMarkOrderPaidRejectsDifferentPaymentForPaidOrder(t *testing.T) {
	ctx := t.Context()
	svc := newSvc(&fakeStock{reservationID: "res-123"}, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}

	_, err = svc.MarkOrderPaid(ctx, order.ID, "pay-2", order.TotalCents)
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("second payment error = %v, want ErrInvalidTransition", err)
	}
}

func TestMarkOrderPaidReinstatesExpiredCanceledOrder(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-original"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}

	canceled, err := svc.CancelOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}
	if canceled.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
	if stock.released != "res-original" {
		t.Fatalf("released = %q, want res-original", stock.released)
	}

	stock.reservationID = "res-late-pay"
	paid, err := svc.MarkOrderPaid(ctx, order.ID, "pay-late", order.TotalCents)
	if err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}
	if paid.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", paid.Status)
	}
	if paid.ReservationID != "res-late-pay" {
		t.Fatalf("reservation_id = %q, want res-late-pay", paid.ReservationID)
	}
	if len(stock.reservedItems) != 1 || stock.reservedItems[0].SKU != "BAG-1" {
		t.Fatalf("reserved items = %+v, want one BAG-1 line", stock.reservedItems)
	}
}

func TestMarkOrderPaidRollsBackReinstatedReservationOnAmountMismatch(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-original"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.CancelOrder(ctx, order.ID); err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}

	stock.reservationID = "res-late-pay"
	_, err = svc.MarkOrderPaid(ctx, order.ID, "pay-late", order.TotalCents+1)
	if !errors.Is(err, domain.ErrPaymentAmountMismatch) {
		t.Fatalf("MarkOrderPaid error = %v, want ErrPaymentAmountMismatch", err)
	}
	if stock.released != "res-late-pay" {
		t.Fatalf("released = %q, want res-late-pay rollback", stock.released)
	}

	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
}

func TestShipOrderRejectsPendingWithoutCommittingStock(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}

	_, err = svc.ShipOrder(ctx, order.ID, "manager-1", testShipTracking())
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("ShipOrder error = %v, want ErrInvalidTransition", err)
	}
	if stock.committed != "" {
		t.Fatalf("committed = %q, want empty (stock must not commit on rejected ship)", stock.committed)
	}

	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != domain.StatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
}

func TestShipOrderRejectsInvalidTrackingWithoutCommittingStock(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}

	_, err = svc.ShipOrder(ctx, order.ID, "manager-1", domain.ShipmentTracking{
		Carrier:        "fedex",
		TrackingNumber: "123456789012",
	})
	if !errors.Is(err, domain.ErrInvalidShipment) {
		t.Fatalf("ShipOrder error = %v, want ErrInvalidShipment", err)
	}
	if stock.committed != "" {
		t.Fatalf("committed = %q, want empty (stock must not commit on invalid tracking)", stock.committed)
	}

	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", got.Status)
	}
}

func TestShipOrderRejectsEmptyShippedByWithoutCommittingStock(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}

	_, err = svc.ShipOrder(ctx, order.ID, "   ", testShipTracking())
	if !errors.Is(err, domain.ErrInvalidOrder) {
		t.Fatalf("ShipOrder error = %v, want ErrInvalidOrder", err)
	}
	if stock.committed != "" {
		t.Fatalf("committed = %q, want empty (stock must not commit when shippedBy is empty)", stock.committed)
	}

	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", got.Status)
	}
}

// saveFailOnPaidRepo simulates a persistence failure after MarkOrderPaid mutates the order.
type saveFailOnPaidRepo struct {
	*memory.Repository
	fail bool
}

func (r *saveFailOnPaidRepo) SaveWithOutbox(ctx context.Context, order *domain.Order, idem *ports.IdempotencyRecord, events []ports.OutboxEvent) error {
	if r.fail && order.Status == domain.StatusPaid {
		return errors.New("simulated persistence failure")
	}
	return r.Repository.SaveWithOutbox(ctx, order, idem, events)
}

func (r *saveFailOnPaidRepo) SavePaidIfCanceled(ctx context.Context, order *domain.Order, events []ports.OutboxEvent) (bool, error) {
	if r.fail {
		return false, errors.New("simulated persistence failure")
	}
	return r.Repository.SavePaidIfCanceled(ctx, order, events)
}

func TestMarkOrderPaidRollsBackReinstatedReservationOnSaveFailure(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-original"}
	repo := &saveFailOnPaidRepo{Repository: memory.NewRepository(), fail: true}
	svc := service.New(repo, stock).WithProduct(&fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.CancelOrder(ctx, order.ID); err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}

	stock.reservationID = "res-late-pay"
	_, err = svc.MarkOrderPaid(ctx, order.ID, "pay-late", order.TotalCents)
	if err == nil {
		t.Fatal("MarkOrderPaid expected save failure")
	}
	if stock.released != "res-late-pay" {
		t.Fatalf("released = %q, want res-late-pay rollback", stock.released)
	}

	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled (save failure must not leave order paid)", got.Status)
	}
}

func TestShipOrderRetriesWhenReservationAlreadyCommitted(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}

	// Simulate a prior ShipOrder that committed stock but failed before saving status.
	stock.committed = order.ReservationID

	shipped, err := svc.ShipOrder(ctx, order.ID, "manager-1", testShipTracking())
	if err != nil {
		t.Fatalf("ShipOrder retry returned error: %v", err)
	}
	if shipped.Status != domain.StatusInTransit {
		t.Fatalf("status = %q, want in_transit", shipped.Status)
	}
}

type refundDuringShipStock struct {
	*fakeStock
	onAfterCommit func()
}

func (f *refundDuringShipStock) CommitReservation(ctx context.Context, reservationID string) error {
	err := f.fakeStock.CommitReservation(ctx, reservationID)
	if err != nil {
		return err
	}
	if f.onAfterCommit != nil {
		f.onAfterCommit()
	}
	return nil
}

func TestShipOrderDoesNotOverwriteRefundCancel(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-ship-race"}
	repo := memory.NewRepository()
	svc := service.New(repo, stock).WithProduct(&fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-race", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}

	raceStock := &refundDuringShipStock{
		fakeStock: stock,
		onAfterCommit: func() {
			if err := svc.CancelOrderForRefund(ctx, order.ID, "pay-race", 0); err != nil {
				t.Fatalf("CancelOrderForRefund during ship: %v", err)
			}
		},
	}
	svc = service.New(repo, raceStock).WithProduct(&fakeProduct{defaultCents: 5000})

	_, err = svc.ShipOrder(ctx, order.ID, "manager-1", testShipTracking())
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("ShipOrder error = %v, want ErrInvalidTransition", err)
	}
	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled (must not overwrite refund cancel)", got.Status)
	}
	if stock.committed != order.ReservationID {
		t.Fatalf("committed = %q, want %q", stock.committed, order.ReservationID)
	}
}

func TestShipOrderRejectsReleasedReservation(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}

	// Simulate paid order rows that still reference a reservation released elsewhere
	// (e.g. expiry race saved paid with a stale reservation_id).
	stock.released = order.ReservationID

	_, err = svc.ShipOrder(ctx, order.ID, "manager-1", testShipTracking())
	if !errors.Is(err, ports.ErrReservationAlreadyReleased) {
		t.Fatalf("ShipOrder error = %v, want ErrReservationAlreadyReleased", err)
	}
	if stock.committed != "" {
		t.Fatalf("committed = %q, want empty", stock.committed)
	}
}

type expiryRaceRepo struct {
	*memory.Repository
	stock    *fakeStock
	getCount map[string]int
}

func (r *expiryRaceRepo) Get(ctx context.Context, id string) (*domain.Order, error) {
	r.getCount[id]++
	order, err := r.Repository.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.getCount[id] == 2 && order.Status == domain.StatusPending {
		_ = r.stock.ReleaseReservation(ctx, order.ReservationID)
		order.Status = domain.StatusCanceled
		if err := r.Repository.Save(ctx, order); err != nil {
			return nil, err
		}
		return r.Repository.Get(ctx, id)
	}
	return order, nil
}

func TestMarkOrderPaidReinstatesWhenExpiryCancelsBeforeSave(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-original"}
	repo := &expiryRaceRepo{
		Repository: memory.NewRepository(),
		stock:      stock,
		getCount:   make(map[string]int),
	}
	svc := service.New(repo, stock).WithProduct(&fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}

	stock.reservationID = "res-late-pay"
	paid, err := svc.MarkOrderPaid(ctx, order.ID, "pay-late", order.TotalCents)
	if err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}
	if paid.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", paid.Status)
	}
	if paid.ReservationID != "res-late-pay" {
		t.Fatalf("reservation_id = %q, want res-late-pay", paid.ReservationID)
	}
	if stock.released != "res-original" {
		t.Fatalf("released = %q, want res-original from simulated expiry", stock.released)
	}
}

type savePaidRaceRepo struct {
	*memory.Repository
	stock *fakeStock
}

func (r *savePaidRaceRepo) SavePaidIfPending(ctx context.Context, order *domain.Order, events []ports.OutboxEvent) (bool, error) {
	stored, err := r.Get(ctx, order.ID)
	if err != nil {
		return false, err
	}
	if stored.Status == domain.StatusPending {
		_ = r.stock.ReleaseReservation(ctx, stored.ReservationID)
		stored.Status = domain.StatusCanceled
		if err := r.Repository.Save(ctx, stored); err != nil {
			return false, err
		}
	}
	return r.Repository.SavePaidIfPending(ctx, order, events)
}

func TestMarkOrderPaidReinstatesWhenExpiryCancelsBeforeSavePaid(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-original"}
	repo := &savePaidRaceRepo{
		Repository: memory.NewRepository(),
		stock:      stock,
	}
	svc := service.New(repo, stock).WithProduct(&fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}

	stock.reservationID = "res-late-pay"
	paid, err := svc.MarkOrderPaid(ctx, order.ID, "pay-late", order.TotalCents)
	if err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}
	if paid.Status != domain.StatusPaid {
		t.Fatalf("status = %q, want paid", paid.Status)
	}
	if paid.ReservationID != "res-late-pay" {
		t.Fatalf("reservation_id = %q, want res-late-pay", paid.ReservationID)
	}
	if stock.released != "res-original" {
		t.Fatalf("released = %q, want res-original from simulated expiry", stock.released)
	}
}

func TestCancelPaidOrderReleasesStock(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 7500})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "clock-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid returned error: %v", err)
	}

	_, err = svc.CancelOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}
	if stock.released != "res-123" {
		t.Fatalf("released reservation = %q, want res-123", stock.released)
	}
}

func TestCancelInTransitOrderFails(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	svc := newSvc(stock, &fakeProduct{defaultCents: 7500})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "clock-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := svc.MarkOrderPaid(ctx, order.ID, "pay-1", order.TotalCents); err != nil {
		t.Fatalf("MarkOrderPaid: %v", err)
	}
	if _, err := svc.ShipOrder(ctx, order.ID, "manager-1", testShipTracking()); err != nil {
		t.Fatalf("ShipOrder: %v", err)
	}

	_, err = svc.CancelOrder(ctx, order.ID)
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("CancelOrder error = %v, want ErrInvalidTransition", err)
	}
}

type saveFailOnCancelRepo struct {
	*memory.Repository
	fail bool
}

func (r *saveFailOnCancelRepo) SaveWithOutbox(ctx context.Context, order *domain.Order, idem *ports.IdempotencyRecord, events []ports.OutboxEvent) error {
	if r.fail && order.Status == domain.StatusCanceled {
		return errors.New("simulated cancel persistence failure")
	}
	return r.Repository.SaveWithOutbox(ctx, order, idem, events)
}

func TestCancelOrderDoesNotReleaseStockWhenSaveFails(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-123"}
	repo := &saveFailOnCancelRepo{Repository: memory.NewRepository(), fail: true}
	svc := service.New(repo, stock).WithProduct(&fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}

	_, err = svc.CancelOrder(ctx, order.ID)
	if err == nil {
		t.Fatal("CancelOrder expected save failure")
	}
	if stock.released != "" {
		t.Fatalf("released = %q, want empty when cancel save fails", stock.released)
	}

	got, err := svc.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != domain.StatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
}

type alreadyReleasedStock struct {
	fakeStock
	releaseCalls int
}

func (f *alreadyReleasedStock) ReleaseReservation(ctx context.Context, reservationID string) error {
	f.releaseCalls++
	return ports.ErrReservationAlreadyReleased
}

func TestCancelOrderSucceedsWhenReservationAlreadyReleased(t *testing.T) {
	ctx := t.Context()
	stock := &alreadyReleasedStock{fakeStock: fakeStock{reservationID: "res-123"}}
	svc := newSvc(stock, &fakeProduct{defaultCents: 5000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}

	canceled, err := svc.CancelOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}
	if canceled.Status != domain.StatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
	if stock.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", stock.releaseCalls)
	}
}

func TestCreateOrderReservesStockWithSkuID(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-999"}
	svc := newSvc(stock, &fakeProduct{
		byKey: map[string]*ports.VariantInfo{
			"SKUID-1": {SkuID: "SKUID-1", SKU: "SHOE-1", UnitPriceCents: 1250},
		},
	})

	_, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items: []domain.OrderItem{
			{SkuID: "SKUID-1", SKU: "shoe-1", Quantity: 2, UnitPriceCents: 1},
		},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if len(stock.reservedItems) != 1 || stock.reservedItems[0].SkuID != "SKUID-1" {
		t.Fatalf("reserved items = %+v, want SkuID SKUID-1 forwarded", stock.reservedItems)
	}
}

func TestCreateOrderEventCarriesSkuID(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-1"}
	publisher := &recordedPublisher{}
	svc := newSvc(stock, &fakeProduct{
		byKey: map[string]*ports.VariantInfo{
			"SKUID-2": {SkuID: "SKUID-2", SKU: "BAG-2", UnitPriceCents: 5000},
		},
	}, publisher)

	_, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items: []domain.OrderItem{
			{SkuID: "SKUID-2", SKU: "bag-2", Quantity: 1, UnitPriceCents: 1},
		},
	})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if len(publisher.events) == 0 {
		t.Fatal("expected at least one published event")
	}

	raw, err := json.Marshal(publisher.events[0])
	if err != nil {
		t.Fatalf("marshal published event: %v", err)
	}
	var decoded struct {
		Items []struct {
			SkuID string `json:"sku_id"`
			SKU   string `json:"sku"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal published event: %v", err)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].SkuID != "SKUID-2" || decoded.Items[0].SKU != "BAG-2" {
		t.Fatalf("published event items = %+v, want sku_id SKUID-2 / sku BAG-2", decoded.Items)
	}
}

func TestCreateOrderIdempotencyKeyReplaysWithoutSecondReserve(t *testing.T) {
	ctx := t.Context()
	stock := &countingStock{fakeStock: fakeStock{reservationID: "res-1"}}
	publisher := &recordedPublisher{}
	svc := newSvc(stock, &fakeProduct{defaultCents: 1000}, publisher)

	input := service.CreateOrderInput{
		CustomerID:     "customer-1",
		IdempotencyKey: "idem-abc",
		Items:          []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	}
	first, err := svc.CreateOrder(ctx, input)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	second, err := svc.CreateOrder(ctx, input)
	if err != nil {
		t.Fatalf("CreateOrder replay: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("replay order id = %q, want %q", second.ID, first.ID)
	}
	if stock.reserveCalls != 1 {
		t.Fatalf("reserve calls = %d, want 1", stock.reserveCalls)
	}
}

func TestCreateOrderIdempotencyKeyConflict(t *testing.T) {
	ctx := t.Context()
	svc := newSvc(&fakeStock{reservationID: "res-1"}, &fakeProduct{defaultCents: 1000})

	_, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID:     "customer-1",
		IdempotencyKey: "idem-conflict",
		Items:          []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	_, err = svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID:     "customer-1",
		IdempotencyKey: "idem-conflict",
		Items:          []domain.OrderItem{{SKU: "bag-2", Quantity: 1}},
	})
	if !errors.Is(err, ports.ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestCreateOrderSucceedsWhenPublishFails(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-1"}
	publisher := &failingPublisher{}
	repo := memory.NewRepository()
	svc := service.New(repo, stock, publisher).WithProduct(&fakeProduct{defaultCents: 1000})

	order, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateOrder should soft-succeed: %v", err)
	}
	if order.ID == "" {
		t.Fatal("expected order id")
	}
	if publisher.calls < 1 {
		t.Fatal("expected publish attempt")
	}
	pending, err := repo.ListPendingOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingOutbox: %v", err)
	}
	if len(pending) != 1 || pending[0].Subject != "order.created" {
		t.Fatalf("pending outbox = %+v, want one order.created", pending)
	}
}

func TestDrainOutboxPublishesPending(t *testing.T) {
	ctx := t.Context()
	stock := &fakeStock{reservationID: "res-1"}
	failPub := &failingPublisher{}
	repo := memory.NewRepository()
	svc := service.New(repo, stock, failPub).WithProduct(&fakeProduct{defaultCents: 1000})

	if _, err := svc.CreateOrder(ctx, service.CreateOrderInput{
		CustomerID: "customer-1",
		Items:      []domain.OrderItem{{SKU: "bag-1", Quantity: 1}},
	}); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	okPub := &recordedPublisher{}
	svcOK := service.New(repo, stock, okPub).WithProduct(&fakeProduct{defaultCents: 1000})
	if err := svcOK.DrainOutbox(ctx); err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	if len(okPub.subjects) != 1 || okPub.subjects[0] != "order.created" {
		t.Fatalf("subjects = %v, want [order.created]", okPub.subjects)
	}
	pending, err := repo.ListPendingOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingOutbox: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(pending))
	}
}
