# API Endpoints

All services listen on port `8080` inside Docker. The nginx gateway proxies by path prefix with no stripping, so gateway paths match service paths.

**Path convention:** `/api/v1/{service_name}/...` (`auth`, `profile`, `products`, `orders`, `cart`, `payments`, `notification`). Legacy top-level aliases (`variants`, `coupons`, `catalog`, `inventory`, `checkout`, `carts`) still work until clients migrate — see [TODO.md](TODO.md).

| Gateway prefix | Upstream service |
|---|---|
| `/gateway/health` | nginx (static) |
| `/api/v1/auth/` | `dupli1-auth:8080` |
| `/api/v1/profile` | `dupli1-profile:8080` |
| `/api/v1/auth/me/profile`, `/api/v1/auth/me/addresses` | `dupli1-profile:8080` (one-release alias — see [profile-service.md](profile-service.md)) |
| `/api/v1/products` | `dupli1-product:8080` |
| `/api/v1/coupons` | `dupli1-product:8080` (legacy alias) |
| `/api/v1/catalog` | `dupli1-product:8080` (legacy alias) |
| `/api/v1/variants` | `dupli1-product:8080` (legacy alias) |
| `/api/v1/inventory/` | `dupli1-product:8080` (legacy alias) |
| `/api/v1/orders` | `dupli1-order:8080` |
| `/api/v1/checkout` | `dupli1-order:8080` (legacy alias) |
| `/api/v1/cart` | `dupli1-cart:8080` |
| `/api/v1/carts/` | `dupli1-cart:8080` (legacy alias) |
| `/api/v1/payments` | `dupli1-payment:8080` |
| `/api/v1/payments/` | `dupli1-payment:8080` |
| `/api/v1/notification/` | `dupli1-notification:8080` |

Local gateway: `http://localhost:8080` (also host port 80).

Each service also registers `/health` and `/settings` directly for internal/sidecar use (product uses versioned paths only: `/api/v1/products/health` and `/api/v1/products/settings`).

`GET /settings` (and `GET /api/v1/<service>/settings`) returns non-secret operational configuration: service name, auth mode, storage backend, feature flags, and dependency hostnames. Secrets, DSNs, and API keys are never included.

---

## Auth Service

| Method | Path | Permission | Description |
|---|---|---|---|
| `GET` | `/api/v1/auth/health` | — | Health check |
| `GET` | `/api/v1/auth/settings` | — | Non-secret service settings |
| `POST` | `/api/v1/auth/register` | `user.create` *or open register* | Create a new user account |
| `POST` | `/api/v1/auth/login` | — | Login and receive a refresh token |
| `POST` | `/api/v1/auth/logout` | — | Invalidate the current session |
| `POST` | `/api/v1/auth/refresh` | — | Exchange a refresh token for a new access token |
| `GET` | `/api/v1/auth/me` | Bearer | Return the authenticated user's account (email, permissions) |
| `DELETE` | `/api/v1/auth/users/:id` | `user.delete` | Permanently delete user; enqueues `user.deleted` in the same transaction (consumed by `profile` to drop owned PII — see [Profile Service](#profile-service)) |
| `GET` | `/api/v1/auth/users` | `user.read` | List users (filtered by auth ABAC hierarchy) |
| `PATCH` | `/api/v1/auth/users/:id/permissions` | `user.permissions.update` | Replace a user's permissions (optional `account_type`) |
| `PATCH` | `/api/v1/auth/users/:id/password` | `user.password.update` | Set a new password for a user |
| `PATCH` | `/api/v1/auth/users/:id/status` | `user.status.update` | Activate or deactivate a user |

**Temporary open register:** when `AUTH_OPEN_REGISTER=true` (current default), `POST /register` accepts unauthenticated requests and always creates `account_type: customer` with empty permissions. Set `AUTH_OPEN_REGISTER=false` to require Bearer + `user.create` again.

**dupli1-web service account:** set `DUPLI1_WEB_SERVICE_EMAIL` and `DUPLI1_WEB_SERVICE_PASSWORD` on `dupli1-auth` to seed a machine user with `permissions: ["user.create"]` and `account_type` `service`. That account may register customers only (`account_type` `customer`).

**Account types:** `customer`, `manager`, `service` — returned on user objects as `account_type`. Distinct from **permissions** (fine-grained authorization strings). `admin` is a permission (`admin.*`), not an `account_type` — use `manager` for operators.

See [permissions.md](permissions.md) for the full catalog, JWT claim shape, and auth ABAC hierarchy.

### GET /api/v1/auth/health

Response `200`: `{"status":"ok"}`

### GET /api/v1/auth/settings

Response `200` JSON with non-secret operational settings (`service`, `api_version`, `auth`, `storage`, `features`, `limits`, `dependencies`).

### POST /api/v1/auth/register

Header: `Authorization: Bearer <access_token>` (requires `user.create`) — **optional** while `AUTH_OPEN_REGISTER=true` (anonymous → customer only).

Request:
```json
{
  "email": "user@example.com",
  "password": "minlen8",
  "account_type": "customer"
}
```

`account_type` is optional (`customer`, `manager`, or `service`); defaults to `customer`. Do not send `admin` — that is a permission tier; use `manager` for operators. Callers with only `user.create` may register `customer` accounts only. Unauthenticated open-register always forces `customer`.

Response `201`:
```json
{ "user_id": "uuid" }
```

Errors: `400` bad request, `401` missing/invalid token, `403` insufficient permission, `409` user already exists, `422` invalid email/password/account_type, `500` internal error.

### POST /api/v1/auth/login

Request:
```json
{ "email": "user@example.com", "password": "secret" }
```

Response `200`:
```json
{ "refresh_token": "<token>" }
```

Errors: `400` bad request, `401` invalid credentials, `403` locked (customers/managers after 5 failures, auto-expires after 15 min) or deactivated. **Admin and owner are never locked.**

### POST /api/v1/auth/logout

Request:
```json
{ "refresh_token": "<token>" }
```

Response `204` (no body).

### POST /api/v1/auth/refresh

Request:
```json
{ "refresh_token": "<token>" }
```

Response `200`:
```json
{ "token": "<new_access_token>", "refresh_token": "<new_refresh_token>" }
```

The refresh token rotates on every call — store `refresh_token` from the response and use it next time; the one just sent no longer works.

Errors: `400` bad request, `401` invalid/expired/already-rotated token, or account deactivated/locked.

### GET /api/v1/auth/me

Header: `Authorization: Bearer <access_token>`

Response `200`:
```json
{
  "user_id": "uuid",
  "email": "user@example.com",
  "account_type": "customer",
  "permissions": [],
  "is_active": true,
  "locked_at": null,
  "failed_login_attempts": 0
}
```

Errors: `401` missing or invalid token, `404` user not found.

### GET /api/v1/auth/users

Header: `Authorization: Bearer <access_token>` (requires `user.read`)

Response `200`:
```json
{
  "users": [
    {
      "user_id": "uuid",
      "email": "user@example.com",
      "account_type": "customer",
      "permissions": [],
      "is_active": true,
      "locked_at": null,
      "failed_login_attempts": 0
    }
  ]
}
```

Errors: `401` missing or invalid token, `403` caller lacks `user.read`, `500` internal error.

### PATCH /api/v1/auth/users/:id/permissions

Header: `Authorization: Bearer <access_token>` (requires `user.permissions.update`)

Request:
```json
{
  "permissions": ["user.password.update", "user.status.update"],
  "account_type": "manager"
}
```

Response `200`: user object (same shape as list item).

Errors: `400` bad request, `401` missing/invalid token, `403` insufficient permission, `404` user not found, `422` invalid account_type/permission, `500` internal error.

### PATCH /api/v1/auth/users/:id/password

Header: `Authorization: Bearer <access_token>` (requires `user.password.update`)

Request:
```json
{ "password": "newpassword" }
```

Response `204` (no body).

Errors: `400` bad request, `401` missing/invalid token, `403` insufficient permission, `404` user not found, `422` password too short, `500` internal error.

### PATCH /api/v1/auth/users/:id/status

Header: `Authorization: Bearer <access_token>` (requires `user.status.update`)

Request:
```json
{ "is_active": false }
```

Response `200`: user object (same shape as list item).

Errors: `400` bad request, `401` missing/invalid token, `403` insufficient permission, `404` user not found, `500` internal error.

---

## Profile Service

Owns customer commerce PII (display name, phone, saved shipping addresses) — separate from `auth`'s identity/credentials data. Self-service only, ABAC by JWT `sub`, no dedicated permission. Extracted from `auth` — see [profile-service.md](profile-service.md) and [auth-profile-extension-plan.md](auth-profile-extension-plan.md).

| Method | Path | Permission | Description |
|---|---|---|---|
| `GET` | `/api/v1/profile/health` | — | Health check |
| `GET` | `/api/v1/profile/settings` | — | Non-secret service settings |
| `GET` | `/api/v1/profile/me/profile` | Bearer | Profile + embedded `addresses` |
| `PATCH` | `/api/v1/profile/me/profile` | Bearer | Merge-patch `display_name` / `phone` |
| `GET` | `/api/v1/profile/me/addresses` | Bearer | List saved addresses |
| `POST` | `/api/v1/profile/me/addresses` | Bearer | Create address (max 10; first is default) |
| `GET` | `/api/v1/profile/me/addresses/:id` | Bearer | Get one address |
| `PATCH` | `/api/v1/profile/me/addresses/:id` | Bearer | Partial update; `is_default: false` un-defaults, `true` promotes and clears others |
| `DELETE` | `/api/v1/profile/me/addresses/:id` | Bearer | Delete address (no auto-promotion if it was default) |
| `POST` | `/api/v1/profile/me/addresses/:id/default` | Bearer | Set sole default |
| (alias) | `/api/v1/auth/me/profile`, `/api/v1/auth/me/addresses…` | Bearer | One-release nginx aliases → `dupli1-profile`; remove once clients are cut over |

Foreign-user access to any `:id` route returns **`404`** (not `403`) — same "don't reveal existence" pattern as cart/order.

### GET /api/v1/profile/me/profile

Header: `Authorization: Bearer <access_token>`

Response `200` — commerce profile and saved addresses (empty when unset):
```json
{
  "user_id": "03f95d58-4840-46d4-9c92-fe48364d2e75",
  "display_name": "윤라희",
  "phone": "01041125167",
  "default_address_id": "addr_000001",
  "addresses": [
    {
      "id": "addr_000001",
      "label": "home",
      "recipient_name": "윤라희",
      "recipient_phone": "01041125167",
      "postal_code": "06194",
      "address_line1": "테헤란로 78길 14-12",
      "address_line2": "9층",
      "city": "강남구",
      "province": "서울특별시",
      "pccc": "P123456789012",
      "is_default": true
    }
  ]
}
```

### PATCH /api/v1/profile/me/profile

Merge-patch body: `{ "display_name": "…", "phone": "010-…" }` (KR mobile). Creates the profile row on first update.

### `/api/v1/profile/me/addresses`

Bearer CRUD for saved shipping addresses (max **10** per user). `POST`/`PATCH` body: `recipient_name`, `recipient_phone`, `postal_code` (5 digits), `address_line1`, `city`, `province` required on create; optional `label`, `address_line2`, `pccc` (`P` + 12 digits), `is_default`.

Errors: `400` invalid field, `401` missing/invalid token, `404` address not found or not owned by caller, `500` internal error.

---

## Product Service

| Method | Path | Permission | Description |
|---|---|---|---|
| `GET` | `/api/v1/products/health` | — | Health check |
| `GET` | `/api/v1/products/settings` | — | Non-secret service settings |
| `GET` | `/api/v1/products` | optional `product.read` | Search **parent styles**; public active-only; `product.read` includes drafts |
| `GET` | `/api/v1/products/{id}` | — | Parent PDP with `variants[]`, `availableColors`, `availableSizes` |
| `GET` | `/api/v1/products/variants` | — | Batch public variants (`?sku_ids=id1,id2`, max 50) |
| `GET` | `/api/v1/products/variants/by-sku/{sku}` | — | Public active variant by human SKU (legacy: `/api/v1/variants/{sku}`) |
| `GET` | `/api/v1/products/variants/by-sku-id/{skuId}` | — | Public active variant by ULID (legacy: `/api/v1/variants/by-sku-id/{skuId}`) |
| `POST` | `/api/v1/products/coupons/redeem` | — | Redeem a coupon code (legacy: `/api/v1/coupons/redeem`) |
| `POST` | `/api/v1/products` | `product.create` | Create parent (ULID `id`; requires existing `brandCode`+`styleCode`) |
| `PUT` | `/api/v1/products/{id}` | `product.update` | Update parent |
| `DELETE` | `/api/v1/products/{id}` | `product.delete` | Delete parent (cascades variants) |
| `POST` | `/api/v1/products/{id}/images` | `product.image.upload` | Upload image to default variant |
| `POST` | `/api/v1/products/{id}/variants` | `product.variant.create` | Create variant (SKU; requires existing color/size codes) |
| `PUT` | `/api/v1/products/{id}/variants/{sku}` | `product.variant.update` | Update variant |
| `DELETE` | `/api/v1/products/{id}/variants/{sku}` | `product.variant.delete` | Delete variant |
| `POST` | `/api/v1/products/{id}/variants/{sku}/images` | `product.image.upload` | Upload image for variant |
| `GET` | `/api/v1/products/catalog/brands` | `product.master.read` | List brand codes (legacy: `/api/v1/catalog/...`) |
| `POST` | `/api/v1/products/catalog/brands` | `product.master.write` | Create brand `{code,name}` |
| `PATCH` | `/api/v1/products/catalog/brands/{code}` | `product.master.write` | Rename brand |
| `DELETE` | `/api/v1/products/catalog/brands/{code}` | `product.master.write` | Delete brand (409 if in use) |
| `GET` | `/api/v1/products/catalog/brands/{code}/styles` | `product.master.read` | List styles for brand |
| `POST` | `/api/v1/products/catalog/brands/{code}/styles` | `product.master.write` | Create style |
| `PATCH` | `/api/v1/products/catalog/brands/{code}/styles/{styleCode}` | `product.master.write` | Rename style |
| `DELETE` | `/api/v1/products/catalog/brands/{code}/styles/{styleCode}` | `product.master.write` | Delete style (409 if in use) |
| `GET` | `/api/v1/products/catalog/colors` | `product.master.read` | List colors |
| `POST` | `/api/v1/products/catalog/colors` | `product.master.write` | Create color |
| `PATCH` | `/api/v1/products/catalog/colors/{code}` | `product.master.write` | Rename color |
| `DELETE` | `/api/v1/products/catalog/colors/{code}` | `product.master.write` | Delete color (409 if in use) |
| `GET` | `/api/v1/products/catalog/sizes` | `product.master.read` | List sizes |
| `POST` | `/api/v1/products/catalog/sizes` | `product.master.write` | Create size |
| `PATCH` | `/api/v1/products/catalog/sizes/{code}` | `product.master.write` | Rename size |
| `DELETE` | `/api/v1/products/catalog/sizes/{code}` | `product.master.write` | Delete size (409 if in use) |
| `GET` | `/api/v1/products/catalog/editions` | `product.master.read` | List editions (VariantCode) |
| `POST` | `/api/v1/products/catalog/editions` | `product.master.write` | Create edition |
| `PATCH` | `/api/v1/products/catalog/editions/{code}` | `product.master.write` | Rename edition |
| `DELETE` | `/api/v1/products/catalog/editions/{code}` | `product.master.write` | Delete edition (409 if in use) |
| `GET` | `/api/v1/products/catalog/master` | public | Bag merchandising taxonomy (`subCategories`, `styles`, `targets`) |
| `GET` | `/api/v1/products/catalog/subcategories` | public | List bag subcategories |
| `GET` | `/api/v1/products/catalog/bag-styles` | public | List bag styles (occasion) |
| `GET` | `/api/v1/products/catalog/targets` | public | List targets (all/men/women/kids) |
| `GET` | `/api/v1/products/coupons` | `coupon.read` | List coupons (legacy: `/api/v1/coupons`) |
| `POST` | `/api/v1/products/coupons` | `coupon.create` | Create coupon |
| `PUT` | `/api/v1/products/coupons/by-code/{code}` | `coupon.update` | Update coupon (legacy: `/api/v1/coupons/{code}`) |
| `DELETE` | `/api/v1/products/coupons/by-code/{code}` | `coupon.delete` | Delete coupon |

Public search defaults to `status = active` on the **parent**. Query filters: `category`, `subcategory`, `style`, `target`, `brand`, `material`, `tags`, `color`, `size` (color/size match any active variant). Managers may also pass `status`. Checkout uses **variant SKU** (human `sku` or canonical `skuId`) with inventory. Identity + masters: [product-sku-system.md](product-sku-system.md). Bag taxonomy: [product-master-catalog.md](product-master-catalog.md). Parent display memo: [product-attributes.md](product-attributes.md). See also [product-variants-plan.md](product-variants-plan.md).

### Catalog master data

Code → name dictionaries for SKU segments. See [product-sku-system.md](product-sku-system.md). Permissions: `product.master.read` / `product.master.write`.

Bag merchandising taxonomy (subcategory / style / target) is separate and publicly readable — [product-master-catalog.md](product-master-catalog.md).

### GET /api/v1/products/health

Response `200`:
```json
{ "status": "ok" }
```

### GET /api/v1/products/settings

Response `200` JSON with non-secret operational settings. Also available at `/api/v1/inventory/settings`.

### GET /api/v1/products

Returns **one row per parent style** (not per color). Query params: `q`, `category`, `subcategory`, `style`, `target`, `brand` (partial), `material`, `tags`, `color`, `size`, `status` (managers only), `sort` (`newest`\|`views`\|`sold`\|`wishlist`\|`price`\|`name`), `order` (`asc`\|`desc`), `period` (`day`\|`week`\|`month`), `limit`, `offset` — [product-rich-search.md](product-rich-search.md), [product-master-catalog.md](product-master-catalog.md).

Example: `GET /api/v1/products?category=bags&sort=views&order=desc&period=week`

Response `200` includes `total`, `limit`, `offset`, `sort`, `order`, optional `period`, `results`.

### PUT|POST /api/v1/products/{id}/wishlist

Add parent to the current owner's wishlist (JWT or guest cookie). Idempotent; bumps `wishlistCount`.

### DELETE /api/v1/products/{id}/wishlist

Remove from wishlist.

### GET /api/v1/products/wishlist

List current owner's wishlisted public parents.

### GET /api/v1/products/{id}

Public PDP: parent plus `variants[]` (active only), `availableColors`, `availableSizes`. Returns `404` for draft/archived parents. Cart/checkout use each variant's `sku`. Sets `dupli1_guest` when absent and increments unique `viewCount` — [product-guest-views-plan.md](product-guest-views-plan.md). Includes `soldCount` (units committed on ship) — [product-sold-count.md](product-sold-count.md). Includes `wishlistCount`. Parent `price` / `officialPrice` are source of truth (also echoed on variants).

### GET /api/v1/products/{id}/recommendations

Public related parents for PDP (`limit` default 8, max 24). Content similarity + `view_count` boost — [product-recommendations.md](product-recommendations.md).

### GET /api/v1/variants/{sku}

Deprecated alias of `GET /api/v1/products/variants/{sku}`. Public variant lookup by SKU. Returns `404` when the variant or parent product is not active. Used by the cart service for price validation.

### GET /api/v1/products/variants?sku_ids=

Batch public variant lookup by canonical ULID `skuId`. Comma-separated `sku_ids` (max 50, deduped). Returns `{ "items": [...], "missing": [...] }` — `items` are active variants with active parents (same visibility as single lookup); `missing` lists unknown, draft/archived, or inactive-parent ids. Used by cart/order enrichment to avoid N single GETs.

---

## Cart Service

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/api/v1/cart/health` | — | Health check |
| `GET` | `/api/v1/cart/settings` | — | Non-secret service settings |
| `GET` | `/api/v1/cart` | Bearer | Get current user's cart (includes `unavailable_items` when stored lines are not sellable) |
| `DELETE` | `/api/v1/cart` | Bearer | Clear current user's cart |
| `PUT` | `/api/v1/cart/items` | Bearer | Replace all cart items (`422` + `unavailable_items` on bad variants) |
| `POST` | `/api/v1/cart/items` | Bearer | Add or update one item (`422` + `unavailable_items` on bad variants) |
| `DELETE` | `/api/v1/cart/items/{sku}` | Bearer | Remove one item |
| `GET` | `/api/v1/cart/customers/{customer_id}` | `cart.read` | Get a customer's cart (legacy: `/api/v1/carts/{customer_id}`) |

See [cart-service.md](cart-service.md) for architecture, boundaries with inventory/order, and checkout handoff.

---

## Payment Service

| Method | Path | Permission / rule | Description |
|---|---|---|---|
| `GET` | `/api/v1/payments/health` | — | Health check |
| `GET` | `/api/v1/payments/settings` | — | Non-secret service settings |
| `POST` | `/api/v1/payments` | ABAC / `payment.create`; Bypass needs `payment.bypass` | Start payment for a pending order (`method`: `credit_card` default, or `bypass`) |
| `GET` | `/api/v1/payments/{id}` | ABAC / `payment.read.all` | Payment status |
| `GET` | `/api/v1/payments/{id}/nano/checkout` | — | Bridge into NANO certified card checkout |
| `POST` | `/api/v1/payments/nano/return` | — | NANO form `receiveUrl` callback |
| `POST` | `/api/v1/payments/webhooks/nano` | — | Optional NANO JSON webhook |

See [payment-service.md](payment-service.md) for NANO / Bypass, 5-minute auto-cancel, and `payment.succeeded` → `paid`. Methods (`credit_card` / `bypass` / `bitcoin` planned): [payment-methods-plan.md](payment-methods-plan.md).

---

## Inventory (served by the product service)

Merged into `dupli1-product`. **Canonical** paths are under `/api/v1/products/inventory/…`; legacy `/api/v1/inventory/…` remains aliased. Each item route also has a `by-sku-id/{skuId}` sibling keyed by the variant's canonical ULID `skuId`.

| Method | Path | Permission | Description |
|---|---|---|---|
| `GET` | `/api/v1/products/inventory/health` | — | Health check (legacy: `/api/v1/inventory/health`) |
| `GET` | `/api/v1/products/inventory/settings` | — | Non-secret product-service settings |
| `GET` | `/api/v1/products/inventory/items/{sku}` | — | Get a stock item by SKU |
| `PUT` | `/api/v1/products/inventory/items/{sku}` | `inventory.stock.write` | Create or overwrite stock quantity |
| `POST` | `/api/v1/products/inventory/items/{sku}/adjust` | `inventory.stock.write` | Add or subtract stock (delta) |
| `POST` | `/api/v1/products/inventory/reservations` | `inventory.reservation.manage` | Create a reservation |
| `POST` | `/api/v1/products/inventory/reservations/{id}/commit` | `inventory.reservation.manage` | Commit a reservation |
| `POST` | `/api/v1/products/inventory/reservations/{id}/release` | `inventory.reservation.manage` | Release a reservation |

### GET /api/v1/products/inventory/health

Response `200`:
```json
{ "status": "ok" }
```

### GET /api/v1/products/inventory/items/{sku}

Response `200`:
```json
{
  "sku": "SHOE-001",
  "quantity": 100,
  "reserved": 10,
  "updated_at": "2026-06-23T12:00:00Z"
}
```

### PUT /api/v1/products/inventory/items/{sku}

Request:
```json
{ "quantity": 50 }
```

Response `200`: stock item object (same shape as GET).

### POST /api/v1/products/inventory/items/{sku}/adjust

Request:
```json
{ "delta": -5 }
```

Response `200`: updated stock item. Errors: `400` insufficient stock.

### POST /api/v1/inventory/reservations

Request:
```json
{
  "order_id": "ord-abc",
  "items": [
    { "sku": "SHOE-001", "quantity": 2 }
  ]
}
```

Response `201`:
```json
{
  "reservation_id": "res-xyz",
  "reservation": {
    "id": "res-xyz",
    "order_id": "ord-abc",
    "items": [ { "sku": "SHOE-001", "quantity": 2 } ],
    "status": "active",
    "created_at": "...",
    "updated_at": "..."
  }
}
```

### POST /api/v1/inventory/reservations/{id}/commit

### POST /api/v1/inventory/reservations/{id}/release

Both return `200` with the updated reservation object. Errors: `404` not found, `400` reservation already closed.

---

## Order Service

Requires `Authorization: Bearer <access_token>` when `AUTH_JWKS_URL` or `JWT_SECRET` is set (RS256 via auth JWKS in Compose). Storefront callers use ABAC on `customer_id` / session owner; `order.create` and `order.read.all` bypass ABAC. See [checkout-session.md](checkout-session.md) and [permissions.md](permissions.md).

| Method | Path | Permission / rule | Description |
|---|---|---|---|
| `GET` | `/api/v1/orders/health` | — | Health check |
| `GET` | `/api/v1/orders/settings` | — | Non-secret service settings |
| `POST` | `/api/v1/orders/checkout/sessions` | ABAC / `order.create` | Create checkout session (legacy: `/api/v1/checkout/sessions`) |
| `GET` | `/api/v1/orders/checkout/sessions/{id}` | ABAC / `order.read.all` | Get session (re-checks variants → `unavailable_items`) |
| `PUT` | `/api/v1/orders/checkout/sessions/{id}/items` | ABAC / `order.create` | Replace all items (`422` + `unavailable_items` on bad variants) |
| `POST` | `/api/v1/orders/checkout/sessions/{id}/items` | ABAC / `order.create` | Add or update one item (`422` + `unavailable_items` on bad variants) |
| `DELETE` | `/api/v1/orders/checkout/sessions/{id}/items/{sku}` | ABAC / `order.create` | Remove item |
| `POST` | `/api/v1/orders/checkout/sessions/{id}/coupon` | ABAC / `order.create` | Apply coupon |
| `POST` | `/api/v1/orders/checkout/sessions/{id}/complete` | ABAC / `order.create` | Complete checkout (`422` + `unavailable_items` when variants invalid) |
| `POST` | `/api/v1/orders` | ABAC / `order.create` | Create a new order |
| `GET` | `/api/v1/orders` | `order.read.all` | List all orders |
| `GET` | `/api/v1/orders?customer_id={id}` | ABAC / `order.read.all` | List orders for a customer |
| `GET` | `/api/v1/orders/{id}` | ABAC / `order.read.all` | Get a single order |
| `POST` | `/api/v1/orders/{id}/ship` | `order.ship` | Ship order (`paid` → `in_transit`, commit stock) |
| `PUT` | `/api/v1/orders/{id}/status` | `order.status.update` | Cancel or fulfill |

### GET /api/v1/orders/health

Response `200`:
```json
{ "status": "ok" }
```

### POST /api/v1/orders

Optional header: `Idempotency-Key` — retries with the same key and body return the original order (no second stock reservation). A reused key with a different body returns `409`.

Unit prices are resolved server-side from the product catalog; client `unit_price_cents` is ignored if sent.

Request:
```json
{
  "customer_id": "cust-123",
  "items": [
    { "sku": "SHOE-001", "quantity": 1 }
  ]
}
```

Response `201`: order object. Events (`order.created`) are written to a transactional outbox and published asynchronously (create succeeds even if NATS is briefly unavailable).

### GET /api/v1/orders

Requires `order.read.all`. Lists orders across all customers.

Response `200`:
```json
{ "total": 2, "orders": [ /* order objects */ ] }
```

### GET /api/v1/orders?customer_id=cust-123

ABAC on `customer_id`; `order.read.all` bypasses it. Storefront callers use their own user id here (there is no `/orders/me`).

Response `200`:
```json
{ "total": 2, "orders": [ /* order objects */ ] }
```

### GET /api/v1/orders/{id}

Response `200`: order object.

### PUT /api/v1/orders/{id}/status

Requires `order.status.update`. **`pending` → `paid` is set only by the payment event consumer** (not this endpoint).

Request:
```json
{ "status": "canceled" }
```

Valid status transitions via this endpoint:
- `pending` → `canceled`
- `paid` → `canceled`
- `in_transit` → `fulfilled`

Response `200`: updated order object. Errors: `400` invalid transition, `404` not found.

### POST /api/v1/orders/{id}/ship

Moves a **`paid`** order to **`in_transit`** and commits inventory reservations. Requires `order.ship`.

Request body (required):
```json
{
  "carrier": "cj",
  "tracking_number": "123456789012",
  "carrier_note": ""
}
```

- `carrier` — one of `cj`, `hanjin`, `lotte`, `logen`, `epost`, `other`
- `tracking_number` — required for every ship
- `carrier_note` — required when `carrier` is `other` (free-text carrier name); ignored otherwise

Response `200`: updated order object with `shipped_by`, `shipped_at`, `carrier`, `tracking_number`, and optional `carrier_note`. Errors: `400` invalid state or missing/invalid tracking, `404` not found.

Order object shape:
```json
{
  "id": "ord-abc",
  "customer_id": "cust-123",
  "reservation_id": "res-xyz",
  "payment_id": "pay_000001",
  "items": [ { "sku": "SHOE-001", "quantity": 1, "unit_price_cents": 9900 } ],
  "status": "paid",
  "total_cents": 9900,
  "payment_due_at": "...",
  "paid_at": "...",
  "created_at": "...",
  "updated_at": "..."
}
```

---

## Notification Service

NATS → Telegram ops alerts. Design: [notification-telegram-bot.md](notification-telegram-bot.md).

| Method | Path | Permission / rule | Description |
|---|---|---|---|
| `GET` | `/api/v1/notification/health` | — | Health check |
| `GET` | `/api/v1/notification/settings` | — | Non-secret service settings |
| `POST` | `/api/v1/notification/telegram/webhook` | webhook secret header | Telegram Bot API webhook (prod) |
| `GET` | `/api/v1/notification/telegram/subscriptions` | `notification.telegram.read` | List Telegram subscriptions |
| `POST` | `/api/v1/notification/telegram/subscriptions` | `notification.telegram.manage` | Create / upsert subscription |
| `POST` | `/api/v1/notification/telegram/subscriptions/{id}/accept` | `notification.telegram.manage` | Accept pending subscription |
| `POST` | `/api/v1/notification/telegram/subscriptions/{id}/reject` | `notification.telegram.manage` | Reject pending subscription |
| `DELETE` | `/api/v1/notification/telegram/subscriptions/{id}` | `notification.telegram.manage` | Delete subscription |

Local Compose uses `getUpdates` polling when webhook URL is unset. Subscriptions persist in PostgreSQL `notifications` (`DUPLI1_NOTIFICATION_DB`, host port **5438**).
