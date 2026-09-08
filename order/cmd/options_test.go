package main

import (
	"flag"
	"io"
	"testing"

	order "github.com/elug3/dupli1/order/pkg"
)

func configureForTest(t *testing.T) order.ServerOptions {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts, err := ConfigureOptions(fs, nil)
	if err != nil {
		t.Fatalf("ConfigureOptions: %v", err)
	}
	return opts
}

func TestApplyEnvShippingFeeKRW(t *testing.T) {
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_KRW", "15000")
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_CENTS", "0")
	opts := configureForTest(t)
	if opts.ShippingFeeKRW != 15000 {
		t.Fatalf("ShippingFeeKRW = %d, want 15000 from KRW env", opts.ShippingFeeKRW)
	}
}

func TestApplyEnvShippingFeeCentsAlias(t *testing.T) {
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_KRW", "")
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_CENTS", "0")
	opts := configureForTest(t)
	if opts.ShippingFeeKRW != 0 {
		t.Fatalf("ShippingFeeKRW = %d, want 0 from CENTS alias (free)", opts.ShippingFeeKRW)
	}
}

func TestApplyEnvShippingFeeKRWWinsOverCents(t *testing.T) {
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_KRW", "18000")
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_CENTS", "0")
	opts := configureForTest(t)
	if opts.ShippingFeeKRW != 18000 {
		t.Fatalf("ShippingFeeKRW = %d, want KRW to win over CENTS", opts.ShippingFeeKRW)
	}
}

func TestApplyEnvShippingFeeInvalidKeepsDefault(t *testing.T) {
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_KRW", "-1")
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_CENTS", "")
	opts := configureForTest(t)
	if opts.ShippingFeeKRW != order.DefaultShippingFeeKRW {
		t.Fatalf("ShippingFeeKRW = %d, want default %d for invalid KRW", opts.ShippingFeeKRW, order.DefaultShippingFeeKRW)
	}
}

func TestApplyEnvShippingFeeUnsetUsesDefault(t *testing.T) {
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_KRW", "")
	t.Setenv("DUPLI1_ORDER_SHIPPING_FEE_CENTS", "")
	opts := configureForTest(t)
	if opts.ShippingFeeKRW != order.DefaultShippingFeeKRW {
		t.Fatalf("ShippingFeeKRW = %d, want default %d when unset", opts.ShippingFeeKRW, order.DefaultShippingFeeKRW)
	}
}
