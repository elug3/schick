package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/elug3/dupli1/payment/pkg/domain"
	"github.com/elug3/dupli1/payment/pkg/infra/checkout"
	"github.com/elug3/dupli1/payment/pkg/ports"
	"github.com/elug3/dupli1/payment/pkg/service"
	"github.com/elug3/dupli1/shared/pkg/authjwt"
	"github.com/elug3/dupli1/shared/pkg/authmiddleware"
	"github.com/elug3/dupli1/shared/pkg/permissions"
	"github.com/elug3/dupli1/shared/pkg/settings"
)

type Handler struct {
	svc          *service.Service
	jwtValidator authjwt.AccessTokenValidator
	nano         *checkout.NanoProvider
	settings     settings.Response
}

func New(svc *service.Service, jwtValidator authjwt.AccessTokenValidator) *Handler {
	return &Handler{
		svc:          svc,
		jwtValidator: jwtValidator,
		settings:     settings.NewResponse("payment"),
	}
}

// WithNano enables NANO certified-payment bridge + return/webhook routes.
func (h *Handler) WithNano(nano *checkout.NanoProvider) *Handler {
	h.nano = nano
	return h
}

// WithSettings sets the non-secret settings payload served by GET /settings.
func (h *Handler) WithSettings(s settings.Response) *Handler {
	h.settings = s
	return h
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/api/v1/payments/health", h.health)
	mux.HandleFunc("/settings", h.settingsHandler)
	mux.HandleFunc("/api/v1/payments/settings", h.settingsHandler)
	mux.HandleFunc("/api/v1/payments", h.requireAuth(h.payments))
	mux.HandleFunc("/api/v1/payments/", h.paymentRoutes)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) settingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	respondJSON(w, http.StatusOK, h.settings)
}

func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return authmiddleware.RequireAuth(h.jwtValidator, respondError)(next)
}

func (h *Handler) payments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	claims, _ := authjwt.FromContext(r.Context())
	var req struct {
		OrderID string `json:"order_id"`
		Method  string `json:"method"`
		Note    string `json:"note"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	bearerToken := ""
	if auth := r.Header.Get("Authorization"); len(auth) > 7 {
		bearerToken = auth[7:]
	}
	payment, err := h.svc.CreatePayment(r.Context(), service.CreatePaymentInput{
		OrderID:           req.OrderID,
		CustomerID:        claims.UserID,
		BearerToken:       bearerToken,
		IdempotencyKey:    r.Header.Get("Idempotency-Key"),
		Method:            req.Method,
		Note:              req.Note,
		CreatedBy:         claims.UserID,
		BypassABAC:        permissions.BypassesPaymentCreateABAC(claims.Permissions),
		AllowMethodBypass: permissions.CanBypassPayment(claims.Permissions),
	})
	if err != nil {
		respondServiceError(w, err)
		return
	}
	respondJSON(w, http.StatusCreated, payment)
}

func (h *Handler) paymentRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/payments/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		respondError(w, http.StatusNotFound, "not found")
		return
	}

	if parts[0] == "nano" && len(parts) == 2 && parts[1] == "return" && r.Method == http.MethodPost {
		h.nanoReturn(w, r)
		return
	}
	if parts[0] == "webhooks" && len(parts) == 2 && parts[1] == "nano" && r.Method == http.MethodPost {
		h.nanoWebhook(w, r)
		return
	}

	if len(parts) == 3 && parts[1] == "nano" && parts[2] == "checkout" && r.Method == http.MethodGet {
		h.nanoCheckout(w, r, parts[0])
		return
	}

	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		h.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			h.cancelPayment(w, r, parts[0])
		})(w, r)
		return
	}

	if len(parts) == 1 && r.Method == http.MethodGet {
		h.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			h.getPayment(w, r, parts[0])
		})(w, r)
		return
	}

	respondError(w, http.StatusNotFound, "not found")
}

func (h *Handler) getPayment(w http.ResponseWriter, r *http.Request, paymentID string) {
	claims, _ := authjwt.FromContext(r.Context())
	ownerID := claims.UserID
	if h.jwtValidator != nil && permissions.BypassesPaymentReadABAC(claims.Permissions) {
		ownerID = ""
	}
	payment, err := h.svc.GetPayment(r.Context(), paymentID, ownerID)
	if err != nil {
		respondServiceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, payment)
}

// cancelPayment cancels (refunds) a captured payment at the PG.
// Staff-only: requires payment.cancel with no ABAC fallback, so a customer can
// never refund their own payment.
func (h *Handler) cancelPayment(w http.ResponseWriter, r *http.Request, paymentID string) {
	claims, _ := authjwt.FromContext(r.Context())
	if h.jwtValidator != nil && !permissions.CanCancelPayment(claims.Permissions) {
		respondError(w, http.StatusForbidden, "forbidden: insufficient permission")
		return
	}

	var req struct {
		// AmountCents omitted or 0 cancels the full remaining balance.
		AmountCents int64  `json:"amount_cents"`
		Reason      string `json:"reason"`
	}
	// An empty body is a valid full cancel.
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AmountCents < 0 {
		respondError(w, http.StatusBadRequest, "amount_cents must not be negative")
		return
	}

	payment, err := h.svc.CancelPayment(r.Context(), service.CancelPaymentInput{
		PaymentID:      paymentID,
		AmountCents:    req.AmountCents,
		Reason:         req.Reason,
		CanceledBy:     claims.UserID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		respondServiceError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, payment)
}

// nanoCheckout bridges the browser into NANO certified checkout (PC or mobile).
// It POSTs a freshly signed request server-side (JSON + API_KEY, per 인증결제
// v2.7) and streams HTML / redirects. The shopper's browser never POSTs to
// NANO's request.io — that would drop the API_KEY and contradict the guide.
//
// It is navigated to directly by the shopper (it is the checkout_url handed to
// the storefront), so like nanoReturn it must answer with a page rather than a
// JSON error body — elug3/dupli1#232 applies here too.
//
// Every failure on this path is safe to retry: the card window has not opened
// yet, so nothing has been charged. That is why the bridge always uses
// nanoReturnCheckoutFailed and never an unconfirmed reason.
//
// An empty 2xx from NANO is a PG-side blip, not something to paper over by
// launching checkout from the browser. Never stream an empty body; send the
// shopper back to the storefront so they can pay again.
func (h *Handler) nanoCheckout(w http.ResponseWriter, r *http.Request, paymentID string) {
	if h.nano == nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	cfg := h.nano.Config()
	payment, err := h.svc.GetPayment(r.Context(), paymentID, "")
	if err != nil {
		log.Printf("payment: nano checkout %s: load payment: %v", paymentID, err)
		h.failNanoReturn(w, r, cfg, "", paymentID, nanoReturnCheckoutFailed)
		return
	}
	if payment.Status != domain.StatusRequiresPayment {
		log.Printf("payment: nano checkout %s: status is %s, not awaiting checkout", payment.ID, payment.Status)
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}
	if payment.Provider != domain.ProviderNano {
		log.Printf("payment: nano checkout %s: provider is %s, not nano", payment.ID, payment.Provider)
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}

	mobile := checkout.IsMobileUserAgent(r.UserAgent())
	reqURL, body, err := h.nano.BuildRequest(
		payment.ID, payment.OrderID, payment.CustomerID,
		payment.PayerName, payment.PayerPhone, payment.PayerEmail,
		"Dupli1 "+payment.OrderID, payment.AmountCents, mobile,
	)
	if err != nil {
		log.Printf("payment: nano checkout %s: build nano request: %v", payment.ID, err)
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}

	payload, err := json.Marshal(body)
	if err != nil {
		log.Printf("payment: nano checkout %s: marshal request: %v", payment.ID, err)
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, reqURL, strings.NewReader(string(payload)))
	if err != nil {
		log.Printf("payment: nano checkout %s: build request: %v", payment.ID, err)
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CharSet", "UTF-8")
	if ua := strings.TrimSpace(r.UserAgent()); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if cfg.APIKey != "" {
		req.Header.Set("API_KEY", cfg.APIKey)
	}

	resp, err := nanoCheckoutHTTPClient(cfg.HTTPClient).Do(req)
	if err != nil {
		log.Printf("payment nano checkout request: %v", err)
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		log.Printf("payment: nano checkout %s: read upstream: %v", payment.ID, err)
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}

	if loc := nanoRedirectLocation(resp); loc != "" {
		http.Redirect(w, r, loc, http.StatusFound)
		return
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") || (len(respBody) > 0 && respBody[0] == '{') {
		var parsed map[string]any
		if err := json.Unmarshal(respBody, &parsed); err == nil {
			for _, key := range []string{"payUrl", "pay_url", "redirectUrl", "redirect_url", "checkoutUrl", "checkout_url", "paymentUrl", "payment_url"} {
				if v, ok := parsed[key].(string); ok && strings.TrimSpace(v) != "" {
					http.Redirect(w, r, v, http.StatusFound)
					return
				}
			}
			if code, _ := parsed["resultCode"].(string); code != "" && code != "0000" {
				msg, _ := parsed["resultMsg"].(string)
				// NANO's resultMsg is an operator diagnostic — it carries their
				// internal identifiers (mbrNo, mbrRefNo) and means nothing to a
				// shopper. Log it, show them a page.
				log.Printf("payment: nano checkout %s rejected: resultCode=%s resultMsg=%s",
					payment.ID, code, truncateForLog(msg, 300))
				h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
				return
			}
			// resultCode 0000 (or no code) without a pay URL is not a payment
			// window — do not dump the JSON as the page.
			log.Printf("payment: nano checkout %s: JSON with no pay URL (resultCode=%v)",
				payment.ID, parsed["resultCode"])
			h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
			return
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("payment nano checkout: status=%d body=%s", resp.StatusCode, truncateForLog(string(respBody), 200))
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		log.Printf("payment: nano checkout %s: empty %d from NANO", payment.ID, resp.StatusCode)
		h.failNanoReturn(w, r, cfg, payment.OrderID, payment.ID, nanoReturnCheckoutFailed)
		return
	}

	if ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)
}

// nanoCheckoutHTTPClient POSTs to NANO without following redirects. The default
// client would consume a 302 to the payment window server-side (no PG cookies),
// then stream whatever empty 200 that hop returned as the shopper's page.
func nanoCheckoutHTTPClient(base *http.Client) *http.Client {
	timeout := 20 * time.Second
	var transport http.RoundTripper
	if base != nil {
		if base.Timeout > 0 {
			timeout = base.Timeout
		}
		transport = base.Transport
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// nanoRedirectLocation returns an absolute Location for a 3xx, resolved against
// the NANO request URL so a relative /pay/… is not applied to dupli1.com.
func nanoRedirectLocation(resp *http.Response) string {
	if resp == nil || resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return ""
	}
	loc := strings.TrimSpace(resp.Header.Get("Location"))
	if loc == "" {
		return ""
	}
	ref, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.ResolveReference(ref).String()
	}
	return loc
}

// Reasons attached to the storefront failure redirect as ?error=.
//
// The storefront splits these into "the shopper may safely pay again" and "the
// PG approved but dupli1 could not confirm it, so never invite a retry" — see
// classifyPaymentReturn in dupli1-web. Any reason emitted for an approved
// callback must be one the storefront recognises as unconfirmed; nanoReturnReason
// is the only place that choice is made, and nanoReturnUnconfirmedReasons is the
// list both sides agree on.
const (
	nanoReturnDeclined       = "declined"
	nanoReturnVerifyFailed   = "verify_failed"
	nanoReturnAmountMismatch = "amount_mismatch"
	nanoReturnInvalidPayload = "invalid_payload"
	// nanoReturnCheckoutFailed is the bridge's reason: NANO never opened a card
	// window, so nothing was charged and paying again is safe. Deliberately not in
	// nanoReturnUnconfirmedReasons.
	nanoReturnCheckoutFailed = "checkout_failed"
)

// nanoReturnUnconfirmedReasons mirrors the storefront's UNCONFIRMED_REASONS set.
var nanoReturnUnconfirmedReasons = map[string]bool{
	nanoReturnVerifyFailed:   true,
	nanoReturnAmountMismatch: true,
	"verification_failed":    true,
	"invalid_payment":        true,
}

func (h *Handler) nanoReturn(w http.ResponseWriter, r *http.Request) {
	if h.nano == nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	cfg := h.nano.Config()
	result, err := parseNanoResult(r)
	if err != nil {
		// Not a NANO callback at all — a scanner, a stale bookmark, a truncated
		// POST. Nothing was approved, so the ordinary failure page is honest; what
		// matters is that a browser never receives a JSON error body (dupli1#232).
		log.Printf("payment: nano return payload unparseable: %v", err)
		h.failNanoReturn(w, r, cfg, "", "", nanoReturnInvalidPayload)
		return
	}
	result.Source = service.NanoSourceReturn
	payment, err := h.svc.HandleNanoResult(r.Context(), nanoCallbackAuth(cfg), result)
	if err != nil {
		orderID, paymentID := nanoReturnIDs(result, err)
		h.failNanoReturn(w, r, cfg, orderID, paymentID, nanoReturnReason(result, err))
		return
	}
	if payment.Status != domain.StatusSucceeded {
		// The PG declined and we recorded it, so the shopper may safely pay again.
		dest := nanoFailureDest(cfg)
		if dest == "" {
			respondNanoReturnPage(w, nanoReturnDeclined, payment.ID)
			return
		}
		http.Redirect(w, r, appendNanoReturnQuery(dest, payment.OrderID, payment.ID, nanoReturnDeclined), http.StatusSeeOther)
		return
	}
	dest := strings.TrimSpace(cfg.SuccessURL)
	if dest == "" {
		// No storefront to return to: an API-only deployment, where the caller is
		// a script reading the payment rather than a browser reading a page.
		respondJSON(w, http.StatusOK, map[string]any{
			"message": "Payment processed",
			"payment": payment,
		})
		return
	}
	http.Redirect(w, r, appendNanoReturnQuery(dest, payment.OrderID, payment.ID, ""), http.StatusSeeOther)
}

// failNanoReturn answers a rejected callback with a page instead of a JSON error
// body (elug3/dupli1#232).
//
// Only FailureURL is acceptable here: a rejected callback means we do not know
// whether the card was charged, and the confirmation page — the fallback the
// recorded-decline path uses — would tell the shopper their order is on its way.
// With no failure page configured we render our own rather than fall back to a
// destination that states the opposite of what we know.
func (h *Handler) failNanoReturn(w http.ResponseWriter, r *http.Request, cfg checkout.NanoConfig, orderID, paymentID, reason string) {
	if dest := strings.TrimSpace(cfg.FailureURL); dest != "" {
		http.Redirect(w, r, appendNanoReturnQuery(dest, orderID, paymentID, reason), http.StatusSeeOther)
		return
	}
	respondNanoReturnPage(w, reason, paymentID)
}

// nanoFailureDest is where a shopper goes after a decline dupli1 recorded — an
// outcome we are sure of, so the confirmation page (which polls the order and
// shows it unpaid) is an acceptable fallback.
func nanoFailureDest(cfg checkout.NanoConfig) string {
	if dest := strings.TrimSpace(cfg.FailureURL); dest != "" {
		return dest
	}
	return strings.TrimSpace(cfg.SuccessURL)
}

// respondNanoReturnPage is the last-resort landing page for a deployment with no
// storefront failure URL. It exists so that "the shopper never sees raw JSON" is
// a property of the code rather than of the configuration.
func respondNanoReturnPage(w http.ResponseWriter, reason, paymentID string) {
	headline, body := "결제가 완료되지 않았습니다.", "다시 시도해 주세요."
	if nanoReturnUnconfirmedReasons[reason] {
		headline = "결제 결과를 확인하지 못했습니다."
		body = "카드가 승인되었을 수 있으니 다시 결제하지 마시고 고객센터로 문의해 주세요."
	}
	ref := ""
	if paymentID != "" {
		ref = fmt.Sprintf(`<p class="ref">문의 번호: %s</p>`, html.EscapeString(paymentID))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 200: the page itself carries the outcome, and a browser must not swap this
	// copy for an error page of its own.
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!doctype html><html lang="ko"><head><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width,initial-scale=1">`+
		`<title>%s</title><style>body{font-family:system-ui,-apple-system,"Apple SD Gothic Neo",sans-serif;`+
		`margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center;padding:24px;color:#111}`+
		`.box{max-width:32rem;text-align:center}h1{font-size:1.25rem;margin:0 0 .75rem}`+
		`p{margin:0 0 .5rem;line-height:1.6}.ref{color:#666;font-size:.875rem}</style></head>`+
		`<body><div class="box"><h1>%s</h1><p>%s</p>%s</div></body></html>`,
		html.EscapeString(headline), html.EscapeString(headline), html.EscapeString(body), ref)
}

// nanoReturnReason picks the shopper-facing classification for a rejected callback.
//
// The safety rule has one direction: if NANO said it approved the card, the
// shopper must never be told they can simply try again, whatever went wrong on
// our side — an unknown payment id, a storage outage, a hash we could not verify.
// Only an actual decline earns the retry wording.
func nanoReturnReason(result service.NanoResult, err error) string {
	if !service.NanoApproved(result) {
		return nanoReturnDeclined
	}
	if rejection, ok := service.NanoRejection(err); ok && rejection.Reason == service.NanoRejectAmountMismatch {
		return nanoReturnAmountMismatch
	}
	return nanoReturnVerifyFailed
}

// nanoReturnIDs recovers what the storefront needs to look the payment up, from
// the rejection when it identified one and from the raw callback otherwise.
func nanoReturnIDs(result service.NanoResult, err error) (orderID, paymentID string) {
	if rejection, ok := service.NanoRejection(err); ok {
		orderID, paymentID = rejection.OrderID, rejection.PaymentID
	}
	if paymentID == "" {
		paymentID = strings.TrimSpace(result.CompOrderNo)
	}
	return orderID, paymentID
}

func (h *Handler) nanoWebhook(w http.ResponseWriter, r *http.Request) {
	if h.nano == nil {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	result, err := parseNanoResult(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid nano webhook payload")
		return
	}
	result.Source = service.NanoSourceWebhook
	if _, err := h.svc.HandleNanoResult(r.Context(), nanoCallbackAuth(h.nano.Config()), result); err != nil {
		respondServiceError(w, err)
		return
	}
	// NANO virtual-account NOTI expects resultCode "00"; card webhook ack is JSON OK.
	respondJSON(w, http.StatusOK, map[string]string{"resultCode": "00"})
}

func nanoCallbackAuth(cfg checkout.NanoConfig) service.NanoCallbackAuth {
	return service.NanoCallbackAuth{
		Ver:      cfg.Ver,
		LoginID:  cfg.LoginID,
		ShopCode: cfg.ShopCode,
		APIKey:   cfg.APIKey,
	}
}

func parseNanoResult(r *http.Request) (service.NanoResult, error) {
	ct := r.Header.Get("Content-Type")
	var result service.NanoResult
	if strings.Contains(ct, "application/json") {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			return result, err
		}
		return result, nil
	}
	if err := r.ParseForm(); err != nil {
		return result, err
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(r.FormValue(k)); v != "" {
				return v
			}
		}
		return ""
	}
	result = service.NanoResult{
		ResultCode:  get("resultCode", "result_code"),
		ResultMsg:   get("resultMsg", "result_msg"),
		ShopCode:    get("shopcode", "shopCode"),
		CompOrderNo: get("compOrderNo", "comp_order_no"),
		ReqPayAmt:   get("reqPayAmt", "req_pay_amt"),
		TranNo:      get("tranNo", "tran_no"),
		PayWay:      get("payWay", "pay_way"),
		Timestamp:   get("timestamp"),
		HashValue:   get("hashValue", "hash_value"),
	}
	if result.CompOrderNo == "" && result.ResultCode == "" {
		return result, fmt.Errorf("empty nano payload")
	}
	return result, nil
}

// appendNanoReturnQuery adds what the storefront reads off a PG return: the ids
// it needs to look the order up, plus the reason it classifies as retryable or
// not. Empty values are omitted so the storefront sees an absent parameter rather
// than a blank one it would have to special-case.
func appendNanoReturnQuery(base, orderID, paymentID, reason string) string {
	params := [][2]string{{"order_id", orderID}, {"payment_id", paymentID}, {"error", reason}}
	u, err := url.Parse(base)
	if err != nil {
		// Unparseable destination: append raw rather than drop the context.
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		var b strings.Builder
		b.WriteString(base)
		for _, p := range params {
			if p[1] == "" {
				continue
			}
			b.WriteString(sep)
			b.WriteString(p[0] + "=" + url.QueryEscape(p[1]))
			sep = "&"
		}
		return b.String()
	}
	q := u.Query()
	for _, p := range params {
		if p[1] == "" {
			continue
		}
		q.Set(p[0], p[1])
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func truncateForLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func respondServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ports.ErrNotFound), errors.Is(err, ports.ErrOrderNotFound):
		respondError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ports.ErrOrderForbidden), errors.Is(err, ports.ErrPaymentForbidden):
		respondError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ports.ErrMethodUnavailable), errors.Is(err, ports.ErrCancelUnsupported):
		respondError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, domain.ErrNotCancelable):
		respondError(w, http.StatusConflict, err.Error())
	case errors.Is(err, domain.ErrCancelAmountInvalid):
		respondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ports.ErrCancelRejected):
		respondError(w, http.StatusBadGateway, err.Error())
	case errors.Is(err, ports.ErrOrderNotPending), errors.Is(err, domain.ErrInvalidPayment):
		respondError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("payment: internal error: %v", err)
		respondError(w, http.StatusInternalServerError, "internal error")
	}
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(target)
}

func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]any{"error": message, "code": status})
}
