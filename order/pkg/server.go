package order

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/elug3/dupli1/order/pkg/bootstrap"
)

type Server struct {
	opts     ServerOptions
	http     *http.Server
	app      *bootstrap.App
	stopped  chan struct{}
	stopOnce sync.Once
}

// BootstrapConfig maps process options onto the bootstrap config. It is split
// out of NewServer so the mapping is testable: a field omitted from this struct
// literal silently takes its zero value, which is how a configured shipping fee
// once failed to reach the service while everything still compiled.
func BootstrapConfig(opts ServerOptions) bootstrap.Config {
	return bootstrap.Config{
		GatewayURL:           opts.GatewayURL,
		ProductURL:           opts.ProductURL,
		InventoryURL:         opts.InventoryURL,
		AuthURL:              opts.AuthURL,
		OrderServiceEmail:    opts.OrderServiceEmail,
		OrderServicePassword: opts.OrderServicePassword,
		StockBearerToken:     opts.StockBearerToken,
		DatabaseConnString:   opts.DatabaseConnString,
		JWTSecret:            opts.JWTSecret,
		JWKSURL:              opts.JWKSURL,
		NATSURL:              opts.NATSURL,
		ShippingFeeKRW:       opts.ShippingFeeKRW,
		HTTPClient:           bootstrap.DefaultHTTPClient(),
	}
}

func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("Addr is required")
	}

	app, err := bootstrap.Bootstrap(BootstrapConfig(opts))
	if err != nil {
		return nil, err
	}
	httpSrv := &http.Server{
		Addr:         opts.Addr,
		Handler:      app.Router,
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
		IdleTimeout:  opts.IdleTimeout,
	}

	return &Server{
		opts:    opts,
		http:    httpSrv,
		app:     app,
		stopped: make(chan struct{}),
	}, nil
}

func (s *Server) Run() error {
	fmt.Printf("Starting order server on %s\n", s.http.Addr)
	err := s.http.ListenAndServe()
	s.markStopped()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Stop() error {
	// Graceful shutdown timeout parent after SIGINT; not tied to a request.
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
	defer cancel()

	fmt.Println("Gracefully stopping order server...")
	err := s.http.Shutdown(ctx)
	if closeErr := s.app.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}

func (s *Server) Wait() {
	<-s.stopped
}

func (s *Server) StopAndWait() {
	_ = s.Stop()
	s.Wait()
}

func (s *Server) markStopped() {
	s.stopOnce.Do(func() {
		close(s.stopped)
	})
}

func (s *Server) App() *bootstrap.App {
	return s.app
}
