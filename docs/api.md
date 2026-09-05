# Dupli1 API Reference

All traffic is routed through the nginx gateway. Locally use **HTTP** at `http://localhost:8080` or `http://localhost` (port 80). Production terminates TLS at the load balancer or gateway.

**Currency:** the storefront uses **KRW only**. Product `price` values and cart/order/payment `*_cents` fields are **whole Korean won** (zero-decimal minor units for `krw` — do not multiply by 100). Settings expose `limits.currency: "krw"`.

**Path convention:** every route is namespaced by its owning service — `/api/v1/products/…` (including inventory, catalog and coupons), `/api/v1/orders/…` (including checkout sessions), `/api/v1/cart/…`, `/api/v1/payments/…`, `/api/v1/auth/…`, `/api/v1/profile/…`. The paths documented here are the canonical ones. Older top-level prefixes (`/api/v1/inventory`, `/api/v1/catalog`, `/api/v1/coupons`, `/api/v1/variants`, `/api/v1/checkout`, `/api/v1/carts`) are still registered as aliases and are called out where they differ; new clients should not use them. Migration table: [TODO.md](TODO.md).

---

## Authentication

Protected routes require an `Authorization` header with a Bearer **access** token:

```
Authorization: Bearer <access_token>
```

**Token flow**

1. `POST /api/v1/auth/login` → `{ "refresh_token": "<jwt>" }`
2. `POST /api/v1/auth/refresh` with that refresh token → `{ "token": "<access_jwt>", "refresh_token": "<new_jwt>" }`
3. Use the access token on protected routes until it expires (default 15 min), then refresh again — using the `refresh_token` the previous refresh returned. Refresh tokens rotate on every use: the one just spent stops working, so the caller must store the new one. Reusing an already-rotated refresh token returns `401`.

**Access token claims**

| Claim | Type | Notes |
|-------|------|-------|
| `sub` | string | User ID |
| `type` | string | `"access"` |
| `permissions` | string[] | Fine-grained authorization strings such as `product.create`, `order.ship`, `*` |
| `exp`, `iat` | number | Standard JWT timestamps |
| `jti` | string | Random per-token ID; makes every issued token unique even if minted in the same second |

Refresh tokens contain `sub`, `type: "refresh"`, and `jti` only. Permissions are loaded from the database on every refresh.

Protected routes check the `permissions` claim. See [permissions.md](permissions.md) for the full catalog and endpoint matrix.

Wildcards: `*` (everything), `admin.*` (user-admin domain), `{resource}.*` (e.g. `product.*`).

### Account types

Every user has an `account_type` field (JSON key `account_type`) separate from **permissions**:

| Value | Meaning | Typical permissions |
|-------|---------|---------------------|
| `customer` | End-user storefront account | `[]` (empty — ABAC self-service only) |
| `manager` | Human operator | job-function permissions (`product.*`, `user.*`, …) or `admin.*` / `*` (owner) |
| `service` | Machine / integration account | `user.create`, `order.ship`, … per job function |

`admin` is **not** an account type — it is a permission/management tier (`admin.*`, auth ABAC `ClassAdmin`). Write APIs reject `account_type: "admin"`; use `manager` for operators. Startup migrate rewrites any leftover DB `account_type=admin` → `manager`.

Seeded accounts: owner (`OWNER_EMAIL`) → `permissions: ["*"]`, `account_type: manager`; `dupli1-web` → `["user.create"]`; `dupli1-order` → `["order.ship", "order.status.update", "inventory.reservation.manage"]`. `POST /register` defaults to `customer` when `account_type` is omitted.

---

## Gateway

### `GET /gateway/health`

Nginx liveness check — responds without touching any backend service.

**Response `200`** (plain text)
```
ok
```

---

## Auth Service — `/api/v1/auth`

### `GET /health` or `GET /api/v1/auth/health`

Auth service liveness check.

**Response `200`**
```json
{ "status": "ok" }
```

### `GET /settings` or `GET /api/v1/auth/settings`

Non-secret operational settings (auth mode, feature flags, dependency configured flags). Never includes secrets or DSNs.

### `GET /api/v1/auth/.well-known/jwks.json`

RS256 public key set for verifying access tokens issued by auth.

---

### `POST /api/v1/auth/register`

Create a new user account. Requires `user.create`.

**Headers** — `Authorization: Bearer <access_token>`

**Request body**
```json
{
  "email": "user@example.com",
  "password": "minlen8",
  "account_type": "customer"
}
```

| Field | Type | Constraints |
|-------|------|-------------|
| `email` | string | required, valid email |
| `password` | string | required, min 8 chars |
| `account_type` | string | optional; one of `customer`, `manager`, `service`; defaults to `customer`. Do not send `admin` (permission tier — use `manager`). Callers with only `user.create` (no `admin.*` or `*`) may register `customer` only |

**Response `201`**
```json
{ "user_id": "03f95d58-4840-46d4-9c92-fe48364d2e75" }
```

**Errors**
| Status | Meaning |
|--------|---------|
| `400` | Validation failed (bad email, password too short) |
| `401` | Missing or invalid access token |
| `403` | Caller lacks `user.create`, or attempted a disallowed `account_type` / management target |
| `409` | Email already registered |
| `422` | Invalid email, weak password, or invalid `account_type` |

---

### Service account: dupli1-web

The `dupli1-web` BFF uses a seeded machine account with `permissions: ["user.create"]` and `account_type: "service"`. It can call `POST /api/v1/auth/register` to create customer accounts, but cannot manage passwords, permissions, or user status.

Configure on `dupli1-auth` startup:

| Variable | Purpose |
|----------|---------|
| `DUPLI1_WEB_SERVICE_EMAIL` | Service account email (skip seeding when empty) |
| `DUPLI1_WEB_SERVICE_PASSWORD` | Service account password (required when email is set) |

`dupli1-web` should log in with these credentials server-side, cache/refresh the access token, and call register from the backend only — never expose the service password to browsers.

---

### `POST /api/v1/auth/login`

Authenticate and receive a refresh token.

**Request body**
```json
{
  "email": "user@example.com",
  "password": "minlen8"
}
```

**Response `200`**
```json
{
  "refresh_token": "<jwt>"
}
```

**Errors**
| Status | Meaning |
|--------|---------|
| `400` | Missing or malformed body |
| `401` | Invalid credentials |
| `403` | Account locked (customers/managers after 5 failed attempts) or deactivated |

**Lockout:** after **5** consecutive failed logins, `customer` and manager-tier accounts set `locked_at` and further logins return `403` for **15 minutes**, after which the lock auto-expires and failed-attempt counting starts fresh. **Admin** and **owner** accounts are never locked (failed attempts do not set `locked_at`; a stale lock is cleared on the next login attempt). See [permissions.md](permissions.md).

---

### `GET /api/v1/auth/me`

Return the currently authenticated user's **account** (credentials tier — not commerce profile).

**Headers** — `Authorization: Bearer <access_token>`

**Response `200`**
```json
{
  "user_id": "03f95d58-4840-46d4-9c92-fe48364d2e75",
  "email": "user@example.com",
  "account_type": "customer",
  "permissions": [],
  "is_active": true,
  "locked_at": null,
  "failed_login_attempts": 0
}
```

**Errors**
| Status | Meaning |
|--------|---------|
| `401` | Missing, malformed, or expired access token |
| `403` | Account deactivated or locked since the access token was issued |
| `404` | User no longer exists |

---

See the **Profile Service** section below for the commerce profile and saved-addresses API — split out of `auth` into its own `profile` service.

---

### `POST /api/v1/auth/refresh`

Exchange a refresh token for a new access token. The refresh token **rotates**: the one sent in the request is invalidated immediately, and the response carries its replacement. Callers must store the returned `refresh_token` and use it next time — resending the token just spent (or any earlier one) fails with `401`.

**Request body**
```json
{ "refresh_token": "<jwt>" }
```

**Response `200`**
```json
{
  "token": "<access_jwt>",
  "refresh_token": "<new_jwt>"
}
```

**Errors**
| Status | Meaning |
|--------|---------|
| `400` | Missing or malformed body |
| `401` | Refresh token invalid, expired, already rotated/revoked, or the account is deactivated/locked |

---

### `POST /api/v1/auth/logout`

Revoke a refresh token. The access token remains valid until it expires.

**Request body**
```json
{ "refresh_token": "<jwt>" }
```

**Response `204`** — no body

**Errors**
| Status | Meaning |
|--------|---------|
| `400` | Missing or malformed body |
| `500` | Internal error |

---

## Auth Admin — `/api/v1/auth/users`

Requires `Authorization: Bearer <access_token>`.

### `GET /api/v1/auth/users`

List all users. Requires `user.read`. Results are filtered by auth ABAC hierarchy (callers only see accounts they may manage).

**Response `200`**
```json
{
  "users": [
    {
      "user_id": "03f95d58-4840-46d4-9c92-fe48364d2e75",
      "email": "owner@dupli1.com",
      "account_type": "manager",
      "permissions": ["*"],
      "is_active": true,
      "locked_at": null,
      "failed_login_attempts": 0
    }
  ]
}
```

**Errors**
| Status | Meaning |
|--------|---------|
| `401` | Missing or invalid access token |
| `403` | Caller lacks `user.read` or management hierarchy forbids listing |

---

### `PATCH /api/v1/auth/users/{id}/permissions`

Replace the permission list for a user. Requires `user.permissions.update`. Subject to auth ABAC hierarchy (who may manage whom).

**Request body**
```json
{
  "permissions": ["user.password.update", "user.status.update"],
  "account_type": "manager"
}
```

| Field | Type | Constraints |
|-------|------|-------------|
| `permissions` | string[] | required |
| `account_type` | string | optional; one of `customer`, `manager`, `service`. Do not send `admin` (permission tier — use `manager`) |

**Response `200`** — updated user object (includes `account_type`, `permissions`)

**Errors**
| Status | Meaning |
|--------|---------|
| `400` | Missing or malformed body |
| `401` | Missing or invalid access token |
| `403` | Caller lacks `user.permissions.update` or may not manage this user |
| `404` | User not found |
| `422` | Invalid `account_type` or permission string |

---

### `PATCH /api/v1/auth/users/{id}/password`

Set a new password for a user. Requires `user.password.update`.

**Request body**
```json
{ "password": "newpassword" }
```

**Response `204`** — no body

**Errors**
| Status | Meaning |
|--------|---------|
| `400` | Missing or malformed body |
| `401` | Missing or invalid access token |
| `403` | Caller lacks `user.password.update` or may not manage this user |
| `404` | User not found |
| `422` | Password too short (min 8 chars) |

---

### `PATCH /api/v1/auth/users/{id}/status`

Activate or deactivate a user. Requires `user.status.update`.

**Request body**
```json
{ "is_active": false }
```

**Response `200`** — updated user object

**Errors**
| Status | Meaning |
|--------|---------|
| `400` | Missing or malformed body |
| `401` | Missing or invalid access token |
| `403` | Caller lacks `user.status.update` or may not manage this user |
| `404` | User not found |

---

## Profile Service — `/api/v1/profile`

PostgreSQL-backed customer commerce profile (display name, phone) and saved shipping addresses — separated from `auth`'s identity/credentials data so it can evolve and scale independently. Requires `Authorization: Bearer <access_token>`; the owner is always the JWT `sub` claim — self-service only, no dedicated permission (same ABAC pattern as cart). Subscribes to `auth`'s `user.deleted` NATS event to cascade-delete owned PII.

For one release, nginx also aliases the legacy `/api/v1/auth/me/profile` and `/api/v1/auth/me/addresses` paths to `dupli1-profile`, so clients still calling the pre-extraction paths keep working. See [profile-service.md](profile-service.md) for architecture, data model, and the extraction/cutover plan; [auth-profile-extension-plan.md](auth-profile-extension-plan.md) for phase history.

### `GET /api/v1/profile/health`

**Response `200`**
```json
{ "status": "ok" }
```

### `GET /api/v1/profile/me/profile`

**Response `200`**
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
`pccc` (Korea Personal Customs Clearance Code, `P` + 12 digits) is omitted per-address when unset — it only applies to overseas-sourced shipments.

### `PATCH /api/v1/profile/me/profile`

Merge-patch: only sent fields change.

**Request**
```json
{ "display_name": "윤라희", "phone": "010-4112-5167" }
```

**Response `200`** — updated `ProfileView` (same shape as `GET`). Phone is normalized to digits-only.

### Saved addresses

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/profile/me/addresses` | `{ "addresses": [ … ] }` |
| `POST` | `/api/v1/profile/me/addresses` | Create (max **10** per user; first address created is default) |
| `GET` | `/api/v1/profile/me/addresses/{id}` | One address |
| `PATCH` | `/api/v1/profile/me/addresses/{id}` | Partial update (merge patch) |
| `DELETE` | `/api/v1/profile/me/addresses/{id}` | Remove |
| `POST` | `/api/v1/profile/me/addresses/{id}/default` | Set sole default |

**Create/update request** — `recipient_name`, `recipient_phone`, `postal_code` (5 digits), `address_line1`, `city`, `province` required on create; optional `label`, `address_line2`, `pccc`, `is_default`:
```json
{
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
```

`is_default: true` clears the default flag on every other address first (at most one default per user, enforced by a unique partial index). `is_default: false` on `PATCH` un-defaults that address without promoting another — same as what happens when the default address is deleted. Omitting `is_default` on `PATCH` leaves it unchanged.

**Errors**
| Status | Meaning |
|--------|---------|
| `400` | Invalid field (phone, postal code, PCCC format, name/line length) or address limit (10) reached |
| `401` | Missing or invalid access token |
| `404` | Address not found, or not owned by the caller — same code whether it doesn't exist or belongs to someone else, to avoid id enumeration |

---

## Product Service — `/api/v1/products`

### `GET /api/v1/products/health`

Product service liveness check.

**Response `200`**
```json
{ "status": "ok" }
```

---

### `GET /api/v1/products`

Search **parent styles** (one row per style; colors are not duplicated). No authentication required for the public catalog view (active parents only). With a valid Bearer token that includes `product.read` (or `product.*` / `*`), returns all statuses.

| Filter / param | Match type |
|----------------|-----------|
| `q` | case-insensitive substring on name, brand, or description |
| `category` | exact (e.g. `bags`) |
| `subcategory` | exact bag type (`handbags`, `tote`, `shoulder`, `cross`, `mini`; alias `subCategory`) |
| `style` | exact bag occasion (`casual`, `evening`, `business`, `weekend`, `statement`) — not SKU `styleCode` |
| `target` | exact audience (`all`, `men`, `women`, `kids`) |
| `brand` | case-insensitive substring |
| `color` | parent has an active variant with this color |
| `size` | parent has an active variant with this size |
| `material` | exact |
| `tags` | parent must include all listed tags (comma-separated or repeated) |
| `status` | exact (`product.read` or wildcard required) |
| `sort` | `newest` (default), `views` (`popular`), `sold`, `wishlist`, `price`, `name` |
| `order` | `asc` \| `desc` (default `desc`; `name` defaults to `asc`) |
| `period` | `day` \| `week` \| `month` — created within that window (`past_week` / `7d` aliases) |
| `limit` | page size (default `50`, max `100`) |
| `offset` | rows to skip (default `0`) |

Example: `GET /api/v1/products?category=bags&subcategory=tote&style=casual&target=women&sort=views&order=desc&limit=20`

See [product-rich-search.md](product-rich-search.md) and [product-master-catalog.md](product-master-catalog.md).

**Response `200`**
```json
{
  "total": 1,
  "limit": 50,
  "offset": 0,
  "sort": "newest",
  "order": "desc",
  "period": "week",
  "results": [
    {
      "id": "BOT-001",
      "name": "Cassette Bag",
      "description": "...",
      "price": 2500.00,
      "officialPrice": 3200.00,
      "brand": "Bottega Veneta",
      "color": "Green",
      "material": "Leather",
      "stock": 5,
      "category": "bags",
      "subCategory": "handbags",
      "style": "casual",
      "target": "women",
      "capacity": "Medium",
      "tags": ["hot"],
      "viewCount": 12,
      "soldCount": 3,
      "wishlistCount": 1,
      "imageUrls": ["https://cdn.example/bot-001.jpg"]
    }
  ]
}
```

`total` is the full match count before pagination; `results` is the current page.

### Wishlist

| Method | Path | Notes |
|--------|------|-------|
| `PUT` / `POST` | `/api/v1/products/{id}/wishlist` | Add; JWT `sub` or guest cookie |
| `DELETE` | `/api/v1/products/{id}/wishlist` | Remove |
| `GET` | `/api/v1/products/wishlist` | List current owner's items |

---

### `POST /api/v1/coupons/redeem`

Redeem a coupon code. No authentication required.

**Request body**
```json
{ "code": "SUMMER20" }
```

**Response `200`** — coupon object

**Errors**
| Status | Meaning |
|--------|---------|
| `404` | Invalid coupon code |

---

### `GET /api/v1/products/{id}`

Public PDP. No authentication required. Returns an active **parent** with `variants[]`, `availableColors`, and `availableSizes`. Cart lines use each variant's `sku` / `skuId` (inventory key). Parent `price` is the charged amount; `officialPrice` is display-only. Each variant may include `dimensions` (`widthMm` / `heightMm` / `depthMm` in millimeters) — distinct from letter `size`/`sizeCode`; see [product-sku-dimensions.md](product-sku-dimensions.md).

Each embedded variant includes stock enrichment: `availableQty` (`max(0, quantity − reserved)`) and `inStock` (`availableQty > 0`). Every sellable SKU has a `stock_items` row (created with qty 0 on variant create; see [product-stock-tracking-plan.md](product-stock-tracking-plan.md)). Legacy parent `stock` is omitted from responses.

On success, the handler ensures a `dupli1_guest` cookie and records a unique view (one count per guest × product). Response includes public `viewCount` and `soldCount` (units committed on ship — [product-sold-count.md](product-sold-count.md)). View-store failures are logged and do not fail the PDP — see [product-guest-views-plan.md](product-guest-views-plan.md).

**Response `200`** — parent product object with variants (includes `viewCount`, `soldCount`)

**Errors**
| Status | Meaning |
|--------|---------|
| `404` | Product not found or not active |

---

### `GET /api/v1/products/{id}/recommendations`

Public related-product list for PDP. No authentication required. Returns ordered active **parent** cards (seed excluded). Algorithm: same-category content similarity + soft `view_count` boost — see [product-recommendations.md](product-recommendations.md).

**Query**
| Param | Default | Notes |
|-------|---------|-------|
| `limit` | `8` | Clamped 1–24 |

**Response `200`**

```json
{
  "seedId": "BOT-001",
  "items": [ /* parent list cards */ ]
}
```

**Errors**
| Status | Meaning |
|--------|---------|
| `404` | Seed product not found or not active |
| `400` | Invalid `limit` |

---

### Product CRUD (authenticated)

Routes below require `Authorization: Bearer <access_token>`. Product validates RS256 tokens via JWKS (`AUTH_JWKS_URL`). Each route requires a specific permission (wildcards such as `product.*` also grant access).

| Method | Path | Permission |
|--------|------|------------|
| POST | `/api/v1/products` | `product.create` |
| PUT | `/api/v1/products/{id}` | `product.update` |
| DELETE | `/api/v1/products/{id}` | `product.delete` |
| POST | `/api/v1/products/{id}/images` | `product.image.upload` |
| POST | `/api/v1/products/{id}/variants` | `product.variant.create` |
| PUT | `/api/v1/products/{id}/variants/{sku}` | `product.variant.update` |
| DELETE | `/api/v1/products/{id}/variants/{sku}` | `product.variant.delete` |
| POST | `/api/v1/products/{id}/variants/{sku}/images` | `product.image.upload` |
| GET | `/api/v1/products/coupons` | `coupon.read` |
| POST | `/api/v1/products/coupons` | `coupon.create` |
| PUT | `/api/v1/products/coupons/by-code/{code}` | `coupon.update` |
| DELETE | `/api/v1/products/coupons/by-code/{code}` | `coupon.delete` |

`PUT /api/v1/products/{id}` and variant updates **merge**: omitted JSON fields keep their current value, so a partial body cannot blank out data. The trade-off is that a zero value is indistinguishable from an omitted one — sending `price: 0` or `officialPrice: 0` is ignored rather than clearing the price. See [product-price-on-parent.md](product-price-on-parent.md).

New parent `id`s are ULIDs (`domain.NewProductID()`); legacy brand-prefixed ids (e.g. `BOT-001`) remain valid. Human identity is `brandCode` + `styleCode`. Dual variant identity and master dictionaries: [product-sku-system.md](product-sku-system.md) — ULID `skuId` (canonical) + human `sku` (`Brand_Style_Color[_Edition]_Size`). Catalog CRUD at `/api/v1/products/catalog/…` (legacy alias `/api/v1/catalog/…`). Product/variant create requires existing master codes (Phase C). See also [product-variants-plan.md](product-variants-plan.md).

---

## Inventory — `/api/v1/products/inventory` (served by the product service)

Merged into the product service. Each item route has a `by-sku-id/{skuId}` sibling
keyed by the variant's canonical ULID `skuId`. **Reads are public.** Writes require
Bearer JWT when `AUTH_JWKS_URL` is configured.

| Method | Path | Permission |
|--------|------|------------|
| GET | `/api/v1/products/inventory/items/{sku}` | — (public) |
| PUT | `/api/v1/products/inventory/items/{sku}` | `inventory.stock.write` |
| POST | `/api/v1/products/inventory/items/{sku}/adjust` | `inventory.stock.write` |
| GET | `/api/v1/products/inventory/items/by-sku-id/{skuId}` | — (public) |
| PUT | `/api/v1/products/inventory/items/by-sku-id/{skuId}` | `inventory.stock.write` |
| POST | `/api/v1/products/inventory/items/by-sku-id/{skuId}/adjust` | `inventory.stock.write` |
| POST | `/api/v1/products/inventory/reservations` | `inventory.reservation.manage` |
| POST | `/api/v1/products/inventory/reservations/{id}/commit` | `inventory.reservation.manage` |
| POST | `/api/v1/products/inventory/reservations/{id}/release` | `inventory.reservation.manage` |

The legacy prefix `/api/v1/inventory/…` remains registered as an alias (item routes there are `/api/v1/inventory/{sku}`, without the `items` segment) and will be removed once clients migrate.

### `GET /api/v1/products/inventory/health`

**Response `200`**
```json
{ "status": "ok" }
```

### `GET /api/v1/products/inventory/items/{sku}`

Get stock for a SKU.

### `PUT /api/v1/products/inventory/items/{sku}`

Set stock quantity.

**Request body**
```json
{ "quantity": 100 }
```

### `POST /api/v1/products/inventory/items/{sku}/adjust`

Adjust stock by delta.

**Request body**
```json
{ "delta": -5 }
```

### `POST /api/v1/products/inventory/reservations`

Reserve stock for an order.

**Request body**
```json
{
  "order_id": "ord-123",
  "items": [{ "sku": "BOT-001", "quantity": 1 }]
}
```

**Response `201`**
```json
{
  "reservation_id": "...",
  "reservation": { }
}
```

### `POST /api/v1/products/inventory/reservations/{id}/commit`

Commit a reservation (deduct stock).

### `POST /api/v1/products/inventory/reservations/{id}/release`

Release a reservation (return stock).

---

## Cart Service — `/api/v1/cart`

PostgreSQL-backed persistent cart. Enriches lines from product (price, images) and inventory (availability). Does **not** reserve stock or create orders. `UpsertItem` / `ReplaceItems` reject when requested quantity exceeds available (including missing stock ⇒ available 0) with `400` and `reason: insufficient_stock`.

When `AUTH_JWKS_URL` or `JWT_SECRET` is set, cart routes require `Authorization: Bearer <access_token>`. The cart owner is the JWT `sub` claim — do not send `customer_id` on `/api/v1/cart` mutations.

See [cart-service.md](cart-service.md) for architecture, service boundaries, and checkout handoff.

### `GET /api/v1/cart/health`

**Response `200`**
```json
{ "status": "ok" }
```

### Cart (current user)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/cart` | Get my cart |
| DELETE | `/api/v1/cart` | Clear my cart |
| PUT | `/api/v1/cart/items` | Replace all items |
| POST | `/api/v1/cart/items` | Add or update one item |
| DELETE | `/api/v1/cart/items/{sku}` | Remove line by human `sku` |
| DELETE | `/api/v1/cart/items/by-sku-id/{skuId}` | Remove line by canonical `skuId` |

**Add item request**
```json
{ "sku": "BOT-001-BLK", "quantity": 1 }
```

**Cart response** (enriched)
```json
{
  "customer_id": "uuid",
  "items": [
    {
      "sku": "BOT-001-BLK",
      "product_id": "BOT-001",
      "quantity": 1,
      "unit_price_cents": 125000,
      "color": "Black",
      "available_qty": 3
    }
  ],
  "unavailable_items": [],
  "subtotal_cents": 125000,
  "updated_at": "2026-07-05T12:00:00Z"
}
```

Lines that fail variant enrichment stay in `items` with `available: false` and are listed in `unavailable_items` (`sku_id`, `sku`, `reason`). Item mutations that cannot resolve variants return **`422`** with the same `unavailable_items` array (and `error: "variant not found"`). See [cart-service.md](cart-service.md).
### Admin

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/cart/customers/{customer_id}` | Get a customer's cart (`cart.read`); legacy alias `/api/v1/carts/{customer_id}` |

### Product variant lookup (used by cart)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/products/variants?sku_ids=` | Batch public active variants by canonical `skuId` (comma-separated, max 50). Response `{items, missing}`. |
| GET | `/api/v1/products/variants/by-sku/{sku}` | Public active variant by human SKU (legacy alias: `/api/v1/variants/{sku}`) |
| GET | `/api/v1/products/variants/by-sku-id/{skuId}` | Public active variant by canonical ULID (legacy alias: `/api/v1/variants/by-sku-id/{skuId}`) |

---

## Order Service — `/api/v1/orders`

PostgreSQL-backed (`DUPLI1_ORDER_DB`; in-memory fallback only when no DB URL is set, for tests). Calls inventory to reserve stock and product to redeem coupons.

When `AUTH_JWKS_URL` or `JWT_SECRET` is set, order and checkout routes require `Authorization: Bearer <access_token>` (RS256 via auth JWKS when configured; HS256 fallback in dev).

**Storefront ABAC:** callers with empty `permissions` may only access their own `customer_id` / checkout session (`sub` must match). `order.create` bypasses create ABAC; `order.read.all` bypasses read/list ABAC. See [permissions.md](permissions.md).

**Pricing.** Orders and checkout sessions price as:

```
total_cents = subtotal_cents - discount_cents + shipping_fee_cents
```

`shipping_fee_cents` is a flat per-order delivery charge in whole KRW, set by `DUPLI1_ORDER_SHIPPING_FEE_CENTS` on the order service. It defaults to **30000** (30,000 KRW); set the variable to `0` for free delivery.

The charge applies to every order regardless of size — there is no free-shipping threshold. A coupon discounts **goods only** and is capped at `subtotal_cents`, so the total can never drop below the shipping fee: a 100%-off coupon still pays delivery. An empty checkout session quotes `total_cents: 0` rather than a bare delivery charge; the fee appears once the session has at least one item.

The fee is **snapshotted** on the checkout session when it opens; `complete` charges that quoted fee even if the configured amount changed. Direct `POST /orders` uses the current configured fee. Orders created before this feature carry `shipping_fee_cents: 0` and keep their original totals.

Because `total_cents` is what the payment service charges and what the order requires to mark itself paid, the fee flows through the money path automatically.

See [checkout-session.md](checkout-session.md) for the full checkout flow.

### `GET /api/v1/orders/health`

**Response `200`**
```json
{ "status": "ok" }
```

### Checkout sessions

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/orders/checkout/sessions` | Create checkout session |
| GET | `/api/v1/orders/checkout/sessions/{id}` | Get session |
| PUT | `/api/v1/orders/checkout/sessions/{id}/items` | Replace all items |
| POST | `/api/v1/orders/checkout/sessions/{id}/items` | Add or update one item |
| DELETE | `/api/v1/orders/checkout/sessions/{id}/items/{sku}` | Remove item by human `sku` |
| DELETE | `/api/v1/orders/checkout/sessions/{id}/items/by-sku-id/{skuId}` | Remove item by canonical `skuId` |
| POST | `/api/v1/orders/checkout/sessions/{id}/coupon` | Apply coupon |
| POST | `/api/v1/orders/checkout/sessions/{id}/complete` | Complete checkout → order |

The legacy prefix `/api/v1/checkout/sessions…` is still registered as an alias for every route above and will be removed once the storefront and admin clients migrate.

### Orders

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/orders` | Create order directly |
| GET | `/api/v1/orders` | List all orders (`order.read.all`) |
| GET | `/api/v1/orders?customer_id=` | List customer orders |
| GET | `/api/v1/orders/{id}` | Get order |
| POST | `/api/v1/orders/{id}/ship` | `order.ship` — ship order (`paid` → `in_transit`); body requires `carrier` + `tracking_number` (`carrier_note` when `carrier=other`) |
| PUT | `/api/v1/orders/{id}/status` | `order.status.update` — cancel or fulfill |

**Create order request**
```json
{
  "customer_id": "cust-1",
  "items": [{ "sku_id": "01J9Z…", "quantity": 1 }]
}
```

Identify each line by canonical `sku_id` (preferred) or human `sku`. Unit prices are **resolved server-side** from the catalog; `unit_price_cents` is not part of the request body and is ignored if sent.

**Status machine**

| From | To | Trigger |
|------|----|---------|
| — | `pending` | Order created |
| `pending` | `paid` | `payment.succeeded` consumer or bypass payment — **payment-driven only**, no client route |
| `paid` | `in_transit` | `POST /api/v1/orders/{id}/ship` (commits reserved stock) |
| `in_transit` | `fulfilled` | `PUT /api/v1/orders/{id}/status` with `fulfilled` |
| `pending`, `paid` | `canceled` | `PUT /api/v1/orders/{id}/status` with `canceled`, or the unpaid-expiry worker |

`PUT /status` accepts only `canceled` and `fulfilled`; use `POST /ship` to reach `in_transit`. There is no `confirmed` status — it was replaced by `paid` and `in_transit`. See [payment-service.md](payment-service.md).

---

## Payment Service — `/api/v1/payments`

Credit card uses **NANO Solution** certified payment when `NANO_*` credentials are set; otherwise `credit_card` is unavailable (501) and payments — including local testing — go through manager **Bypass**. Dupli1 never handles card numbers, CVC, or card passwords.

**Methods:** create body accepts `method`: `credit_card` (NANO; 501 when unconfigured), `bypass` (order manager / `payment.bypass`), `bitcoin` (501 until implemented). See [payment-methods-plan.md](payment-methods-plan.md).

When JWT is configured, `POST` and `GET` require Bearer tokens. Storefront callers may only pay for / read their own orders unless they hold `payment.create` or `payment.read.all`.

| Method | Path | Permission / rule |
|--------|------|-------------------|
| POST | `/api/v1/payments` | ABAC or `payment.create`; `method=bypass` requires `payment.bypass` |
| GET | `/api/v1/payments/{id}` | ABAC or `payment.read.all` |
| POST | `/api/v1/payments/{id}/cancel` | `payment.cancel` — **staff only, no ABAC** |

**Create payment**
```json
{ "order_id": "ord_000001", "method": "credit_card" }
```

**Bypass (manage-web / order manager)**
```json
{ "order_id": "ord_000001", "method": "bypass", "note": "Cash received" }
```
Returns `status: "succeeded"` immediately and publishes `payment.succeeded` (no `checkout_url`).

**Cancel / refund payment**

`POST /api/v1/payments/{id}/cancel` refunds a `succeeded` payment at the PG (NANO `/api/payment/cancel.io`). Requires `payment.cancel`; there is no ABAC path, so a customer can never refund their own payment.

```json
{ "amount_cents": 20000, "reason": "ops reject" }
```

Both fields are optional and an empty body is valid: omitting `amount_cents` (or sending `0`) cancels the **full remaining balance**. Send an `Idempotency-Key` header to make a retry of the same cancel a no-op — strongly recommended for partial cancels, which local state alone cannot distinguish from a deliberate second refund.

A full cancel moves the payment to `canceled`. A **partial** cancel leaves it `succeeded` with a reduced remaining balance, matching NANO's `remainAmt` semantics; repeat partials until the balance reaches zero, at which point the payment becomes `canceled`. The response is the updated payment, including `canceled_amount_cents` (cumulative), `canceled_at`, `cancel_reason`, and `canceled_by`.

| Status | Meaning |
|--------|---------|
| `200` | Cancel accepted by the PG and recorded |
| `400` | `amount_cents` negative or above the remaining balance |
| `403` | Caller lacks `payment.cancel` |
| `404` | No such payment |
| `409` | Payment is not cancelable (not `succeeded`, or already fully canceled) |
| `501` | Provider has no cancel API / PG not configured |
| `502` | PG was reached and refused the cancel — payment left unchanged |

`bypass` payments never touched a PG, so they are canceled locally only and the matching refund is made out of band.

The cancel publishes **`payment.canceled`** (NATS, via the payment outbox). Order cancels a still-`paid` order on a full refund when `remaining_cents` is present and `0` and `payment_id` matches; notification alerts ops. Concurrent cancels of the same payment serialize on a row lock so NANO is not called twice.

Unpaid `pending` orders auto-cancel after **5 minutes**. Full design: [payment-service.md](payment-service.md).

---

## Notification Service

Health and settings (`GET /health`, `GET /api/v1/notification/health`, `GET /settings`, `GET /api/v1/notification/settings`). Outbound ops alerts are driven by NATS subscriptions (Telegram when configured). Inbound Telegram uses `POST /api/v1/notification/telegram/webhook` (production, requires `TELEGRAM_WEBHOOK_SECRET`) or `getUpdates` polling (local). Managers manage subscriptions at `/api/v1/notification/telegram/subscriptions` (`notification.telegram.read` / `notification.telegram.manage`). Full runbook: [notification-telegram-bot.md](notification-telegram-bot.md).

---

## Common error shape

All error responses use a JSON envelope:

**Auth service** (Gin)
```json
{ "error": "human-readable message" }
```

**Other services** (stdlib)
```json
{ "error": "human-readable message", "code": 400 }
```

---

## Quick reference

Permission strings are authoritative; see [permissions.md](permissions.md). `—` = no auth. `Bearer` = valid access token. `Bearer*` = required when JWT is configured on the service.

| Method | Path | Permission / auth | Service |
|--------|------|-------------------|---------|
| GET | `/gateway/health` | — | nginx |
| GET | `/api/v1/auth/health` | — | auth |
| GET | `/api/v1/auth/settings` | — | auth |
| GET | `/api/v1/auth/.well-known/jwks.json` | — | auth |
| POST | `/api/v1/auth/register` | `user.create` | auth |
| POST | `/api/v1/auth/login` | — | auth |
| GET | `/api/v1/auth/me` | Bearer | auth |
| POST | `/api/v1/auth/refresh` | — | auth |
| POST | `/api/v1/auth/logout` | — | auth |
| GET | `/api/v1/auth/users` | `user.read` | auth |
| PATCH | `/api/v1/auth/users/{id}/permissions` | `user.permissions.update` | auth |
| PATCH | `/api/v1/auth/users/{id}/password` | `user.password.update` | auth |
| PATCH | `/api/v1/auth/users/{id}/status` | `user.status.update` | auth |
| GET | `/api/v1/profile/health` | — | profile |
| GET | `/api/v1/profile/settings` | — | profile |
| GET/PATCH | `/api/v1/profile/me/profile` | Bearer | profile |
| GET/POST | `/api/v1/profile/me/addresses` | Bearer | profile |
| GET/PATCH/DELETE | `/api/v1/profile/me/addresses/{id}` | Bearer | profile |
| POST | `/api/v1/profile/me/addresses/{id}/default` | Bearer | profile |
| GET | `/api/v1/products/health` | — | product |
| GET | `/api/v1/products/settings` | — | product |
| GET | `/api/v1/products` | optional `product.read` | product |
| GET | `/api/v1/products/{id}` | — | product |
| POST | `/api/v1/products/coupons/redeem` | — | product |
| POST | `/api/v1/products` | `product.create` | product |
| PUT/DELETE | `/api/v1/products/{id}` | `product.update` / `product.delete` | product |
| POST | `/api/v1/products/{id}/images` | `product.image.upload` | product |
| POST | `/api/v1/products/{id}/variants` | `product.variant.create` | product |
| PUT/DELETE | `/api/v1/products/{id}/variants/{sku}` | `product.variant.update` / `product.variant.delete` | product |
| POST | `/api/v1/products/{id}/variants/{sku}/images` | `product.image.upload` | product |
| GET/POST | `/api/v1/products/coupons` | `coupon.read` / `coupon.create` | product |
| PUT/DELETE | `/api/v1/products/coupons/by-code/{code}` | `coupon.update` / `coupon.delete` | product |
| GET | `/api/v1/products/inventory/health` | — | product |
| GET | `/api/v1/products/inventory/settings` | — | product |
| GET | `/api/v1/products/inventory/items/{sku}` | — | product |
| PUT | `/api/v1/products/inventory/items/{sku}` | `inventory.stock.write` | product |
| POST | `/api/v1/products/inventory/items/{sku}/adjust` | `inventory.stock.write` | product |
| POST | `/api/v1/products/inventory/reservations` | `inventory.reservation.manage` | product |
| POST | `/api/v1/products/inventory/reservations/{id}/commit` | `inventory.reservation.manage` | product |
| POST | `/api/v1/products/inventory/reservations/{id}/release` | `inventory.reservation.manage` | product |
| POST/GET | `/api/v1/orders/checkout/sessions` | ABAC / `order.create` / `order.read.all` | order |
| GET | `/api/v1/orders/health` | — | order |
| GET | `/api/v1/orders/settings` | — | order |
| GET/PUT/POST/DELETE | `/api/v1/orders/checkout/sessions/{id}/...` | ABAC (same as orders) | order |
| POST/GET | `/api/v1/orders` | ABAC / `order.create` / `order.read.all` | order |
| GET | `/api/v1/orders/{id}` | ABAC / `order.read.all` | order |
| POST | `/api/v1/orders/{id}/ship` | `order.ship` | order |
| PUT | `/api/v1/orders/{id}/status` | `order.status.update` | order |
| GET | `/api/v1/cart/health` | — | cart |
| GET | `/api/v1/cart/settings` | — | cart |
| GET/POST/PUT/DELETE | `/api/v1/cart/*` | Bearer (own `sub`) | cart |
| GET | `/api/v1/cart/customers/{customer_id}` | `cart.read` | cart |
| GET | `/api/v1/payments/health` | — | payment |
| GET | `/api/v1/payments/settings` | — | payment |
| POST/GET | `/api/v1/payments` | ABAC / `payment.create` / `payment.read.all` | payment |
| POST | `/api/v1/payments/{id}/cancel` | `payment.cancel` | payment |
| GET | `/api/v1/notification/health` | — | notification |
| GET | `/api/v1/notification/settings` | — | notification |
| POST | `/api/v1/notification/telegram/webhook` | webhook secret header | notification |
| GET | `/api/v1/notification/telegram/subscriptions` | `notification.telegram.read` | notification |
| POST | `/api/v1/notification/telegram/subscriptions` | `notification.telegram.manage` | notification |
| POST | `/api/v1/notification/telegram/subscriptions/{id}/accept` | `notification.telegram.manage` | notification |
| POST | `/api/v1/notification/telegram/subscriptions/{id}/reject` | `notification.telegram.manage` | notification |
| DELETE | `/api/v1/notification/telegram/subscriptions/{id}` | `notification.telegram.manage` | notification |
