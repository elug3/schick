package main

import (
	"flag"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/elug3/schick/pkg/auth"
)

// Options configures the schick-auth executable.
type Options = auth.ServerOptions

func ConfigureOptions(fs *flag.FlagSet, args []string) (Options, error) {
	opts := auth.NewServerOptions()
	opts.DBURL = "postgres://schick:schick_dev@localhost:5432/schick_db?sslmode=disable"
	applyEnv(opts)

	host, port, err := splitAddr(opts.Addr)
	if err != nil {
		return Options{}, err
	}

	var (
		addr               string
		publicAddr         = opts.PublicAddr
		dbURL              = opts.DBURL
		redisURL           = opts.RedisURL
		jwtSecret          string
		readTimeoutSec     = int(opts.ReadTimeout / time.Second)
		writeTimeoutSec    = int(opts.WriteTimeout / time.Second)
		idleTimeoutSec     = int(opts.IdleTimeout / time.Second)
		shutdownTimeoutSec = int(opts.ShutdownTimeout / time.Second)
		tokenExpiry        = opts.TokenExpiry.String()
		refreshTokenExpiry = opts.RefreshTokenExpiry.String()
		corsOrigins        = strings.Join(opts.CORSOrigins, ",")
	)

	if len(opts.TokenSigningKey) > 0 {
		jwtSecret = string(opts.TokenSigningKey)
	}

	fs.StringVar(&host, "host", host, "Server host address")
	fs.IntVar(&port, "port", port, "Server port number")
	fs.StringVar(&addr, "addr", "", "Server listen address (overrides host/port)")
	fs.StringVar(&publicAddr, "public-addr", publicAddr, "Publicly reachable base URL")
	fs.StringVar(&dbURL, "db", dbURL, "Database connection URL")
	fs.StringVar(&redisURL, "redis", redisURL, "Redis connection URL")
	fs.StringVar(&jwtSecret, "jwt-secret", jwtSecret, "JWT signing secret")
	fs.IntVar(&readTimeoutSec, "read-timeout", readTimeoutSec, "Read timeout in seconds")
	fs.IntVar(&writeTimeoutSec, "write-timeout", writeTimeoutSec, "Write timeout in seconds")
	fs.IntVar(&idleTimeoutSec, "idle-timeout", idleTimeoutSec, "Idle timeout in seconds")
	fs.IntVar(&shutdownTimeoutSec, "shutdown-timeout", shutdownTimeoutSec, "Graceful shutdown timeout in seconds")
	fs.StringVar(&tokenExpiry, "token-expiry", tokenExpiry, "Access token lifetime")
	fs.StringVar(&refreshTokenExpiry, "refresh-token-expiry", refreshTokenExpiry, "Refresh token lifetime")
	fs.StringVar(&opts.CookieName, "cookie-name", opts.CookieName, "Session cookie name")
	fs.BoolVar(&opts.CookieSecure, "cookie-secure", opts.CookieSecure, "Set Secure flag on session cookies")
	fs.BoolVar(&opts.CookieHTTPOnly, "cookie-http-only", opts.CookieHTTPOnly, "Set HttpOnly flag on session cookies")
	fs.StringVar(&corsOrigins, "cors-origins", corsOrigins, "Comma-separated CORS allowed origins")
	fs.IntVar(&opts.MaxConns, "max-conns", opts.MaxConns, "Maximum concurrent connections")
	fs.BoolVar(&opts.Debug, "debug", opts.Debug, "Enable debug mode")

	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}

	if addr != "" {
		opts.Addr = addr
	} else {
		opts.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	}

	opts.PublicAddr = publicAddr
	opts.DBURL = dbURL
	opts.RedisURL = redisURL
	if jwtSecret != "" {
		opts.TokenSigningKey = []byte(jwtSecret)
	}

	opts.ReadTimeout = time.Duration(readTimeoutSec) * time.Second
	opts.WriteTimeout = time.Duration(writeTimeoutSec) * time.Second
	opts.IdleTimeout = time.Duration(idleTimeoutSec) * time.Second
	opts.ShutdownTimeout = time.Duration(shutdownTimeoutSec) * time.Second

	if tokenExpiry != "" {
		d, err := time.ParseDuration(tokenExpiry)
		if err != nil {
			return Options{}, err
		}
		opts.TokenExpiry = d
	}

	if refreshTokenExpiry != "" {
		d, err := time.ParseDuration(refreshTokenExpiry)
		if err != nil {
			return Options{}, err
		}
		opts.RefreshTokenExpiry = d
	}

	if corsOrigins != "" {
		opts.CORSOrigins = strings.Split(corsOrigins, ",")
	}

	return *opts, nil
}

func applyEnv(opts *auth.ServerOptions) {
	if v := os.Getenv("SCHICK_AUTH_ADDR"); v != "" {
		opts.Addr = v
	}
	if host := os.Getenv("SERVER_HOST"); host != "" {
		port := os.Getenv("SERVER_PORT")
		if port == "" {
			port = "8080"
		}
		opts.Addr = net.JoinHostPort(host, port)
	}
	if v := os.Getenv("SCHICK_AUTH_PUBLIC_ADDR"); v != "" {
		opts.PublicAddr = v
	}
	if v := os.Getenv("DB_URL"); v != "" {
		opts.DBURL = v
	}
	if v := os.Getenv("REDIS_URL"); v != "" {
		opts.RedisURL = v
	}
	if v := os.Getenv("JWT_SECRET"); v != "" {
		opts.TokenSigningKey = []byte(v)
	}
	if v := os.Getenv("SCHICK_AUTH_DEBUG"); v != "" {
		opts.Debug = strings.EqualFold(v, "true") || v == "1"
	}

	setDurationEnv(&opts.ReadTimeout, "SCHICK_AUTH_READ_TIMEOUT")
	setDurationEnv(&opts.WriteTimeout, "SCHICK_AUTH_WRITE_TIMEOUT")
	setDurationEnv(&opts.IdleTimeout, "SCHICK_AUTH_IDLE_TIMEOUT")
	setDurationEnv(&opts.ShutdownTimeout, "SCHICK_AUTH_SHUTDOWN_TIMEOUT")
	setDurationEnv(&opts.TokenExpiry, "JWT_EXPIRATION")
	setDurationEnv(&opts.RefreshTokenExpiry, "SCHICK_AUTH_REFRESH_TOKEN_EXPIRY")
}

func setDurationEnv(target *time.Duration, key string) {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			*target = d
		}
	}
}

func splitAddr(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		if addr == "" {
			return "", 8080, nil
		}
		return "", 0, err
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}

	return host, port, nil
}
