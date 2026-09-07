package checkout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/ports"
)

const (
	nanoPayWayCard        = "card"
	nanoDefaultTestBase   = "https://dev3.nanopay.co.kr"
	nanoDefaultProdBase   = "https://pay.nanopay.co.kr"
	nanoPCRequestPath     = "/api/payment/cert/pc/request.io"
	nanoMobileRequestPath = "/api/payment/cert/mobile/request.io"
	nanoCancelPath        = "/api/payment/cancel.io"
)

// NanoConfig holds NANO Solution certified-payment (인증결제) credentials.
type NanoConfig struct {
	BaseURL       string // https://dev3.nanopay.co.kr or https://pay.nanopay.co.kr
	Ver           string
	ShopCode      string
	LoginID       string
	APIKey        string
	PublicBaseURL string // gateway URL used for receiveUrl + checkout bridge
	SuccessURL    string // storefront redirect after paid
	FailureURL    string // storefront redirect after failed/canceled
	HTTPClient    *http.Client
}

func (c NanoConfig) Enabled() bool {
	return strings.TrimSpace(c.APIKey) != "" &&
		strings.TrimSpace(c.ShopCode) != "" &&
		strings.TrimSpace(c.LoginID) != ""
}

func (c NanoConfig) normalizedBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return nanoDefaultTestBase
	}
	return base
}

func (c NanoConfig) publicBase() string {
	base := strings.TrimRight(strings.TrimSpace(c.PublicBaseURL), "/")
	if base == "" {
		return "http://localhost:8080"
	}
	return base
}

// NanoProvider starts NANO certified card checkout via a Dupli1 bridge URL.
// The bridge POSTs to NANO (PC or mobile) with a fresh timestamp/hash; NANO
// redirects the browser to receiveUrl with the approval form result.
type NanoProvider struct {
	cfg NanoConfig
}

func NewNanoProvider(cfg NanoConfig) *NanoProvider {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if strings.TrimSpace(cfg.Ver) == "" {
		cfg.Ver = cfg.ShopCode
	}
	return &NanoProvider{cfg: cfg}
}

func (p *NanoProvider) Config() NanoConfig {
	return p.cfg
}

func (p *NanoProvider) CreateSession(_ context.Context, input ports.CheckoutSessionInput) (*ports.CheckoutSessionResult, error) {
	if !p.cfg.Enabled() {
		return nil, fmt.Errorf("%w: nano credentials not configured", ports.ErrMethodUnavailable)
	}
	name := strings.TrimSpace(input.OrderName)
	tel := normalizeKRPhone(input.OrderTel)
	if name == "" || tel == "" {
		return nil, fmt.Errorf("%w: order recipient name and phone are required for card payment", domain.ErrInvalidPayment)
	}
	if input.PaymentID == "" || input.AmountCents <= 0 {
		return nil, domain.ErrInvalidPayment
	}

	checkoutURL := fmt.Sprintf("%s/api/v1/payments/%s/nano/checkout", p.cfg.publicBase(), input.PaymentID)
	return &ports.CheckoutSessionResult{
		Provider:    domain.ProviderNano,
		ProviderRef: PlaceholderProviderRef(input.PaymentID),
		CheckoutURL: checkoutURL,
	}, nil
}

// CallbackReachable reports whether the configured public base could plausibly
// be reached by NANO's servers.
//
// receiveUrl is built from this base, and NANO POSTs the approval result to it
// from its own infrastructure. A loopback or unset base resolves to NANO's own
// host, so the callback never arrives: the card window can complete and the
// payment still sits at requires_payment forever, with nothing logged. This
// exists so that state is detected at startup rather than inferred from a pile
// of stranded payments.
func (c NanoConfig) CallbackReachable() bool {
	raw := strings.TrimSpace(c.PublicBaseURL)
	if raw == "" {
		return false // publicBase() falls back to localhost
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
	}
	return true
}

// NanoRequest is the JSON body for NANO cert PC/mobile request.io.
type NanoRequest struct {
	Ver          string `json:"ver"`
	LoginID      string `json:"loginId"`
	ShopCode     string `json:"shopcode"`
	OrderName    string `json:"orderName"`
	OrderTel     string `json:"orderTel"`
	OrderEmail   string `json:"orderEmail,omitempty"`
	PayWay       string `json:"payWay"`
	GoodsName    string `json:"goodsName"`
	ReqPayAmt    string `json:"reqPayAmt"`
	ReceiveURL   string `json:"receiveUrl"`
	CompOrderNo  string `json:"compOrderNo,omitempty"`
	CompOrderMem string `json:"compOrderMem,omitempty"`
	Timestamp    string `json:"timestamp"`
	HashValue    string `json:"hashValue"`
}

// BuildRequest builds a signed NANO cert payment request for the given payment snapshot.
func (p *NanoProvider) BuildRequest(paymentID, orderID, customerID, orderName, orderTel, orderEmail, goodsName string, amountCents int64, mobile bool) (requestURL string, body NanoRequest, err error) {
	if !p.cfg.Enabled() {
		return "", NanoRequest{}, fmt.Errorf("nano credentials not configured")
	}
	name := strings.TrimSpace(orderName)
	tel := normalizeKRPhone(orderTel)
	if name == "" || tel == "" {
		return "", NanoRequest{}, fmt.Errorf("order name and phone required")
	}
	if amountCents <= 0 {
		return "", NanoRequest{}, fmt.Errorf("invalid amount")
	}
	if strings.TrimSpace(goodsName) == "" {
		goodsName = "Dupli1 " + orderID
	}
	amt := fmt.Sprintf("%d", amountCents)
	ts := nanoTimestamp(time.Now())
	ver := strings.TrimSpace(p.cfg.Ver)
	login := strings.TrimSpace(p.cfg.LoginID)
	shop := strings.TrimSpace(p.cfg.ShopCode)
	hash := NanoHash(ver, login, shop, amt, ts, p.cfg.APIKey)
	q := url.Values{}
	q.Set("nano_ts", ts)
	q.Set("nano_mac", NanoReturnMAC(ver, login, shop, paymentID, amt, ts, p.cfg.APIKey))

	path := nanoPCRequestPath
	if mobile {
		path = nanoMobileRequestPath
	}
	body = NanoRequest{
		Ver:          ver,
		LoginID:      login,
		ShopCode:     shop,
		OrderName:    name,
		OrderTel:     tel,
		OrderEmail:   strings.TrimSpace(orderEmail),
		PayWay:       nanoPayWayCard,
		GoodsName:    goodsName,
		ReqPayAmt:    amt,
		ReceiveURL:   p.cfg.publicBase() + "/api/v1/payments/nano/return?" + q.Encode(),
		CompOrderNo:  paymentID,
		CompOrderMem: customerID,
		Timestamp:    ts,
		HashValue:    hash,
	}
	return p.cfg.normalizedBaseURL() + path, body, nil
}

// NanoHash returns SHA256(ver+loginId+shopcode+reqPayAmt+timestamp+API_KEY+"NANO") hex digest.
// Used for outbound cert requests and for verifying a callback hashValue when the
// PG actually sends one. Unsigned v2.7 browser returns use NanoReturnMAC instead.
func NanoHash(ver, loginID, shopCode, reqPayAmt, timestamp, apiKey string) string {
	sum := sha256.Sum256([]byte(ver + loginID + shopCode + reqPayAmt + timestamp + apiKey + "NANO"))
	return hex.EncodeToString(sum[:])
}

// VerifyNanoCallbackHash reports whether hashValue matches the NANO request-style
// hash over callback fields. Used when the PG actually sends timestamp/hashValue
// (not listed on the 인증결제 v2.7 response). Callers must fail closed on false
// unless VerifyNanoReturnMAC also succeeds.
//
// Formula:
//
//	SHA256(ver+loginId+shopcode+reqPayAmt+timestamp+API_KEY+"NANO")
func VerifyNanoCallbackHash(cfg NanoConfig, shopCode, reqPayAmt, timestamp, hashValue string) bool {
	got := strings.TrimSpace(hashValue)
	ts := strings.TrimSpace(timestamp)
	if got == "" || ts == "" || !cfg.Enabled() {
		return false
	}
	ver := strings.TrimSpace(cfg.Ver)
	if ver == "" {
		ver = strings.TrimSpace(cfg.ShopCode)
	}
	want := NanoHash(
		ver,
		strings.TrimSpace(cfg.LoginID),
		strings.TrimSpace(shopCode),
		strings.TrimSpace(reqPayAmt),
		ts,
		cfg.APIKey,
	)
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(got)), []byte(strings.ToLower(want))) == 1
}

// nanoReturnMACTTL is how long a receiveUrl binding stays valid. The unpaid
// order TTL is 5 minutes; this is looser so a slow card window still returns.
const nanoReturnMACTTL = 20 * time.Minute

// NanoReturnMAC binds receiveUrl to one payment. 인증결제 v2.7 does not sign
// the browser return, so we put this digest (and its timestamp) on the query
// string NANO POSTs back to. It includes compOrderNo so a MAC for one pending
// payment cannot approve another of the same amount.
//
//	SHA256(ver+loginId+shopcode+compOrderNo+reqPayAmt+timestamp+API_KEY+"RETURN")
func NanoReturnMAC(ver, loginID, shopCode, paymentID, reqPayAmt, timestamp, apiKey string) string {
	sum := sha256.Sum256([]byte(ver + loginID + shopCode + paymentID + reqPayAmt + timestamp + apiKey + "RETURN"))
	return hex.EncodeToString(sum[:])
}

// VerifyNanoReturnMAC reports whether the receiveUrl query binding matches this
// payment. now is the verification clock (tests pass a frozen time).
func VerifyNanoReturnMAC(cfg NanoConfig, paymentID, shopCode, reqPayAmt, timestamp, mac string, now time.Time) bool {
	got := strings.TrimSpace(mac)
	ts := strings.TrimSpace(timestamp)
	if got == "" || ts == "" || !cfg.Enabled() {
		return false
	}
	if !nanoTimestampFresh(ts, now) {
		return false
	}
	ver := strings.TrimSpace(cfg.Ver)
	if ver == "" {
		ver = strings.TrimSpace(cfg.ShopCode)
	}
	want := NanoReturnMAC(
		ver,
		strings.TrimSpace(cfg.LoginID),
		strings.TrimSpace(shopCode),
		strings.TrimSpace(paymentID),
		strings.TrimSpace(reqPayAmt),
		ts,
		cfg.APIKey,
	)
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(got)), []byte(strings.ToLower(want))) == 1
}

func nanoTimestampFresh(ts string, now time.Time) bool {
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || ms <= 0 {
		return false
	}
	issued := time.UnixMilli(ms).UTC()
	now = now.UTC()
	if issued.After(now.Add(2 * time.Minute)) {
		return false
	}
	return now.Sub(issued) <= nanoReturnMACTTL
}

func nanoTimestamp(now time.Time) string {
	// Docs: unix seconds (UTC) concatenated with 3-digit millis — equivalent to UnixMilli.
	return fmt.Sprintf("%d", now.UTC().UnixMilli())
}

func normalizeKRPhone(phone string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(phone) {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IsMobileUserAgent reports whether the client should use the mobile cert endpoint.
func IsMobileUserAgent(ua string) bool {
	ua = strings.ToLower(ua)
	for _, needle := range []string{"iphone", "ipod", "ipad", "android", "mobile", "blackberry", "windows phone"} {
		if strings.Contains(ua, needle) {
			return true
		}
	}
	return false
}

// nanoProviderRefPrefix marks a provider_ref that is still the placeholder
// written at checkout, before NANO's real tranNo arrives on the approval
// callback.
const nanoProviderRefPrefix = "nano_"

// PlaceholderProviderRef is the provider_ref stored at checkout, standing in
// until the approval callback replaces it with NANO's tranNo.
func PlaceholderProviderRef(paymentID string) string {
	return nanoProviderRefPrefix + paymentID
}

// IsPlaceholderProviderRef reports whether ref is still the checkout-time
// placeholder rather than a real NANO tranNo.
func IsPlaceholderProviderRef(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), nanoProviderRefPrefix)
}

// NanoCancelRequest is the JSON body for NANO payment cancel (결제취소).
//
// Source: [NANO] 수기결제 연동 API 안내 v2.5 §3 (POST /api/payment/cancel.io).
// The certified-payment guide (인증결제 v2.7 §4 취소) has no cancel body of its
// own — it defers to this one, so cert-approved card payments are canceled
// through the same endpoint.
//
// Unlike the cert request this carries no timestamp and no hashValue: the
// cancel endpoint authenticates with the API_KEY HTTP header instead (수기결제
// v2.5 §1). It also carries no encData, so none of that guide's AES-256-CBC
// card encryption applies here.
type NanoCancelRequest struct {
	Ver         string `json:"ver,omitempty"`
	LoginID     string `json:"loginId"`
	ShopCode    string `json:"shopcode"`
	PayMethod   string `json:"payMethod,omitempty"`
	CancelAmt   string `json:"cancelAmt"`
	TranNo      string `json:"tranNo"`
	CompOrderNo string `json:"compOrderNo,omitempty"`
}

// NanoCancelResponse is the NANO cancel result. remainAmt was added in 수기결제
// v2.5 alongside partial cancel; it is absent from that guide's own example
// response, so it is treated as optional and parsed defensively.
type NanoCancelResponse struct {
	ResultCode  string `json:"resultCode"`
	ResultMsg   string `json:"resultMsg"`
	ShopCode    string `json:"shopcode"`
	CancelDate  string `json:"cancelDate"`
	CancelTime  string `json:"cancelTime"`
	CancelAmt   string `json:"cancelAmt"`
	ApprTranNo  string `json:"apprTranNo"`
	RemainAmt   string `json:"remainAmt"`
	CompOrderNo string `json:"compOrderNo"`
}

// nanoResultCodeSuccess is returned in resultCode for an accepted request.
const nanoResultCodeSuccess = "0000"

// BuildCancelRequest builds the signed-by-header cancel body for a captured
// payment. tranNo is the original approval's 거래번호.
func (p *NanoProvider) BuildCancelRequest(tranNo, paymentID string, amountCents int64) (requestURL string, body NanoCancelRequest, err error) {
	if !p.cfg.Enabled() {
		return "", NanoCancelRequest{}, fmt.Errorf("%w: nano credentials not configured", ports.ErrCancelUnsupported)
	}
	tranNo = strings.TrimSpace(tranNo)
	if tranNo == "" || IsPlaceholderProviderRef(tranNo) {
		// The approval callback never delivered a tranNo, so there is nothing to
		// address the cancel to. Sending the placeholder would earn a confusing
		// rejection from NANO; refuse locally and point ops at the console.
		return "", NanoCancelRequest{}, fmt.Errorf(
			"%w: payment %s has no nano tranNo (approval callback never recorded one); refund via the NANO console",
			ports.ErrCancelUnsupported, paymentID,
		)
	}
	if amountCents <= 0 {
		return "", NanoCancelRequest{}, domain.ErrCancelAmountInvalid
	}
	body = NanoCancelRequest{
		Ver:         strings.TrimSpace(p.cfg.Ver),
		LoginID:     strings.TrimSpace(p.cfg.LoginID),
		ShopCode:    strings.TrimSpace(p.cfg.ShopCode),
		CancelAmt:   fmt.Sprintf("%d", amountCents),
		TranNo:      tranNo,
		CompOrderNo: strings.TrimSpace(paymentID),
	}
	return p.cfg.normalizedBaseURL() + nanoCancelPath, body, nil
}

// CancelPayment cancels a captured NANO card payment. Any outcome other than a
// parsed resultCode of 0000 returns an error and leaves the caller's state
// untouched, so a payment is never recorded as refunded on an unconfirmed call.
func (p *NanoProvider) CancelPayment(ctx context.Context, input ports.CancelPaymentInput) (*ports.CancelPaymentResult, error) {
	reqURL, body, err := p.BuildCancelRequest(input.ProviderRef, input.PaymentID, input.AmountCents)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal nano cancel: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build nano cancel request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CharSet", "UTF-8")
	req.Header.Set("API_KEY", p.cfg.APIKey)

	client := p.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nano cancel request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("nano cancel read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: nano cancel http %d", ports.ErrCancelRejected, resp.StatusCode)
	}

	var parsed NanoCancelResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("%w: nano cancel response is not JSON", ports.ErrCancelRejected)
	}
	if strings.TrimSpace(parsed.ResultCode) != nanoResultCodeSuccess {
		msg := strings.TrimSpace(parsed.ResultMsg)
		if msg == "" {
			msg = "resultCode " + strings.TrimSpace(parsed.ResultCode)
		}
		return nil, fmt.Errorf("%w: %s", ports.ErrCancelRejected, msg)
	}

	// NANO echoes the amount it actually canceled; trust it over the request
	// when both are present so a provider-side adjustment is not lost.
	canceled := input.AmountCents
	if v, ok := parseNanoAmount(parsed.CancelAmt); ok {
		canceled = v
	}
	result := &ports.CancelPaymentResult{
		CanceledAmountCents: canceled,
		ProviderRef:         strings.TrimSpace(parsed.ApprTranNo),
		CanceledAt:          strings.TrimSpace(parsed.CancelDate + parsed.CancelTime),
	}
	if v, ok := parseNanoAmount(parsed.RemainAmt); ok {
		result.RemainingCents = v
		result.RemainingKnown = true
	}
	return result, nil
}

// parseNanoAmount parses a NANO amount string (whole KRW). It reports false for
// an empty or non-numeric field so optional amounts can be distinguished from a
// genuine zero.
func parseNanoAmount(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}
