package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/elug3/dupli1/product/pkg/domain"
	"github.com/elug3/dupli1/product/pkg/ports"
	"github.com/jackc/pgconn"
	"github.com/jackc/pgtype"
)

const variantSelectCols = `sku_id, sku, product_id, color, size,
	COALESCE(color_code, ''), COALESCE(edition_code, ''), COALESCE(size_code, ''),
	width_mm, height_mm, depth_mm,
	status, image_urls, COALESCE(listing_image_urls, '{}'), created_at`

func scanVariant(scan func(...any) error) (domain.Variant, error) {
	var v domain.Variant
	var createdAt time.Time
	var imageURLs pgtype.TextArray
	var listingImageURLs pgtype.TextArray
	var widthMm, heightMm, depthMm *int
	err := scan(
		&v.SkuID, &v.SKU, &v.ProductID, &v.Color, &v.Size,
		&v.ColorCode, &v.EditionCode, &v.SizeCode,
		&widthMm, &heightMm, &depthMm,
		&v.Status, &imageURLs, &listingImageURLs, &createdAt,
	)
	if err != nil {
		return domain.Variant{}, err
	}
	v.Dimensions = dimensionsFromNullable(widthMm, heightMm, depthMm)
	v.ImageURLs = scanTextArray(imageURLs)
	v.ListingImageURLs = scanTextArray(listingImageURLs)
	v.CreatedAt = createdAt.Format(time.RFC3339)
	return v, nil
}

func dimensionsFromNullable(width, height, depth *int) *domain.Dimensions {
	if width == nil && height == nil && depth == nil {
		return nil
	}
	d := &domain.Dimensions{}
	if width != nil {
		d.WidthMm = *width
	}
	if height != nil {
		d.HeightMm = *height
	}
	if depth != nil {
		d.DepthMm = *depth
	}
	if d.Empty() {
		return nil
	}
	return d
}

func nullInt(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

func dimensionArgs(d *domain.Dimensions) (width, height, depth interface{}) {
	if d == nil {
		return nil, nil, nil
	}
	return nullInt(d.WidthMm), nullInt(d.HeightMm), nullInt(d.DepthMm)
}

func (s *ProductSearchStore) ListVariants(ctx context.Context, productID string) ([]domain.Variant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+variantSelectCols+` FROM product_variants
		 WHERE product_id = $1
		 ORDER BY created_at ASC, sku ASC`,
		productID,
	)
	if err != nil {
		return nil, wrapDB("list variants", err)
	}
	defer rows.Close()

	var results []domain.Variant
	for rows.Next() {
		v, err := scanVariant(rows.Scan)
		if err != nil {
			return nil, wrapDB("list variants", err)
		}
		results = append(results, v)
	}
	return results, wrapDB("list variants", rows.Err())
}

func (s *ProductSearchStore) GetVariant(ctx context.Context, sku string) (*domain.Variant, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+variantSelectCols+` FROM product_variants WHERE sku = $1`, sku,
	)
	v, err := scanVariant(row.Scan)
	if err != nil {
		return nil, wrapDB("get variant", err)
	}
	return &v, nil
}

func (s *ProductSearchStore) GetVariantBySkuID(ctx context.Context, skuID string) (*domain.Variant, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+variantSelectCols+` FROM product_variants WHERE sku_id = $1`, skuID,
	)
	v, err := scanVariant(row.Scan)
	if err != nil {
		return nil, wrapDB("get variant by sku id", err)
	}
	return &v, nil
}

func (s *ProductSearchStore) GetVariantsBySkuIDs(ctx context.Context, skuIDs []string) ([]domain.Variant, error) {
	if len(skuIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+variantSelectCols+` FROM product_variants WHERE sku_id = ANY($1)`,
		toTextArray(skuIDs),
	)
	if err != nil {
		return nil, wrapDB("get variants by sku ids", err)
	}
	defer rows.Close()

	var results []domain.Variant
	for rows.Next() {
		v, err := scanVariant(rows.Scan)
		if err != nil {
			return nil, wrapDB("get variants by sku ids", err)
		}
		results = append(results, v)
	}
	return results, wrapDB("get variants by sku ids", rows.Err())
}

func (s *ProductSearchStore) parentSKUCodes(ctx context.Context, productID string) (brandCode, styleCode string, err error) {
	var bc, sc *string
	err = s.pool.QueryRow(ctx,
		`SELECT brand_code, style_code FROM products WHERE id = $1`, productID,
	).Scan(&bc, &sc)
	if err != nil {
		return "", "", wrapDB("parent sku codes", err)
	}
	if bc != nil {
		brandCode = *bc
	}
	if sc != nil {
		styleCode = *sc
	}
	return brandCode, styleCode, nil
}

func (s *ProductSearchStore) nextVariantSKU(ctx context.Context, productID, brandCode, styleCode string, v *domain.Variant) (string, error) {
	base := domain.ComposeVariantSKU(productID, brandCode, styleCode, v)

	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_variants WHERE sku = $1)`, base).Scan(&exists)
	if err != nil {
		return "", wrapDB("next variant sku", err)
	}
	if !exists {
		return base, nil
	}
	if brandCode != "" && styleCode != "" && v.ColorCode != "" {
		return "", ports.Conflict(fmt.Sprintf("duplicate sku %s: same brand/style/color/edition/size already exists", base))
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_variants WHERE sku = $1)`, candidate).Scan(&exists)
		if err != nil {
			return "", wrapDB("next variant sku", err)
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", ports.Invalid(fmt.Sprintf("generate variant sku: exhausted candidates for %s", productID))
}

func (s *ProductSearchStore) CreateVariant(ctx context.Context, v domain.Variant) (*domain.Variant, error) {
	if v.ProductID == "" {
		return nil, ports.Invalid("productId is required")
	}
	if v.Status == "" {
		v.Status = "active"
	}

	brandCode, styleCode, err := s.parentSKUCodes(ctx, v.ProductID)
	if err != nil {
		return nil, err
	}
	if brandCode == "" || styleCode == "" {
		return nil, fmt.Errorf("%w: parent product missing brandCode/styleCode", domain.ErrMissingSKUCodes)
	}

	if err := domain.RequireVariantSKUCodes(&v); err != nil {
		return nil, err
	}
	if err := s.requireColor(ctx, v.ColorCode); err != nil {
		return nil, err
	}
	if err := s.requireSize(ctx, v.SizeCode); err != nil {
		return nil, err
	}
	if err := s.requireEdition(ctx, v.EditionCode); err != nil {
		return nil, err
	}
	if v.Color == "" {
		v.Color = s.colorName(ctx, v.ColorCode)
	}
	if v.Size == "" {
		v.Size = s.sizeName(ctx, v.SizeCode)
	}

	if v.SKU == "" {
		sku, err := s.nextVariantSKU(ctx, v.ProductID, brandCode, styleCode, &v)
		if err != nil {
			return nil, err
		}
		v.SKU = sku
	}
	if v.SkuID == "" {
		v.SkuID = domain.NewSkuID()
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrapDB("create variant begin", err)
	}
	defer tx.Rollback(ctx)

	var createdAt time.Time
	w, h, d := dimensionArgs(v.Dimensions)
	err = tx.QueryRow(ctx,
		`INSERT INTO product_variants (sku_id, sku, product_id, color, size, color_code, edition_code, size_code,
		     width_mm, height_mm, depth_mm, status, image_urls, listing_image_urls)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		 RETURNING created_at`,
		v.SkuID, v.SKU, v.ProductID, v.Color, v.Size,
		nullEmpty(v.ColorCode), nullEmpty(v.EditionCode), nullEmpty(v.SizeCode),
		w, h, d,
		v.Status, toTextArray(v.ImageURLs), toTextArray(v.ListingImageURLs),
	).Scan(&createdAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, fmt.Errorf("%w: %s", domain.ErrMasterNotFound, pgErr.Message)
		}
		return nil, wrapDB("create variant", err)
	}

	// Always-tracked SKU: every variant gets a stock row (qty 0 by default).
	if _, err := tx.Exec(ctx, `
		INSERT INTO stock_items (sku_id, quantity, reserved, updated_at)
		VALUES ($1, 0, 0, NOW())
		ON CONFLICT (sku_id) DO NOTHING
	`, v.SkuID); err != nil {
		return nil, wrapDB("create variant stock", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrapDB("create variant commit", err)
	}
	v.CreatedAt = createdAt.Format(time.RFC3339)
	v.AvailableQty = 0
	v.InStock = false
	return &v, nil
}

// UpdateVariant updates a variant by its (immutable) sku. sku_id and sku are never
// rewritten — codes may be filled when previously blank, but the human sku stays stable.
func (s *ProductSearchStore) UpdateVariant(ctx context.Context, v domain.Variant) (*domain.Variant, error) {
	var createdAt time.Time
	w, h, d := dimensionArgs(v.Dimensions)
	err := s.pool.QueryRow(ctx,
		`UPDATE product_variants
		 SET color=$2, size=$3, color_code=$4, edition_code=$5, size_code=$6,
		     width_mm=$7, height_mm=$8, depth_mm=$9,
		     status=$10, image_urls=$11, listing_image_urls=$12
		 WHERE sku=$1
		 RETURNING sku_id, product_id, created_at`,
		v.SKU, v.Color, v.Size,
		nullEmpty(v.ColorCode), nullEmpty(v.EditionCode), nullEmpty(v.SizeCode),
		w, h, d,
		v.Status, toTextArray(v.ImageURLs), toTextArray(v.ListingImageURLs),
	).Scan(&v.SkuID, &v.ProductID, &createdAt)
	if err != nil {
		return nil, wrapDB("update variant", err)
	}
	v.CreatedAt = createdAt.Format(time.RFC3339)
	return &v, nil
}

func (s *ProductSearchStore) DeleteVariant(ctx context.Context, sku string) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM product_variants WHERE sku = $1`, sku)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ports.Conflict(fmt.Sprintf("cannot delete variant %s: stock exists for it in inventory", sku))
		}
		return wrapDB("delete variant", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("variant %s: %w", sku, ports.ErrNotFound)
	}
	return nil
}
