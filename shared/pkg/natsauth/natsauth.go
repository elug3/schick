// Package natsauth attaches the shared NATS authorization token (NATS_TOKEN)
// to client connections. The broker is started with --auth; without this
// option any TCP client on the bus can publish user.deleted / payment.canceled.
package natsauth

import (
	"os"
	"strings"

	natsgo "github.com/nats-io/nats.go"
)

// TokenFromEnv returns the trimmed NATS_TOKEN, or empty when unset.
func TokenFromEnv() string {
	return strings.TrimSpace(os.Getenv("NATS_TOKEN"))
}

// ConnectOpts prepends nats.Token(NATS_TOKEN) when the env var is set, then
// appends extra. Tests and in-process brokers that do not use --auth omit the
// env var and connect as before.
func ConnectOpts(extra ...natsgo.Option) []natsgo.Option {
	tok := TokenFromEnv()
	if tok == "" {
		return extra
	}
	opts := make([]natsgo.Option, 0, 1+len(extra))
	opts = append(opts, natsgo.Token(tok))
	return append(opts, extra...)
}
