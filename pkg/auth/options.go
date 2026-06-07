package auth

import (
	"fmt"
	"time"
)

type ServerOptions struct {
	// Listen address (e.g. ":8080")
	Addr string

	// Publicly reachable base URL (used for redirect/links)
	PublicAddr string

	// TLS files (optional)
	TLSCertFile string
	TLSKeyFile  string

	// HTTP server timeouts
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration

	// Token settings
	TokenSigningKey    []byte
	TokenExpiry        time.Duration
	RefreshTokenExpiry time.Duration

	// Cookie settings for session cookies
	CookieName     string
	CookieSecure   bool
	CookieHTTPOnly bool

	// CORS allowed origins
	CORSOrigins []string

	// Persistence/infra
	DBURL    string
	RedisURL string

	// Limits / misc
	MaxConns int
	Debug    bool
}

// NewServerOptions returns ServerOptions populated with sensible defaults.
func NewServerOptions() *ServerOptions {
	return &ServerOptions{
		Addr:               ":8080",
		PublicAddr:         "http://localhost:8080",
		ReadTimeout:        5 * time.Second,
		WriteTimeout:       10 * time.Second,
		IdleTimeout:        120 * time.Second,
		ShutdownTimeout:    10 * time.Second,
		TokenExpiry:        15 * time.Minute,
		RefreshTokenExpiry: 24 * time.Hour,
		CookieName:         "schick_session",
		CookieSecure:       false,
		CookieHTTPOnly:     true,
		MaxConns:           100,
		Debug:              false,
	}
}

// Validate performs basic sanity checks on the options.
func (o *ServerOptions) Validate() error {
	if o == nil {
		return fmt.Errorf("server options are nil")
	}
	if o.Addr == "" {
		return fmt.Errorf("Addr is required")
	}
	if len(o.TokenSigningKey) == 0 {
		return fmt.Errorf("TokenSigningKey is required")
	}
	if o.TokenExpiry <= 0 {
		return fmt.Errorf("TokenExpiry must be > 0")
	}
	if o.DBURL == "" {
		return fmt.Errorf("DBURL is required")
	}
	return nil
}
