# Dupli1 documentation

Index of `docs/`. Prefer **living** docs for as-built behavior; treat **historical / plan** docs as design context unless their status line says otherwise.

When the API surface changes, update [current-state.md](current-state.md) and [api.md](api.md) (and [endpoints.md](endpoints.md) / [openapi.yaml](openapi.yaml) when routes change). See [AGENTS.md](../AGENTS.md).

## Living (start here)

| Doc | Role |
|-----|------|
| [current-state.md](current-state.md) | Authoritative snapshot of what is implemented |
| [api.md](api.md) | Service API narrative |
| [endpoints.md](endpoints.md) | Route index |
| [openapi.yaml](openapi.yaml) | OpenAPI aggregate |
| [permissions.md](permissions.md) | Permission catalog and ABAC |
| [service-layout.md](service-layout.md) | Module / directory layout |
| [TODO.md](TODO.md) | Living backlog and schedule |
| [payment-service.md](payment-service.md) | Money path (NANO / Bypass) |
| [payment-nano-return-verification.md](payment-nano-return-verification.md) | **Blocked on NANO** — why no card payment can succeed, and the question that unblocks it |
| [checkout-session.md](checkout-session.md) | Checkout sessions in order |
| [cart-service.md](cart-service.md) | Persistent cart |
| [profile-service.md](profile-service.md) | Customer commerce profile + saved addresses |
| [notification-telegram-bot.md](notification-telegram-bot.md) | Ops Telegram alerts |
| [deployment-aws.md](deployment-aws.md) | Production ECS / RDS |
| [deployment-ec2.md](deployment-ec2.md) | Single-EC2 Compose overlay |
| [auth-logging.md](auth-logging.md) | Auth zerolog events (as-built) |

### Product as-built references

| Doc | Topic |
|-----|-------|
| [product-sku-system.md](product-sku-system.md) | Dual SKU identity + masters |
| [product-sku-dimensions.md](product-sku-dimensions.md) | Physical dimensions |
| [product-price-on-parent.md](product-price-on-parent.md) | Parent pricing |
| [product-attributes.md](product-attributes.md) | Parent attributes memo |
| [product-master-catalog.md](product-master-catalog.md) | Bag merchandising taxonomy |
| [product-rich-search.md](product-rich-search.md) | Search / sort / wishlist |
| [product-recommendations.md](product-recommendations.md) | PDP recommendations |
| [product-sold-count.md](product-sold-count.md) | `soldCount` on ship |
| [product-error-wrapping.md](product-error-wrapping.md) | Store-boundary errors |
| [product-images-browser-access.md](product-images-browser-access.md) | CloudFront / images |
| [product-structure-final-review.md](product-structure-final-review.md) | Field ownership locks |

### Product plans (status line first)

| Doc | Topic |
|-----|-------|
| [product-stock-tracking-plan.md](product-stock-tracking-plan.md) | Always-tracked SKUs, PDP `inStock`, cart stock-on-add (implemented) |
| [product-promo-referral-code-plan.md](product-promo-referral-code-plan.md) | Coupons → sales-trackable promo / referral codes (planning) |

## Release / ops

| Doc | Role |
|-----|------|
| [v1.0-release-spec.md](v1.0-release-spec.md) | v1.0 closeout checklist |
| [v1-release-plan.md](v1-release-plan.md) | v1.0 narrative + launch runbook |
| [v1.1-release-plan.md](v1.1-release-plan.md) | Post-launch slices (blocked on v1.0) |
| [aws-cost-reduction-plan.md](aws-cost-reduction-plan.md) | Cost cut plan |
| [aws-cost-optimization.md](aws-cost-optimization.md) | Mid-month cost review |
| [aws-july-2026-cost-report.md](aws-july-2026-cost-report.md) | July 2026 cost evidence |

## Historical / plan (read status line first)

These retain design history. Prefer the living docs above for current behavior.

| Doc | Notes |
|-----|-------|
| [product-variants-plan.md](product-variants-plan.md) | Historical — parent/variant shipped; prefer sku-system + structure review; stock leftovers → [product-stock-tracking-plan.md](product-stock-tracking-plan.md) |
| [product-guest-views-plan.md](product-guest-views-plan.md) | Phase 1 shipped; guest cart still open |
| [product-views-recommendations-plan.md](product-views-recommendations-plan.md) | Phases 0–1 shipped; co-view open |
| [product-sku-master-data-plan.md](product-sku-master-data-plan.md) | A–C shipped; Phase D (admin UI) open |
| [product-flat-sellable-model-plan.md](product-flat-sellable-model-plan.md) | Accepted; not implemented |
| [product-sale-unit-reflection.md](product-sale-unit-reflection.md) | Decision background for flatten plan |
| [product-multi-category-design.md](product-multi-category-design.md) | Wallets Sep / padded Oct schedule |
| [product-multi-category-naming-plan.md](product-multi-category-naming-plan.md) | Keep Product/Variant names |
| [auth-profile-extension-plan.md](auth-profile-extension-plan.md) | Phases A–B shipped; Phase D (`profile` service) live — data copy from auth's orphaned tables still open. See [profile-service.md](profile-service.md) |
| [order-tracking-plan.md](order-tracking-plan.md) | Customer order history + required ship tracking |
| [manager-settings-api.md](manager-settings-api.md) | Sketch only |
| [frontend-product-variants-migration.md](frontend-product-variants-migration.md) | Sibling frontend migration notes |
| [quality-performance-review.md](quality-performance-review.md) | Jul 2026 audit (mostly fixed) |
| [quality-bugs-fix-plan.md](quality-bugs-fix-plan.md) | Remaining: Redis cache, frontend paths (H6 done) |
