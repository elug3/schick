package checkout

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/ports"
)

func TestNanoHashMatchesDocExampleShape(t *testing.T) {
	// SHA256(ver+loginId+shopcode+reqPayAmt+timestamp+API_KEY+"NANO")
	got := NanoHash("240000005", "shoptest", "240000005", "1004", "1725440123456", "R7L9PxM5V8K2Jc4N6dWqY1Eb3T5XhZU2")
	if len(got) != 64 {
		t.Fatalf("hash len = %d, want 64 hex chars", len(got))
	}
	// Deterministic known value for regression.
	want := NanoHash("240000005", "shoptest", "240000005", "1004", "1725440123456", "R7L9PxM5V8K2Jc4N6dWqY1Eb3T5XhZU2")
	if got != want {
		t.Fatalf("hash not stable")
	}
}

func TestVerifyNanoCallbackHash(t *testing.T) {
	cfg := NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key",
	}
	ts := "1725440123456"
	hash := NanoHash(cfg.Ver, cfg.LoginID, cfg.ShopCode, "70000", ts, cfg.APIKey)
	if !VerifyNanoCallbackHash(cfg, cfg.ShopCode, "70000", ts, hash) {
		t.Fatal("expected valid hash")
	}
	if VerifyNanoCallbackHash(cfg, cfg.ShopCode, "70000", ts, "00"+hash[2:]) {
		t.Fatal("tampered hash must fail")
	}
	if VerifyNanoCallbackHash(cfg, cfg.ShopCode, "70000", "", hash) {
		t.Fatal("missing timestamp must fail closed")
	}
	if VerifyNanoCallbackHash(cfg, cfg.ShopCode, "70000", ts, "") {
		t.Fatal("missing hashValue must fail closed")
	}
	if VerifyNanoCallbackHash(NanoConfig{}, cfg.ShopCode, "70000", ts, hash) {
		t.Fatal("disabled credentials must fail closed")
	}
}

func TestVerifyNanoReturnMAC(t *testing.T) {
	cfg := NanoConfig{
		Ver: "240000005", ShopCode: "240000005", LoginID: "shoptest", APIKey: "test-key",
	}
	now := time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)
	ts := fmt.Sprintf("%d", now.UnixMilli())
	mac := NanoReturnMAC(cfg.Ver, cfg.LoginID, cfg.ShopCode, "pay_1", "70000", ts, cfg.APIKey)
	if !VerifyNanoReturnMAC(cfg, "pay_1", cfg.ShopCode, "70000", ts, mac, now) {
		t.Fatal("expected valid receiveUrl MAC")
	}
	if VerifyNanoReturnMAC(cfg, "pay_2", cfg.ShopCode, "70000", ts, mac, now) {
		t.Fatal("MAC for another payment must fail")
	}
	if VerifyNanoReturnMAC(cfg, "pay_1", cfg.ShopCode, "70000", ts, "00"+mac[2:], now) {
		t.Fatal("tampered MAC must fail")
	}
	old := fmt.Sprintf("%d", now.Add(-21*time.Minute).UnixMilli())
	oldMAC := NanoReturnMAC(cfg.Ver, cfg.LoginID, cfg.ShopCode, "pay_1", "70000", old, cfg.APIKey)
	if VerifyNanoReturnMAC(cfg, "pay_1", cfg.ShopCode, "70000", old, oldMAC, now) {
		t.Fatal("expired MAC must fail")
	}
	if VerifyNanoReturnMAC(cfg, "pay_1", cfg.ShopCode, "70000", "", mac, now) {
		t.Fatal("missing timestamp must fail")
	}
}

func TestNanoProviderCreateSession(t *testing.T) {
	p := NewNanoProvider(NanoConfig{
		BaseURL:       "https://dev3.nanopay.co.kr",
		Ver:           "240000005",
		ShopCode:      "240000005",
		LoginID:       "shoptest",
		APIKey:        "test-key",
		PublicBaseURL: "https://dupli1.com",
	})
	sess, err := p.CreateSession(t.Context(), ports.CheckoutSessionInput{
		OrderID:     "ord_1",
		PaymentID:   "pay_000001",
		AmountCents: 70000,
		OrderName:   "윤라희",
		OrderTel:    "010-4112-5167",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Provider != domain.ProviderNano {
		t.Fatalf("provider = %s", sess.Provider)
	}
	if !strings.HasSuffix(sess.CheckoutURL, "/api/v1/payments/pay_000001/nano/checkout") {
		t.Fatalf("checkout_url = %s", sess.CheckoutURL)
	}
}

func TestNanoProviderRequiresPayer(t *testing.T) {
	p := NewNanoProvider(NanoConfig{
		ShopCode: "240000005", LoginID: "shoptest", APIKey: "k", PublicBaseURL: "http://localhost:8080",
	})
	_, err := p.CreateSession(t.Context(), ports.CheckoutSessionInput{
		PaymentID: "pay_1", AmountCents: 1000, OrderName: "", OrderTel: "01012345678",
	})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestBuildRequest(t *testing.T) {
	p := NewNanoProvider(NanoConfig{
		BaseURL: "https://dev3.nanopay.co.kr", Ver: "240000005", ShopCode: "240000005",
		LoginID: "shoptest", APIKey: "secret", PublicBaseURL: "https://dupli1.com",
	})
	reqURL, body, err := p.BuildRequest("pay_1", "ord_1", "cust_1", "홍길동", "01012345678", "a@b.c", "가방", 1004, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(reqURL, nanoPCRequestPath) {
		t.Fatalf("url = %s", reqURL)
	}
	if body.PayWay != "card" || body.ReqPayAmt != "1004" || body.CompOrderNo != "pay_1" {
		t.Fatalf("body = %+v", body)
	}
	if !strings.HasPrefix(body.ReceiveURL, "https://dupli1.com/api/v1/payments/nano/return?") {
		t.Fatalf("receiveUrl = %s", body.ReceiveURL)
	}
	u, err := url.Parse(body.ReceiveURL)
	if err != nil {
		t.Fatal(err)
	}
	ts := u.Query().Get("nano_ts")
	mac := u.Query().Get("nano_mac")
	if ts != body.Timestamp {
		t.Fatalf("nano_ts = %q, want request timestamp %q", ts, body.Timestamp)
	}
	wantMAC := NanoReturnMAC(body.Ver, body.LoginID, body.ShopCode, body.CompOrderNo, body.ReqPayAmt, ts, "secret")
	if mac != wantMAC {
		t.Fatalf("nano_mac mismatch")
	}
	wantHash := NanoHash(body.Ver, body.LoginID, body.ShopCode, body.ReqPayAmt, body.Timestamp, "secret")
	if body.HashValue != wantHash {
		t.Fatalf("hash mismatch")
	}
	_, bodyM, err := p.BuildRequest("pay_1", "ord_1", "cust_1", "홍길동", "01012345678", "", "가방", 1004, true)
	if err != nil {
		t.Fatal(err)
	}
	if bodyM.OrderTel != "01012345678" {
		t.Fatalf("phone = %s", bodyM.OrderTel)
	}
}

func TestIsMobileUserAgent(t *testing.T) {
	if !IsMobileUserAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)") {
		t.Fatal("iphone should be mobile")
	}
	if IsMobileUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120") {
		t.Fatal("desktop chrome should not be mobile")
	}
}

func TestNormalizeKRPhone(t *testing.T) {
	if got := normalizeKRPhone("010-4112-5167"); got != "01041125167" {
		t.Fatalf("got %s", got)
	}
}

func TestNanoConfigEnabled(t *testing.T) {
	if (NanoConfig{}).Enabled() {
		t.Fatal("empty config must not enable nano")
	}
	if !(NanoConfig{ShopCode: "s", LoginID: "l", APIKey: "k"}).Enabled() {
		t.Fatal("full credentials should enable nano")
	}
	partial := []NanoConfig{
		{ShopCode: "s", LoginID: "l"},
		{ShopCode: "s", APIKey: "k"},
		{LoginID: "l", APIKey: "k"},
		{ShopCode: "  ", LoginID: "l", APIKey: "k"},
	}
	for i, cfg := range partial {
		if cfg.Enabled() {
			t.Fatalf("partial config %d should not enable nano", i)
		}
	}
}

func TestNanoConfigNormalizedBaseURL(t *testing.T) {
	if got := (NanoConfig{}).normalizedBaseURL(); got != nanoDefaultTestBase {
		t.Fatalf("empty base = %q, want sandbox default %q", got, nanoDefaultTestBase)
	}
	prod := NanoConfig{BaseURL: "https://pay.nanopay.co.kr/"}.normalizedBaseURL()
	if prod != "https://pay.nanopay.co.kr" {
		t.Fatalf("prod base = %q", prod)
	}
}
