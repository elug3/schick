# TODO

**Release boundary:** [v1.0-release-spec.md](v1.0-release-spec.md) (v1.0 closeout checklist) · [v1-release-plan.md](v1-release-plan.md) (narrative) · [v1.1-release-plan.md](v1.1-release-plan.md) (post-launch).

## Schedule

| When | Milestone |
|------|-----------|
| **By 2026-08-30** | Dupli1 **v1.0** released — [v1.0-release-spec.md](v1.0-release-spec.md) |
| **September 2026** | **Wallet** products added — [product-multi-category-design.md](product-multi-category-design.md) |
| **October 2026** | **Padded** products added (bag line catalog expansion) |

Full narrative: [v1-release-plan.md § Schedule](v1-release-plan.md#schedule).

## v1.0 closeout (**target 2026-08-30** — was postponed 2026-07-27)

**Do not tag v1.0** until every open item below and in [v1.0-release-spec.md](v1.0-release-spec.md) is resolved.

**Checklist:** [v1.0-release-spec.md](v1.0-release-spec.md) — ops (A), smoke (B), backend (C), `dupli1-web` (D), `dupli1-manage-web` (E), sign-off (F).

Backend section C is **done in the repo** (auth register soft-success, notification handler
logging, `api.md` status machine, OpenAPI refresh), as is the code and Terraform for the
persistent JWT signing key. Operator steps: [launch runbook](v1-release-plan.md#launch-runbook-section-a).

Open highlights:

- [x] Create the JWT signing-key secret and set `jwt_private_key_secret_arn` — prod `ephemeral_jwt_key` is `false` (A6)
- [x] Product images CDN applied on prod (A2–A3) — `imageUrls` use CloudFront
- [x] Card PG / NANO (A4) — NANO when configured; launch without NANO = Bypass
- [x] Telegram wired (A7); gateway uses `nginx.ecs.conf` (A8)
- [x] A5 — moot: dev simulate removed from the codebase entirely (merged into Bypass); no flag to leave unset
- [ ] Catalog prices not zeroed (A9) — sample product currently `price: 0`
- [ ] Prod money-path smoke — Bypass path via `scripts/smoke-money-path.sh` (A10, B)
- [ ] Frontends: canonical paths, parent pricing, `skuId` (D, E)
- [ ] Tag `v1.0` (F4)

## Blocked on a third party

- [x] **NANO card payments can succeed without a PG-signed return** — 인증결제 v2.7 does not
  put `hashValue`/`timestamp` on the callback. Checkout binds `receiveUrl` with a
  payment-scoped MAC (`nano_ts`/`nano_mac`). Record:
  [payment-nano-return-verification.md](payment-nano-return-verification.md).
  ([#232](https://github.com/elug3/dupli1/issues/232))
  - Shopper-facing damage for refused callbacks is shipped
    ([dupli1#233](https://github.com/elug3/dupli1/pull/233)).
    [dupli1-web#108](https://github.com/elug3/dupli1-web/pull/108) is independent.
    of NANO's answer.

## v1.1 (post-launch — logging, sessions, access control, deployment, automation)

See [v1.1-release-plan.md](v1.1-release-plan.md) for full slices and exit criteria.

- [ ] **Structured API errors + zerolog** — stable machine `code`; shared `shared/pkg/log` zerolog factory on all services — [product-error-wrapping.md](product-error-wrapping.md)
- [ ] **Consistent sessions** — BFF holds refresh; `dupli1_session` HttpOnly cookie contract — [api.md](api.md)
- [ ] **Verify access control** — ABAC + permissions matrix; negative tests on money path — [permissions.md](permissions.md)
- [ ] **Revocable session from BFF** — logout revokes refresh in Redis + clears cookie — `dupli1-web`
- [ ] **AWS deployment alignment** — [aws-cost-reduction-plan.md](aws-cost-reduction-plan.md), [deployment-aws.md](deployment-aws.md)
- [ ] **CI/CD automation** — backend OIDC, frontend task-def alignment, post-deploy smoke

## v1.2 (commerce & product — deferred)

Guest cart, refunds, co-view, legacy alias removal, manager settings, Redis cache — see v1.1 plan “Deferred to v1.2” table.

- [ ] **Promo / referral codes** — harden coupons (expiry, caps, ledger) + sales attribution by code/partner; no separate referral service — [product-promo-referral-code-plan.md](product-promo-referral-code-plan.md)

## Notification service (reviewed 2026-08-07)

Completeness / code-quality follow-ups for `dupli1-notification`. Design: [notification-telegram-bot.md](notification-telegram-bot.md).

### Bugs / correctness

- [x] **Pending `/start` reply silently dropped** — fixed: `Client.Reply` bypasses outbound `AllowsChat` for `/start` acks; `TestUpdateProcessorStartPending` sets access policy on the client.
- [ ] **Any inbound Telegram message creates a pending subscription** — discoverable bots can fill `telegram_subscriptions`; consider registering only on `/start` (or rate-limit / require allowlisted user for upsert).

### Production / ECS wiring

- [ ] **Wire `DUPLI1_NOTIFICATION_DB` in ECS** — Terraform task def has no notification DB URL; service falls back to in-memory repo (subscriptions lost on restart). Add RDS `notifications` DB + Secrets Manager + task secret (mirror cart/payment).
- [ ] **Wire `AUTH_JWKS_URL` on notification ECS task** — without JWKS, manager subscription API returns 503.
- [ ] **Wire Telegram webhook in prod** — set `TELEGRAM_WEBHOOK_URL` + `TELEGRAM_WEBHOOK_SECRET` (today: polling only).

### Reliability / product gaps

- [ ] **NATS queue group for notification** — use `QueueSubscribe` so multi-replica ECS does not duplicate Telegram alerts (deferred in [v1-release-plan.md](v1-release-plan.md); log-first done).
- [ ] **Fan-out to multiple accepted chats** — routing picks only the first accepted `alert_order` / `alert_product` chat.
- [ ] **Manager Settings `notifications` section** — load/reload toggles + chat routing from auth settings on `settings.updated` ([manager-settings-api.md](manager-settings-api.md)); keep only `TELEGRAM_BOT_TOKEN` in Secrets Manager.
- [ ] **Document `notification.telegram.read|manage` in permissions.md** — catalog constants exist; owner `*` works; manager seeds/docs lag.

### Docs / API surface drift

- [ ] **Refresh stale notification docs** — keep `endpoints.md` / OpenAPI in sync with webhook + subscriptions; reconcile webhook vs polling notes in [notification-telegram-bot.md](notification-telegram-bot.md). (`service-layout.md` / `current-state.md` API table updated 2026-08-17.)
- [ ] **OpenAPI: telegram manager + webhook** — extend `api/specs/notification-v1.yaml` (and `docs/openapi.yaml` if needed) beyond health/settings.
- [ ] **Fix CLI usage blurb** — `notification/cmd/main.go` claims “customer and admin messaging APIs”; service is ops Telegram only.

### Tests / quality

- [ ] **HTTP handler tests** — authz (read/manage), webhook secret, accept/reject/delete, 503 without JWT/DB.
- [ ] **PG repository tests** — upsert pending, accept/reject, unique chat/user constraints.
- [ ] **Structured logging (v1.1)** — replace `log.Printf` with shared zerolog (covered under v1.1 logging slice).

## Database schema (reviewed 2026-08-30)

Cross-service review of each service's inline Postgres schema (auth, cart, order, payment,
notification, product all migrate their own DB on startup — no separate migration tool; see
`CLAUDE.md`). Safe/low-risk items fixed same-day (migrations verified against live dev DBs,
run twice each for idempotency); the two structural items are intentionally left open.

### Correctness risks

- [x] **`users.email` unique with no case normalization** — fixed: `domain.NormalizeEmail` lowercases on every write path (`NewUser`, admin reset); `FindByEmail` matches `LOWER(email) = LOWER($1)`; `auth/pkg/bootstrap/migrate.go` backfills existing rows and adds `ux_users_email_lower` (case-insensitive unique index), skipping the index — and logging — if an unresolved pre-existing collision remains.
- [x] **Status columns have no DB-level enum guard** — fixed: `CHECK` constraints added on `orders.status`, `checkout_sessions.status` (`order/pkg/infra/pg/repository.go`), `payments.status` (`payment/pkg/infra/pg/repository.go`), and `reservations.status` (`product/pkg/infra/pg/inventory_store.go`). Postgres has no `ADD CONSTRAINT IF NOT EXISTS`, so each checks `pg_constraint` first (same pattern as product's SKU-master FKs).
- [ ] **`DeleteProduct`'s FK workaround is fragile** — `product/pkg/infra/pg/product_store.go` manually deletes `stock_items` rows before the `products → product_variants` cascade runs, to route around `stock_items`'s `ON DELETE RESTRICT`. A future RESTRICT FK added elsewhere to `product_variants` would start failing deletes with a raw FK error unless this method is updated too — needs a code comment pointer (or a central list of "things that block variant deletion"). Not fixed — documentation/process fix, not a schema change.

### Consistency with project conventions

- [ ] **`products.price` / `official_price` break the KRW `_cents` convention** — stored as `NUMERIC(10,2)` (`product/pkg/infra/pg/product_store.go`) while every other money field in the codebase (`orders.*_cents`, `payments.amount_cents`, `order_items.unit_price_cents`) is a whole-won `BIGINT`, per the "Currency: KRW only" rule in `CLAUDE.md`. Deliberately **not fixed** — needs its own migration plan (domain struct, JSON API shape, search/sort queries, possibly frontend consumers); too invasive for a routine schema cleanup, especially on v1.0 release day.
- [ ] **Two different ID-sequencing mechanisms** — order and product use a hand-rolled `id_sequences` table; payment uses a native Postgres `SEQUENCE`. Both work; pick one pattern for new services to avoid a third variant appearing. Not fixed — no functional benefit to migrating existing services, just a convention pick for the future.

### Missing indexes / cleanup gaps

- [x] **No index on `payments.customer_id`** — fixed: `idx_payments_customer_id` added in `payment/pkg/infra/pg/repository.go`.
- [x] **No index backing the `telegram_subscriptions` status filter** — fixed: `telegram_subscriptions_status_idx` on `(status, created_at DESC)` added in `notification/pkg/infra/pg/telegram_repository.go`, matching `List(status)`'s query shape.
- [ ] **Outbox tables have no purge path** — `order_outbox` / `payment_outbox` (order and payment repositories) only ever grow; published rows are never archived or deleted. Not fixed — needs a retention job/worker, not just a schema tweak.
- [x] **`order_idempotency_keys` has no expiry or supporting index** — index on `created_at` added in `order/pkg/infra/pg/repository.go` (`idx_order_idempotency_created_at`); the TTL sweep itself is still unwritten.
- [x] **`products` has no `updated_at`** — fixed: column added (`DEFAULT NOW()`), `UpdateProduct` now sets `updated_at = NOW()` on every write, and `domain.Product.UpdatedAt` (`updatedAt` in JSON) surfaces it through the API. Not backfilled into the in-memory product store (already doesn't track `CreatedAt` either — pre-existing gap, out of scope here).

## Temporary / ops

- [ ] **Re-lock auth register** — `AUTH_OPEN_REGISTER` is temporarily **true** (public customer signup). Set `false` / remove when storefront no longer needs open registration; restore Bearer + `user.create` only.

## Quality / performance (reviewed 2026-07-15)

Full write-up: [quality-performance-review.md](quality-performance-review.md).
**Fix plan (how to solve remaining bugs):** [quality-bugs-fix-plan.md](quality-bugs-fix-plan.md).

### Fixed in review PR

- [x] Checkout `DELETE …/items/by-sku-id/{id}` ownership ABAC
- [x] Payment `CompletePayment` republishes `payment.succeeded` after prior publish failure
- [x] Gate `simulate-success` behind dev-only flag (NANO unset + `PAYMENT_ALLOW_DEV_SIMULATE`) — since removed entirely; dev testing merged into Bypass
- [x] Product search pagination (`limit`/`offset`) + filter indexes
- [x] Order list / expiry batch item load + pending `payment_due_at` index
- [x] Cart enrichment parallelized (bounded concurrency)

### Still open (priority)

Implement remaining open items in [quality-bugs-fix-plan.md](quality-bugs-fix-plan.md). Money-path Criticals (**C1** pricing, **H7** JWT fail-closed) and Highs (**H1**/**H3**/**H4**/**H5**/**H6**/**H8**/**H9**) are **done**; still open: Redis catalog cache, frontend legacy-path/`skuId` migration.

- [ ] **API path convention** — `/api/v1/{service_name}/...` only. Canonical paths added; legacy top-level aliases kept until clients migrate, then remove.
  - [x] New canonical paths registered (product / order / cart) with legacy aliases
  - [x] Internal clients (cart/order) switched to canonical paths
  - [ ] Frontends / external callers migrate off legacy prefixes
  - [ ] Remove legacy aliases + nginx locations for `variants`, `coupons`, `catalog`, `inventory`, `checkout`, `carts`
  - Mapping:
    | Legacy | Canonical |
    |--------|-----------|
    | `/api/v1/variants/{sku}` | `/api/v1/products/variants/by-sku/{sku}` |
    | `/api/v1/variants/by-sku-id/{skuId}` | `/api/v1/products/variants/by-sku-id/{skuId}` |
    | `/api/v1/coupons` | `/api/v1/products/coupons` |
    | `/api/v1/coupons/{code}` | `/api/v1/products/coupons/by-code/{code}` |
    | `/api/v1/catalog/*` | `/api/v1/products/catalog/*` |
    | `/api/v1/inventory/{sku}` | `/api/v1/products/inventory/items/{sku}` |
    | `/api/v1/inventory/reservations/*` | `/api/v1/products/inventory/reservations/*` |
    | `/api/v1/checkout/*` | `/api/v1/orders/checkout/*` |
    | `/api/v1/carts/{id}` | `/api/v1/cart/customers/{id}` |
- [x] **Product images CDN** — CloudFront + OAC in prod; `imageUrls` use CloudFront hosts ([product-images-browser-access.md](product-images-browser-access.md)). Checklist A2–A3 done.
- [x] **Server-side order/checkout pricing (C1)** — ignore client `unit_price_cents`; resolve from product like cart ([quality-bugs-fix-plan.md](quality-bugs-fix-plan.md)#1-c1--server-side-pricing-critical)
- [x] Inventory service token refresh in order bootstrap
- [x] **H1** Order create `Idempotency-Key` + transactional outbox (soft-success publish; worker drain)
- [x] **H3** Payment `payment.succeeded` outbox + soft-success; order NATS queue group + handler error logging; reconcile republish
- [ ] Batch cart/product APIs (`?sku_ids=`); Redis catalog cache
  - [x] `GET /api/v1/products/variants?sku_ids=` batch public variant lookup (max 50)
  - [ ] Redis catalog cache
  - [ ] Cart client switch from N GETs to batch (optional follow-up)
- [x] Plumb request `context` through product PG stores (**H6**) — ports/services/handlers pass `r.Context()`; `Background` only for migrate/seed/CLI/bootstrap/shutdown
- [x] Sanitize product 500 responses (error wrapping) — see [product-error-wrapping.md](product-error-wrapping.md)
- [x] Sanitize auth/order/cart/payment 500 responses (**H4**) — stop returning raw `err.Error()` to clients
- [x] Product migrate: check ignored `Exec` errors (**H5**)
- [x] Consolidate duplicated `authjwt` into `shared/` + JWKS `singleflight` (**H8** + **H9**)
- [x] **Fail closed without JWT (H7)** — order/cart/payment `requireAuth` (and payment Bypass) must not no-op / allow when `jwtValidator` is nil outside tests
- [x] **Payment methods** — [payment-methods-plan.md](payment-methods-plan.md): `method` field + Bypass (`payment.bypass`) implemented; Bitcoin still planned (do not implement yet)

## Weekly review follow-ups (2026-07-20)

From the Jul 13–19 progress / quality / security check. Merged PRs from that week (#111 gateway, #113 canonical paths) are done.

### Still open from that review

- [ ] **API path convention** — migrate storefront / manage-web / external callers off legacy prefixes (`/variants`, `/coupons`, `/catalog`, `/inventory`, `/checkout`, `/carts`)
- [ ] **Remove legacy aliases** — drop dual routes + matching nginx locations once callers are migrated

### Security / quality

See [quality-bugs-fix-plan.md](quality-bugs-fix-plan.md).

- [x] **Server-side pricing** — Critical C1 done
- [x] **Fail closed without JWT** — H7 done; prod must always wire JWKS
- [ ] **Admin/owner lockout exemption** — keep intentional; ensure compensating controls (auth rate limits, strong passwords, no public admin email enum)
- [ ] **Bitcoin payment method** — planned only; do not implement yet ([payment-methods-plan.md](payment-methods-plan.md))

## Product API

- [x] **Parent + variants** — implemented; see [product-variants-plan.md](product-variants-plan.md). Remaining: drop legacy parent `color`/`imageUrls` columns, merge pre-existing duplicate color products.
- [x] **Product stock tracking** — always-tracked SKUs, PDP `inStock`/`availableQty`, cart stock-on-add; [product-stock-tracking-plan.md](product-stock-tracking-plan.md).
- [x] **Product attributes memo** — parent `attributes` string map (JSONB); PDP display only — [product-attributes.md](product-attributes.md).
- [ ] **Auth-aware `GET /api/v1/products/{id}`** — managers should see drafts/cost on PDP without a separate `/manage` path (optional Bearer, same pattern as list search).
- [x] **Guest session cookie + unique product view counter** — implemented; see [product-guest-views-plan.md](product-guest-views-plan.md). Browser `dupli1_guest` cookie; exact unique views per parent product on public PDP (`viewCount`).
- [x] **Product sold count** — implemented; see [product-sold-count.md](product-sold-count.md). Parent `soldCount` increments on inventory reservation commit (order ship).
- [x] **Rich product search + wishlist** — implemented; see [product-rich-search.md](product-rich-search.md). `sort`/`order`/`q`; wishlist add/remove/list with `wishlistCount`.
- [x] **Simple PDP recommendations** — implemented; see [product-recommendations.md](product-recommendations.md). `GET /api/v1/products/{id}/recommendations`; content similarity + `view_count` boost.
- [ ] **Co-view recommendations (phase 2)** — still open; see [product-views-recommendations-plan.md](product-views-recommendations-plan.md).
- [x] **Final review: Product / sellable SKU structure** — field ownership, no `Sku{}` type, Phase 0 locks; [product-structure-final-review.md](product-structure-final-review.md).
- [ ] **Flat sellable product model** — fold SKU/variant into `Product` (product becomes the unit of sale; style becomes the grouping); plan + phases in [product-flat-sellable-model-plan.md](product-flat-sellable-model-plan.md). Phase 0 locked in the final review; implementation not started.
- [ ] **Naming for multi-category catalog** — keep `Product` / `Variant` category-agnostic; do not rename to `Bag` / `BagSku`; see [product-multi-category-naming-plan.md](product-multi-category-naming-plan.md).
- [ ] **Wallets + clothing categories** — **wallets scheduled September 2026**; category master, per-category taxonomy, and validated `details` facets instead of per-category Go types. `NormalizeProductTaxonomy` currently validates bag seeds regardless of `category`, so wallet/clothing subcategories are rejected today while bag terms leak onto non-bags. Design + phases in [product-multi-category-design.md](product-multi-category-design.md); phases 1–2 unblock non-bag products. **Padded** bag-line products scheduled **October 2026** (catalog seed, not a new category).

### Found in review (2026-07-08, size/color variants)

- [x] **`product/pkg/handler` test package doesn't build** — fixed as part of the fine-grained-permissions rewrite (`access_control_test.go` was rewritten against the current API); `go test ./...` builds and passes, including `TestCreateVariant` and `TestSearchProductsByColor`.
- [x] **`UpdateVariant` silently clears omitted fields** — fixed with merge-on-update semantics: `domain.Variant.MergeUpdate` (`product/pkg/domain/enrich.go`) applies only the non-zero-value fields from the request onto the existing row, and `ProductSearchService.UpdateVariant` fetches the existing variant and merges before writing. Status specifically keeps its current value rather than resetting to `"active"`. Price lives on the parent product (not the SKU); variant PUT ignores price fields.
- [x] **`UpdateProduct` style-only wipe of price** — after price moved to parent, `UPDATE` wrote `price` from the JSON body; omitted `price` decoded as `0` and overwrote the stored value. Fixed with `Product.MergeUpdate` + service-layer merge (same pattern as variants).
- [x] **Variant SKU auto-naming (`optionCode`) differs between stores** — fixed by extracting `domain.OptionCode`/`domain.BuildVariantSKUBase` as shared helpers (`product/pkg/domain/skuid.go`) used by both `infra/pg/variant_store.go` and `infra/memory/product_store.go`; no longer possible to diverge.
- [x] **Luxury SKU naming system** — `Brand_Style_Color[_Edition]_Size` with master tables (`brands`, `colors`, `sizes`, `sku_editions`); see [product-sku-system.md](product-sku-system.md).
- [x] **SKU master-data runtime CRUD** — Phase A+B+C: styles table, FKs, catalog APIs (`/api/v1/catalog/...`), `product.master.read|write`, ULID product `id`, strict master codes on product/variant create, read-name enrichment; see [product-sku-master-data-plan.md](product-sku-master-data-plan.md).
- [x] **Bag merchandising master catalog** — subcategory / style / target seeds + public catalog APIs + `GET /products` filters; see [product-master-catalog.md](product-master-catalog.md).
- [x] **SKU physical dimensions** — variant `dimensions` `{widthMm, heightMm, depthMm}` in millimeters, distinct from letter `size`/`sizeCode`; see [product-sku-dimensions.md](product-sku-dimensions.md).
- [ ] **SKU master-data Phase D (admin UI)** — manage brands/styles/colors/sizes/editions in manage-web.

### Found while implementing SkuID + inventory merge (2026-07-10)

- [ ] **Frontend repos (`dupli1-web`, `dupli1-manage-web`) legacy path + `skuId` finish** — clients prefer `skuId` for cart/inventory where known, but still call legacy prefixes (`/api/v1/inventory/*`, `/coupons`, `/catalog`, …). Migrate to canonical `/api/v1/products/…` (and peers), then drop aliases. See [frontend-product-variants-migration.md](frontend-product-variants-migration.md).

## AWS deployment readiness (reviewed 2026-07-13)

Architecture is suitable (ECS on EC2 + ALB + RDS + Terraform + GitHub Actions). See [deployment-aws.md](deployment-aws.md).

### Working today (live ALB)

- [x] Gateway health, auth, product catalog
- [x] Storefront (`dupli1-web`) and admin (`dupli1-manage-web`) ECS services
- [x] Redis, NATS, NAT, Secrets Manager (auth/product DB URLs), Cloud Map `dupli1.local`
- [x] **Cart + payment on ECS** — ECR repos, Cloud Map (`cart` / `payment`), task defs, RDS DBs + secrets, nginx upstreams; APIs return 401 without auth (not 502)
- [x] **Order stabilized** — listens on `:8080`, `DUPLI1_ORDER_DB` from Secrets Manager; ASG sized for awsvpc ENI limits
- [x] **HTTPS on ALB** — ACM cert + `:443` listener; `/api/*` + `/gateway/*` → proxy
- [x] **Route53 → current ALB** — `dupli1.com` / `www` alias `dupli1-production-alb`
- [x] **JWT_SECRET in Secrets Manager** — no longer plain env default in task defs
- [x] **Orphan `dupli1-inventory` Fargate service removed**
- [x] **Docs updated** — [deployment-aws.md](deployment-aws.md) lists cart/payment/frontends/RDS DBs

### Remaining

- [ ] **Manager settings API** — sketch in [manager-settings-api.md](manager-settings-api.md) (`GET|PATCH /api/v1/settings/{section}`).
- [x] **Enable `awsvpcTrunking` for the ECS instance role** — confirmed live on container instances (2026-07-14); ASG Terraform defaults lowered to 2/1/4.
- [ ] **Create `dupli1-profile` ECR repo** — Terraform: `aws_ecr_repository.profile` + `github_actions_ecr_create` IAM. `terraform apply` (or the one-shot `aws ecr create-repository` in [deployment-aws.md](deployment-aws.md)) unblocks `Build dupli1-profile`. PowerUser cannot `CreateRepository`.
- [x] **Prefer OIDC for backend CI** — backend `.github/workflows/aws.yml` uses `github-actions-deploy-role` (no long-lived access keys).
- [ ] **HTTP→HTTPS redirect on ALB `:80` default action** — Terraform models redirect; live still serves HTTP for health/clients (API rule intact).
- [ ] **Apply cost cleanup** — follow [aws-cost-reduction-plan.md](aws-cost-reduction-plan.md) (Phases 1–2); script: `infra/scripts/cleanup-aws-orphans.sh`. Evidence: [aws-cost-optimization.md](aws-cost-optimization.md).