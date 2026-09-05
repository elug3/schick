package natsauth

import (
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
)

func TestConnectOpts_EmptyWithoutToken(t *testing.T) {
	t.Setenv("NATS_TOKEN", "")
	if got := ConnectOpts(); len(got) != 0 {
		t.Fatalf("ConnectOpts() = %d options, want 0 when NATS_TOKEN is empty", len(got))
	}
}

func TestTokenFromEnv_Trims(t *testing.T) {
	t.Setenv("NATS_TOKEN", "  secret-token  ")
	if TokenFromEnv() != "secret-token" {
		t.Fatalf("TokenFromEnv() = %q, want secret-token", TokenFromEnv())
	}
}

func TestConnectOpts_IncludesTokenAndExtra(t *testing.T) {
	t.Setenv("NATS_TOKEN", "tok")
	extra := func(*natsgo.Options) error { return nil }
	opts := ConnectOpts(extra)
	if len(opts) != 2 {
		t.Fatalf("ConnectOpts(extra) = %d options, want token + extra", len(opts))
	}
}

func TestConnectOpts_RejectsAnonymousWhenBrokerRequiresToken(t *testing.T) {
	const token = "bus-secret"
	s := startTokenServer(t, token)
	url := s.ClientURL()
	quick := []natsgo.Option{natsgo.MaxReconnects(0), natsgo.Timeout(time.Second)}

	t.Setenv("NATS_TOKEN", "")
	if _, err := natsgo.Connect(url, ConnectOpts(quick...)...); err == nil {
		t.Fatal("anonymous connect succeeded against a --auth broker")
	}

	t.Setenv("NATS_TOKEN", "wrong-token")
	if _, err := natsgo.Connect(url, ConnectOpts(quick...)...); err == nil {
		t.Fatal("wrong-token connect succeeded against a --auth broker")
	}

	t.Setenv("NATS_TOKEN", token)
	nc, err := natsgo.Connect(url, ConnectOpts(quick...)...)
	if err != nil {
		t.Fatalf("token connect: %v", err)
	}
	defer nc.Close()
	if err := nc.Publish("user.deleted", []byte(`{"user_id":"x"}`)); err != nil {
		t.Fatalf("authenticated publish user.deleted: %v", err)
	}
}

func startTokenServer(t *testing.T, token string) *natsserver.Server {
	t.Helper()
	s, err := natsserver.NewServer(&natsserver.Options{
		Host:          "127.0.0.1",
		Port:          -1,
		Authorization: token,
		NoLog:         true,
		NoSigs:        true,
	})
	if err != nil {
		t.Fatalf("nats-server: %v", err)
	}
	go s.Start()
	t.Cleanup(s.Shutdown)
	if !s.ReadyForConnections(2 * time.Second) {
		t.Fatal("nats-server not ready")
	}
	return s
}
