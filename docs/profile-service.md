# Profile Service

The **profile service** (`dupli1-profile`) owns customer **commerce PII**: display name, phone, and saved shipping addresses used to prefill checkout. It does **not** own login credentials, permissions, or account status — that stays in **auth**. It does **not** create orders or snapshot shipping data at purchase time — that stays in **order** (see [checkout-session.md](checkout-session.md)).

Profile was extracted out of `auth` (Phase D of [auth-profile-extension-plan.md](auth-profile-extension-plan.md)); Phases A–B of that plan describe the original design intent and are still the source of truth for *why* this data model looks the way it does.

## Role in the purchase flow

```mermaid
flowchart LR
    subgraph identity["Identity"]
        Auth["dupli1-auth<br/>(credentials, JWT)"]
    end

    subgraph commerce["Commerce PII (persistent)"]
        Profile["dupli1-profile<br/>(display name, phone, addresses)"]
    end

    subgraph checkout["Checkout (short-lived)"]
        Order["dupli1-order"]
    end

    Auth -.->|"user.deleted (NATS)"| Profile
    Profile -.->|"client copies a chosen address"| Order
    Order -->|"snapshots recipient + shipping_address on complete"| Order
```

Profile is **read by the client, not by order**: the storefront calls `GET /api/v1/profile/me/profile`, lets the customer pick or edit an address, then sends that address as part of the checkout-session `complete` body. Order stores its own immutable snapshot (`recipient_name`, `recipient_phone`, `shipping_address`) and never calls profile server-to-server — so profile can be down, slow, or mid-migration without blocking checkout. The only server-to-server coupling is one-directional: auth publishes `user.deleted`, profile subscribes and cascade-deletes.

---

## Service boundaries

### What profile owns

- One profile row per user (`user_id` = JWT `sub`): `display_name`, `phone`
- Up to **10** saved addresses per user, one of which may be `is_default`
- CRUD at `/api/v1/profile/me/profile` and `/api/v1/profile/me/addresses`

### What profile does **not** own

| Concern | Owner | Notes |
|---------|-------|-------|
| Login, password, JWT issuance | `dupli1-auth` | Profile only *validates* tokens (JWKS), never issues them |
| Permissions, account status, account deletion | `dupli1-auth` | `DELETE /api/v1/auth/users/:id` publishes `user.deleted`; profile reacts, doesn't decide |
| Per-order recipient/shipping snapshot | `dupli1-order` | Immutable at purchase time — see [checkout-session.md](checkout-session.md) §"Order schema" |
| Address validation logic reuse | Not shared (yet) | `order/pkg/domain/shipping.go` keeps its own copy of the KR phone/postal/PCCC regexes for its snapshot, independent of profile — see "Out of scope" in [auth-profile-extension-plan.md](auth-profile-extension-plan.md) Phase D |

There is no runtime dependency from `order` (or any other service) *into* profile. The only inbound coupling is the `user.deleted` NATS subscription described below.

---

## Extraction status (read before relying on this in prod)

Profile's code, deploy wiring, and event-driven cleanup are done. Two migration steps from [auth-profile-extension-plan.md](auth-profile-extension-plan.md) Phase D are **not**:

1. **One-time data copy** — the `pg_dump | psql` copy of `auth`'s pre-extraction `customer_profiles`/`customer_addresses` rows into profile's `profiles` database has not run. Any address saved through `auth` before the cutover is not visible through `profile` today.
2. **Drop `auth`'s orphan tables** — `auth` no longer creates or serves `customer_profiles`/`customer_addresses` (`auth/pkg/bootstrap/migrate.go` only leaves a comment), but the tables themselves still physically exist in `dupli1_db` pending confirmation that cutover is stable.

Everything else in Phase D's checklist — service scaffold, Compose/nginx/CI/Terraform wiring, `user.deleted` publish/subscribe, `DELETE /api/v1/auth/users/:id` — is live.

---

## Authentication

Profile does not issue tokens — unlike `auth`'s own `RequireAuth()` (which is also the token issuer), profile validates the way `cart`/`order`/`product`/`payment` do:

1. `POST /api/v1/auth/login` → `refresh_token`
2. `POST /api/v1/auth/refresh` → `token` (access JWT)
3. Send `Authorization: Bearer <token>` on profile requests

`authjwt.NewAccessTokenValidator(cfg.JWKSURL, cfg.JWTSecret)` fetches RS256 keys from `AUTH_JWKS_URL` (Compose: `http://dupli1-auth:8080/api/v1/auth/.well-known/jwks.json`), with `JWT_SECRET` as an HS256 dev fallback.

The resource owner is always **`sub` from the JWT** — there is no request field for `user_id`; every route operates on the caller's own data. Requesting another user's address by `{id}` returns **`404`**, not `403` — same "don't reveal existence" pattern as cart and order, enforced by scoping every repository query on `(user_id, id)` rather than checking ownership after a lookup.

No new permission exists for self-service, matching cart's pattern. There is currently no manager/support read endpoint for another customer's profile (deferred in the original plan, pending manage-web needing it).

---

## Data model

PostgreSQL, migrated inline on startup (`postgres.Migrate`, safe to re-run):

```sql
CREATE TABLE customer_profiles (
    user_id      TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    phone        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE customer_addresses (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    label           TEXT NOT NULL DEFAULT '',
    recipient_name  TEXT NOT NULL,
    recipient_phone TEXT NOT NULL,
    postal_code     TEXT NOT NULL,
    address_line1   TEXT NOT NULL,
    address_line2   TEXT NOT NULL DEFAULT '',
    city            TEXT NOT NULL,
    province        TEXT NOT NULL,
    pccc            TEXT NOT NULL DEFAULT '',
    is_default      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_customer_addresses_user_id ON customer_addresses (user_id);
CREATE UNIQUE INDEX idx_customer_addresses_one_default
    ON customer_addresses (user_id) WHERE is_default;
```

Unlike `auth`'s pre-extraction copy of this schema, there is **no `REFERENCES users(id)` foreign key** — profile owns no FK into auth's database (different Postgres instance entirely). Account deletion cleanup runs through the `user.deleted` event instead of `ON DELETE CASCADE`.

The unique partial index enforces **at most one default address per user** at the database level, not just in application code.

### Validation (KR-first, unchanged from Phase A)

| Field | Rule |
|-------|------|
| `display_name` / `recipient_name` | 1–50 chars, trimmed |
| `phone` / `recipient_phone` | Digits only after stripping formatting; Korean mobile pattern (`01[0-9]{8,9}`) |
| `postal_code` | Exactly 5 digits |
| `address_line1` | Required, max 200 chars |
| `address_line2` | Optional, max 200 chars |
| `city` / `province` | Required, max 100 chars |
| `pccc` | Optional; when present, `P` + 12 digits (Korea Personal Customs Clearance Code for overseas-purchase shipments), normalized to uppercase |
| Addresses per user | Max **10** |

---

## API

Base path: **`/api/v1/profile/me/…`** on `dupli1-profile` (port **8088** locally, gateway **8080**). For one release, nginx also aliases `/api/v1/auth/me/profile` and `/api/v1/auth/me/addresses` to the same service so clients mid-migration keep working — remove once the storefront is fully cut over.

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
Returns defaults (empty `display_name`/`phone`, empty `addresses`) when the user has never touched their profile — no 404 for "no profile yet."

---

### `PATCH /api/v1/profile/me/profile`

JSON merge-patch — only sent fields change.

**Request**
```json
{ "display_name": "윤라희", "phone": "010-4112-5167" }
```

**Response `200`** — updated `ProfileView` (same shape as `GET`). Phone is normalized to digits before storage. Creates the profile row lazily on first patch.

---

### `GET /api/v1/profile/me/addresses`

**Response `200`** — `{ "addresses": [ … ] }`, same address shape as above, ordered default-first then oldest-first.

---

### `POST /api/v1/profile/me/addresses`

**Request** — `recipient_name`, `recipient_phone`, `postal_code`, `address_line1`, `city`, `province` required; `label`, `address_line2`, `pccc`, `is_default` optional:
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

**Response `201`** — created address. The **first** address a user creates is always default regardless of the `is_default` field. `is_default: true` on any later create clears every other address's default flag first (one DB round trip, then the insert — see the concurrency note below).

Rejects with **`400`** (`ErrAddressLimitReached`) at the 11th address.

---

### `GET` / `PATCH` / `DELETE /api/v1/profile/me/addresses/{id}`

- `GET` — one address; `404` if missing or owned by someone else.
- `PATCH` — partial update (unset fields keep their existing value). `is_default: true` promotes this address and clears the others; **`is_default: false` un-defaults this address without promoting another** — "no default" is a valid state, same as what happens after deleting the default address. Omitting `is_default` entirely leaves it unchanged.
- `DELETE` — removes the row (`204`). If it was the default, no other address is auto-promoted.

---

### `POST /api/v1/profile/me/addresses/{id}/default`

Sets this address as the sole default, clearing any other. **Response `200`** — the now-default address.

---

## Concurrency note

`ClearDefaultAddresses` and the address write that follows it (`CreateAddress`, `PatchAddress` with `is_default: true`, `SetDefaultAddress`) are two separate statements, not one transaction. Two concurrent "make default" requests for different addresses can interleave so both attempt `is_default = true`; the second hits the unique partial index and fails with a raw Postgres constraint error (surfaced as a `500`, not a clean `409`). This is a pre-existing characteristic carried over from the Phase A implementation in `auth` — noted here as a known edge case, not something the extraction introduced or fixed.

---

## Events

| Event | Direction | Behavior |
|-------|-----------|----------|
| `user.deleted` (`shared/pkg/events.UserDeleted`) | Subscribe | `profile/pkg/consumer/user_deleted.go` decodes the payload and calls `DeleteUserData`, which deletes all of that user's addresses and their profile row in one transaction. Idempotent — safe under NATS at-least-once redelivery. |

The broker is started with `--auth`. Clients send `NATS_TOKEN` (Compose default `dupli1_nats_dev`; production Secrets Manager `dupli1/production/nats-token`). A TCP client without that token cannot publish `user.deleted`. A compromised Dupli1 service that already has the token still can — this is bus authorization, not per-subject HMAC.

Publishing side: `auth`'s `DELETE /api/v1/auth/users/:id` (`user.delete` permission) writes this event to `auth_outbox` in the same transaction as the user-row delete, then a drain worker publishes to NATS. The delete fails if that outbox write cannot be persisted. Profile is the only known subscriber today.

Profile does **not** publish its own `profile.updated` / `profile.address.created` events — those were scoped as optional in Phase A.2 and were never implemented; nothing currently consumes profile changes.

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `DUPLI1_PROFILE_ADDR` | `:8088` | Listen address |
| `DUPLI1_PROFILE_DB` | — | Postgres URL (`profiles` database); omit for in-memory (tests). `DB_URL` also accepted. |
| `AUTH_JWKS_URL` | — | RS256 JWKS endpoint on auth |
| `JWT_SECRET` | — | HS256 dev fallback validator |
| `DUPLI1_PROFILE_NATS_URL` | — | NATS URL for the `user.deleted` subscription; `NATS_URL` also accepted. Subscription is skipped entirely if unset — deleted users' PII then only gets cleaned up once this is configured. |
| `NATS_TOKEN` | — | Token matching nats-server `--auth`. Required in Compose/ECS; omit only for in-process tests. |

Local Postgres: `postgres://dupli1:dupli1_dev@localhost:5439/profiles?sslmode=disable`

---

## Package layout

| Path | Role |
|------|------|
| `profile/pkg/domain/profile.go` | `Profile`, `Address`, `ProfileView` entities + KR validation/normalization |
| `profile/pkg/service/service.go` | Use cases: profile merge-patch, address CRUD, default-address logic, `DeleteUserData` |
| `profile/pkg/ports/profile_repository.go` | Repository interface |
| `profile/pkg/ports/event_subscriber.go` | NATS subscription interface |
| `profile/pkg/infra/postgres/profile_repository.go` | Postgres persistence + inline schema migration |
| `profile/pkg/infra/memory/profile_repository.go` | In-memory repository for tests / no-DB fallback |
| `profile/pkg/infra/nats/subscriber.go` | NATS subscriber adapter |
| `profile/pkg/consumer/user_deleted.go` | Wires `events.UserDeletedEvent` → `service.DeleteUserData` |
| `profile/pkg/handler/http.go` | stdlib `net/http` routes (not Gin — see framework note below) |
| `profile/pkg/bootstrap/bootstrap.go` | Wiring: DB/memory repo → service → handler → NATS subscription |
| `shared/pkg/authjwt` | JWT validation (shared with cart/order/payment/product) |
| `shared/pkg/authmiddleware` | `RequireAuth` bearer-token middleware |
| `shared/pkg/events` | `UserDeleted` subject + `UserDeletedEvent` payload contract |

**Framework note:** `auth`'s pre-extraction handler used Gin, because it lived inside auth's Gin-based router. Profile is a standalone service and follows the stdlib `net/http` convention used by cart/order/payment/product/notification — only the handler layer was rewritten during extraction; `domain`/`service`/`ports`/`infra` moved verbatim.

---

## Errors

| Status | Condition |
|--------|-----------|
| `400` | Invalid field (phone/postal code/PCCC format, name/line length) or address limit (10) reached |
| `401` | Missing or invalid access token |
| `404` | Address not found, or belongs to a different user — same code either way, to avoid id enumeration |
| `500` | Persistence failure, or a concurrent default-address race (see concurrency note above) |

---

## Related documentation

- [auth-profile-extension-plan.md](auth-profile-extension-plan.md) — original design (Phases A–B) and the extraction plan (Phase D) this service implements
- [checkout-session.md](checkout-session.md) — where a chosen address becomes an order's immutable shipping snapshot
- [api.md](api.md) — full request/response reference
- [endpoints.md](endpoints.md) — route index
- [service-layout.md](service-layout.md) — repo layout
- [current-state.md](current-state.md) — service status snapshot

## Planned / open work

| Item | Status |
|------|--------|
| One-time data copy from auth's orphaned tables + dual-run verification | Not started |
| Drop `customer_profiles`/`customer_addresses` from auth's database | Blocked on the above |
| Storefront cutover from `/api/v1/auth/me/...` to `/api/v1/profile/me/...` | Frontend work, not tracked here |
| Manager/support read of another customer's profile | Deferred (no `profile.read` permission defined yet) |
| `profile.updated` / `profile.address.created` events | Deferred — no consumer needs them yet |
