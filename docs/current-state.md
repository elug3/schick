# Current Code State

Authoritative snapshot of what is implemented in the Dupli1 repository today.

## Overview

Dupli1 is a fashion bag marketplace backend: Go microservices behind an nginx gateway. Local dev uses Docker Compose; production uses AWS ECS on EC2, ALB, and Amazon RDS PostgreSQL.

| Area | Status |
|------|--------|
| Auth (login, JWT, fine-grained permissions) | Implemented |
| Product catalog (bags, coupons, images, PDP) | Implemented |
| Currency | **KRW only** — product prices and `*_cents` amounts are whole won ([payment-service.md](payment-service.md)) |
| Inventory (stock, reservations) | Implemented (PostgreSQL, owned by product) |
| Orders + checkout sessions | Implemented (PostgreSQL) |
| Shopping cart | Implemented (PostgreSQL) |
| Payments (NANO card + Bypass) | Implemented — see [payment-service.md](payment-service.md) |
| Payment methods | Credit card (NANO) + Bypass implemented; Bitcoin planned — see [payment-methods-plan.md](payment-methods-plan.md) |
| Notifications | Implemented (NATS → Telegram when configured) |
| Customer commerce profile + addresses | Implemented — own **`profile`** service (PostgreSQL), extracted from auth ([profile-service.md](profile-service.md), [auth-profile-extension-plan.md](auth-profile-extension-plan.md)); chat/analytics not started |
| Guest PDP views + recommendations | Implemented — in product |
| Manager settings (mutable store policy) | Sketch — see [manager-settings-api.md](manager-settings-api.md) |

## Repository layout

Services live in **per-service directories**, not `cmd/dupli1-*` / `pkg/*` at the repo root:

```text
auth/, profile/, product/, order/, cart/, payment/, notification/   # each has cmd/ + pkg/
api/nginx.conf                                      # gateway
```

See [service-layout.md](service-layout.md) for details.

## Services

### dupli1-auth

- **Host port (Compose):** 18080 → container 8080
- **Stack:** Gin, PostgreSQL, Redis, optional NATS
- **Persistence:** `dupli1_db` on `postgres-auth`
- **Features:**
  - Login returns a **refresh token**; `POST /refresh` returns a short-lived **access token** (`token` field) plus a **rotated refresh token** (`refresh_token` field) — the token sent in is invalidated immediately
  - RS256 JWT + JWKS at `/api/v1/auth/.well-known/jwks.json`
  - Access tokens include `type: "access"`; refresh tokens include `type: "refresh"`; both include a random `jti` so same-second issuances never collide
  - Fine-grained **permissions** stored on users (`users.permissions TEXT[]`); JWT access tokens include `permissions` claim only
  - Permission constants and evaluation in `shared/pkg/permissions` (`github.com/elug3/dupli1/shared`)
  - Wildcards: `*`, `admin.*`, `{resource}.*` (e.g. `product.*`)
  - Account types: `customer`, `manager`, `service` only (`account_type`). `admin` is a permission tier (`admin.*`), not an account type — write APIs reject it.
  - Register: **temporary open customer signup** via `AUTH_OPEN_REGISTER` (default on); anonymous callers create `customer` only. Set `AUTH_OPEN_REGISTER=false` to require `user.create` again. Authenticated `user.create` still follows ABAC for other account types.
  - Auth ABAC hierarchy governs who may manage whom
  - User admin at `/api/v1/auth/users`; update via `PATCH …/permissions`
  - Customer commerce profile/addresses moved to **`profile`** service (`/api/v1/profile/me/…`); one-release gateway aliases keep `/api/v1/auth/me/profile` and `/api/v1/auth/me/addresses` — [auth-profile-extension-plan.md](auth-profile-extension-plan.md)
  - `DELETE /api/v1/auth/users/:id` (`user.delete`) writes `user.deleted` to a transactional **outbox** in the same Postgres transaction as the user row delete; the drain worker publishes to NATS so profile can drop owned PII. In-memory/tests publish first and refuse the delete if the broker rejects.
  - Owner seeded from `OWNER_EMAIL` / `OWNER_PASSWORD` (`permissions: ["*"]`, `account_type` `manager`)
  - Login lockout after 5 failed attempts for customers/managers, auto-expiring after 15 minutes; **admin and owner are never locked**
  - Deactivated/locked accounts are rejected on their very next authenticated request (not just next login/refresh) — `RequireAuth` re-checks account status on every call
  - `dupli1-web` service account: `permissions: ["user.create"]` (`DUPLI1_WEB_SERVICE_*`); seeded/synced on auth boot; ECS injects the shared Secrets Manager secret into auth + web (see [infra/terraform/README.md](../infra/terraform/README.md))
  - `dupli1-order` service account: `order.ship`, `order.status.update`, `inventory.reservation.manage` (`DUPLI1_ORDER_SERVICE_*`); order refreshes a Bearer access token and calls product stock/coupons via **`DUPLI1_GATEWAY_URL`** (`httpstock` / gateway paths)
  - Login/refresh rate-limited per IP via Redis; Gin trusts only RFC1918 proxy hops (`SetTrustedProxies`) so a client-supplied `X-Forwarded-For` can't spoof a fresh IP and bypass the limit
  - Session store falls back to in-memory (with background GC) when no Redis is configured, so `/logout` and refresh-token revocation still work on a single instance instead of silently no-op'ing
  - `user.registered` NATS publish is best-effort: a broker outage is logged and the account still registers
  - Structured **zerolog** logging (`event` field) for session paths, internal errors, and bootstrap — [auth-logging.md](auth-logging.md)
- **Tests:** `cd auth && go test ./...`

### dupli1-profile

- **Host port:** 8088
- **Stack:** stdlib HTTP, PostgreSQL, NATS
- **Persistence:** `profiles` on `postgres-profile`
- **Features:**
  - Customer commerce profile (`display_name`, `phone`) + saved shipping addresses at `/api/v1/profile/me/…`, extracted from `auth` — see [profile-service.md](profile-service.md)
  - One-release nginx aliases keep `/api/v1/auth/me/profile` and `/api/v1/auth/me/addresses` working during cutover
  - Self-service only: owner is the JWT `sub`, no dedicated permission (ABAC, same pattern as cart); foreign-user address access returns `404` not `403`
  - Max **10** addresses per user; at most one `is_default` per user enforced by a Postgres partial unique index
  - Subscribes to auth's `user.deleted` NATS event and cascade-deletes owned profile + addresses (no FK to auth's `users` table — different database)
  - No coupling to order/checkout: order's shipping snapshot is copied client-side from a chosen address, not fetched server-to-server
- **Auth:** Bearer JWT via `AUTH_JWKS_URL` (RS256 JWKS from auth), with `JWT_SECRET` HS256 fallback in dev
- **Known gap:** one-time data copy from auth's pre-extraction `customer_profiles`/`customer_addresses` tables has not run — addresses saved before cutover aren't visible through `profile` yet (see [auth-profile-extension-plan.md](auth-profile-extension-plan.md) Phase D)
- **Tests:** `cd profile && go test ./...`

### dupli1-product

- **Host port:** 8081
- **Stack:** stdlib HTTP, PostgreSQL, MinIO/S3
- **Persistence:** `products` on `postgres-product`
- **Features:**
  - Parent (style) + variant (SKU) model: search returns parents only (no color duplicates)
  - Bag merchandising taxonomy (`subCategory`, `style`, `target`) with public master catalog + product search filters
  - Price stored on parent product (`price` / `officialPrice`); variants inherit for cart JSON — [product-price-on-parent.md](product-price-on-parent.md)
  - Parent `attributes` string map (PDP memo; not searched) — [product-attributes.md](product-attributes.md)
  - Dual SKU identity + master dictionaries: [product-sku-system.md](product-sku-system.md) (ULID product `id` + `skuId`; human `sku`; `/api/v1/products/catalog/…`; Phase C enforces existing master codes on create)
  - Variant physical dimensions (`dimensions.widthMm` / `heightMm` / `depthMm`) distinct from letter `size`/`sizeCode` — [product-sku-dimensions.md](product-sku-dimensions.md)
  - Error wrapping: store-boundary sentinels + sanitized 500s — [product-error-wrapping.md](product-error-wrapping.md)
  - Public: `GET /api/v1/products` (optional `product.read` widens view; filters `q`, `category`, `subcategory`, `style`, `target`, `brand`, `color`, `size`, `material`, `tags`; `sort`/`order` — [product-rich-search.md](product-rich-search.md), [product-master-catalog.md](product-master-catalog.md)), `GET /api/v1/products/{id}` (parent + variants with per-variant `availableQty`/`inStock`; unique guest `viewCount` via `dupli1_guest` cookie; `soldCount` on reservation commit — [product-sold-count.md](product-sold-count.md); `wishlistCount`), wishlist add/remove/list, `GET /api/v1/products/{id}/recommendations` (content + popularity — [product-recommendations.md](product-recommendations.md)), `GET /api/v1/products/variants?sku_ids=` (batch public variant lookup), coupon redeem
  - Admin: per-route permissions (`product.create`, `coupon.read`, …) — see [permissions.md](permissions.md); parent CRUD, variant CRUD at `/api/v1/products/{id}/variants`, images on variant or default variant
  - Stock and reservations at `/api/v1/products/inventory/*` (merged in from the former standalone `inventory` service; legacy `/api/v1/inventory/*` still aliased), keyed by a canonical ULID `SkuID` with `sku` and `by-sku-id/{skuId}` lookups both supported; reads are public, writes require `inventory.stock.write` or `inventory.reservation.manage`. Every variant gets a `stock_items` row on create (qty 0); orphans backfilled on inventory migrate — [product-stock-tracking-plan.md](product-stock-tracking-plan.md)
  - Protected routes validate RS256 via `AUTH_JWKS_URL`; authorization from `permissions` claim
  - Inline schema migration + variant backfill on startup; brand/color/size/edition master tables seeded on migrate
  - Structure review (Product vs sellable SKU, flatten Phase 0 locks): [product-structure-final-review.md](product-structure-final-review.md)
  - Plan: [product-variants-plan.md](product-variants-plan.md), [product-sku-system.md](product-sku-system.md), [product-flat-sellable-model-plan.md](product-flat-sellable-model-plan.md)
- **Tests:** `cd product && go test ./...`

### dupli1-order

- **Host port:** 8083
- **Persistence:** PostgreSQL (`orders` on `postgres-order`)
- **Features:**
  - Checkout sessions at `/api/v1/orders/checkout/sessions` (legacy `/api/v1/checkout/sessions` still aliased; see [checkout-session.md](checkout-session.md))
  - Order lifecycle at `/api/v1/orders` — statuses: `pending`, `paid`, `in_transit`, `fulfilled`, `canceled`
  - List: `GET /api/v1/orders` (all — requires `order.read.all`); `GET /api/v1/orders?customer_id=` (ABAC). There is no `/orders/all` or `/orders/me`.
  - Consumes **`payment.succeeded`** (NATS) → `paid` (idempotent on `payment_id`; replays after ship/fulfill are no-ops); late payment on auto-`canceled` orders **re-reserves stock** and reopens the payment window before marking `paid`
  - Consumes **`payment.canceled`** (NATS) → cancels a still-`paid` order on a full refund (`remaining_cents == 0`) only when `payment_id` matches; omitted `remaining_cents`, partial refunds, pending, and already-shipped orders are skipped. Atomic `paid`+`payment_id` guard so a concurrent ship is not last-write-wins canceled.
  - 5-minute unpaid `pending` expiry worker (skips when payment wins the race)
  - **Shipping fee:** flat per-order delivery charge via `DUPLI1_ORDER_SHIPPING_FEE_CENTS` (whole KRW, default 30000 = 30,000 KRW; set 0 for free). `total = subtotal - discount + shipping`; no free-shipping threshold; coupons discount goods only. Snapshotted on the checkout session when it opens; `complete` charges that quoted fee even if the configured amount changed mid-checkout. Direct `POST /orders` uses the current configured fee.
  - Publishes order events via transactional **outbox** (`order.created` / status updates); outbox drain worker
  - Optional `Idempotency-Key` on `POST /api/v1/orders` (replay-safe create)
  - Checkout `complete` snapshots recipient + shipping address (optional prefill from auth profile)
  - Checkout `complete` uses atomic session claim — concurrent completes cannot create duplicate orders
  - `POST /api/v1/orders/{id}/ship` validates `paid` → `in_transit` **before** committing inventory (plan B); **requires** `carrier` + `tracking_number` (fixed KR set: `cj`/`hanjin`/`lotte`/`logen`/`epost`/`other`; `carrier_note` required when `other`)
  - Calls product to reserve stock and redeem coupons
  - Order responses include optional `carrier`, `tracking_number`, `carrier_note` after ship
- **Auth:** Bearer JWT via `AUTH_JWKS_URL` (RS256 JWKS; HS256 fallback in dev). Storefront ABAC on `customer_id`; `order.create` / `order.read.all` bypass ABAC. Ship requires `order.ship`; status changes require `order.status.update`
- **Tests:** `cd order && go test ./...`

### dupli1-cart

- **Host port:** 8086
- **Persistence:** PostgreSQL (`cart` on `postgres-cart`)
- **Features:**
  - Persistent per-customer cart at `/api/v1/cart` (see [cart-service.md](cart-service.md))
  - Admin read at `/api/v1/cart/customers/{customer_id}` requires `cart.read` (legacy `/api/v1/carts/{customer_id}` still aliased)
  - Enriches lines from product (price, images, availability)
  - Upsert/replace reject when quantity exceeds available (`400` `insufficient_stock`)
- **Auth:** Bearer JWT via `AUTH_JWKS_URL` (RS256 JWKS from auth; access tokens only), with `JWT_SECRET` HS256 fallback in dev
- **Tests:** `cd cart && go test ./...`

### dupli1-payment

- **Host port:** 8087
- **Persistence:** PostgreSQL (`payments` on `postgres-payment`)
- **Features:**
  - **NANO** certified card PG when `NANO_*` credentials set; else `credit_card` is unavailable (501) and manager **Bypass** is used, including for local testing (see [payment-service.md](payment-service.md))
  - Default payment currency: **`krw` only** (whole won; `*_cents` fields are KRW minor units = won)
  - Publishes **`payment.succeeded`** via transactional **outbox** (soft-success complete; drain + reconcile workers)
  - **Cancel / refund:** `POST /api/v1/payments/{id}/cancel` (`payment.cancel`, staff-only) calls NANO `/api/payment/cancel.io`; full or partial (`amount_cents`), `Idempotency-Key` honored. Concurrent cancels serialize on a row lock (`SELECT … FOR UPDATE` / in-memory mutex) so two in-flight requests cannot both call NANO. Publishes **`payment.canceled`** via outbox; order cancels a still-`paid` matching order on a full refund, notification alerts ops.
  - **Methods:** `method` on create — `credit_card` (NANO; 501 when unconfigured), `bypass` (requires `payment.bypass`; succeeds immediately), `bitcoin` (501). See [payment-methods-plan.md](payment-methods-plan.md)
- **Auth:** Bearer JWT on customer routes; ownership ABAC unless `payment.create` / `payment.read.all`. Bypass requires `payment.bypass`
- **Tests:** `cd payment && go test ./...`

### dupli1-notification

- **Host port:** 8084
- **Features:** NATS subscriber; Telegram ops alerts; webhook or `getUpdates` stores `chat_id` in PostgreSQL; manager API to accept users/chats. See [notification-telegram-bot.md](notification-telegram-bot.md)
- **Database:** PostgreSQL `notifications` (`DUPLI1_NOTIFICATION_DB`; local port 5438)
- **Handler failures** (payload decode, Telegram send) are logged; core NATS does not redeliver, so a failed alert is dropped after the log line
- **Production:** bot token from Secrets Manager; subscriptions and routing in notification DB
- **Status:** Health + event dispatch + Telegram manager API (no outbound email/SMS yet)

### dupli1-proxy

- **Host ports:** 8080 and 80 (HTTP), 443 exposed but TLS not configured in nginx
- **Config:** [api/nginx.conf](../api/nginx.conf) locally, [api/nginx.prod.conf](../api/nginx.prod.conf) for the single-EC2 Compose overlay, [api/nginx.ecs.conf](../api/nginx.ecs.conf) for production ECS (baked into `api/Dockerfile.ecs`). All three route the same API prefixes
- **Health:** `GET /gateway/health` → `ok`

## Data stores

| Store | Used by | Local |
|-------|---------|-------|
| PostgreSQL `dupli1_db` | auth | `postgres-auth:5432` |
| PostgreSQL `products` | product (also stock/reservations) | `postgres-product:5433` |
| PostgreSQL `orders` | order | `postgres-order:5435` |
| PostgreSQL `cart` | cart | `postgres-cart:5436` |
| PostgreSQL `payments` | payment | `postgres-payment:5437` |
| PostgreSQL `notifications` | notification | `postgres-notification:5438` |
| PostgreSQL `profiles` | profile | `postgres-profile:5439` |
| MinIO `product-images` | product (local) | `minio:9000` via gateway `/product-images/` |
| S3 + CloudFront OAC | product (AWS) | `images.dupli1.com` — see [product-images-browser-access.md](product-images-browser-access.md) |
| Redis | auth | `redis:6379` (in Compose) |
| NATS | auth, product, order, payment, notification, profile | `127.0.0.1:4222` in Compose (`--auth` / `NATS_TOKEN`); Cloud Map `nats.dupli1.local` in ECS |

## API surface (summary)

| Service | Public | Authenticated |
|---------|--------|---------------|
| auth | login, refresh, logout, JWKS | register (`user.create` or open register), me, user admin (permissions, delete) |
| profile | health only | commerce profile + saved addresses (ABAC self-service) |
| product | health, product search/PDP, coupon redeem, inventory reads | product/coupon CRUD (per permission), image upload, inventory writes (`inventory.stock.write`, `inventory.reservation.manage`) |
| order | health only | orders (list all / by customer), checkout (ABAC + permissions), ship (`order.ship`) |
| cart | health only | own cart; admin read (`cart.read`) |
| payment | health only | payments (ABAC + permissions); Bypass (`payment.bypass`); cancel/refund (`payment.cancel`) |
| notification | health, Telegram webhook | Telegram subscriptions (`notification.telegram.read` / `notification.telegram.manage`) |

Full reference: [api.md](api.md). Route index: [endpoints.md](endpoints.md). Permission spec: [permissions.md](permissions.md).

## Go modules

| Module | Path |
|--------|------|
| `github.com/elug3/dupli1` | root stub |
| `github.com/elug3/dupli1/auth` | `auth/` |
| `github.com/elug3/dupli1/profile` | `profile/` |
| `github.com/elug3/dupli1/product` | `product/` |
| `github.com/elug3/dupli1/order` | `order/` |
| `github.com/elug3/dupli1/cart` | `cart/` |
| `github.com/elug3/dupli1/payment` | `payment/` |
| `github.com/elug3/dupli1/notification` | `notification/` |
| `github.com/elug3/dupli1/shared` | `shared/` (permissions library) |

## Known gaps

1. **Local TLS** — certs in `certs/` are not wired into nginx; gateway is HTTP only
2. **Notification** — Telegram ops alerts only; no email/SMS
3. **No migrations directory** — product migrates inline; auth uses bootstrap DDL
4. **Planned packages not started** — user, chat, analytics (beyond `shared/pkg/permissions`)
5. **Quality/performance** — see [quality-performance-review.md](quality-performance-review.md); money-path Criticals (C1 pricing, H7 JWT) are fixed — remaining items in [TODO.md](TODO.md)
6. **v1.0 vs v1.1 vs v1.2** — **v1.0 postponed** (2026-07-27) until all checklist items in [v1.0-release-spec.md](v1.0-release-spec.md) are done; narrative: [v1-release-plan.md](v1-release-plan.md); v1.1 starts after v1.0 tags: [v1.1-release-plan.md](v1.1-release-plan.md)
7. **Production JWT signing key** — **done**. Secret `dupli1/production/jwt-private-key` is injected as `JWT_PRIVATE_KEY` on ECS auth; `GET /api/v1/auth/settings` reports `features.ephemeral_jwt_key: false` (checklist A6).
8. **Legacy API path aliases** — canonical `/api/v1/{service}/…` paths are documented; the old top-level prefixes stay registered until the frontends migrate ([TODO.md](TODO.md))

## Running and testing

```bash
cp .env.example .env
docker compose up --build

# Gateway (HTTP)
curl http://localhost:8080/gateway/health

# Tests (per service directory)
cd auth && go test ./...
cd product && go test ./...
```

## Deployment

Production: ECS on EC2, ALB, RDS PostgreSQL 16, S3, Secrets Manager. See [deployment-aws.md](deployment-aws.md).
