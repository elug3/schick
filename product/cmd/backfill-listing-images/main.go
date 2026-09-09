// backfill-listing-images generates ~600px JPEG listing thumbs for existing
// variant image_urls that lack listing_image_urls, uploads them beside the
// originals ({key}.w600.jpg), and updates product_variants.listing_image_urls.
//
// Defaults to dry-run. Pass -confirm to write.
//
// Usage:
//
//	DUPLI1_PRODUCT_DB=postgres://dupli1:dupli1_dev@localhost:5433/products?sslmode=disable \
//	S3_ENDPOINT=http://localhost:9000 S3_PUBLIC_ENDPOINT=http://localhost:8080/product-images \
//	S3_ACCESS_KEY=minioadmin S3_SECRET_KEY=minioadmin S3_BUCKET=product-images \
//	    go run ./cmd/backfill-listing-images
//	… go run ./cmd/backfill-listing-images -confirm
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/elug3/dupli1/product/pkg/imaging"
	s3store "github.com/elug3/dupli1/product/pkg/infra/s3"
	"github.com/jackc/pgtype"
	"github.com/jackc/pgx/v4"
)

func main() {
	fs := flag.NewFlagSet("backfill-listing-images", flag.ExitOnError)
	productDB := fs.String("product-db", os.Getenv("DUPLI1_PRODUCT_DB"), "product database URL")
	confirm := fs.Bool("confirm", false, "upload thumbs and update listing_image_urls")
	limit := fs.Int("limit", 0, "max variants to process (0 = all)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: backfill-listing-images [OPTIONS]

Generates listing JPEG thumbs ({objectKey}.w600.jpg) for variants that have
image_urls but missing/short listing_image_urls.

Options:
  -product-db string   Product DB URL (also DUPLI1_PRODUCT_DB)
  -confirm             Actually upload + UPDATE (dry-run without this)
  -limit int           Cap variants processed (0 = all)

S3/MinIO env (same as product service):
  S3_ENDPOINT, S3_PUBLIC_ENDPOINT, S3_ACCESS_KEY, S3_SECRET_KEY, S3_BUCKET
`)
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
	if *productDB == "" {
		fmt.Fprintln(os.Stderr, "-product-db (or DUPLI1_PRODUCT_DB) is required")
		fs.Usage()
		os.Exit(1)
	}

	endpoint := envOr("S3_ENDPOINT", "http://localhost:9000")
	publicEndpoint := envOr("S3_PUBLIC_ENDPOINT", "http://localhost:8080/product-images")
	accessKey := envOr("S3_ACCESS_KEY", "minioadmin")
	secretKey := envOr("S3_SECRET_KEY", "minioadmin")
	bucket := envOr("S3_BUCKET", "product-images")

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, *productDB)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Ensure column exists (same as product migrate).
	if _, err := conn.Exec(ctx, `ALTER TABLE product_variants ADD COLUMN IF NOT EXISTS listing_image_urls TEXT[] NOT NULL DEFAULT '{}'`); err != nil {
		log.Fatalf("migrate listing_image_urls: %v", err)
	}

	store, err := s3store.NewImageStore(endpoint, publicEndpoint, accessKey, secretKey, bucket)
	if err != nil {
		log.Fatalf("image store: %v", err)
	}

	rows, err := conn.Query(ctx, `
		SELECT sku, image_urls, COALESCE(listing_image_urls, '{}')
		FROM product_variants
		WHERE cardinality(image_urls) > 0
		ORDER BY sku`)
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	defer rows.Close()

	publicBase := strings.TrimRight(publicEndpoint, "/")
	client := &http.Client{Timeout: 60 * time.Second}

	var examined, needed, wrote, skipped, failed int
	for rows.Next() {
		var sku string
		var imageURLs, listingURLs pgtype.TextArray
		if err := rows.Scan(&sku, &imageURLs, &listingURLs); err != nil {
			log.Fatalf("scan: %v", err)
		}
		images := textArray(imageURLs)
		listings := textArray(listingURLs)
		examined++
		if *limit > 0 && examined > *limit {
			break
		}

		out := make([]string, len(images))
		copy(out, pad(listings, len(images)))
		changed := false
		for i, imageURL := range images {
			if strings.TrimSpace(imageURL) == "" {
				continue
			}
			if i < len(out) && strings.TrimSpace(out[i]) != "" {
				skipped++
				continue
			}
			needed++
			key, err := objectKeyFromURL(publicBase, imageURL)
			if err != nil {
				log.Printf("sku=%s skip %s: %v", sku, imageURL, err)
				failed++
				continue
			}
			if !*confirm {
				log.Printf("dry-run sku=%s would write %s", sku, imaging.ListingObjectKey(key))
				changed = true
				out[i] = publicBase + "/" + imaging.ListingObjectKey(key)
				continue
			}
			listingURL, err := generateAndUpload(ctx, client, store, imageURL, key)
			if err != nil {
				log.Printf("sku=%s fail %s: %v", sku, imageURL, err)
				failed++
				continue
			}
			out[i] = listingURL
			changed = true
			wrote++
			log.Printf("sku=%s wrote %s", sku, listingURL)
		}
		if !changed {
			continue
		}
		if !*confirm {
			continue
		}
		if _, err := conn.Exec(ctx,
			`UPDATE product_variants SET listing_image_urls = $2 WHERE sku = $1`,
			sku, toTextArray(out),
		); err != nil {
			log.Printf("sku=%s update failed: %v", sku, err)
			failed++
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	mode := "dry-run"
	if *confirm {
		mode = "confirm"
	}
	log.Printf("%s done: examined=%d needed=%d wrote=%d skipped=%d failed=%d",
		mode, examined, needed, wrote, skipped, failed)
}

func generateAndUpload(ctx context.Context, client *http.Client, store *s3store.ImageStore, imageURL, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", imageURL, res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 12<<20))
	if err != nil {
		return "", err
	}
	thumb, err := imaging.ListingJPEG(data, res.Header.Get("Content-Type"))
	if err != nil {
		return "", err
	}
	listingKey := imaging.ListingObjectKey(key)
	return store.Upload(ctx, listingKey, bytes.NewReader(thumb), int64(len(thumb)), imaging.ListingContentType)
}

func objectKeyFromURL(publicBase, imageURL string) (string, error) {
	u := strings.TrimSpace(imageURL)
	prefix := publicBase + "/"
	if !strings.HasPrefix(u, prefix) {
		return "", fmt.Errorf("url %q does not start with public base %q", u, publicBase)
	}
	key := strings.TrimPrefix(u, prefix)
	if key == "" || strings.Contains(key, "..") {
		return "", fmt.Errorf("bad object key from %q", u)
	}
	return key, nil
}

func textArray(a pgtype.TextArray) []string {
	if a.Status != pgtype.Present {
		return nil
	}
	out := make([]string, len(a.Elements))
	for i, el := range a.Elements {
		if el.Status == pgtype.Present {
			out[i] = el.String
		}
	}
	return out
}

func pad(in []string, n int) []string {
	out := make([]string, n)
	copy(out, in)
	return out
}

func toTextArray(vals []string) pgtype.TextArray {
	var a pgtype.TextArray
	_ = a.Set(vals)
	return a
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
