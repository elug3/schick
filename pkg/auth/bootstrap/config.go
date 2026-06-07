package bootstrap

import "time"

// Config holds the dependencies required to wire the auth service.
type Config struct {
	DBURL              string
	RedisURL           string
	TokenSigningKey    []byte
	TokenExpiry        time.Duration
	RefreshTokenExpiry time.Duration
	Debug              bool
	MaxConns           int
}
