package main

import (
	"flag"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	order "github.com/elug3/dupli1/order/pkg"
)

type Options = order.ServerOptions

func ConfigureOptions(fs *flag.FlagSet, args []string) (Options, error) {
	opts := order.NewServerOptions()
	applyEnv(opts)

	host, port, err := splitAddr(opts.Addr)
	if err != nil {
		return Options{}, err
	}

	var (
		addr               string
		gatewayURL         = opts.GatewayURL
		productURL         = opts.ProductURL // deprecated
		inventoryURL       = opts.InventoryURL
		natsURL            = opts.NATSURL
		readTimeoutSec     = int(opts.ReadTimeout / time.Second)
		writeTimeoutSec    = int(opts.WriteTimeout / time.Second)
		idleTimeoutSec     = int(opts.IdleTimeout / time.Second)
		shutdownTimeoutSec = int(opts.ShutdownTimeout / time.Second)
	)

	fs.StringVar(&host, "host", host, "Server host address")
	fs.IntVar(&port, "port", port, "Server port number")
	fs.StringVar(&addr, "addr", "", "Server listen address (overrides host/port)")
	fs.StringVar(&gatewayURL, "gateway-url", gatewayURL, "Internal API gateway base URL (stock + coupons)")
	fs.StringVar(&productURL, "product-url", productURL, "Deprecated direct product URL; prefer -gateway-url")
	fs.StringVar(&inventoryURL, "inventory-url", inventoryURL, "Deprecated alias for -product-url")
	fs.StringVar(&natsURL, "nats-url", natsURL, "NATS server URL for order events")
	fs.IntVar(&readTimeoutSec, "read-timeout", readTimeoutSec, "Read timeout in seconds")
	fs.IntVar(&writeTimeoutSec, "write-timeout", writeTimeoutSec, "Write timeout in seconds")
	fs.IntVar(&idleTimeoutSec, "idle-timeout", idleTimeoutSec, "Idle timeout in seconds")
	fs.IntVar(&shutdownTimeoutSec, "shutdown-timeout", shutdownTimeoutSec, "Graceful shutdown timeout in seconds")

	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}

	if addr != "" {
		opts.Addr = addr
	} else {
		opts.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	}
	opts.GatewayURL = gatewayURL
	opts.ProductURL = productURL
	opts.InventoryURL = inventoryURL
	opts.NATSURL = natsURL
	opts.ReadTimeout = time.Duration(readTimeoutSec) * time.Second
	opts.WriteTimeout = time.Duration(writeTimeoutSec) * time.Second
	opts.IdleTimeout = time.Duration(idleTimeoutSec) * time.Second
	opts.ShutdownTimeout = time.Duration(shutdownTimeoutSec) * time.Second

	return *opts, nil
}

func applyEnv(opts *order.ServerOptions) {
	if v := os.Getenv("DUPLI1_ORDER_ADDR"); v != "" {
		opts.Addr = v
	}
	if v := os.Getenv("DUPLI1_GATEWAY_URL"); v != "" {
		opts.GatewayURL = v
	}
	if v := os.Getenv("DUPLI1_PRODUCT_URL"); v != "" {
		opts.ProductURL = v
	}
	// Deprecated: formerly pointed at a standalone inventory service.
	if v := os.Getenv("DUPLI1_INVENTORY_URL"); v != "" {
		opts.InventoryURL = v
	}
	if v := os.Getenv("DUPLI1_AUTH_URL"); v != "" {
		opts.AuthURL = v
	}
	if v := os.Getenv("DUPLI1_ORDER_SERVICE_EMAIL"); v != "" {
		opts.OrderServiceEmail = v
	}
	if v := os.Getenv("DUPLI1_ORDER_SERVICE_PASSWORD"); v != "" {
		opts.OrderServicePassword = v
	}
	if v := os.Getenv("DUPLI1_ORDER_STOCK_BEARER_TOKEN"); v != "" {
		opts.StockBearerToken = v
	} else if v := os.Getenv("DUPLI1_INVENTORY_BEARER_TOKEN"); v != "" {
		opts.StockBearerToken = v
	}
	if v := os.Getenv("JWT_SECRET"); v != "" {
		opts.JWTSecret = v
	}
	if v := os.Getenv("AUTH_JWKS_URL"); v != "" {
		opts.JWKSURL = v
	}
	if v := os.Getenv("DUPLI1_ORDER_DB"); v != "" {
		opts.DatabaseConnString = v
	} else if v := os.Getenv("DB_URL"); v != "" {
		opts.DatabaseConnString = v
	}
	applyShippingFeeEnv(opts)
	if v := os.Getenv("DUPLI1_ORDER_NATS_URL"); v != "" {
		opts.NATSURL = v
	} else if v := os.Getenv("NATS_URL"); v != "" {
		opts.NATSURL = v
	}
}

// applyShippingFeeEnv prefers DUPLI1_ORDER_SHIPPING_FEE_KRW. If that is empty,
// it falls back to the deprecated DUPLI1_ORDER_SHIPPING_FEE_CENTS alias. Both
// set → KRW wins. Invalid or negative values are logged and ignored so the
// compiled default (30000) stays in place.
func applyShippingFeeEnv(opts *order.ServerOptions) {
	name := "DUPLI1_ORDER_SHIPPING_FEE_KRW"
	v := os.Getenv(name)
	if v == "" {
		name = "DUPLI1_ORDER_SHIPPING_FEE_CENTS"
		v = os.Getenv(name)
	}
	if v == "" {
		return
	}
	krw, err := strconv.ParseInt(v, 10, 64)
	if err != nil || krw < 0 {
		log.Printf("order: ignoring invalid %s=%q", name, v)
		return
	}
	opts.ShippingFeeKRW = krw
}

func splitAddr(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		if addr == "" {
			return "", 8083, nil
		}
		return "", 0, err
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}
