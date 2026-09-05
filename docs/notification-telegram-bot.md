# Notification Telegram bot

Design and operations guide for the Dupli1 ops Telegram bot (`dupli1-notification`).

**Status:** Webhook (or polling fallback), PostgreSQL subscriptions, manager accept API, and `/start` implemented. Global Manager Settings integration remains a follow-up.

**Related:** [current-state.md](current-state.md), [deployment-aws.md](deployment-aws.md), [manager-settings-api.md](manager-settings-api.md), [payment-service.md](payment-service.md).

---

## Purpose

The Telegram bot gives **operations staff** real-time alerts from the marketplace:

- New and updated orders (especially **paid** orders that need shipping)
- Product catalog changes (create, update, delete, image upload)

It is **not** a customer-facing channel. Shoppers never interact with this bot.

```text
Product / Order / Payment services
        │ publish NATS events
        ▼
   dupli1-nats
        │
        ▼
 dupli1-notification  ──►  Telegram Bot API  ──►  ops group / DM
        ▲
        │ webhook POST /telegram/webhook  (prod)
        │ or getUpdates drain/poll        (local / fallback)
        └── stores chat_id in PostgreSQL; manager accepts users/chats
```

---

## Components

| Piece | Role |
|-------|------|
| `dupli1-notification` | Subscribes to NATS; formats HTML messages; sends via Bot API |
| `dupli1-nats` | Event bus (`order.*`, `product.*`, `payment.succeeded` consumed indirectly via order) |
| Telegram Bot API | Outbound `sendMessage`; inbound webhook or `getUpdates` |
| Secrets Manager `dupli1/production/telegram` | **Bot token** (+ transitional env chat IDs) |
| PostgreSQL `notifications` | `telegram_subscriptions` — pending/accepted users and chat IDs |

Production bot (2026-08): `@MHYM7_BOT` (`dupli1_notification`).

---

## Security model

### Principle: least privilege

| Data | Sensitivity | Store (target) | Store (today) |
|------|-------------|----------------|---------------|
| `TELEGRAM_BOT_TOKEN` | **Secret** — full send access as the bot | Secrets Manager | Secrets Manager |
| Telegram **user IDs** (allowlist) | ACL — who may use bot commands | PostgreSQL `telegram_subscriptions` | Env `TELEGRAM_ALLOWED_USER_IDS` (bootstrap) |
| **Chat IDs** (destinations) | Config — where alerts go; not credentials | PostgreSQL `telegram_subscriptions` | Env `TELEGRAM_ORDER_CHAT_ID` / `PRODUCT` (fallback) |
| Channel / event toggles | Policy | Auth DB (`settings.notifications`) | Not implemented |

### Access rules (implemented)

1. **Inbound messages** (webhook or `getUpdates`) upsert a **pending** `telegram_subscriptions` row with `chat_id` and `telegram_user_id`.
2. **Managers** accept or reject pending rows, or manually add a user ID / chat ID via the REST API (`notification.telegram.manage`).
3. **Outbound ops alerts** — sent to env chat IDs, or to accepted subscriptions with `alert_order` / `alert_product` when env is unset.
4. **`/start` replies** — pending users get a “registration received” message; accepted users (or env allowlist) get the welcome + chat ID.
5. **Everyone else** — silently ignored on `/start` (unknown users cannot register).

### Webhook authentication (fail closed)

When `TELEGRAM_WEBHOOK_URL` is set, **`TELEGRAM_WEBHOOK_SECRET` is required**. Requests to `POST /api/v1/notification/telegram/webhook` without a matching `X-Telegram-Bot-Api-Secret-Token` header receive **`403`**; if the secret is unset, the handler returns **`503`** (`webhook secret not configured`). Invalid JSON bodies return **`400`**.

### Manager API

Requires Bearer JWT with `notification.telegram.read` (list) or `notification.telegram.manage` (create/accept/reject/delete).

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/notification/telegram/subscriptions?status=pending` | List registrations |
| `POST` | `/api/v1/notification/telegram/subscriptions` | Manually accept a user ID and/or chat ID |
| `POST` | `/api/v1/notification/telegram/subscriptions/{id}/accept` | Accept pending registration (`alert_order`, `alert_product` in body) |
| `POST` | `/api/v1/notification/telegram/subscriptions/{id}/reject` | Reject pending registration |
| `DELETE` | `/api/v1/notification/telegram/subscriptions/{id}` | Remove subscription |

Manual create body example:

```json
{
  "telegram_user_id": 123456789,
  "chat_id": "-1001234567890",
  "chat_label": "Ops group",
  "alert_order": true,
  "alert_product": true
}
```

Either `telegram_user_id` or `chat_id` is required (both may be set).

### Webhook vs polling

| Mode | When | Behaviour |
|------|------|-----------|
| **Webhook** | `TELEGRAM_WEBHOOK_URL` set (production) | `setWebhook` on startup; Telegram POSTs updates; service drains `getUpdates` once to store backlog chat IDs |
| **Polling** | webhook URL empty (local dev) | `deleteWebhook` + long-poll `getUpdates` |

Webhook URL (via gateway): `https://<host>/api/v1/notification/telegram/webhook`

Set `TELEGRAM_WEBHOOK_SECRET` and register the same value with Telegram via `setWebhook`; Telegram sends it as `X-Telegram-Bot-Api-Secret-Token`. The handler rejects requests when the secret is missing or mismatched (see [Webhook authentication](#webhook-authentication-fail-closed)).

### Why chat IDs should not live in Secrets Manager

Chat IDs are **routing configuration**, not secrets. Keeping them in Secrets Manager forces AWS console edits and ECS redeploys for every group change. The target design persists them in the database and exposes them through the Manager Settings API (see [Configuration tiers](#configuration-tiers) below).

---

## Configuration tiers

### Environment variables

| Variable | Required | Purpose |
|----------|----------|---------|
| `DUPLI1_NOTIFICATION_DB` | Recommended (prod) | PostgreSQL `notifications` database |
| `TELEGRAM_BOT_TOKEN` | Yes (for Telegram) | Bot API token from [@BotFather](https://t.me/BotFather) |
| `TELEGRAM_WEBHOOK_URL` | Production | Public HTTPS webhook URL |
| `TELEGRAM_WEBHOOK_SECRET` | **Required** when webhook URL is set | Validates `X-Telegram-Bot-Api-Secret-Token`; handler is fail-closed without it |
| `AUTH_JWKS_URL` | **Required for manage-web** | Auth JWKS for RS256 manager tokens. Without it, Telegram manager routes return `503 auth not configured` and manage-web `/telegram` shows **Failed to load Telegram subscriptions**. |
| `TELEGRAM_ALLOWED_USER_IDS` | Optional bootstrap | Comma-separated user IDs until DB entries exist |
| `TELEGRAM_ORDER_CHAT_ID` | Fallback routing | Order alerts chat when no DB `alert_order` row |
| `TELEGRAM_PRODUCT_CHAT_ID` | Fallback routing | Product alerts chat when no DB `alert_product` row |
| `NATS_URL` | Yes (for dispatch) | e.g. `nats://nats.dupli1.local:4222` |
| `NATS_TOKEN` | Yes (with `--auth`) | Must match the broker token. Compose default `dupli1_nats_dev`; prod Secrets Manager `dupli1/production/nats-token` |
| `MANAGE_WEB_URL` | Recommended | Base URL for “View order in manage-web” links (default `https://manage.dupli1.com`) |

Local DB: `postgres://dupli1:dupli1_dev@localhost:5438/notifications?sslmode=disable`

If order/product chat IDs are empty, events are **logged and skipped** (no Telegram send). Core NATS does not redeliver — a missed alert is only visible in CloudWatch (`/ecs/dupli1-notification`).

### Target (follow-up — global Manager Settings)

Host mutable notification config in **auth** (`dupli1_db`), consistent with [manager-settings-api.md](manager-settings-api.md):

```json
{
  "allow_outbound": true,
  "channels": {
    "telegram": true
  },
  "telegram": {
    "order_chat_id": "-1001234567890",
    "product_chat_id": "-1001234567890",
    "allowed_user_ids": [123456789, 987654321]
  },
  "events": {
    "order.created": true,
    "order.status_updated": true,
    "order.paid": true,
    "product.created": true,
    "product.updated": true,
    "product.deleted": true,
    "product.image_uploaded": true
  },
  "etag": "…"
}
```

**Enforcement:**

- `dupli1-notification` loads the `notifications` section on startup and reloads on NATS `settings.updated`.
- Only `TELEGRAM_BOT_TOKEN` remains in Secrets Manager.
- Manage-web edits `PATCH /api/v1/settings/notifications` (requires `settings.update`).

**Optional table** (if settings JSON is too coarse):

```sql
-- dupli1_db (auth), illustrative
CREATE TABLE notification_telegram_destinations (
  purpose     TEXT NOT NULL CHECK (purpose IN ('order', 'product')),
  chat_id     TEXT NOT NULL,
  chat_label  TEXT,
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (purpose)
);

CREATE TABLE notification_telegram_allowed_users (
  telegram_user_id  BIGINT PRIMARY KEY,
  display_name      TEXT,
  registered_chat_id TEXT,  -- filled when user sends /start
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`/start` (target behaviour): allowlisted user sends `/start` → service upserts `registered_chat_id` → manager assigns that chat to `order` or `product` purpose in the UI (or auto-bind for single-channel setups).

---

## NATS events → Telegram messages

| NATS subject | Destination chat | Message summary |
|--------------|------------------|-----------------|
| `order.created` | Order | New order, `created_at`, manage-web link, status, customer, items, total (KRW) |
| `order.status_updated` | Order | Status change |
| `order.paid` | Order | **Paid — action required** (ship when ready) |
| `product.created` | Product | New product, brand, category, price |
| `product.updated` | Product | Updated product |
| `product.deleted` | Product | Deleted product |
| `product.image_uploaded` | Product | Image URL |

Messages use **HTML** (`parse_mode: HTML`). Amounts use KRW formatting via `shared/pkg/money`.

**Example `order.created` message:**

```text
🛒 New order ORD-2026-0042
Created: 2026-08-05 19:30 KST
View order in manage-web
Status: pending
Customer: cust_01JAY6Z9K3F8QW1G7H2T5X0ABC
Items: 1× BAG-BV-CASSETTE-BLACK, 2× BAG-CH-CLASSIC-TAN
Total: ₩3,450,000
```

The manage-web link uses `MANAGE_WEB_URL` (default `https://manage.dupli1.com`) + `/orders/{order_id}`.

Publishers: `order` and `product` services (payment success flows through order → `order.paid`).

---

## Bot commands

| Command | Who | Behaviour |
|---------|-----|-----------|
| `/start` | Allowlisted users only | Welcome text + **chat ID** for ops setup (today: paste into config; target: auto-register in DB) |

No other commands are implemented. Unknown commands are ignored.

**Inbound transport:** webhook when `TELEGRAM_WEBHOOK_URL` is set (production); otherwise long-polling `getUpdates` (local dev). On startup with polling mode, the service calls `deleteWebhook` so `getUpdates` works.

---

## Setup runbook

### 1. Create the bot

1. Open [@BotFather](https://t.me/BotFather) → `/newbot` → save the **token**.
2. Store token in Secrets Manager:

```bash
aws secretsmanager put-secret-value --region us-east-1 \
  --secret-id dupli1/production/telegram \
  --secret-string '{"TELEGRAM_BOT_TOKEN":"<token>"}'
```

### 2. Allowlist ops users

Get each ops person's Telegram **user ID** (e.g. [@userinfobot](https://t.me/userinfobot)).

**Today (env):**

```bash
# Add to secret or .env
TELEGRAM_ALLOWED_USER_IDS=123456789,987654321
```

**Target:** add IDs via `PATCH /api/v1/settings/notifications` or manage-web.

### 3. Configure alert destinations

**Option A — ops group (recommended)**

1. Create a private Telegram supergroup for ops.
2. Add `@MHYM7_BOT` to the group.
3. An allowlisted user sends `/start` in the group (or DM) to learn the **chat ID** (negative for groups).
4. Set `TELEGRAM_ORDER_CHAT_ID` and `TELEGRAM_PRODUCT_CHAT_ID` to that ID.

**Option B — separate channels**

Use one chat ID for orders and another for catalog alerts.

**Today:** update secret + redeploy notification service:

```bash
aws ecs update-service --region us-east-1 \
  --cluster production --service dupli1-notification \
  --force-new-deployment
```

**Target:** set chat IDs in Manager Settings — no redeploy.

### 4. Local development

In `.env` (see `.env.example`):

```env
TELEGRAM_BOT_TOKEN=
TELEGRAM_ALLOWED_USER_IDS=
TELEGRAM_ORDER_CHAT_ID=
TELEGRAM_PRODUCT_CHAT_ID=
```

Run the stack: `sudo docker compose up --build`. Trigger a test order or product change; check `dupli1-notification` logs.

---

## Operations

### Health

- `GET /api/v1/notification/health` → `{"status":"ok"}`
- `GET /api/v1/notification/settings` → runtime flags (`telegram_enabled`, `order_chat_configured`, …)

### Logs

CloudWatch: `/ecs/dupli1-notification`

| Log line | Meaning |
|----------|---------|
| `notification dispatcher subscribed to order and product events` | NATS wiring OK |
| `telegram command poller started` | Inbound `/start` active |
| `… skipped: TELEGRAM_ORDER_CHAT_ID not set` | Event received; no destination configured |
| `notification nats handler subject=… error=…` | Handler failed; alert **not** retried |

### Failure behaviour

- NATS: at-most-once delivery; failed Telegram sends are logged and dropped.
- Missing token: Telegram disabled; NATS handler may still run.
- Missing chat ID: per-event skip with log line.
- Non-allowlisted `/start`: no reply.

### Rotating the bot token

1. Revoke/regenerate in BotFather.
2. Update `TELEGRAM_BOT_TOKEN` in Secrets Manager.
3. Redeploy `dupli1-notification`.
4. Chat IDs and allowlist unchanged.

---

## Implementation map

| Area | Path |
|------|------|
| NATS dispatcher | `notification/pkg/service/dispatcher.go` |
| Telegram client | `notification/pkg/infra/telegram/client.go` |
| Allowlist | `notification/pkg/infra/telegram/allowlist.go` |
| `/start` handler | `notification/pkg/infra/telegram/commands.go` |
| Update poller | `notification/pkg/infra/telegram/poller.go` |
| Bootstrap | `notification/pkg/bootstrap/bootstrap.go` |
| ECS secrets (transitional) | `infra/terraform/ecs_services.tf` |

---

## Roadmap

| Step | Description |
|------|-------------|
| **Done** | NATS → Telegram dispatch; PostgreSQL subscriptions; webhook + getUpdates; manager accept API; `/start` |
| **Next** | Manage-web UI; wire `settings.notifications` in auth for global toggles |
| **Later** | Event-type mute flags per subscription |

---

## Open questions

1. **Single ops channel vs split** — default both order and product to one group, or require two?
2. **Auto-bind on `/start`** — should the first allowlisted user's group become `order_chat_id` without manual PATCH?
3. **Audit** — log Telegram sends to `settings/audit` or a dedicated `notification_deliveries` table?
