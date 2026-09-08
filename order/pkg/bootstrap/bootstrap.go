package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/elug3/dupli1/order/pkg/handler"
	"github.com/elug3/dupli1/order/pkg/infra/httpauth"
	"github.com/elug3/dupli1/order/pkg/infra/httpcoupon"
	"github.com/elug3/dupli1/order/pkg/infra/httpproduct"
	"github.com/elug3/dupli1/order/pkg/infra/httpstock"
	"github.com/elug3/dupli1/order/pkg/infra/memory"
	natsinfra "github.com/elug3/dupli1/order/pkg/infra/nats"
	"github.com/elug3/dupli1/order/pkg/infra/pg"
	"github.com/elug3/dupli1/order/pkg/ports"
	"github.com/elug3/dupli1/order/pkg/service"
	"github.com/elug3/dupli1/shared/pkg/authjwt"
)

type Config struct {
	// GatewayURL is the internal API gateway (nginx). Preferred base for product
	// stock/coupon HTTP calls so order does not hard-code product service DNS.
	GatewayURL string

	// ProductURL / InventoryURL are deprecated direct overrides.
	ProductURL   string
	InventoryURL string

	AuthURL              string
	OrderServiceEmail    string
	OrderServicePassword string
	// StockBearerToken overrides the service-account login (static token).
	StockBearerToken string

	DatabaseConnString string
	JWTSecret          string
	JWKSURL            string
	NATSURL            string

	// ShippingFeeKRW is the flat delivery charge added to every order, in
	// whole KRW. Zero means free delivery.
	ShippingFeeKRW int64

	HTTPClient *http.Client
}

type App struct {
	Router         *http.ServeMux
	Handler        *handler.Handler
	Service        *service.Service
	Repo           ports.Repository
	Stock          ports.StockClient
	natsPublisher  *natsinfra.Publisher
	natsSubscriber *natsinfra.Subscriber
	expiryCancel   context.CancelFunc
	close          func() error
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	var errs []error
	if a.expiryCancel != nil {
		a.expiryCancel()
	}
	if a.natsSubscriber != nil {
		a.natsSubscriber.Close()
	}
	if a.natsPublisher != nil {
		a.natsPublisher.Close()
	}
	if a.close != nil {
		errs = append(errs, a.close())
	}
	return errors.Join(errs...)
}

func Bootstrap(cfg Config) (*App, error) {
	repo, closeFn, err := openRepository(cfg.DatabaseConnString)
	if err != nil {
		return nil, err
	}

	apiBase, err := resolveAPIBaseURL(cfg)
	if err != nil {
		closeFn()
		return nil, err
	}

	// Process bootstrap: no HTTP request context available.
	stockTokenSource, err := resolveStockTokenSource(context.Background(), cfg)
	if err != nil {
		closeFn()
		return nil, err
	}
	stock := httpstock.NewClient(apiBase, cfg.HTTPClient, stockTokenSource)
	product := httpproduct.NewClient(apiBase, cfg.HTTPClient)
	couponClient := httpcoupon.NewClient(apiBase, cfg.HTTPClient)

	var eventPublisher ports.EventPublisher
	var natsPublisher *natsinfra.Publisher
	var natsSubscriber *natsinfra.Subscriber
	if cfg.NATSURL != "" {
		var err error
		natsPublisher, err = natsinfra.NewPublisher(cfg.NATSURL)
		if err != nil {
			return nil, err
		}
		eventPublisher = natsPublisher

		natsSubscriber, err = natsinfra.NewSubscriber(cfg.NATSURL)
		if err != nil {
			natsPublisher.Close()
			return nil, err
		}
	}

	svc := service.NewWithCheckout(repo, stock, couponClient, 0, eventPublisher).
		WithProduct(product).
		WithShippingFee(cfg.ShippingFeeKRW)

	if natsSubscriber != nil {
		// Long-lived worker/subscriber root; cancelled on process shutdown.
		if err := svc.RegisterPaymentConsumer(context.Background(), natsSubscriber); err != nil {
			natsSubscriber.Close()
			natsPublisher.Close()
			closeFn()
			return nil, fmt.Errorf("payment consumer: %w", err)
		}
		if err := svc.RegisterPaymentCanceledConsumer(context.Background(), natsSubscriber); err != nil {
			natsSubscriber.Close()
			natsPublisher.Close()
			closeFn()
			return nil, fmt.Errorf("payment canceled consumer: %w", err)
		}
	}
	// Long-lived worker/subscriber root; cancelled on process shutdown.
	expiryCtx, expiryCancel := context.WithCancel(context.Background())
	svc.StartPendingExpiryWorker(expiryCtx, 30*time.Second)
	svc.StartOutboxWorker(expiryCtx, 2*time.Second)

	jwtValidator, err := authjwt.NewAccessTokenValidator(cfg.JWKSURL, cfg.JWTSecret)
	if err != nil {
		expiryCancel()
		if natsSubscriber != nil {
			natsSubscriber.Close()
		}
		if natsPublisher != nil {
			natsPublisher.Close()
		}
		closeFn()
		return nil, fmt.Errorf("auth validator: %w", err)
	}

	h := handler.New(svc, jwtValidator).WithSettings(BuildSettings(cfg))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	return &App{
		Router:         mux,
		Handler:        h,
		Service:        svc,
		Repo:           repo,
		Stock:          stock,
		natsPublisher:  natsPublisher,
		natsSubscriber: natsSubscriber,
		expiryCancel:   expiryCancel,
		close:          closeFn,
	}, nil
}

func openRepository(connString string) (ports.Repository, func() error, error) {
	if connString == "" {
		return memory.NewRepository(), func() error { return nil }, nil
	}

	pgRepo, err := pg.NewRepository(connString)
	if err != nil {
		return nil, nil, fmt.Errorf("order repository: %w", err)
	}
	return pgRepo, func() error {
		pgRepo.Close()
		return nil
	}, nil
}

// resolveAPIBaseURL prefers the internal gateway so stock/coupon calls use the
// same path routing as external clients (/api/v1/inventory, /api/v1/coupons).
// Direct ProductURL / InventoryURL remain as escape hatches.
func resolveAPIBaseURL(cfg Config) (string, error) {
	if u := strings.TrimSpace(cfg.GatewayURL); u != "" {
		return strings.TrimRight(u, "/"), nil
	}
	if u := strings.TrimSpace(cfg.ProductURL); u != "" {
		return strings.TrimRight(u, "/"), nil
	}
	if u := strings.TrimSpace(cfg.InventoryURL); u != "" {
		return strings.TrimRight(u, "/"), nil
	}
	return "", fmt.Errorf("DUPLI1_GATEWAY_URL is required (or deprecated DUPLI1_PRODUCT_URL)")
}

func resolveStockTokenSource(ctx context.Context, cfg Config) (httpauth.TokenSource, error) {
	if cfg.StockBearerToken != "" {
		return httpauth.StaticToken(cfg.StockBearerToken), nil
	}
	authBase := strings.TrimSpace(cfg.AuthURL)
	if authBase == "" {
		// Fall back to gateway for login/refresh when AuthURL is unset.
		authBase = strings.TrimSpace(cfg.GatewayURL)
	}
	if authBase == "" || cfg.OrderServiceEmail == "" || cfg.OrderServicePassword == "" {
		return nil, nil
	}
	src := httpauth.NewServiceAccountTokenSource(authBase, cfg.OrderServiceEmail, cfg.OrderServicePassword, cfg.HTTPClient)
	// Prime the cache at startup so misconfigured credentials fail fast.
	// Prefer a direct AuthURL in Compose so bootstrap does not depend on the
	// proxy (proxy itself waits for order health).
	if _, err := src.Token(ctx); err != nil {
		return nil, fmt.Errorf("order service account token for product stock: %w", err)
	}
	return src, nil
}

func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func CloseApps(apps ...*App) error {
	var errs []error
	for _, app := range apps {
		if app != nil {
			errs = append(errs, app.Close())
		}
	}
	return errors.Join(errs...)
}
