// Package nats implements ports.EventSubscriber for the profile service:
// connect once, subscribe handlers to subjects, and log (never propagate)
// handler errors — core NATS does not redeliver, so a dropped message is
// only ever visible in the logs.
package nats

import (
	"context"
	"fmt"
	"log"
	"sync"

	natsgo "github.com/nats-io/nats.go"

	"github.com/elug3/dupli1/profile/pkg/ports"
	"github.com/elug3/dupli1/shared/pkg/natsauth"
)

// Subscriber listens to NATS subjects and dispatches messages to handlers.
type Subscriber struct {
	conn      *natsgo.Conn
	subs      []*natsgo.Subscription
	closeOnce sync.Once
}

// NewSubscriber connects to NATS and returns an event subscriber.
func NewSubscriber(url string, opts ...natsgo.Option) (*Subscriber, error) {
	if url == "" {
		url = natsgo.DefaultURL
	}

	conn, err := natsgo.Connect(url, natsauth.ConnectOpts(opts...)...)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}

	return &Subscriber{conn: conn}, nil
}

// Subscribe registers a handler for a subject.
func (s *Subscriber) Subscribe(ctx context.Context, subject string, handler ports.MessageHandler) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("nats subscriber not initialized")
	}

	sub, err := s.conn.Subscribe(subject, func(msg *natsgo.Msg) {
		dispatch(ctx, handler, msg.Subject, msg.Data)
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subject, err)
	}

	s.subs = append(s.subs, sub)
	return nil
}

func dispatch(ctx context.Context, handler ports.MessageHandler, subject string, data []byte) {
	if handler == nil {
		return
	}
	if err := handler(ctx, subject, data); err != nil {
		log.Printf("profile nats handler subject=%s error=%v", subject, err)
	}
}

// Close drains and closes the NATS connection.
func (s *Subscriber) Close() {
	s.closeOnce.Do(func() {
		if s == nil {
			return
		}
		for _, sub := range s.subs {
			_ = sub.Unsubscribe()
		}
		if s.conn != nil {
			_ = s.conn.Drain()
			s.conn.Close()
		}
	})
}
