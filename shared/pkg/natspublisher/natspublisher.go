// Package natspublisher is a shared NATS event publisher: connect once,
// JSON-marshal each event, publish, and flush with a bounded deadline.
//
// Every service previously carried its own ~40-line copy of this wrapper
// around nats.go; auth, order, and product's copies were byte-identical,
// and payment's was a slightly older variant missing the DefaultURL
// fallback, the pre-flight ctx.Err() check, and the flush-timeout guard
// that avoids double-timing-out when the caller's context already has a
// deadline.
package natspublisher

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/elug3/dupli1/shared/pkg/natsauth"
	natsgo "github.com/nats-io/nats.go"
)

const defaultFlushTimeout = 5 * time.Second

// Publisher publishes JSON-encoded events to NATS subjects.
type Publisher struct {
	conn *natsgo.Conn
}

// New connects to NATS and returns an event publisher.
func New(url string, opts ...natsgo.Option) (*Publisher, error) {
	if url == "" {
		url = natsgo.DefaultURL
	}

	conn, err := natsgo.Connect(url, natsauth.ConnectOpts(opts...)...)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}

	return &Publisher{conn: conn}, nil
}

// Publish marshals event as JSON and publishes it to subject.
func (p *Publisher) Publish(ctx context.Context, subject string, event any) error {
	if p == nil || p.conn == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if err := p.conn.Publish(subject, payload); err != nil {
		return fmt.Errorf("publish nats event: %w", err)
	}
	flushCtx, cancel := flushContext(ctx)
	defer cancel()
	if err := p.conn.FlushWithContext(flushCtx); err != nil {
		return fmt.Errorf("flush nats event: %w", err)
	}

	return nil
}

// flushContext returns a context suitable for FlushWithContext, which requires a deadline.
func flushContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultFlushTimeout)
}

// Close closes the NATS connection.
func (p *Publisher) Close() {
	if p != nil && p.conn != nil {
		p.conn.Close()
	}
}
