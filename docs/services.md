# Services Layout

This document describes how Schick's service-oriented Go backend is organized, what each service owns, and how new service code should be placed.

## Directory Map

```text
schick/
├── cmd/
│   ├── server/              # API server entrypoint and dependency wiring
│   └── migrate/             # Database migration runner
├── internal/
│   ├── handlers/            # HTTP transport layer; parses requests and writes responses
│   ├── middleware/          # Cross-cutting HTTP concerns such as auth, CORS, and rate limits
│   ├── models/              # Shared database entities and API-facing data structures
│   ├── repository/          # Data access layer used by services
│   └── utils/               # Private helper functions
├── pkg/
│   ├── analytics/           # Reporting, metrics, and dashboard queries
│   ├── auth/                # Login, token lifecycle, 2FA, password recovery, and SSO hooks
│   ├── chat/                # Support conversations, chat threads, and message persistence
│   ├── config/              # Super Admin system settings, feature flags, and config audit logs
│   ├── notification/        # Email, push, SMS, templates, and event-triggered notifications
│   ├── order/               # Checkout, order lifecycle, status changes, and fulfillment coordination
│   ├── product/             # Catalog, variants, inventory, search, and categorization
│   └── user/                # Profiles, addresses, wishlists, preferences, and account settings
├── migrations/              # SQL or migration framework files
├── config/                  # Runtime configuration loading and defaults
├── tests/                   # Integration tests, fixtures, and shared test helpers
└── docs/                    # Architecture and API documentation
```

## Layer Responsibilities

Schick separates HTTP concerns, business logic, and persistence so each service can evolve independently.

1. **Entrypoints (`cmd/`)** initialize configuration, database clients, caches, external providers, repositories, services, routers, and background workers.
2. **Handlers (`internal/handlers`)** translate HTTP requests into service calls. Handlers should validate transport-level input, extract route parameters, attach request context, and map service errors to HTTP responses.
3. **Services (`pkg/<service>`)** contain business workflows and enforce domain rules. A service should coordinate repositories and other services, but should not depend on HTTP framework types.
4. **Repositories (`internal/repository`)** own persistence queries and database transactions. Repositories should expose intent-based methods instead of leaking raw SQL or ORM details into services.
5. **Models (`internal/models`)** define shared entities, value objects, and DTOs used across handlers, repositories, and service boundaries.
6. **Middleware (`internal/middleware`)** handles cross-cutting request concerns before execution reaches a handler.

## Service Packages

### Auth (`pkg/auth`)

Owns identity and access workflows:

- Registration, login, logout, and session invalidation.
- JWT creation, refresh, and verification helpers.
- Password reset, recovery, and credential rotation.
- Two-factor authentication setup and verification.
- OAuth or SSO integration points.

Auth should be the source of truth for authentication decisions. Authorization policies that depend on roles or ownership can be exposed as helpers and reused by handlers or other services.

### Product (`pkg/product`)

Owns catalog and inventory behavior:

- Product create, read, update, and delete operations.
- Variants, attributes, categories, images, and metadata.
- Inventory availability, stock adjustments, and low-stock rules.
- Product search and filter criteria.

Product code should protect catalog invariants, such as valid variant combinations and non-negative inventory counts.

### Order (`pkg/order`)

Owns checkout and the order lifecycle:

- Cart-to-order conversion and order creation.
- Order status transitions and history.
- Payment coordination and failure handling.
- Shipment, tracking, and fulfillment state.
- Order cancellation and refund workflows.

Order should coordinate with product inventory, notification delivery, user addresses, and payment adapters through interfaces to avoid tight package coupling.

### User (`pkg/user`)

Owns customer profile data and account preferences:

- Profile read and update operations.
- Address book management.
- Wishlists and favorites.
- Notification preferences and account settings.

User should avoid owning authentication credentials directly; credential and session behavior belongs in Auth.

### Notification (`pkg/notification`)

Owns outbound customer and admin messaging:

- Email, push, and SMS providers.
- Template rendering and localization.
- Notification scheduling and retries.
- Event-triggered notifications for orders, auth, support, and system activity.

Notification should expose intent-based methods such as `SendOrderConfirmation` instead of requiring callers to know templates or provider-specific payloads.

### Chat (`pkg/chat`)

Owns support conversations:

- Chat thread creation and resolution.
- Message storage and retrieval.
- Support ticket metadata.
- Real-time delivery coordination.

Chat should isolate WebSocket or real-time hub details behind interfaces so tests and non-real-time channels can share the same domain logic.

### Analytics (`pkg/analytics`)

Owns reporting queries and metrics:

- Sales, revenue, and product performance reports.
- Customer behavior metrics.
- Dashboard summaries.
- Custom report filters and time windows.

Analytics can read across domains, but it should not mutate transactional state owned by other services.

### Config (`pkg/config`)

Owns Super Admin system configuration:

- Feature flags and rollout switches.
- Runtime settings and validation rules.
- Audit logging for setting changes.
- Role-restricted configuration access.

Config should validate all settings before persistence and provide typed accessors where possible so callers do not depend on raw string keys.

## Dependency Direction

Keep dependencies flowing inward and downward:

```text
cmd/server
  -> internal/handlers
      -> pkg/<service>
          -> internal/repository
              -> database/cache clients
```

Recommended rules:

- Handlers may depend on services, but services should not depend on handlers.
- Services may depend on repository interfaces and small interfaces from peer services.
- Repositories should not call handlers or services.
- Shared request-independent helpers belong in `internal/utils` when private or `pkg/<service>` when part of a public service API.
- External providers should be wrapped behind interfaces so services can be tested without network calls.

## Adding a New Service

When adding a service, follow this checklist:

1. Create `pkg/<service>/` for the domain workflow and public service API.
2. Define the service constructor, dependencies, and exported methods around business use cases.
3. Add repository interfaces in the service package if the service owns the contract, then implement them under `internal/repository`.
4. Add HTTP handlers under `internal/handlers` only for transport-specific routing and response mapping.
5. Wire dependencies in `cmd/server`.
6. Add database changes under `migrations/` when persistence changes are required.
7. Add tests near the package under test or in `tests/` for integration coverage.
8. Update docs and API references with the new endpoints and ownership boundaries.

## Testing Expectations

- Unit test service business rules with fake repositories and fake peer-service interfaces.
- Test repository implementations with a real or containerized database when query behavior matters.
- Test handlers with HTTP request/response fixtures and mocked services.
- Prefer deterministic tests for notification, payment, and real-time adapters by replacing external providers with local fakes.
