package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/elug3/schick/pkg/auth/bootstrap"
)

// Server represents the auth service HTTP server.
type Server struct {
	opts      ServerOptions
	http      *http.Server
	app       *bootstrap.App
	mu        sync.RWMutex
	stopped   chan struct{}
	stopOnce  sync.Once
}

// NewServer creates a new auth server.
func NewServer(opts ServerOptions) (*Server, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	app, err := bootstrap.Bootstrap(context.Background(), bootstrap.Config{
		DBURL:              opts.DBURL,
		RedisURL:           opts.RedisURL,
		TokenSigningKey:    opts.TokenSigningKey,
		TokenExpiry:        opts.TokenExpiry,
		RefreshTokenExpiry: opts.RefreshTokenExpiry,
		Debug:              opts.Debug,
		MaxConns:           opts.MaxConns,
	})
	if err != nil {
		return nil, err
	}

	srv := &http.Server{
		Addr:         opts.Addr,
		Handler:      app.Engine,
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
		IdleTimeout:  opts.IdleTimeout,
	}

	return &Server{
		opts:    opts,
		http:    srv,
		app:     app,
		stopped: make(chan struct{}),
	}, nil
}

// Run starts the server and blocks until it stops or returns an error.
func (s *Server) Run() error {
	s.mu.RLock()
	httpSrv := s.http
	addr := httpSrv.Addr
	s.mu.RUnlock()

	fmt.Printf("Starting auth server on %s\n", addr)
	err := httpSrv.ListenAndServe()
	s.markStopped()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop gracefully stops the server.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.http == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
	defer cancel()

	fmt.Println("Gracefully stopping auth server...")
	err := s.http.Shutdown(ctx)
	if closeErr := s.app.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}

func (s *Server) markStopped() {
	s.stopOnce.Do(func() {
		close(s.stopped)
	})
}

// Wait blocks until the server has stopped.
func (s *Server) Wait() {
	<-s.stopped
}

// StopAndWait gracefully stops the server and waits for it to close.
func (s *Server) StopAndWait() {
	_ = s.Stop()
	s.Wait()
}
