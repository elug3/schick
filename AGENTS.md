# AGENTS.md

Guidance for AI agents working in the Schick repository.

## Repository status

This repository currently contains **architecture and setup documentation only** (`README.md`, `ARCHITECTURE.md`, `docs/services.md`). There is no `go.mod`, application source, migrations, or `.env.example` on any branch yet. The README describes the intended layout and run workflow for when implementation lands.

## Cursor Cloud specific instructions

### Prerequisites (pre-installed on the VM)

| Tool | Version / notes |
|------|-----------------|
| Go | 1.22+ (`/usr/bin/go`) — meets README requirement of Go 1.21+ |
| PostgreSQL | 16 — `localhost:5432` |
| Redis | 7 — `localhost:6379` |

### Starting infrastructure services

PostgreSQL and Redis are installed via apt but may not auto-start in this environment. Start them before running the API or integration tests:

```bash
sudo service postgresql start
sudo service redis-server start
```

Verify with:

```bash
pg_isready -h localhost
redis-cli ping
```

### Database credentials (dev)

A local dev database is configured to match README defaults:

| Setting | Value |
|---------|-------|
| `DB_HOST` | `localhost` |
| `DB_PORT` | `5432` |
| `DB_USER` | `schick` |
| `DB_PASSWORD` | `schick_dev` |
| `DB_NAME` | `schick_db` |

Connection string: `postgres://schick:schick_dev@localhost:5432/schick_db?sslmode=disable`

### Running the application (once code exists)

Follow `README.md`:

```bash
go mod download
cp .env.example .env   # edit with values above
go run cmd/migrate/main.go
go run cmd/server/main.go
```

API: `http://localhost:8080`  
Swagger (when implemented): `http://localhost:8080/swagger/index.html`

### Testing and linting (once code exists)

```bash
go test ./...
go test -cover ./...
```

There is no `golangci-lint` config or Makefile in the repo today.

### Gotchas

- **`go mod download` / `go test ./...` fail today** because `go.mod` is not present. This is expected until implementation is added.
- **No Docker Compose** — PostgreSQL and Redis run as system services, not containers.
- **Redis is optional** per README but installed and running for session/cache development.
- **SMTP, payment, and OAuth providers** are external; no local mocks are bundled.
