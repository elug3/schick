# Payment Service

**Status:** Implemented (`payment/`). Credit card uses **NANO Solution certified payment (인증결제)** when `NANO_API_KEY` + shop credentials are set. When NANO is unset — including local Compose by default — `credit_card` is unavailable and payments (local testing included) go through **Bypass** (`payment.bypass`).

The **payment service** (`dupli1-payment`) records money for **pending** orders via **NANO card** or manager **Bypass**. There is no separate dev-simulate path — local/manual testing uses Bypass too. Dupli1 **never** handles card numbers, CVC, or card passwords — NANO hosts the payment window.

On PG success, payment enqueues **`payment.succeeded`** in a transactional outbox (soft-success even if NATS is briefly down). The **order service** consumes it (queue group + logged handler errors), verifies amount, and moves the order to **`paid`**. A payment reconcile worker re-publishes recent succeeded payments so lost Core NATS deliveries still land (`MarkOrderPaid` is idempotent). The **notification service** sends a Telegram alert to ops. An **order manager** ships the order (`paid` → **`in_transit`**), which **commits** inventory (plan B).

See also: [cart-service.md](cart-service.md), [checkout-session.md](checkout-session.md), [payment-methods-plan.md](payment-methods-plan.md) (credit card / Bypass / Bitcoin methods).

---

## Order state machine

| Status | Meaning | Stock (plan B) |
|--------|---------|----------------|
| `pending` | Created at checkout, **not paid** | **Reserved** |
| `paid` | PG success; ops queue | Reserved |
| `in_transit` | Order-manager shipped | **Committed** |
| `fulfilled` | Delivered | Committed |
| `canceled` | Unpaid timeout, payment failed, or ops reject | **Released** |

```mermaid
stateDiagram-v2
    [*] --> pending: checkout complete
    pending --> paid: payment.succeeded
    pending --> canceled: 5min TTL / payment failed
    paid --> in_transit: POST /orders/{id}/ship
    paid --> canceled: ops reject (+ refund)
    in_transit --> fulfilled: ops fulfill
    canceled --> [*]
    fulfilled --> [*]
```

**Removed:** `confirmed` — replaced by `paid` (money received) and `in_transit` (approved to ship).

---

## Payment cancel / refund

`POST /api/v1/payments/{id}/cancel` (permission `payment.cancel`, staff-only — no ABAC) refunds a `succeeded` payment through the PG.

**Provider endpoint.** NANO `POST /api/payment/cancel.io`, documented in **[NANO] 수기결제 연동 API 안내 v2.5 §3**. The certified-payment guide (인증결제 v2.7 §4 취소) defines no cancel body of its own and defers to that section, so cert-approved card payments cancel through the same endpoint.

Two differences from the cert request matter:

| | Cert request (`/cert/pc/request.io`) | Cancel (`/cancel.io`) |
|---|---|---|
| Auth | body `hashValue` = `SHA256(ver+loginId+shopcode+reqPayAmt+timestamp+API_KEY+"NANO")` | **`API_KEY` HTTP header** |
| `timestamp` / `hashValue` | required | absent |

The cancel body carries no `encData`, so the 수기결제 guide's AES-256-CBC card encryption does **not** apply — no card data passes through Dupli1 on this path.

**Transaction id.** The cancel needs NANO's `tranNo` from the original approval. It is captured on the verified return/webhook callback and stored as the payment's `provider_ref`, replacing the `nano_<payment_id>` placeholder written at checkout.

**Partial cancel.** Supported since 수기결제 v2.5 (`cancelAmt` cancels exactly the amount sent; the response reports `remainAmt`). A partial cancel leaves the payment `succeeded` with a reduced remaining balance; the payment becomes `canceled` only when the balance reaches zero. `canceled_amount_cents` on the payment is cumulative.

> `remainAmt` appears in the v2.5 field table but is missing from that guide's own example response, so it is parsed as optional — an absent value is treated as unknown rather than zero, and local accounting stays authoritative.

**Ordering.** Concurrent cancels serialize on `MutateLocked` (Postgres `SELECT … FOR UPDATE`, in-memory mutex) so two in-flight requests cannot both pass validation and both call the PG. The PG is still invoked **before** the local write commits, so a rejected or unreachable PG leaves the payment untouched (`502`). Once the provider confirms, the refund is recorded even if its reported amount disagrees with local accounting — the amount is reconciled toward the provider and clamped into a recordable range, never discarded, since dropping it would show money on the books that is no longer held.

If the process dies after NANO accepts a cancel and before the local transaction commits, a retry can refund again. That crash window is retained so a rejected PG never records a refund that did not happen.

**Idempotency.** An `Idempotency-Key` header makes a retry of the same cancel a no-op. Only the most recent key is retained, which covers the realistic double-submit (timeout then retry) but not an arbitrary replay of an older partial cancel. A full cancel is additionally guarded by the status transition, and any cancel by the remaining balance.

**Bypass payments** never reached a PG, so they are canceled locally only and the matching refund is made out of band.

**Event.** A cancel publishes `payment.canceled` through the payment outbox (same transaction as the state change). Order cancels a still-`paid` order on a **full** refund (`remaining_cents` present and `== 0`) only when `payment_id` matches the order's captured payment; omitted `remaining_cents` is not treated as zero. Notification alerts ops on the same subject.

**Not implemented:** NANO `/api/payment/refund.io` (인증결제 v2.7 §5). That endpoint is 가상계좌(vbank)-only, explicitly no-partial, and needs `inputTranNo` from the deposit NOTI. Dupli1 sends `payWay: card` on every cert request, so it never applies.

---

## End-to-end flow

```mermaid
sequenceDiagram
    participant Client
    participant Cart as dupli1-cart
    participant Order as dupli1-order
    participant Pay as dupli1-payment
    participant Bus as NATS
    participant Notif as dupli1-notification
    participant Ops as order-manager

    Client->>Cart: shop
    Client->>Order: checkout complete
    Order-->>Client: order_id (pending, stock reserved)

    Note over Client,Pay: NANO card when configured · else manager Bypass (also used for local testing)

    Client->>Pay: POST /api/v1/payments { order_id, method }
    Pay->>Order: GET order (verify pending + total + recipient for NANO)
    Pay->>Pay: NANO checkout URL / bypass succeed
    Pay->>Bus: payment.succeeded { order_id, payment_id, amount_cents }

    Bus->>Order: consume payment.succeeded
    Order->>Order: pending → paid
    Order->>Bus: order.paid
    Bus->>Notif: Telegram to ops

    Note over Client: "Payment received — we're preparing your order"

    Ops->>Order: POST /api/v1/orders/{id}/ship
    Order->>Order: paid → in_transit (commit stock)
    Ops->>Order: PUT status → fulfilled (later)
```

---

## Design decisions (locked)

| Topic | Choice |
|-------|--------|
| Card PG | **NANO Solution** certified payment (`payWay=card`) when configured |
| Card data on Dupli1 | **Never** |
| Default currency | **`krw` only** (single currency; other codes rejected) |
| Amount unit | Whole Korean won (`amount_cents` = zero-decimal minor units for KRW — **not** won×100) |
| Unpaid `pending` TTL | **5 minutes** → auto-cancel + release stock |
| Inventory plan | **B** — reserve on checkout complete; **commit on `in_transit`** |
| Payment → order | **`payment.succeeded` event** (not HTTP confirm from payment) |
| Who sets `paid` | **Order service** (event consumer) |
| Who sets `in_transit` | **Order-manager** via `POST /orders/{id}/ship` |
| Manual `confirmed` | **Removed** |
| Telegram | **Notification service** on `order.paid` |
| Event payload | `order_id`, **`payment_id`**, **`amount_cents`** (must match `order.total_cents`) |
| Audit | `shipped_by`, `shipped_at` on order |

---

## Service boundaries

### Payment service owns

- Payment records (NANO card + Bypass)
- NANO checkout bridge + `receiveUrl` / webhook completion
- Publishing **`payment.succeeded`** (transactional outbox + drain/reconcile workers)

### Order service owns

- Order lifecycle and inventory reserve/commit/release
- Consuming **`payment.succeeded`** → `paid`
- **`POST /api/v1/orders/{id}/ship`** → `in_transit` + commit
- 5-minute pending expiry worker

### Notification service owns

- Subscribing to **`order.paid`** (and other order events)
- Telegram messages to `TELEGRAM_ORDER_CHAT_ID`

### Payment does **not**

- Change order status directly over HTTP
- Touch inventory
- Send Telegram

---

## Events

### `payment.succeeded` (payment → order)

```json
{
  "event_type": "payment.succeeded",
  "order_id": "ord_000001",
  "payment_id": "pay_000001",
  "amount_cents": 70000,
  "occurred_at": "2026-07-05T12:03:00Z"
}
```

Order consumer: idempotent on `payment_id`; reject if `amount_cents != order.total_cents`. If the order already carries the same `payment_id` and is no longer `pending` (e.g. `paid`, `in_transit`, or `fulfilled`), a replay is a no-op — the payment reconcile worker republishes for up to two hours after success, so late deliveries must not fail after ship.

### `order.paid` (order → notification)

Published when order transitions `pending` → `paid`. Notification formats ops queue message.

---

## API

### Payment (`dupli1-payment`, port **8087**)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/api/v1/payments/health` | — | Health |
| `GET` | `/api/v1/payments/settings` | — | Non-secret service settings |
| `POST` | `/api/v1/payments` | Bearer | Create payment (`credit_card` or `bypass`) |
| `GET` | `/api/v1/payments/{id}` | Bearer | Payment status |
| `GET` | `/api/v1/payments/{id}/nano/checkout` | — | Bridge into NANO cert checkout (when NANO configured) |
| `POST` | `/api/v1/payments/nano/return` | — | NANO form `receiveUrl` callback → succeed/fail + redirect |
| `POST` | `/api/v1/payments/webhooks/nano` | — | Optional JSON webhook (register URL with NANO) |

NANO success callbacks (`resultCode=0000`) fail closed unless `shopcode` and `reqPayAmt` match the payment and `hashValue` verifies with `NANO_API_KEY` (request-style digest over callback `timestamp`; confirm against merchant return-hash docs).

**Create payment (credit card)**
```json
{ "order_id": "ord_000001", "method": "credit_card" }
```

`credit_card` requires order `recipient_name` + `recipient_phone` when NANO is configured (returns `checkout_url` to the NANO bridge); it responds `501` when NANO is unset. Managers with `payment.bypass` may send `method: "bypass"` (+ optional `note`) to mark paid without a PG — this is also how local/dev environments without NANO pay.

**Bypass response**
```json
{
  "id": "pay_000001",
  "order_id": "ord_000001",
  "method": "bypass",
  "amount_cents": 70000,
  "currency": "krw",
  "status": "succeeded",
  "expires_at": "2026-07-05T12:05:00Z"
}
```

**Customer UX:** show *"Payment received — we're preparing your order"* while status is `paid`; poll `GET /api/v1/orders/{id}` or `GET /api/v1/payments/{id}`.

### Order (changes)

| Method | Path | Who | Description |
|--------|------|-----|-------------|
| `POST` | `/api/v1/orders/{id}/ship` | `order.ship` | `paid` → `in_transit`, commit stock, audit (transition validated **before** stock commit) |
| `PUT` | `/api/v1/orders/{id}/status` | RBAC | `fulfilled` from `in_transit`; `canceled` from `pending`/`paid` |

**Ship response** includes `shipped_by`, `shipped_at`.

---

## Inventory (plan B)

| When | Action |
|------|--------|
| Checkout `complete` | `Reserve` |
| `pending` → `canceled` (timeout/fail) | `Release` |
| `paid` → `in_transit` (ship) | `Commit` |
| `paid` → `canceled` (reject) | `Release` |

---

## Security

1. `payment.succeeded` (outbox) is source of truth for marking orders paid
2. Verify `amount_cents` on `payment.succeeded`
3. Idempotent complete / replay handling (`payment_id`)
4. Customer may only pay own orders
5. Ship endpoint requires elevated role; writes `shipped_by` from JWT `sub`
6. Bypass requires `payment.bypass`

---

## Configuration

| Variable | Service | Description |
|----------|---------|-------------|
| `DUPLI1_PAYMENT_ADDR` | payment | Listen `:8087` |
| `DUPLI1_PAYMENT_DB` | payment | Postgres `payments` |
| `DUPLI1_ORDER_URL` | payment | Fetch order for validation |
| `DUPLI1_PAYMENT_PUBLIC_URL` | payment | Public gateway base for NANO `receiveUrl` + checkout bridge |
| `NANO_BASE_URL` | payment | `https://dev3.nanopay.co.kr` (test) or `https://pay.nanopay.co.kr` (prod) |
| `NANO_VER` / `NANO_SHOPCODE` / `NANO_LOGIN_ID` / `NANO_API_KEY` | payment | Merchant credentials (prod: Secrets Manager `dupli1/production/nano-payment`) |
| `NANO_SUCCESS_URL` / `NANO_FAILURE_URL` | payment | Storefront redirects after `nano/return` |
| `DUPLI1_PAYMENT_ORDER_TTL` | order | `5m` pending payment window |
| `NATS_URL` | all | Event bus |
| `NATS_TOKEN` | all | Token matching nats-server `--auth` (Compose default `dupli1_nats_dev`; prod `dupli1/production/nats-token`) |
| `TELEGRAM_BOT_TOKEN` | notification | Bot token (prod: Secrets Manager `dupli1/production/telegram`) — **only secret**; see [notification-telegram-bot.md](notification-telegram-bot.md) |
| `TELEGRAM_ALLOWED_USER_IDS` | notification | Comma-separated Telegram user IDs allowed to use `/start` (transitional; target: Manager Settings) |
| `TELEGRAM_ORDER_CHAT_ID` | notification | Ops order alerts chat (transitional; target: Manager Settings) |
| `TELEGRAM_PRODUCT_CHAT_ID` | notification | Product ops chat (same secret) |

Local Postgres (payment): `postgres://dupli1:dupli1_dev@localhost:5437/payments?sslmode=disable`

---

## Failure paths

| Case | Result |
|------|--------|
| Unpaid > 5 min | `canceled`, release stock |
| Checkout abandoned / never completed | stay `pending` until TTL, then cancel |
| Paid, ops rejects | `canceled` (order) + `POST /api/v1/payments/{id}/cancel` (refund) — **two separate calls**, not yet chained |
| Duplicate `payment.succeeded` | idempotent — order stays `paid` |
| Replayed `payment.succeeded` after ship | no-op when `payment_id` already set and status ≠ `pending` |
| Payment succeeds after 5 min auto-cancel | order **reinstated** to `pending` with a fresh reservation and extended payment window, then marked `paid` |
| Ship on non-`paid` order | rejected before stock commit — inventory unchanged |

---

## Package layout

```text
payment/
├── cmd/
└── pkg/
    ├── domain/
    ├── service/
    ├── ports/
    ├── infra/pg/, checkout/, httporder/, nats/
    ├── handler/
    └── bootstrap/
```

---

## Related documentation

- [payment-methods-plan.md](payment-methods-plan.md) — credit card (live), Bypass (order manager), Bitcoin (planned)
- [cart-service.md](cart-service.md)
- [checkout-session.md](checkout-session.md)
- [endpoints.md](endpoints.md)
- [api.md](api.md)
