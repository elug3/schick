# AWS deployment

Dupli1 production runs on **ECS (EC2 launch type)** in `us-east-1`, fronted by an **Application Load Balancer** (HTTP + HTTPS). Images are built and pushed by `.github/workflows/aws.yml`.

## Architecture

```text
Internet → Route53 (dupli1.com / www / manage.dupli1.com / images.dupli1.com)
        → ALB (HTTPS :443, HTTP :80)
             ├── manage.dupli1.com   → dupli1-manage-web (admin)
             ├── /api/*, /gateway/* → dupli1-proxy (nginx → Cloud Map)
             │     auth / product / order / cart / payment / notification
             └── /*                 → dupli1-web (storefront, bridge mode)
        → CloudFront (images.dupli1.com) → private S3 product-images (OAC)
         EC2 ASG (ECS capacity provider) in private subnets
         NAT Gateway → ECR / Secrets Manager / CloudWatch
         RDS PostgreSQL (private)
```

Product `imageUrls` use `S3_PUBLIC_ENDPOINT` (CloudFront / `images.dupli1.com`). Do not point browsers at the raw S3 bucket URL — see [product-images-browser-access.md](product-images-browser-access.md).
IaC lives in [`infra/terraform/`](../infra/terraform/README.md).

## Database

Production uses **Amazon RDS PostgreSQL 16** (`dupli1-production`).

| Component | Details |
|-----------|---------|
| Databases | `dupli1_db` (auth), `products`, `orders`, `cart`, `payments`, `notifications` (local Compose; wire on ECS — see [TODO.md](TODO.md)) |
| Credentials | AWS Secrets Manager (`dupli1/production/*-db-url`, `jwt-secret`, `telegram`, `nats-token`) |
| Network | Private subnets; ECS tasks + ECS instances SG → port 5432 |
| SSL | `sslmode=require` |

Create app DBs after RDS is up: `bash infra/scripts/create-rds-databases.sh`.

## JWT signing key

Auth signs RS256 access and refresh tokens with an RSA key and publishes the public half
at `/.well-known/jwks.json`, which product, order, cart and payment fetch to validate
tokens. The key comes from one of, in order:

| Source | Env var | Used by |
|--------|---------|---------|
| PEM contents | `JWT_PRIVATE_KEY` | ECS (Secrets Manager injects secrets as env vars) |
| PEM file path | `JWT_PRIVATE_KEY_FILE` | single-EC2 Compose (mounted `/run/secrets`) |
| Generated at startup | — | local dev only |

**Neither set means a throwaway key per start**: every previously issued token stops
validating and JWKS changes under the other services on each auth deploy or task
replacement. Auth logs a warning at startup and reports
`features.ephemeral_jwt_key: true` on `GET /api/v1/auth/settings`.

**Production (2026-07-28):** secret `dupli1/production/jwt-private-key` is injected as
`JWT_PRIVATE_KEY` on `dupli1-auth`; live settings report `ephemeral_jwt_key: false`.

Create/replace the key and point Terraform at it:

```bash
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out jwt-private-key.pem
aws secretsmanager create-secret \
  --name dupli1/production/jwt-private-key \
  --secret-string "file://jwt-private-key.pem"
# then set jwt_private_key_secret_arn in Terraform and apply
```

`JWT_SECRET` stays as the HS256 dev fallback for order/cart/payment and is unrelated to
this key. Rotating the RSA key invalidates all outstanding refresh tokens, so every user
must log in again — do it during a quiet window.

## ECS services

| Service | Purpose |
|---------|---------|
| `dupli1-auth` | Authentication API |
| `dupli1-product` | Product catalog + inventory |
| `dupli1-order` | Order / checkout API |
| `dupli1-cart` | Shopping cart API |
| `dupli1-payment` | Payments (NANO card / Bypass) |
| `dupli1-notification` | NATS → Telegram ops alerts — see [notification-telegram-bot.md](notification-telegram-bot.md) |
| `dupli1-proxy` | nginx gateway (ALB `/api/*`, `/gateway/*`) |
| `dupli1-web` | Public storefront (ALB default) |
| `dupli1-manage-web` | Admin UI (`https://manage.dupli1.com`) |
| `dupli1-redis` | Auth rate-limit / session cache |
| `dupli1-nats` | Event bus |

Cloud Map namespace: `dupli1.local` (short names: `auth`, `product`, `order`, `cart`, `payment`, …).

## Capacity notes

User-data sets `ECS_ENABLE_AWSVPC_TRUNKING=true`, but as of **2026-07-26** hosts still only get **2 awsvpc task ENIs** per `t3.large` (no trunk ENI attached). With 10 awsvpc services, the safe ASG floor is **5** instances until trunking works for role `dupli1-production-ecs-instance`. See [aws-july-2026-cost-report.md](aws-july-2026-cost-report.md).

## Cost

After Phase 1 cleanup (2026-07-26): orphans removed, ASG **5×`t3.large`**. Target **2×** (~$210–230/mo) needs working ENI trunking first. See [aws-july-2026-cost-report.md](aws-july-2026-cost-report.md) and [aws-cost-reduction-plan.md](aws-cost-reduction-plan.md).

## Required GitHub configuration

| Type | Name | Purpose |
|------|------|---------|
| Variable | `AWS_REGION` | `us-east-1` |
| Variable | `ECS_CLUSTER` | `production` |
| Variable | `AWS_ROLE_ARN` | (optional) defaults to `arn:aws:iam::845061289093:role/github-actions-deploy-role` |

Backend (`dupli1`) and frontends (`dupli1-web`, `dupli1-manage-web`) deploy via **GitHub OIDC** and IAM role `github-actions-deploy-role` (ECR push + ECS deploy). Do not use long-lived `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` secrets for CI.

## Local development

Local development still uses Docker Compose. See the root `README.md`. For a single-box EC2 alternative, see [deployment-ec2.md](deployment-ec2.md).
