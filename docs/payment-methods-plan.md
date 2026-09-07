# Plan: Payment Methods

**Status:** Design + Bypass + **NANO certified card** implemented. Bitcoin is still spec-only (do not implement yet).

**Update:** the **dev simulate** provider/endpoint described throughout this plan (provider `dev`, `GET .../simulate-success`) was **removed** and merged into Bypass — local/dev `credit_card` testing without NANO now uses `method=bypass` like everywhere else. Treat every `dev`/`simulate` mention below as historical design context, not current behavior; see [payment-service.md](payment-service.md) for as-built.

**Related:** [payment-service.md](payment-service.md), [permissions.md](permissions.md), [checkout-session.md](checkout-session.md), [auth-profile-extension-plan.md](auth-profile-extension-plan.md), [current-state.md](current-state.md).

## Goals

Offer three payment methods for pending orders, with a single confirmation path into the existing order state machine:

| Method | Who can use it | Status |
|--------|----------------|--------|
| **Credit card** | Customer (own order) or `payment.create` | **NANO Solution 인증결제** when `NANO_*` configured; else local simulate |
| **Bypass** | Order manager only (`payment.bypass`) | **Implemented** — mark paid without a PG (**v1.0 launch path**) |
| **Bitcoin** | Customer (own order) | **Planned — do not implement yet** |

All successful methods must end the same way: payment record → **`succeeded`** → NATS **`payment.succeeded`** → order **`pending` → `paid`**. Order managers still ship via `POST /orders/{id}/ship`.

## Non-goals (this plan)

- Handling card PAN / CVC / card passwords on Dupli1 (NANO-hosted certified window only; do not use 수기결제)
- Refunds / chargebacks UI (still payment phase 2 in [payment-service.md](payment-service.md))
- Multi-currency storefront (KRW only stays locked)
- Implementing Bitcoin rails, wallets, or FX settlement in this phase
- Replacing the 5-minute unpaid TTL for card/bypass (Bitcoin will need its own TTL story later)

## Current state

| Piece | Today |
|-------|--------|
| Create payment | `POST /api/v1/payments` with `{ "order_id", "method" }` |
| Card PG | **NANO** certified adapter (`infra/checkout/nano.go`) |
| Provider | `nano` \| `dev` (local simulate) \| `bypass` |
| Checkout | NANO when credentials set; else DevProvider when `PAYMENT_ALLOW_DEV_SIMULATE`; else UnavailableProvider |
| Permissions | `payment.create`, `payment.read.all`, `payment.bypass` |
| Order paid | Only via `payment.succeeded` consumer (not manual `PUT …/status`) |
| Naming collision | Service input `BypassABAC` means “skip customer ownership check” — **not** the Bypass payment method |

## Design summary

```text
POST /api/v1/payments
  { "order_id": "ord_…", "method": "credit_card" | "bypass" | "bitcoin" }

                    ┌─────────────────┐
  credit_card  ───► │ NANO cert PG    │ ── receiveUrl/webhook ──► HandleNanoResult
                    │ or Dev simulate │     else 501 Unavailable
                    └─────────────────┘
                    ┌─────────────────┐
  bypass       ───► │ Immediate mark  │ ──► CompletePayment (no PG)
                    │ succeeded       │     requires payment.bypass
                    └─────────────────┘
                    ┌─────────────────┐
  bitcoin      ───► │ (future)        │ ── confirm ──► CompletePayment
                    │ invoice / QR    │     DO NOT BUILD YET
                    └─────────────────┘
                              │
                              ▼
                     payment.succeeded
                              │
                              ▼
                     order pending → paid
```

Default when `method` is omitted: **`credit_card`** (NANO when configured; else local simulate; else 501).

---

## Method catalog

### 1. Credit card (`credit_card`)

**Status:** **NANO Solution 인증결제** (PC/mobile `payWay=card`). Local simulate when NANO unset.

| Topic | Choice |
|-------|--------|
| API `method` | `credit_card` (omit → this) |
| Provider value | `nano` (or `dev` for local simulate) |
| UI | Storefront “credit card” → redirect to `checkout_url` (NANO bridge or simulate) |
| Auth | Own order (ABAC) or `payment.create` |
| Payer fields | Order snapshot `recipient_name` / `recipient_phone` (required for NANO) |
| Completion | NANO `receiveUrl` form + optional JSON webhook → `HandleNanoResult`; local `simulate-success` when NANO unset |
| TTL | Existing 5-minute unpaid window |
| Currency | KRW only (`amount_cents` = whole won) |

**API shape (NANO):**

```json
// Request
{ "order_id": "ord_000001", "method": "credit_card" }

// Response
{
  "id": "pay_000001",
  "order_id": "ord_000001",
  "method": "credit_card",
  "amount_cents": 70000,
  "currency": "krw",
  "status": "requires_payment",
  "provider": "nano",
  "checkout_url": "https://dupli1.com/api/v1/payments/pay_000001/nano/checkout",
  "expires_at": "..."
}
```

---

### 2. Bypass (`bypass`) — order manager only

**Status:** Implemented.

Staff mark a pending order as paid **without** collecting money through a PG. Use cases: cash / bank transfer recorded offline, VIP / comps, ops corrections. This is **not** a storefront option and **not** the same as `BypassABAC` / `payment.create`.

| Topic | Choice |
|-------|--------|
| API `method` | `bypass` |
| Provider value | `bypass` |
| Auth | **`payment.bypass` required** (fail closed). ABAC on `customer_id` does **not** apply — managers act on any pending order. Holders of only `payment.create` **cannot** use bypass. |
| Who gets the permission | Order-manager bundle / legacy `order_manager` expansion; also `*` / `admin.*` |
| Completion | Synchronous: create payment → `succeeded` → publish `payment.succeeded` in the same request (no `checkout_url`) |
| TTL | Payment may still set `expires_at` for consistency, but status is already `succeeded` so the expiry worker must ignore succeeded rows (already true today) |
| Audit | Persist `created_by` (JWT `sub`) and optional `note` on the payment row |
| Response | No redirect; client polls order → `paid` |

**Request / response**

```json
// Request (manage-web)
{
  "order_id": "ord_000001",
  "method": "bypass",
  "note": "Cash received at showroom"
}

// Response
{
  "id": "pay_000002",
  "order_id": "ord_000001",
  "method": "bypass",
  "amount_cents": 70000,
  "currency": "krw",
  "status": "succeeded",
  "provider": "bypass",
  "provider_ref": "bypass_<payment_id>",
  "created_by": "usr_manager_…",
  "note": "Cash received at showroom",
  "expires_at": "...",
  "created_at": "...",
  "updated_at": "..."
}
```

**Guards**

1. Order must be `pending` (same as card).
2. Amount always taken from order `total_cents` — never from the request body.
3. Idempotency: reuse existing `Idempotency-Key` header behavior; a second bypass for an already-paid order fails with order-not-pending.
4. Reject `method=bypass` from storefront tokens (empty permissions / no `payment.bypass`) with **403**.
5. Do **not** expose Bypass in public `GET /api/v1/payments/settings` customer-facing method list; settings may list it under a manager-only flag or omit it until manage-web needs it.

**Permission additions**

| Permission | Description |
|------------|-------------|
| `payment.bypass` | Create a succeeded Bypass payment for any pending order |

Add to `shared/pkg/permissions` catalog, fulfillment / order-manager bundles, and [permissions.md](permissions.md). Expand legacy `order_manager` to include `payment.bypass`.

**Naming note:** Keep Go field `BypassABAC` as-is for ownership override. Prefer names like `MethodBypass` / `CreateBypassPayment` for the payment method to avoid code confusion.

---

### 3. Bitcoin (`bitcoin`) — planned only

**Status:** Spec placeholder. **Do not implement** providers, webhooks, wallets, or API acceptance of `method=bitcoin` until a dedicated follow-up.

| Topic | Direction (locked for planning) |
|-------|----------------------------------|
| API `method` | `bitcoin` (rejected with **501** or **400** “not available” until shipped) |
| Storefront currency | Order remains **KRW**; BTC is a settlement rail, not a catalog currency |
| UX sketch | Hosted invoice / QR (similar redirect pattern to Stripe) — never custodial keys in Dupli1 app servers if avoidable |
| Provider candidates (TBD) | BTCPay Server, Coinbase Commerce, or equivalent — pick in the Bitcoin implementation PR |
| Completion | Async confirmation → same `CompletePayment` / `payment.succeeded` path |
| Hard problem | On-chain confirmation latency vs today’s **5-minute** unpaid cancel window |

### PG integration (NANO) — implemented

Customer **name and phone** for certified PG requests come from the **order snapshot** at checkout complete (copied onto the payment row as `payer_*` for the checkout bridge). Optional prefill from auth profile: [auth-profile-extension-plan.md](auth-profile-extension-plan.md).

Hash (request): `SHA256(ver+loginId+shopcode+reqPayAmt+timestamp+API_KEY+"NANO")`. 인증결제 v2.7 does not sign the browser return, so checkout binds `receiveUrl` with `nano_ts`/`nano_mac` (`SHA256(ver+loginId+shopcode+compOrderNo+reqPayAmt+timestamp+API_KEY+"RETURN")`). Success callbacks must match `shopcode` + `reqPayAmt` and either a PG `hashValue` or that MAC. Do **not** integrate 수기결제 (key-in) — that would put PAN on Dupli1.

**Open questions (resolve before coding Bitcoin)**

1. **TTL:** Extend unpaid window for Bitcoin-only payments (e.g. 30–60 min), or introduce `awaiting_crypto` without auto-cancel until invoice expiry?
2. **FX:** Lock KRW→BTC rate at invoice creation; who is the rate source; how to handle under/overpay?
3. **Partial pays / dust:** Reject and keep `pending`, or auto-adjust?
4. **Refunds:** On-chain refunds vs store credit — out of scope until refunds phase exists.
5. **Compliance:** KR accounting / AML expectations for the operating entity.

Until those are decided, `POST /api/v1/payments` with `method=bitcoin` must not create payment rows.

---

## Data model changes (payment DB)

Extend `payments` (additive; card rows remain valid with defaults):

| Column | Type | Notes |
|--------|------|--------|
| `method` | `TEXT NOT NULL DEFAULT 'credit_card'` | `credit_card` \| `bypass` \| `bitcoin` |
| `created_by` | `TEXT NULL` | JWT `sub` for Bypass (and optionally all creates) |
| `note` | `TEXT NULL` | Bypass reason / ops note; ignore for card |

`provider` stays: `nano` \| `dev` \| `bypass` \| (future bitcoin provider id).

Indexes: none required beyond existing `provider_ref` / idempotency for MVP.

Domain JSON should expose `method` on create/get responses.

---

## API contract (target)

### `POST /api/v1/payments`

```json
{
  "order_id": "ord_000001",
  "method": "credit_card",
  "note": "optional; only meaningful for bypass"
}
```

| `method` | Auth | Immediate status | `checkout_url` |
|----------|------|------------------|----------------|
| `credit_card` (default) | ABAC or `payment.create` | `requires_payment` | yes |
| `bypass` | `payment.bypass` | `succeeded` | omitted |
| `bitcoin` | — | **reject until implemented** | — |

### `GET /api/v1/payments/settings`

Expose non-secret capability flags for clients:

```json
{
  "methods": {
    "credit_card": true,
    "bypass": false,
    "bitcoin": false
  }
}
```

`bypass: true` only when the caller’s token includes `payment.bypass` (or always list enabled server methods and let manage-web hide by permission — prefer **permission-aware** response if cheap; otherwise static flags + client-side hide).

### Events

`payment.succeeded` payload stays:

```json
{
  "event_type": "payment.succeeded",
  "order_id": "ord_000001",
  "payment_id": "pay_000001",
  "amount_cents": 70000,
  "occurred_at": "..."
}
```

Optional later: add `method` to the event for notification copy (“paid via bypass”). Not required for order `MarkOrderPaid`.

---

## Service / package shape

Today: one `CheckoutProvider`. Target:

```text
payment/pkg/
  domain/          # MethodCreditCard, MethodBypass, MethodBitcoin constants
  ports/
    checkout.go    # card / future bitcoin session providers
    # Bypass needs no CheckoutProvider — service marks succeeded directly
  service/
    CreatePayment  # switch on method; enforce permissions
  infra/checkout/  # stripe.go, dev.go; bitcoin/ later
```

Bypass should **not** go through Stripe or the `simulate-success` URL. It calls the same `CompletePayment` (or internal succeed+publish) used after webhooks so order transition stays one path.

---

## Security

1. **Bypass is privileged.** Missing `payment.bypass` → 403 even if the caller owns the order.
2. **Never trust client amount** for any method.
3. **NANO return/webhook is source of truth for card** — fail closed: require `shopcode` + `reqPayAmt` match, and either a verifying `hashValue` or the `receiveUrl` MAC issued at checkout. Browser landing without either does not mark paid.
4. **Bitcoin (later):** verify provider signatures / IPN authenticity the same way NANO result fields are checked.
5. **Audit:** Bypass always stores `created_by`; manage-web should show who marked paid.
6. Keep **dev `simulate-success`** gated (NANO unset only) — distinct from Bypass (Bypass is intentional prod ops tooling).

---

## Phased delivery

### Phase 1 — Method field + credit card naming (docs + small code)

- Add `method` column / domain field; default `credit_card`
- Accept optional `method` on create; reject unknown values
- Echo `method` on responses and settings
- No behavior change for existing clients that omit `method`

### Phase 2 — Bypass (order manager) — **done**

- Add `payment.bypass` permission + bundle updates
- Implement Bypass create path (succeed + publish)
- Persist `created_by` / `note`
- manage-web: “Mark paid (bypass)” on pending orders (client follow-up)
- Tests: permission matrix, happy path → order `paid`, forbidden for customers

### Phase 3 — Bitcoin (later PR only)

- Resolve TTL / FX / provider open questions above
- Implement provider adapter + webhook/IPN
- Enable `method=bitcoin` in API and settings
- Storefront Bitcoin CTA

**Explicit:** Phase 3 code is **out of scope** until product asks to start Bitcoin.

---

## Frontend notes

| Client | Behavior |
|--------|----------|
| `dupli1-web` | Offer **credit card** only. Do not show Bypass or Bitcoin until Phase 3 enables Bitcoin. |
| `dupli1-manage-web` | On pending order detail: **Mark as paid (bypass)** calling `POST /payments` with `method=bypass` + optional note. Requires manager token with `payment.bypass`. |

---

## Doc / code touch list (when implementing)

| Area | Change |
|------|--------|
| `payment/pkg/domain` | `Method` constants; optional `CreatedBy`, `Note` |
| `payment/pkg/service` | Branch on method; Bypass succeed path |
| `payment/pkg/handler` | Parse `method` / `note`; map 403 |
| `payment/pkg/infra/pg` | Schema columns |
| `shared/pkg/permissions` | `payment.bypass` + bundles |
| [payment-service.md](payment-service.md) | Methods section |
| [permissions.md](permissions.md) | New permission + matrix |
| [endpoints.md](endpoints.md) / [api.md](api.md) | Request body `method` |
| [current-state.md](current-state.md) | Methods status |

---

## Decision log

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Unified create endpoint | One `POST /payments` + `method` | Matches current client flow; avoids parallel confirm APIs |
| Bypass vs status PUT | Bypass creates a payment + event | Keeps “who sets `paid`” = payment event consumer only |
| Bypass permission | New `payment.bypass` | Separates “start Stripe for anyone” (`payment.create`) from “mark paid without money” |
| Card default | Omit `method` → `credit_card` | Backward compatible |
| Bitcoin now | Spec only | User-requested; TTL/FX unresolved |
| Currency | KRW order total for all methods | Storefront single-currency lock |
