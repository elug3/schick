# Fine-grained permissions (Phase 0)

Authoritative specification for migrating Dupli1 from coarse service-manager **roles** to fine-grained **permissions**.

**Status:** Phase 5 complete — legacy JWT `roles` claim, dual-read validators, and `PATCH …/roles` alias removed. Authorization uses the `permissions` claim only.

**Related docs:** [endpoints.md](endpoints.md) (route index), [api.md](api.md), [current-state.md](current-state.md), [openapi.yaml](openapi.yaml).

---

## Goals

| Today | Target |
|-------|--------|
| `product_manager` grants all product + coupon actions | `product.create`, `product.update`, … assigned independently |
| `order_manager` grants ship, status, inventory writes, admin cart | `order.ship`, `inventory.stock.write`, … per action |
| `user_manager` / `customer_registrar` gate user admin | `user.create`, `user.password.update`, … per action |
| `admin` / `owner` implicit super-access everywhere | `admin.*` and `*` wildcards with explicit evaluation rules |

Storefront **customers** are not permissions. Customer self-service stays **authenticated + ABAC** (caller `sub` must match the resource owner).

---

## Frozen design decisions (Phase 0)

### 1. Permission string format

- Pattern: `{resource}.{action}` (lowercase, dot-separated).
- Valid characters: `[a-z0-9._*]`.
- Actions are verbs (`create`, `update`, `delete`, `read`, `ship`, …).
- Resources match service domains (`product`, `coupon`, `order`, `user`, `inventory`, `cart`, `payment`).

### 2. JWT access-token claim

| Claim | Type | Notes |
|-------|------|-------|
| `sub` | string | User ID (unchanged) |
| `type` | string | `"access"` (unchanged) |
| `permissions` | string[] | Fine-grained authorization strings |
| `email` | string | User's login email (omitted when empty). Used by payment for NANO `compOrderMem`; not an authorization claim — do not trust it for access control without re-checking auth |
| `exp`, `iat` | number | Unchanged |
| `jti` | string | Random per-token ID; prevents same-second token collisions (required for refresh rotation) |

Refresh tokens keep `sub` + `type: "refresh"` only. Permissions are loaded from the database on every refresh so revocations apply on the next access token.

### 3. Database column

Rename `users.roles TEXT[]` → `users.permissions TEXT[]`.

Rationale: `roles` today stores values like `product_manager`. Reusing the column name after migration would be confusing for operators and SQL.

### 4. User admin API

| Endpoint | Status |
|----------|--------|
| `PATCH /api/v1/auth/users/:id/permissions` | Replace a user's permission list — body `{ "permissions": ["..."], "account_type": "..." }` |

User objects expose `permissions` in JSON responses.

Optional later: `POST /api/v1/auth/users/:id/permissions/bundles` with `{ "bundle": "catalog_editor" }` — **not in Phase 0 scope**; bundles are code-defined presets for now.

### 5. Wildcards

| Token | Grants |
|-------|--------|
| `*` | Every permission (owner only) |
| `admin.*` | All permissions listed under **Auth** and **User admin** in the catalog below, plus `user.*` |
| `{resource}.*` | All permissions whose resource prefix matches (e.g. `product.*` → `product.create`, `product.update`, …) |

Evaluation order: exact match → resource wildcard → `admin.*` → `*`.

No other prefix wildcards (e.g. `product.variant.*` is not supported; list `product.variant.create` explicitly or use `product.*`).

### 6. `customer` is not a permission

`customer` remains implicit for accounts with **no admin permissions** (empty `permissions` array, storefront `account_type: customer`). ABAC rules key off authentication + absence of elevated permissions, not a `customer` string in the token.

### 7. Migration phases (complete)

| Phase | Behaviour |
|-------|-----------|
| 2 | Auth writes `permissions` claim; DB column renamed |
| 3 | All services enforce per-route permission checks |
| 4 | Docs and OpenAPI aligned with permissions model |
| 5 | Legacy JWT `roles` claim, dual-read, and alias endpoint removed |

### 8. Shared library location

Phase 1 introduces `shared/pkg/permissions` (Go module `github.com/elug3/dupli1/shared`) consumed by auth and downstream services. The library is intentionally thin: permission constants, wildcard evaluation, legacy role expansion, and bundles only — no JWT or HTTP code. See [shared/README.md](../shared/README.md).

---

## Permission catalog

### Auth / user admin

| Permission | Description |
|------------|-------------|
| `user.create` | Register a new user (`POST /api/v1/auth/register`) |
| `user.read` | List users (`GET /api/v1/auth/users`) |
| `user.permissions.update` | Replace a user's permission list (`PATCH …/permissions`) |
| `user.password.update` | Set another user's password |
| `user.status.update` | Activate or deactivate a user |
| `user.delete` | Permanently delete a user (publishes `user.deleted` for profile cleanup) |

**ABAC (auth service only):** user management follows a role hierarchy independent of downstream services:

| Caller tier | May manage |
|-------------|------------|
| `user_manager` | `customer` accounts |
| `admin` | `manager` and `customer` accounts |
| `owner` (unique `*` account) | `admin`, `manager`, and `customer` accounts |

Tiers are derived from permissions inside auth only. Other services continue to use fine-grained permissions without this hierarchy.

**Account types vs tiers:** stored `account_type` values are `customer` | `manager` | `service` only. The ABAC labels `admin` / `manager` / `owner` above are **permission-derived tiers** (`ClassAdmin`, etc.), not `account_type` strings. A human operator uses `account_type: manager`; whether they are ClassAdmin vs ClassManager depends on permissions (`admin.*`, …). Write APIs **reject** `account_type: "admin"`.

**ABAC (register):** callers with only `user.create` (and without `user.password.update`, `admin.*`, or `*`) may register **`account_type: customer`** only. Higher-privilege callers may set any valid `account_type` subject to the hierarchy above.

### Login lockout

Failed password attempts increment `failed_login_attempts`. After **5** failures, the account sets `locked_at` and `POST /login` returns `403` until an operator unlocks it (or status tooling clears the lock).

**Exempt:** **owner** (`*` permission) and **admin** tier (`account_type: manager` with admin-level permissions such as `admin.*`) are **never locked**. `User.Lock` / lock checks are no-ops for them, and a stale `locked_at` is cleared on the next login attempt so manage-web cannot lock out operators.

Manager-tier and `customer` / `service` accounts still use the normal lockout.

### Product

| Permission | Description |
|------------|-------------|
| `product.create` | Create parent product |
| `product.update` | Update parent product |
| `product.delete` | Delete parent product (cascades variants) |
| `product.read` | List/search with drafts and `status` filter |
| `product.variant.create` | Create variant (SKU) |
| `product.variant.update` | Update variant |
| `product.variant.delete` | Delete variant |
| `product.image.upload` | Upload image on parent default variant or specific variant |
| `product.master.read` | List SKU master dictionaries (brands, styles, colors, sizes, editions) |
| `product.master.write` | Create / rename / delete SKU master dictionary rows |

Public `GET /api/v1/products` and `GET /api/v1/products/{id}` stay **unauthenticated**. `product.read` only widens manager view when a valid token is present (same as today's `product_manager` optional-auth behaviour).

### Coupon (product service)

| Permission | Description |
|------------|-------------|
| `coupon.read` | List coupons |
| `coupon.create` | Create coupon |
| `coupon.update` | Update coupon |
| `coupon.delete` | Delete coupon |

`coupon.redeem` is **public** (checkout flow) — no permission.

### Inventory

| Permission | Description |
|------------|-------------|
| `inventory.stock.read` | Reserved for future private stock APIs (reads stay public today) |
| `inventory.stock.write` | `PUT /{sku}`, `POST /{sku}/adjust` |
| `inventory.reservation.manage` | Create, commit, and release reservations |

### Order

| Permission | Description |
|------------|-------------|
| `order.create` | Create order for any `customer_id` (without ABAC self-check) |
| `order.read.all` | List/get any customer's orders (bypass ABAC) |
| `order.ship` | `POST /orders/{id}/ship` |
| `order.status.update` | `PUT /orders/{id}/status` (cancel, fulfill) |

Checkout session routes (`/api/v1/orders/checkout/sessions/*`) follow the same rules as orders: authenticated customers use ABAC on `customer_id`; `order.create` / `order.read.all` bypass ABAC.

**Default storefront:** authenticated user with empty `permissions` may create/read/list **only their own** orders (ABAC on `sub` == `customer_id`).

### Cart

| Permission | Description |
|------------|-------------|
| `cart.read` | `GET /api/v1/cart/customers/{customer_id}` (any customer) |

Own-cart routes (`/api/v1/cart/*`) require authentication only; scoped to `sub`.

### Payment

| Permission | Description |
|------------|-------------|
| `payment.create` | Start checkout for any user's order (service accounts) |
| `payment.read.all` | Read any payment by ID (bypass ownership check in service layer) |
| `payment.bypass` | Mark a pending order paid without a PG (`method=bypass`). Order-manager / fulfillment; not the same as ABAC bypass. See [payment-methods-plan.md](payment-methods-plan.md) |
| `payment.cancel` | Cancel / refund a succeeded payment at the PG (`POST /api/v1/payments/{id}/cancel`). Staff-only — no ABAC path, so customers can never refund themselves |

**Default storefront:** authenticated user with empty `permissions` may create/read **only their own** payments (ownership enforced in service).

NANO return/webhook endpoints are **unauthenticated** (callback / webhook secret). Bypass requires `payment.bypass`.

`payment.cancel` is granted by the `fulfillment` bundle and by the `*` / `admin.*` wildcards. It is deliberately **not** added to the legacy `order_manager` role expansion, so existing legacy-role accounts do not silently gain the ability to move money; grant the `fulfillment` bundle (or the permission itself) to opt an account in.

### Notification

| Permission | Description |
|------------|-------------|
| `notification.telegram.read` | List Telegram subscription rows (pending/accepted users and chat IDs) |
| `notification.telegram.manage` | Create, accept, reject, or delete Telegram subscriptions |

Ops staff use these from manage-web `/telegram`. Owner `*` and `admin.*` wildcards grant both. They are **not** part of the legacy `order_manager` or `product_manager` role expansions — assign explicitly or via `admin.*` / `*`.

Telegram webhook and NATS-driven outbound alerts are unauthenticated / internal; only the manager REST API requires JWT.

See [notification-telegram-bot.md](notification-telegram-bot.md).

---

## Endpoint → permission matrix

### Auth service

| Method | Path | Permission |
|--------|------|------------|
| `POST` | `/api/v1/auth/register` | `user.create` (or temporary open register) |
| `GET` | `/api/v1/auth/me` | (authenticated) |
| `GET` | `/api/v1/auth/users` | `user.read` |
| `PATCH` | `/api/v1/auth/users/:id/permissions` | `user.permissions.update` |
| `PATCH` | `/api/v1/auth/users/:id/password` | `user.password.update` |
| `PATCH` | `/api/v1/auth/users/:id/status` | `user.status.update` |
| `DELETE` | `/api/v1/auth/users/:id` | `user.delete` |

**Temporary:** `AUTH_OPEN_REGISTER=true` allows unauthenticated customer signup (empty permissions). Re-lock with `AUTH_OPEN_REGISTER=false`.

Login, refresh, logout, health, settings, JWKS — public.

### Product service

| Method | Path | Permission |
|--------|------|------------|
| `GET` | `/api/v1/products` | optional: `product.read` widens response |
| `GET` | `/api/v1/products/{id}` | — (public) |
| `GET` | `/api/v1/products/variants` | — (public; `?sku_ids=` batch) |
| `GET` | `/api/v1/products/variants/by-sku/{sku}` | — (public) |
| `GET` | `/api/v1/products/variants/by-sku-id/{skuId}` | — (public) |
| `POST` | `/api/v1/products` | `product.create` |
| `PUT` | `/api/v1/products/{id}` | `product.update` |
| `DELETE` | `/api/v1/products/{id}` | `product.delete` |
| `POST` | `/api/v1/products/{id}/images` | `product.image.upload` |
| `POST` | `/api/v1/products/{id}/variants` | `product.variant.create` |
| `PUT` | `/api/v1/products/{id}/variants/{sku}` | `product.variant.update` |
| `DELETE` | `/api/v1/products/{id}/variants/{sku}` | `product.variant.delete` |
| `POST` | `/api/v1/products/{id}/variants/{sku}/images` | `product.image.upload` |
| `GET` | `/api/v1/products/catalog/brands` | `product.master.read` |
| `POST` | `/api/v1/products/catalog/brands` | `product.master.write` |
| `PATCH` | `/api/v1/products/catalog/brands/{code}` | `product.master.write` |
| `DELETE` | `/api/v1/products/catalog/brands/{code}` | `product.master.write` |
| `GET` | `/api/v1/products/catalog/brands/{code}/styles` | `product.master.read` |
| `POST` | `/api/v1/products/catalog/brands/{code}/styles` | `product.master.write` |
| `PATCH` | `/api/v1/products/catalog/brands/{code}/styles/{styleCode}` | `product.master.write` |
| `DELETE` | `/api/v1/products/catalog/brands/{code}/styles/{styleCode}` | `product.master.write` |
| `GET` | `/api/v1/products/catalog/colors` | `product.master.read` |
| `POST` | `/api/v1/products/catalog/colors` | `product.master.write` |
| `PATCH` | `/api/v1/products/catalog/colors/{code}` | `product.master.write` |
| `DELETE` | `/api/v1/products/catalog/colors/{code}` | `product.master.write` |
| `GET` | `/api/v1/products/catalog/sizes` | `product.master.read` |
| `POST` | `/api/v1/products/catalog/sizes` | `product.master.write` |
| `PATCH` | `/api/v1/products/catalog/sizes/{code}` | `product.master.write` |
| `DELETE` | `/api/v1/products/catalog/sizes/{code}` | `product.master.write` |
| `GET` | `/api/v1/products/catalog/editions` | `product.master.read` |
| `POST` | `/api/v1/products/catalog/editions` | `product.master.write` |
| `PATCH` | `/api/v1/products/catalog/editions/{code}` | `product.master.write` |
| `DELETE` | `/api/v1/products/catalog/editions/{code}` | `product.master.write` |
| `GET` | `/api/v1/products/coupons` | `coupon.read` |
| `POST` | `/api/v1/products/coupons` | `coupon.create` |
| `PUT` | `/api/v1/products/coupons/by-code/{code}` | `coupon.update` |
| `DELETE` | `/api/v1/products/coupons/by-code/{code}` | `coupon.delete` |
| `POST` | `/api/v1/products/coupons/redeem` | — (public) |

Legacy top-level aliases (`/api/v1/variants/…`, `/api/v1/catalog/…`, `/api/v1/coupons/…`) are still registered with the same permissions; see [TODO.md](TODO.md) for the migration table.

### Inventory (served by the product service)

Stock and reservations were merged from a standalone inventory service into
`dupli1-product`; permission names are unchanged, and the routes moved under the
`/api/v1/products/inventory` prefix. Each item route also has a
`by-sku-id/{skuId}` sibling, and the legacy `/api/v1/inventory/…` prefix is still
registered as an alias.

| Method | Path | Permission |
|--------|------|------------|
| `GET` | `/api/v1/products/inventory/items/{sku}` | — (public) |
| `PUT` | `/api/v1/products/inventory/items/{sku}` | `inventory.stock.write` |
| `POST` | `/api/v1/products/inventory/items/{sku}/adjust` | `inventory.stock.write` |
| `POST` | `/api/v1/products/inventory/reservations` | `inventory.reservation.manage` |
| `POST` | `/api/v1/products/inventory/reservations/{id}/commit` | `inventory.reservation.manage` |
| `POST` | `/api/v1/products/inventory/reservations/{id}/release` | `inventory.reservation.manage` |

### Order service

| Method | Path | Permission / rule |
|--------|------|-------------------|
| `POST` | `/api/v1/orders` | ABAC or `order.create` |
| `GET` | `/api/v1/orders` | `order.read.all` |
| `GET` | `/api/v1/orders?customer_id=` | ABAC or `order.read.all` |
| `GET` | `/api/v1/orders/{id}` | ABAC or `order.read.all` |
| `POST` | `/api/v1/orders/{id}/ship` | `order.ship` |
| `PUT` | `/api/v1/orders/{id}/status` | `order.status.update` |
| `*` | `/api/v1/orders/checkout/sessions/*` | same as orders (ABAC on session owner); legacy alias `/api/v1/checkout/sessions/*` |

**ABAC:** caller lacks `order.create` / `order.read.all` / `admin.*` / `*` → `customer_id` (or session owner) must equal `sub`.

### Cart service

| Method | Path | Permission / rule |
|--------|------|-------------------|
| `*` | `/api/v1/cart/*` | authenticated; scoped to `sub` |
| `GET` | `/api/v1/cart/customers/{customer_id}` | `cart.read`; legacy alias `/api/v1/carts/{customer_id}` |

### Payment service

| Method | Path | Permission / rule |
|--------|------|-------------------|
| `POST` | `/api/v1/payments` | ABAC or `payment.create`; `method=bypass` requires `payment.bypass` |
| `GET` | `/api/v1/payments/{id}` | ABAC or `payment.read.all` |
| `POST` | `/api/v1/payments/{id}/cancel` | `payment.cancel` (no ABAC) |

### Notification service

| Method | Path | Permission |
|--------|------|------------|
| `GET` | `/api/v1/notification/telegram/subscriptions` | `notification.telegram.read` |
| `POST` | `/api/v1/notification/telegram/subscriptions` | `notification.telegram.manage` |
| `POST` | `/api/v1/notification/telegram/subscriptions/{id}/accept` | `notification.telegram.manage` |
| `POST` | `/api/v1/notification/telegram/subscriptions/{id}/reject` | `notification.telegram.manage` |
| `DELETE` | `/api/v1/notification/telegram/subscriptions/{id}` | `notification.telegram.manage` |

Webhook (`POST /api/v1/notification/telegram/webhook`) and NATS event dispatch require no JWT. Manager routes require Bearer + `AUTH_JWKS_URL` on the notification task in production.

---

## Named bundles (presets)

Code-defined sets for common job functions. Assigning a bundle expands to explicit permissions before save (bundles are not stored on the user row).

| Bundle | Permissions |
|--------|-------------|
| `catalog_editor` | `product.create`, `product.update`, `product.read`, `product.variant.create`, `product.variant.update`, `product.image.upload`, `product.master.read`, `product.master.write` |
| `catalog_admin` | `product.*`, `coupon.*` |
| `fulfillment` | `order.ship`, `order.status.update`, `inventory.stock.write`, `inventory.reservation.manage`, `cart.read`, `payment.bypass`, `payment.cancel` |
| `user_admin` | `user.create`, `user.read`, `user.password.update`, `user.status.update`, `user.delete` |
| `customer_registrar` | `user.create` |

API exposure of bundles is deferred; Phase 2 seeds and migrations use these expansions directly.

---

## Legacy role migration

One-time mapping applied to `users.permissions` during database migration (`auth/pkg/bootstrap/migrate.go`).

| Legacy role | Expanded permissions |
|-------------|---------------------|
| `owner` | `*` |
| `admin` | `admin.*`, `user.*`, `product.*`, `coupon.*`, `inventory.stock.write`, `inventory.reservation.manage`, `order.ship`, `order.status.update`, `order.read.all`, `cart.read`, `payment.bypass` |
| `user_manager` | `user.password.update`, `user.status.update` |
| `customer_registrar` | `user.create` |
| `product_manager` | `product.*`, `coupon.*` |
| `order_manager` | `order.ship`, `order.status.update`, `order.read.all`, `inventory.stock.write`, `inventory.reservation.manage`, `cart.read`, `payment.bypass` |
| `customer` | _(empty — storefront ABAC only)_ |

Users with multiple legacy roles receive the **union** of expanded permissions (deduplicated).

### Seeded accounts (target permissions)

| Account | Env vars | Today | After migration |
|---------|----------|-------|-----------------|
| Owner | `OWNER_EMAIL` | `owner`, `product_manager` | `*` |
| dupli1-web | `DUPLI1_WEB_SERVICE_*` | `customer_registrar` | `user.create` |
| dupli1-order | `DUPLI1_ORDER_SERVICE_*` | `order_manager` | `order.ship`, `order.status.update`, `inventory.reservation.manage` |

Note: `dupli1-order` does not need `cart.read` or `inventory.stock.write` for its runtime paths (reservations only). The legacy `order_manager` role was broader than the order service account requires.

---

## SQL migration sketch (Phase 2)

```sql
-- Rename column
ALTER TABLE users RENAME COLUMN roles TO permissions;

-- Expand legacy values (run per-role; order matters for owner/admin)
UPDATE users SET permissions = ARRAY['*']
  WHERE 'owner' = ANY(permissions);

UPDATE users SET permissions = ARRAY[
  'admin.*','user.create','user.read','user.permissions.update',
  'user.password.update','user.status.update',
  'product.*','coupon.*',
  'inventory.stock.write','inventory.reservation.manage',
  'order.ship','order.status.update','order.read.all',
  'cart.read'
] WHERE 'admin' = ANY(permissions) AND NOT ('*' = ANY(permissions));

-- ... product_manager, order_manager, user_manager, customer_registrar, customer
```

Exact SQL will live in `auth/pkg/bootstrap/migrate.go` with tests in Phase 2.

---

## Implementation roadmap

| Phase | Deliverable |
|-------|-------------|
| **0** | This document (frozen catalog + decisions) |
| **1** | `shared/pkg/permissions` — catalog constants, `Has`, `ExpandLegacyRoles`, bundles (**done**) |
| **2** | Auth: DB rename, JWT issuer, middleware, seeds, `PATCH …/permissions` (**done**) |
| **3** | Product, inventory, order, cart, payment: per-route permission checks + dual-read (**done**) |
| **4** | `docs/api.md`, `docs/endpoints.md`, OpenAPI specs (**done**) |
| **5** | Remove `roles` claim, deprecated endpoint, dual-read (**done**) |

---

## Open items (not blocking Phase 1)

- Whether `admin` should shrink to `admin.*` only (user-domain) vs the full cross-service list above — current decision: **admin gets explicit cross-service set** equivalent to today's behaviour.
- Persisted bundle entities in the database — deferred.
- Permission audit log on `user.permissions.update` — deferred.
