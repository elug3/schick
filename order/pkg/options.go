package order

import "time"

// DefaultShippingFeeKRW is the flat per-order delivery charge in whole KRW
// applied when DUPLI1_ORDER_SHIPPING_FEE_KRW (and the deprecated
// DUPLI1_ORDER_SHIPPING_FEE_CENTS alias) are not set.
const DefaultShippingFeeKRW int64 = 30000

type ServerOptions struct {
	Addr string

	// GatewayURL is the internal nginx gateway base (preferred for product stock/coupons).
	// Example Compose: http://dupli1-proxy  Example ECS: http://proxy.dupli1.local
	GatewayURL string

	// ProductURL is a deprecated direct product base URL. Prefer GatewayURL.
	ProductURL string
	// InventoryURL is a deprecated alias for ProductURL.
	InventoryURL string

	AuthURL              string
	OrderServiceEmail    string
	OrderServicePassword string
	StockBearerToken     string

	DatabaseConnString string
	JWTSecret          string
	JWKSURL            string
	NATSURL            string

	// ShippingFeeKRW is the flat delivery charge added to every order, in
	// whole KRW. Set DUPLI1_ORDER_SHIPPING_FEE_KRW to override (deprecated
	// alias: DUPLI1_ORDER_SHIPPING_FEE_CENTS). An explicit 0 means free
	// delivery.
	ShippingFeeKRW int64

	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

func NewServerOptions() *ServerOptions {
	return &ServerOptions{
		Addr: ":8083",
		// GatewayURL must stay empty unless set via DUPLI1_GATEWAY_URL / -gateway-url.
		// A localhost default would shadow DUPLI1_PRODUCT_URL (preferred in older ECS
		// task defs) and make order call itself → 404 → checkout "unavailable items".
		// Local Compose sets DUPLI1_GATEWAY_URL; bare `go run` can still use ProductURL.
		ProductURL: "http://localhost:8081",
		// Flat delivery charge in whole KRW (30,000 KRW).
		ShippingFeeKRW:  DefaultShippingFeeKRW,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     120 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	}
}
