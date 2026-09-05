package nats

import (
	"context"
	"fmt"
	"log"
	"sync"

	natsgo "github.com/nats-io/nats.go"

	"github.com/elug3/dupli1/order/pkg/ports"
	"github.com/elug3/dupli1/shared/pkg/natsauth"
)

// DefaultQueueGroup is the NATS queue group for order service consumers so
// multiple replicas share work instead of each receiving every message.
const DefaultQueueGroup = "order-workers"

// Subscriber listens to NATS subjects.
type Subscriber struct {
	conn      *natsgo.Conn
	subs      []*natsgo.Subscription
	closeOnce sync.Once
	queue     string
}

func NewSubscriber(url string, opts ...natsgo.Option) (*Subscriber, error) {
	if url == "" {
		url = natsgo.DefaultURL
	}
	conn, err := natsgo.Connect(url, natsauth.ConnectOpts(opts...)...)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	return &Subscriber{conn: conn, queue: DefaultQueueGroup}, nil
}

func (s *Subscriber) Subscribe(ctx context.Context, subject string, handler ports.MessageHandler) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("nats subscriber not initialized")
	}
	queue := s.queue
	if queue == "" {
		queue = DefaultQueueGroup
	}
	sub, err := s.conn.QueueSubscribe(subject, queue, func(msg *natsgo.Msg) {
		if handler == nil {
			return
		}
		if err := handler(ctx, msg.Subject, msg.Data); err != nil {
			log.Printf("order nats handler subject=%s error=%v", msg.Subject, err)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subject, err)
	}
	s.subs = append(s.subs, sub)
	return nil
}

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
