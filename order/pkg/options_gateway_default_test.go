package order_test

import (
	"testing"

	order "github.com/elug3/dupli1/order/pkg"
)

func TestNewServerOptions_GatewayURLEmptyByDefault(t *testing.T) {
	opts := order.NewServerOptions()
	if opts.GatewayURL != "" {
		t.Fatalf("GatewayURL default = %q, want empty so DUPLI1_PRODUCT_URL is usable", opts.GatewayURL)
	}
	if opts.ProductURL == "" {
		t.Fatal("ProductURL default should remain set for local go run")
	}
}

// The configured default is the charge every deployment gets unless it opts
// out, so it is worth pinning: a silent change here re-prices every order.
func TestNewServerOptions_ShippingFeeDefault(t *testing.T) {
	opts := order.NewServerOptions()
	if opts.ShippingFeeKRW != 30000 {
		t.Fatalf("ShippingFeeKRW default = %d, want 30000 (30,000 KRW)", opts.ShippingFeeKRW)
	}
	if order.DefaultShippingFeeKRW != 30000 {
		t.Fatalf("DefaultShippingFeeKRW = %d, want 30000", order.DefaultShippingFeeKRW)
	}
}
