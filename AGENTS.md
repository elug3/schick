# AGENTS.md

Guidance for AI agents working in the Dupli1 repository.

## Repository status

Dupli1 is a Go microservice backend for a fashion bag marketplace. The repo contains:

- HTTP services in `auth/`, `product/`, `order/`, `cart/`, `payment/`, `notification/`, `profile/` (each with `cmd/` + `pkg/`). `product` also owns stock/reservations (the former standalone `inventory` service was merged in). `profile` (customer display name/phone + saved addresses) was extracted from `auth` per [docs/auth-profile-extension-plan.md](docs/auth-profile-extension-plan.md) Phase D — the auth code path is removed; the one-time data copy from auth's old tables and dropping them is still open.
- nginx gateway in `api/` (`dupli1-proxy` in Docker Compose)
- Docker Compose for local development
- Terraform and GitHub Actions for AWS ECS deployment

See [docs/current-state.md](docs/current-state.md) for the authoritative snapshot of what is implemented today. See [docs/service-layout.md](docs/service-layout.md) for directory and module layout.

## Documentation conventions

- AI agents must look up existing documents in `docs/` before writing new ones. Start with [docs/README.md](docs/README.md) for the living vs historical index. You don't have to read everything — skim for relevant overlap first.
- Add a service-name prefix when creating a new document (e.g. `product-*.md`, `cart-*.md`). You can skip the `order` document when working in `product-service`.

## Cursor Cloud specific instructions

### Prerequisites (on the VM)

| Tool | Version / notes |
|------|-----------------|
| Go | 1.22 on `PATH`; modules target Go 1.26.3. `GOTOOLCHAIN=auto` (the default) auto-downloads the 1.26.3 toolchain on first `go build`/`go test` — needs network. |
| Node / npm | Node 22, npm 10 — only needed for the sibling frontend repos. |
| Docker + Compose | Installed. **The daemon is NOT auto-started** (no systemd) — run `sudo dockerd` once per session (see below). |

Postgres, Redis, NATS and MinIO are **not** installed as system services; they run as Docker Compose containers (see below). There is no `service postgresql`/`service redis-server` on this VM.

### Starting Docker (required before Compose)

Cloud Agent `install` / `start` helpers live in `scripts/cloud-agent-install.sh` and `scripts/cloud-agent-start.sh` (Go module refresh + dockerd/Compose readiness). Manual session bootstrap:

The Docker daemon does not start automatically. Start it in the background once per session, then verify:

```bash
sudo dockerd >/tmp/dockerd.log 2>&1 &
sudo docker info | grep -i "storage driver"   # expect: fuse-overlayfs
```


Docker is configured with the `fuse-overlayfs` storage driver and the containerd-snapshotter feature disabled (`/etc/docker/daemon.json`), and `iptables-legacy` — needed for Docker-in-Firecracker on this VM. All `docker`/`docker compose` commands need `sudo`.

### Database credentials (dev)

Docker Compose provides Postgres instances per service:

| Service | Host port | Database | User | Password |
|---------|-----------|----------|------|----------|
| `postgres-auth` | 5432 | `dupli1_db` | `dupli1` | `dupli1_dev` |
| `postgres-product` | 5433 | `products` | `dupli1` | `dupli1_dev` |
| `postgres-order` | 5435 | `orders` | `dupli1` | `dupli1_dev` |
| `postgres-cart` | 5436 | `cart` | `dupli1` | `dupli1_dev` |
| `postgres-payment` | 5437 | `payments` | `dupli1` | `dupli1_dev` |
| `postgres-notification` | 5438 | `notifications` | `dupli1` | `dupli1_dev` |
| `postgres-profile` | 5439 | `profiles` | `dupli1` | `dupli1_dev` |

Connection strings:

- Auth: `postgres://dupli1:dupli1_dev@localhost:5432/dupli1_db?sslmode=disable`
- Product: `postgres://dupli1:dupli1_dev@localhost:5433/products?sslmode=disable` (also stock/reservations)
- Order: `postgres://dupli1:dupli1_dev@localhost:5435/orders?sslmode=disable`
- Cart: `postgres://dupli1:dupli1_dev@localhost:5436/cart?sslmode=disable`
- Payment: `postgres://dupli1:dupli1_dev@localhost:5437/payments?sslmode=disable`
- Notification: `postgres://dupli1:dupli1_dev@localhost:5438/notifications?sslmode=disable`
- Profile: `postgres://dupli1:dupli1_dev@localhost:5439/profiles?sslmode=disable`

Production uses **Amazon RDS** — see [docs/deployment-aws.md](docs/deployment-aws.md) and [infra/terraform/README.md](infra/terraform/README.md).

### Running locally

```bash
cp .env.example .env   # optional; compose already has working dev defaults
sudo docker compose up --build   # all docker commands need sudo here
```

The full stack (6 Postgres + Redis + NATS + MinIO + 6 Go services + nginx) comes up healthy in ~1–2 min after the first image build. The seeded owner account is `admin@dupli1.com` / `password`.

Gateway (HTTP): `http://localhost:8080` or `http://localhost` (port 80). TLS certs exist in `certs/` but are not wired into local nginx yet.

Direct service ports (bypass gateway):

| Service | Host port |
|---------|-----------|
| `dupli1-auth` | 18080 |
| `dupli1-product` | 8081 |
| `dupli1-order` | 8083 |
| `dupli1-cart` | 8086 |
| `dupli1-payment` | 8087 |
| `dupli1-notification` | 8084 |
| `dupli1-profile` | 8088 |

### Running a single service (without Docker)

```bash
cd auth && go test ./...
cd product && go test ./...

# Auth server
cd auth/cmd && go run . -help

# Product server
cd product/cmd && go run . -help
```

### Testing

Run tests per service module (root `go test ./...` does not work — the root `go.mod` is a stub):

```bash
cd auth && go test ./...
cd product && go test ./...
cd order && go test ./...
cd cart && go test ./...
cd payment && go test ./...
cd profile && go test ./...
```

### Gotchas

- **Module paths:** all services use `github.com/elug3/dupli1/<service>` (e.g. `github.com/elug3/dupli1/product`). There is no top-level `go.work`.
- **Auth token flow:** login returns only a `refresh_token`; call `POST /api/v1/auth/refresh` to obtain a short-lived access token in the `token` field. The refresh token rotates on every call — the response's `refresh_token` field carries the replacement, and the one you sent is invalidated immediately.
- **Product JWT:** protected routes validate RS256 via `AUTH_JWKS_URL` (set in Compose to auth's JWKS endpoint).
- **Order JWT:** protected routes validate RS256 via `AUTH_JWKS_URL` (set in Compose to auth's JWKS endpoint), with `JWT_SECRET` HS256 fallback in dev.
- **Order, cart, and payment** use PostgreSQL when `DUPLI1_ORDER_DB` / `DUPLI1_CART_DB` / `DUPLI1_PAYMENT_DB` are set (Docker Compose); in-memory fallback for tests without a DB URL.
- **Payment flow:** `POST /api/v1/payments` → **NANO card** (`credit_card`) when configured, else manager **Bypass** (`payment.bypass`) — also how local/dev testing pays. On success, payment publishes **`payment.succeeded`**; order service marks order **`paid`**. Ship via `POST /api/v1/orders/{id}/ship` → **`in_transit`** (commits stock). See [docs/payment-service.md](docs/payment-service.md).
- **Notification** subscribes to NATS (e.g. `order.paid` → Telegram when configured).
- **Redis and NATS** are optional for auth (rate limits, session cache, events); Redis and NATS are wired in Compose. Order and payment use NATS for payment events. NATS requires `NATS_TOKEN` (Compose default `dupli1_nats_dev`; production from Secrets Manager `dupli1/production/nats-token`). Client libraries read it automatically. Host port 4222 is bound to `127.0.0.1` only.
- **SMTP and OAuth providers** are external. Credit card uses **NANO Solution 인증결제** when `NANO_*` credentials are set (prod: Secrets Manager `dupli1/production/nano-payment`). Without NANO, `credit_card` is unavailable and manager **Bypass** is used instead, including in local Compose.
- **Local gateway DNS resolver:** `api/nginx.conf` (the local `dupli1-proxy` config; production uses `api/nginx.prod.conf`) uses variable `proxy_pass` with a `resolver`. It must list **only** Docker's embedded DNS `127.0.0.11`. Do not add the AWS VPC resolver `10.0.0.2` here — it's unreachable in local Compose, so nginx round-robins onto a dead resolver and returns intermittent `SERVFAIL` → `502 {"error":"bad gateway"}` on ~half of requests. After editing this file, rebuild the proxy: `sudo docker compose up -d --build dupli1-proxy`.
