# Product images — browser access (private S3)

**Status (2026-07-27):** **Done in production** — product `imageUrls` use CloudFront (the CDN). Checklist A2–A3 closed. Keep this doc as the architecture / ops reference.

**Historical problem:** the product-images S3 bucket is **private** (Block Public Access on). When `S3_PUBLIC_ENDPOINT` pointed at the raw S3 regional domain, `imageUrls` were `https://<bucket>.s3.<region>.amazonaws.com/...` and browsers got 403. ECS nginx (`api/nginx.ecs.conf`) has **no** `/product-images/` proxy, and the ALB does not route image paths to S3.

**Local:** MinIO + gateway `location /product-images/` works (`api/nginx.conf` / `api/nginx.prod.conf`).

This is a **backend / infra** issue. Do **not** “fix” it only in `dupli1-web` / `dupli1-manage-web` (e.g. proxying images through the Next.js app). Storefront and admin should keep using absolute `imageUrls` from the product API.

---

## Options

| Option | What to do | Best when |
|--------|------------|-----------|
| **CloudFront + OAC (recommended)** | Front the private bucket with CloudFront; set `S3_PUBLIC_ENDPOINT` to the CloudFront URL (or custom domain like `images.dupli1.com`) | Production / public storefront + admin |
| ALB + nginx → S3 | Add `/product-images/` on ECS proxy and point `S3_PUBLIC_ENDPOINT` at the API origin | Temporary / no CDN budget |
| Public bucket | Disable Block Public Access + public-read policy | **Avoid** — loses private-bucket posture |

---

## Recommended: CloudFront + Origin Access Control

### Flow

```text
Browser  →  https://images.dupli1.com/{objectKey}
         →  CloudFront (OAC signs GetObject)
         →  private S3 bucket (dupli1-*-product-images-*)

Product service uploads with IAM keys → S3
Product API returns imageUrls = {S3_PUBLIC_ENDPOINT}/{objectKey}
```

### Terraform

Resources in [`infra/terraform/s3.tf`](../infra/terraform/s3.tf) / [`infra/terraform/cloudfront_images.tf`](../infra/terraform/cloudfront_images.tf):

- `aws_cloudfront_origin_access_control` (SigV4, always sign)
- `aws_cloudfront_distribution` (S3 origin, HTTPS, CachingOptimized)
- `aws_s3_bucket_policy` allowing `cloudfront.amazonaws.com` `s3:GetObject` conditioned on the distribution ARN
- Optional alias `images.dupli1.com` + Route53 alias (when `product_images_cdn_aliases` is set)
- Secrets Manager + ECS task env: `S3_PUBLIC_ENDPOINT` = CDN base (no bucket name in the path)

### Apply

```bash
cd infra/terraform
terraform plan
terraform apply
terraform output product_images_cdn_url
```

Redeploy / force new deployment of `dupli1-product` so it picks up the new `S3_PUBLIC_ENDPOINT` (task definition env + secret version).

### URL shape

`S3_PUBLIC_ENDPOINT` is the **full public prefix** for objects. The product service appends `/{objectKey}` only (it does **not** insert the bucket name).

| Environment | `S3_PUBLIC_ENDPOINT` | Example `imageUrls` entry |
|-------------|----------------------|---------------------------|
| Local Compose | `http://localhost:8080/product-images` | `http://localhost:8080/product-images/{id}/{sku}/{uuid}` |
| AWS | `https://images.dupli1.com` (or `https://dxxxx.cloudfront.net`) | `https://images.dupli1.com/{id}/{sku}/{uuid}` |

Object keys remain `{productId}/{sku}/{uuid}` inside the bucket.

---

## Existing `imageUrls` in the database

Uploads store **absolute** URLs. Rows written while `S3_PUBLIC_ENDPOINT` pointed at the S3 domain stay broken until rewritten or re-uploaded.

After CDN is live, rewrite the host (adjust old/new bases to match your bucket / distribution):

```sql
-- Inspect
SELECT sku, image_urls FROM variants WHERE image_urls::text LIKE '%amazonaws.com%' LIMIT 20;

-- Rewrite S3 regional host → CDN (example; run carefully)
UPDATE variants
SET image_urls = (
  SELECT COALESCE(array_agg(
    replace(u,
      'https://OLD_BUCKET.s3.us-east-1.amazonaws.com/OLD_BUCKET/',
      'https://images.dupli1.com/'
    )
  ), '{}')
  FROM unnest(image_urls) AS u
)
WHERE image_urls::text LIKE '%amazonaws.com%';
```

Also check legacy parent `image_urls` if still populated. Prefer a dry-run / transaction.

New uploads after apply use the CDN base automatically.

---

## Listing derivatives (upload-time thumbs)

Category / home cards should not download full ~2000×2000 originals. On each successful `UploadVariantImage`, product-service also writes a **JPEG listing thumb** next to the original:

| | |
|--|--|
| Object key | `{originalKey}.w600.jpg` (e.g. `products/…/uuid.w600.jpg`) |
| Geometry | Longest edge ≤ **600px**, aspect preserved |
| Encoding | JPEG quality ~82 (`image/jpeg`) — no CGO; works with `CGO_ENABLED=0` Docker builds |
| Variant fields | `listingImageUrls` (parallel to `imageUrls`) |
| Product fields | `defaultListingImageUrl` (first listing URL of the default/first variant) |

Storefront list/search/home should prefer `defaultListingImageUrl`, then fall back to `defaultImageUrl` / first `imageUrls` entry. PDP / zoom keep the original URLs.

### Backfill existing objects

```bash
# Dry-run
cd product && DUPLI1_PRODUCT_DB='postgres://dupli1:dupli1_dev@localhost:5433/products?sslmode=disable' \
  S3_ENDPOINT=http://localhost:9000 \
  S3_PUBLIC_ENDPOINT=http://localhost:8080/product-images \
  S3_ACCESS_KEY=minioadmin S3_SECRET_KEY=minioadmin S3_BUCKET=product-images \
  go run ./cmd/backfill-listing-images

# Write objects + UPDATE variants
… go run ./cmd/backfill-listing-images -confirm
```

Requires HTTP GET reachability to each original (public CDN / MinIO via gateway). Skips URLs that already have a sibling `.w600.jpg` in `listing_image_urls`.

### Optional later

- WebP/AVIF thumbs if/when the product image is built with CGO or an external encoder
- CloudFront image transforms as a second tier

---

## What not to do

- Make the bucket world-readable “just for images”
- Proxy every image through the Next.js frontends
- Point `S3_PUBLIC_ENDPOINT` at the ALB API host without an nginx `/product-images/` (or equivalent) that can fetch from S3 with credentials — ECS proxy has no MinIO and no S3 proxy today

---

## Verification

```bash
# CDN health (object must exist)
curl -sI "https://images.dupli1.com/<productId>/<sku>/<uuid>"
# expect: 200, via CloudFront

# Product API returns CDN hosts
curl -s "https://dupli1.com/api/v1/products/<id>" | jq '..|objects|select(has("imageUrls"))|.imageUrls'
```

Local:

```bash
curl -sI "http://localhost:8080/product-images/<key>"
# expect: 200 from MinIO via dupli1-proxy
```
