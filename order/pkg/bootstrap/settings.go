package bootstrap

import (
	"github.com/elug3/dupli1/shared/pkg/money"
	"github.com/elug3/dupli1/shared/pkg/settings"
)

// BuildSettings returns the public, non-secret settings payload for the order service.
func BuildSettings(cfg Config) settings.Response {
	resp := settings.NewResponse("order")
	resp.Auth = settings.ConsumerAuth(cfg.JWKSURL, cfg.JWTSecret)
	resp.Storage = settings.StorageMode(cfg.DatabaseConnString)
	resp.Features = map[string]bool{
		"nats_events":           cfg.NATSURL != "",
		"payment_consumer":      cfg.NATSURL != "",
		"pending_expiry_worker": true,
		"checkout_sessions":     true,
	}
	resp.Limits = map[string]any{
		"currency": money.Currency, // *_cents amounts are whole KRW won
		// Published so storefronts quote the charge the service will actually
		// apply, instead of hardcoding their own copy that can drift from it.
		// Whole KRW; 0 means free delivery.
		"shipping_fee_krw": cfg.ShippingFeeKRW,
	}
	apiBase, _ := resolveAPIBaseURL(cfg)
	authBase := cfg.AuthURL
	if authBase == "" {
		authBase = cfg.GatewayURL
	}
	resp.Dependencies = map[string]settings.Dependency{
		"gateway": settings.Dep(cfg.GatewayURL),
		"product": settings.Dep(apiBase), // via gateway (or deprecated direct URL)
		"auth":    settings.Dep(authBase),
		"nats":    {Configured: cfg.NATSURL != ""},
	}
	return resp
}
