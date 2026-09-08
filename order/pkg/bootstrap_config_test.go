package order_test

import (
	"reflect"
	"testing"

	order "github.com/elug3/dupli1/order/pkg"
)

// A configured shipping fee once failed to reach the service because
// NewServer's bootstrap.Config literal simply omitted the field — Go zeroes an
// omitted field, so it compiled, every unit test passed, and the running
// service quietly charged nothing.
//
// Rather than pin one field, walk them: every bootstrap.Config field that has a
// same-named, same-typed ServerOptions field must be carried across. A future
// option added to both structs but forgotten in the mapping fails here.
func TestBootstrapConfig_CarriesEveryMatchingOption(t *testing.T) {
	opts := order.ServerOptions{
		Addr:                 ":8083",
		GatewayURL:           "http://gateway.test",
		ProductURL:           "http://product.test",
		InventoryURL:         "http://inventory.test",
		AuthURL:              "http://auth.test",
		OrderServiceEmail:    "svc@order.test",
		OrderServicePassword: "svc-secret",
		StockBearerToken:     "stock-token",
		DatabaseConnString:   "postgres://user:pass@db.test/orders",
		JWTSecret:            "jwt-secret",
		JWKSURL:              "http://auth.test/jwks.json",
		NATSURL:              "nats://nats.test:4222",
		ShippingFeeKRW:     30000,
	}

	cfg := order.BootstrapConfig(opts)
	cfgVal, optVal := reflect.ValueOf(cfg), reflect.ValueOf(opts)
	optType := optVal.Type()

	checked := 0
	for i := 0; i < cfgVal.NumField(); i++ {
		name := cfgVal.Type().Field(i).Name
		optField, ok := optType.FieldByName(name)
		if !ok || optField.Type != cfgVal.Type().Field(i).Type {
			continue // not sourced from options (e.g. HTTPClient)
		}
		want := optVal.FieldByName(name).Interface()
		got := cfgVal.Field(i).Interface()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("bootstrap.Config.%s = %v, want %v — field dropped in the mapping", name, got, want)
		}
		if reflect.ValueOf(want).IsZero() {
			t.Fatalf("test setup: %s must be non-zero to detect a dropped field", name)
		}
		checked++
	}
	if checked < 12 {
		t.Fatalf("only %d fields compared; the mapping test is not covering the struct", checked)
	}
}

// The fee specifically, since it is the one that reached production wrong.
func TestBootstrapConfig_CarriesShippingFee(t *testing.T) {
	cfg := order.BootstrapConfig(order.ServerOptions{ShippingFeeKRW: 30000})
	if cfg.ShippingFeeKRW != 30000 {
		t.Fatalf("ShippingFeeKRW = %d, want 30000", cfg.ShippingFeeKRW)
	}
	if zero := order.BootstrapConfig(order.ServerOptions{}); zero.ShippingFeeKRW != 0 {
		t.Fatalf("unset fee = %d, want 0", zero.ShippingFeeKRW)
	}
}
