# NANO return verification

**Status:** **Fixed in-repo (receiveUrl MAC).** 인증결제 v2.7 still does not sign the
browser return. Dupli1 binds `receiveUrl` with a payment-scoped HMAC instead of
waiting for a PG-side formula.

---

## The one-paragraph version

The v2.7 response field list has no `hashValue` or `timestamp`. Requiring those
on the callback refused every genuine approval (**0 of 18** NANO payments ever
reached `succeeded`). Checkout now sets

`receiveUrl = …/nano/return?nano_ts=…&nano_mac=…`

where `nano_mac` is `SHA256(ver+loginId+shopcode+compOrderNo+reqPayAmt+timestamp+API_KEY+"RETURN")`.
A `resultCode=0000` return is accepted if that MAC verifies **or** a PG-supplied
`hashValue` verifies with the request-style formula. A POST without either still
fails closed (`verify_failed` + `payment.callback_rejected`).

---

## Evidence

### 1. What our code required (before the MAC)

`VerifyNanoCallbackHash` in [`payment/pkg/infra/checkout/nano.go`](../payment/pkg/infra/checkout/nano.go)
fails closed when **either** field is missing:

```go
got := strings.TrimSpace(hashValue)
ts := strings.TrimSpace(timestamp)
if got == "" || ts == "" || !cfg.Enabled() {
    return false
}
```

The digest it expects is the *request* formula:

```
SHA256(ver + loginId + shopcode + reqPayAmt + timestamp + API_KEY + "NANO")
```

`HandleNanoResult` calls it only for `resultCode=0000`, and treats `false` as a
hard reject. That is the correct posture — an unverified approval must not be
taken on the PG's word — but it presumes the fields exist.

### 2. What the merchant guide documents

In 인증결제 v2.7, `hashValue` and `timestamp` appear **only in request tables**:
§2.1 (PC request) and §3.3 (mobile request).

The §2.2 **response** field list is:

```
resultCode  resultMsg   shopcode    compOrderNo  compOrderMem  goodsName
reqPayAmt   orderName   orderTel    orderEmail   tranNo        apprDate
apprTime    apprNo      payWay      receiveUrl   cardNo        cardSrc
cardcode    installment
+ vbank/dbank fields (vactBankCode, vactBankNm, vactNm, vactNo, expDate,
  expTime, acctBankCode, cshrApprNo, cshrResult, cshrType)
```

**Neither `hashValue` nor `timestamp` is in it.** This is stronger than "the
formula might differ" — on the documented contract the fields are not sent at
all, so no formula could succeed.

### 3. What production shows

```
 provider |      status      | count
----------+------------------+-------
 bypass   | canceled         |     2
 bypass   | succeeded        |     4
 nano     | failed           |     3
 nano     | requires_payment |    15
```

**Zero** NANO payments have ever reached `succeeded`. Every payment that has
ever succeeded in this system went through manager Bypass. The 15
`requires_payment` rows are approvals whose callback never landed or never
verified; their orders were auto-canceled by the 5-minute expiry worker.

> The `nano/failed` count includes two rows marked failed by decline-path
> testing on 2026-09-05, so read it as "not a measure of real declines". The
> figure that matters is the zero.

---

## What we implemented instead of waiting

v2.7 documents an unsigned return. Deleting the check would let anyone POST to
`/nano/return` and mark a pending order paid. The replacement is a MAC **we**
issue on `receiveUrl`, not a guess at NANO's formula:

- `nano_mac` includes `compOrderNo`, so a MAC for one payment cannot approve
  another of the same amount.
- `nano_ts` must be within 20 minutes.
- Query params are read from the request URL only (`json:"-"` / not from the
  form or JSON body), so a crafted webhook cannot forge them.
- If NANO later sends a verifying `hashValue`, that path still works.

A leftover independent issue: `DUPLI1_PAYMENT_PUBLIC_URL` must be
internet-reachable or NANO cannot deliver the form POST at all (local Compose
warns on startup).

The Ready-API amount rejection (`mbrNo=100011`) is a separate PG-side question
and is not required for return verification.

---

## What is already built, and where it is parked

| PR | Repo | State | Contents |
|---|---|---|---|
| [dupli1#233](https://github.com/elug3/dupli1/pull/233) | backend | **merged** 2026-09-05 | Browser return never renders JSON; declined vs unconfirmed classification; `payment.callback_rejected` ops alert |
| [dupli1-web#106](https://github.com/elug3/dupli1-web/pull/106) | storefront | **merged** | The unconfirmed notice itself; retry suppression |
| [dupli1-web#108](https://github.com/elug3/dupli1-web/pull/108) | storefront | **draft — on hold** | Unconfirmed warning shows even when the return carries no `order_id` |

Until #108 lands there is a live gap worth knowing about: the backend omits
`order_id` from the failure redirect on the `unknown_payment`, `shop_mismatch`
and `lookup_failed` paths, and the merged storefront gates the "do not pay
again" warning on that parameter. In those cases the shopper currently sees no
warning at all.

The `?error=` reason vocabulary is a cross-repo contract, pinned on both sides —
`TestNanoReturnUnconfirmedReasonsMatchStorefront` (Go) and
`classifyPaymentReturn` tests (TypeScript). Changing it requires changing both.

---

## Second, independent blocker: the callback cannot be delivered

`DUPLI1_PAYMENT_PUBLIC_URL=http://localhost:8080` is not resolvable from the
internet, so NANO cannot deliver an approval callback to a local stack at all.
The service warns about this on startup (`CallbackReachable()` rejects loopback,
private, link-local and unset bases).

Clearing it needs a public tunnel — but note that exposing the local Compose
stack also exposes the seeded owner account (`admin@dupli1.com` / `password`),
so change that first or tunnel only the payment service.

Local Compose still cannot receive a real NANO form POST (`CallbackReachable()`
rejects loopback). Production `DUPLI1_PAYMENT_PUBLIC_URL=https://dupli1.com` is
reachable. A tunnel is only needed to exercise the return locally.

---

## Related

- Issue: [elug3/dupli1#232](https://github.com/elug3/dupli1/issues/232)
- [payment-service.md](payment-service.md) — the return contract table and event payloads
- [current-state.md](current-state.md) — as-built payment summary
- Code: `payment/pkg/infra/checkout/nano.go` (`VerifyNanoCallbackHash`, `NanoReturnMAC`),
  `payment/pkg/service/nano_callback.go` (rejection reasons + alert),
  `payment/pkg/handler/http.go` (`nanoReturn`, `nanoReturnReason`)
