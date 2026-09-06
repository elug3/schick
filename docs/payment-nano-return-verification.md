# NANO return verification — blocked on the merchant

**Status:** **Blocked on NANO Solution.** Work is complete and parked, not abandoned —
two PRs are open as drafts. Put on hold **2026-09-06**.

**Owner of the blocker:** whoever can reach NANO merchant support. No amount of
work in this repo resolves it.

---

## The one-paragraph version

Dupli1 refuses a NANO card approval unless the callback carries a `hashValue`
that verifies against `NANO_API_KEY`. The 인증결제 v2.7 merchant guide does not
document `hashValue` on the callback at all — only on the *request*. If the
guide is complete, then **every genuine approval is refused**, which is exactly
what production shows: **0 of 18 NANO payments have ever reached `succeeded`.**
The shopper-facing damage from that refusal is fixed
([#232](https://github.com/elug3/dupli1/issues/232)); the refusal itself cannot
be fixed without NANO telling us how — or whether — the return is signed.

---

## Evidence

### 1. What our code requires

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

## The two possibilities

Only NANO can say which is true.

| | If NANO signs the return | If NANO does not sign the return |
|---|---|---|
| **What it means** | The guide is incomplete; there is an undocumented field or formula | The browser return is unauthenticated by design |
| **The fix** | Implement their formula in `VerifyNanoCallbackHash` | Verification here is unachievable — it must be *replaced*, not relaxed |
| **Effort** | Small — one function, existing tests cover the shape | Larger — needs an out-of-band confirmation before marking `succeeded`: a server-side transaction lookup, or trusting only the server-to-server webhook |
| **Risk if guessed wrong** | Accepting forged approvals — anyone who can POST to `/nano/return` marks orders paid | Same |

**Do not "fix" this by deleting the check.** The return URL is a public,
unauthenticated endpoint. Without verification, a single crafted POST marks any
pending order paid. The current behaviour — refuse, alert, tell the shopper not
to pay again — is the safe failure mode, and it is deliberately loud.

---

## The exact question for NANO

Two things to ask in one conversation, with merchant `shopcode 240000005`
(`loginId shoptest`):

1. **Does the `receiveUrl` callback carry a signature?**
   The v2.7 §2.2 response field list shows no `hashValue` or `timestamp`. If
   the return *is* signed, we need the field name and the exact digest input
   and ordering. If it is **not** signed, we need to know that too — it changes
   the design, not just a constant.

2. **Unexplained Ready-API amount rejection.**
   `## amount ## Ready API결제요청 값과 상이 합니다. [mbrNo=100011, mbrRefNo=260905001496]`
   Our request chain was self-consistent at **31,004 KRW** end to end, and the
   card UI displayed the correct figure. NANO's Ready API accepted every amount
   we tested in isolation (1 → 280,000). **`mbrNo=100011` matches nothing in
   our configuration** — ours is `240000005` / `shoptest` — so we cannot tell
   whose merchant number that is or which request it refers to.

---

## What is already built, and where it is parked

| PR | Repo | State | Contents |
|---|---|---|---|
| [dupli1#233](https://github.com/elug3/dupli1/pull/233) | backend | **merged** 2026-09-05 | Browser return never renders JSON; declined vs unconfirmed classification; `payment.callback_rejected` ops alert |
| [dupli1-web#106](https://github.com/elug3/dupli1-web/pull/106) | storefront | **merged** | The unconfirmed notice itself; retry suppression |
| [dupli1-web#108](https://github.com/elug3/dupli1-web/pull/108) | storefront | **draft — on hold** | Unconfirmed warning shows even when the return carries no `order_id` |

The shopper-facing half is therefore **shipped on the backend and half-shipped on
the storefront**. #108 is green (89 tests, typecheck, build, mutation-checked)
and held only by the pause on this work — it is independent of NANO's answer and
can be merged at any time.

Until #108 lands there is a live gap worth knowing about: the backend omits
`order_id` from the failure redirect on the `unknown_payment`, `shop_mismatch`
and `lookup_failed` paths, and the merged storefront gates the "do not pay
again" warning on that parameter. In those cases the shopper currently sees no
warning at all.

The `?error=` reason vocabulary is a cross-repo contract, pinned on both sides —
`TestNanoReturnUnconfirmedReasonsMatchStorefront` (Go) and
`classifyPaymentReturn` tests (TypeScript). Changing it requires changing both.

---

## Picking this back up

**If NANO gives a signing spec:** implement it inside `VerifyNanoCallbackHash`
and delete the UNRESOLVED note above it. The surrounding call sites, the
alerting, and the shopper-facing paths all stay as they are — they were built to
survive either answer.

**If NANO says the return is unsigned:** `VerifyNanoCallbackHash` cannot be
salvaged. Replace it with confirmation from a source the shopper's browser
cannot forge, and keep `payment.callback_rejected` firing whenever the two
disagree.

**Verifying either answer** needs a real approval to land, which needs the
second blocker below cleared first.

---

## Second, independent blocker: the callback cannot be delivered

`DUPLI1_PAYMENT_PUBLIC_URL=http://localhost:8080` is not resolvable from the
internet, so NANO cannot deliver an approval callback to a local stack at all.
The service warns about this on startup (`CallbackReachable()` rejects loopback,
private, link-local and unset bases).

Clearing it needs a public tunnel — but note that exposing the local Compose
stack also exposes the seeded owner account (`admin@dupli1.com` / `password`),
so change that first or tunnel only the payment service.

**These two blockers are separate.** A reachable URL alone will not make card
payments work while verification rejects every approval; a signing spec alone
cannot be tested until callbacks can arrive.

---

## Related

- Issue: [elug3/dupli1#232](https://github.com/elug3/dupli1/issues/232)
- [payment-service.md](payment-service.md) — the return contract table and event payloads
- [current-state.md](current-state.md) — as-built payment summary
- Code: `payment/pkg/infra/checkout/nano.go` (`VerifyNanoCallbackHash`),
  `payment/pkg/service/nano_callback.go` (rejection reasons + alert),
  `payment/pkg/handler/http.go` (`nanoReturn`, `nanoReturnReason`)
