package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/elug3/dupli1/product/pkg/domain"
	"github.com/elug3/dupli1/product/pkg/imaging"
	"github.com/elug3/dupli1/product/pkg/ports"
	"github.com/elug3/dupli1/shared/pkg/events"
	"github.com/google/uuid"
)

const (
	// Subject aliases of the shared event contract — see shared/pkg/events.
	productCreatedSubject       = events.ProductCreated
	productUpdatedSubject       = events.ProductUpdated
	productDeletedSubject       = events.ProductDeleted
	productImageUploadedSubject = events.ProductImage
	// Variant subjects have no cross-service subscriber today, so they stay
	// local rather than move into shared/pkg/events.
	variantCreatedSubject = "product.variant_created"
	variantUpdatedSubject = "product.variant_updated"
	variantDeletedSubject = "product.variant_deleted"
)

type ProductSearchService struct {
	store          ports.ProductStore
	imageStore     ports.ImageStore
	inventory      ports.InventoryStore
	eventPublisher ports.EventPublisher
	now            func() time.Time
}

func NewProductSearchService(store ports.ProductStore, imageStore ports.ImageStore, eventPublisher ...ports.EventPublisher) *ProductSearchService {
	s := &ProductSearchService{
		store:      store,
		imageStore: imageStore,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	if len(eventPublisher) > 0 {
		s.eventPublisher = eventPublisher[0]
	}
	return s
}

// WithInventory enables stock enrichment on PDP and ensure-stock after creates.
func (s *ProductSearchService) WithInventory(inv ports.InventoryStore) *ProductSearchService {
	s.inventory = inv
	return s
}

// SearchProducts returns parent styles only (no duplicate colors).
// When public is true, only active parents are returned.
// Optional filter keys "limit" and "offset" paginate results; total is the
// full match count before pagination.
func (s *ProductSearchService) SearchProducts(ctx context.Context, filter map[string]string, public bool) ([]domain.Product, int, error) {
	if s.store == nil {
		return nil, 0, fmt.Errorf("store not initialized")
	}
	f := make(map[string]string, len(filter)+1)
	for k, v := range filter {
		f[k] = v
	}
	if public {
		f["status"] = "active"
	}
	return s.store.SearchProducts(ctx, f)
}

func (s *ProductSearchService) ListProducts(ctx context.Context) ([]domain.Product, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	return s.store.ListProducts(ctx)
}

func (s *ProductSearchService) GetProduct(ctx context.Context, id string) (*domain.Product, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	p, err := s.store.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	s.enrichVariantsStock(ctx, p)
	return p, nil
}

func (s *ProductSearchService) GetPublicProduct(ctx context.Context, id string) (*domain.Product, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	p, err := s.store.GetActiveProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	s.enrichVariantsStock(ctx, p)
	return p, nil
}

const (
	recommendDefaultLimit = 8
	recommendMaxLimit     = 24
	recommendCandidateCap = 200
)

// Recommend returns related active parent products for a public PDP seed.
func (s *ProductSearchService) Recommend(ctx context.Context, seedID string, limit int) ([]domain.Product, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	if limit <= 0 {
		limit = recommendDefaultLimit
	}
	if limit > recommendMaxLimit {
		limit = recommendMaxLimit
	}
	seed, err := s.store.GetActiveProduct(ctx, seedID)
	if err != nil {
		return nil, err
	}
	filter := map[string]string{
		"status": "active",
		"limit":  strconv.Itoa(recommendCandidateCap),
	}
	if seed.Category != "" {
		filter["category"] = seed.Category
	}
	candidates, _, err := s.store.SearchProducts(ctx, filter)
	if err != nil {
		return nil, err
	}
	// Seed from GetActiveProduct includes variants; strip for fair scoring vs list cards.
	seedCard := *seed
	seedCard.Variants = nil
	return domain.RankRecommendations(seedCard, candidates, limit), nil
}

func (s *ProductSearchService) GetPublicVariant(ctx context.Context, sku string) (*domain.Variant, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	v, err := s.store.GetVariant(ctx, sku)
	if err != nil {
		return nil, err
	}
	return s.checkPublicVariant(ctx, v)
}

func (s *ProductSearchService) GetPublicVariantBySkuID(ctx context.Context, skuID string) (*domain.Variant, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	v, err := s.store.GetVariantBySkuID(ctx, skuID)
	if err != nil {
		return nil, err
	}
	return s.checkPublicVariant(ctx, v)
}

// MaxBatchSkuIDs caps GET /api/v1/products/variants?sku_ids= batch size.
const MaxBatchSkuIDs = 50

// GetPublicVariantsBySkuIDs returns active variants whose parent products are
// also active. Missing or non-public sku_ids are listed in missing (same
// visibility rule as the single-variant public lookups).
func (s *ProductSearchService) GetPublicVariantsBySkuIDs(ctx context.Context, skuIDs []string) (items []domain.Variant, missing []string, err error) {
	if s.store == nil {
		return nil, nil, fmt.Errorf("store not initialized")
	}
	ids := normalizeSkuIDs(skuIDs)
	if len(ids) == 0 {
		return nil, nil, ports.Invalid("sku_ids is required")
	}
	if len(ids) > MaxBatchSkuIDs {
		return nil, nil, ports.Invalid(fmt.Sprintf("sku_ids exceeds max of %d", MaxBatchSkuIDs))
	}

	found, err := s.store.GetVariantsBySkuIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]domain.Variant, len(found))
	for _, v := range found {
		byID[v.SkuID] = v
	}

	activeParent := make(map[string]*domain.Product)
	items = make([]domain.Variant, 0, len(ids))
	missing = make([]string, 0)
	for _, id := range ids {
		v, ok := byID[id]
		if !ok || v.Status != "active" {
			missing = append(missing, id)
			continue
		}
		parent, seen := activeParent[v.ProductID]
		if !seen {
			p, err := s.store.GetActiveProduct(ctx, v.ProductID)
			if err == nil {
				parent = p
			}
			activeParent[v.ProductID] = parent
		}
		if parent == nil {
			missing = append(missing, id)
			continue
		}
		v.ApplyParentPrice(*parent)
		items = append(items, v)
	}
	s.enrichVariantListStock(ctx, items)
	return items, missing, nil
}

func normalizeSkuIDs(skuIDs []string) []string {
	seen := make(map[string]struct{}, len(skuIDs))
	out := make([]string, 0, len(skuIDs))
	for _, raw := range skuIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *ProductSearchService) checkPublicVariant(ctx context.Context, v *domain.Variant) (*domain.Variant, error) {
	if v.Status != "active" {
		return nil, fmt.Errorf("variant: %w", ports.ErrNotFound)
	}
	parent, err := s.store.GetActiveProduct(ctx, v.ProductID)
	if err != nil {
		return nil, fmt.Errorf("variant: %w", ports.ErrNotFound)
	}
	v.ApplyParentPrice(*parent)
	v.ProductName = parent.Name
	s.applyStockToVariant(ctx, v)
	return v, nil
}

func (s *ProductSearchService) CreateProduct(ctx context.Context, p domain.Product) (*domain.Product, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	if err := domain.NormalizeProductTaxonomy(&p); err != nil {
		return nil, ports.Invalid(err.Error())
	}
	attrs, err := domain.NormalizeAttributes(p.Attributes)
	if err != nil {
		return nil, ports.Invalid(err.Error())
	}
	p.Attributes = attrs
	created, err := s.store.CreateProduct(ctx, p)
	if err != nil {
		return nil, err
	}
	for i := range created.Variants {
		v := &created.Variants[i]
		if err := s.ensureStockRow(ctx, v.SkuID, v.SKU, 0); err != nil {
			return nil, err
		}
	}
	s.enrichVariantsStock(ctx, created)
	if err := s.publish(ctx, productCreatedSubject, created, "", ""); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *ProductSearchService) UpdateProduct(ctx context.Context, p domain.Product) (*domain.Product, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	existing, err := s.store.GetProduct(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	merged := existing.MergeUpdate(p)
	if err := domain.NormalizeProductTaxonomy(&merged); err != nil {
		return nil, ports.Invalid(err.Error())
	}
	attrs, err := domain.NormalizeAttributes(merged.Attributes)
	if err != nil {
		return nil, ports.Invalid(err.Error())
	}
	merged.Attributes = attrs
	updated, err := s.store.UpdateProduct(ctx, merged)
	if err != nil {
		return nil, err
	}
	if err := s.publish(ctx, productUpdatedSubject, updated, "", ""); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *ProductSearchService) DeleteProduct(ctx context.Context, id string) error {
	if s.store == nil {
		return fmt.Errorf("store not initialized")
	}
	existing, err := s.store.GetProduct(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteProduct(ctx, id); err != nil {
		return err
	}
	return s.publish(ctx, productDeletedSubject, existing, "", "")
}

func (s *ProductSearchService) CreateVariant(ctx context.Context, productID string, v domain.Variant) (*domain.Variant, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	parent, err := s.store.GetProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	dims, err := domain.NormalizeDimensions(v.Dimensions)
	if err != nil {
		return nil, ports.Invalid(err.Error())
	}
	v.Dimensions = dims
	v.ProductID = productID
	created, err := s.store.CreateVariant(ctx, v)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStockRow(ctx, created.SkuID, created.SKU, 0); err != nil {
		return nil, err
	}
	created.ApplyParentPrice(*parent)
	s.applyStockToVariant(ctx, created)
	_ = s.publish(ctx, variantCreatedSubject, parent, created.SKU, "")
	return created, nil
}

// UpdateVariant merges the incoming (possibly partial) body onto the
// existing variant rather than overwriting it outright, so an update that
// only sets e.g. color can't silently blank size/status/images.
// Price is owned by the parent product and is not updated here.
func (s *ProductSearchService) UpdateVariant(ctx context.Context, productID, sku string, v domain.Variant) (*domain.Variant, error) {
	if s.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	existing, err := s.store.GetVariant(ctx, sku)
	if err != nil {
		return nil, err
	}
	if existing.ProductID != productID {
		return nil, fmt.Errorf("variant %s: %w", sku, ports.ErrNotFound)
	}
	merged := existing.MergeUpdate(v)
	dims, err := domain.NormalizeDimensions(merged.Dimensions)
	if err != nil {
		return nil, ports.Invalid(err.Error())
	}
	merged.Dimensions = dims
	merged.SKU = sku
	merged.ProductID = productID
	updated, err := s.store.UpdateVariant(ctx, merged)
	if err != nil {
		return nil, err
	}
	parent, _ := s.store.GetProduct(ctx, productID)
	if parent != nil {
		updated.ApplyParentPrice(*parent)
	}
	_ = s.publish(ctx, variantUpdatedSubject, parent, updated.SKU, "")
	return updated, nil
}

func (s *ProductSearchService) DeleteVariant(ctx context.Context, productID, sku string) error {
	if s.store == nil {
		return fmt.Errorf("store not initialized")
	}
	existing, err := s.store.GetVariant(ctx, sku)
	if err != nil {
		return err
	}
	if existing.ProductID != productID {
		return fmt.Errorf("variant %s: %w", sku, ports.ErrNotFound)
	}
	parent, _ := s.store.GetProduct(ctx, productID)
	if err := s.store.DeleteVariant(ctx, sku); err != nil {
		return err
	}
	return s.publish(ctx, variantDeletedSubject, parent, sku, "")
}

// UploadImage appends an image to the default variant (sku == productID, else first variant).
func (s *ProductSearchService) UploadImage(ctx context.Context, productID string, r io.Reader, size int64, contentType string) (*domain.Product, error) {
	p, err := s.store.GetProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	sku, err := defaultVariantSKU(p)
	if err != nil {
		return nil, err
	}
	if _, err := s.UploadVariantImage(ctx, productID, sku, r, size, contentType); err != nil {
		return nil, err
	}
	return s.store.GetProduct(ctx, productID)
}

// UploadVariantImage uploads a file and appends its URL to the variant's ImageURLs.
// It also writes a listing-size JPEG sibling ({key}.w600.jpg) and appends that
// URL to ListingImageURLs for category/home cards.
func (s *ProductSearchService) UploadVariantImage(ctx context.Context, productID, sku string, r io.Reader, size int64, contentType string) (*domain.Variant, error) {
	if s.imageStore == nil {
		return nil, fmt.Errorf("image store not configured")
	}
	v, err := s.store.GetVariant(ctx, sku)
	if err != nil {
		return nil, err
	}
	if v.ProductID != productID {
		return nil, fmt.Errorf("variant %s: %w", sku, ports.ErrNotFound)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if size > 0 && int64(len(data)) != size {
		// Multipart Size can be 0 on some clients; prefer actual bytes when mismatched.
		_ = size
	}
	if len(data) == 0 {
		return nil, ports.Invalid("empty image")
	}

	thumb, err := imaging.ListingJPEG(data, contentType)
	if err != nil {
		return nil, fmt.Errorf("listing thumb: %w", err)
	}

	objectKey := productID + "/" + sku + "/" + uuid.New().String()
	url, err := s.imageStore.Upload(ctx, objectKey, bytes.NewReader(data), int64(len(data)), contentType)
	if err != nil {
		return nil, err
	}
	listingKey := imaging.ListingObjectKey(objectKey)
	listingURL, err := s.imageStore.Upload(ctx, listingKey, bytes.NewReader(thumb), int64(len(thumb)), imaging.ListingContentType)
	if err != nil {
		return nil, fmt.Errorf("upload listing thumb: %w", err)
	}

	v.ImageURLs = append(v.ImageURLs, url)
	v.ListingImageURLs = append(v.ListingImageURLs, listingURL)
	updated, err := s.store.UpdateVariant(ctx, *v)
	if err != nil {
		return nil, err
	}
	parent, err := s.store.GetProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	updated.ApplyParentPrice(*parent)
	if err := s.publish(ctx, productImageUploadedSubject, parent, sku, url); err != nil {
		return nil, err
	}
	return updated, nil
}

func defaultVariantSKU(p *domain.Product) (string, error) {
	if p == nil || len(p.Variants) == 0 {
		return "", ports.Invalid("product has no variants")
	}
	for _, v := range p.Variants {
		if v.SKU == p.ID {
			return v.SKU, nil
		}
	}
	return p.Variants[0].SKU, nil
}

func (s *ProductSearchService) publish(ctx context.Context, subject string, product *domain.Product, sku, imageURL string) error {
	if s.eventPublisher == nil || product == nil {
		return nil
	}
	return s.eventPublisher.Publish(ctx, subject, events.Product{
		EventType: subject,
		ProductID: product.ID,
		SKU:       sku,
		Name:      product.Name,
		Brand:     product.Brand,
		Category:  product.Category,
		Status:    product.Status,
		Price:     product.Price,
		ImageURL:  imageURL,
		Occurred:  s.now(),
	})
}

func (s *ProductSearchService) ensureStockRow(ctx context.Context, skuID, sku string, quantity int) error {
	if s.inventory == nil || skuID == "" {
		return nil
	}
	_, err := s.inventory.GetItem(ctx, skuID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ports.ErrInventoryItemNotFound) {
		return err
	}
	return s.inventory.SaveItem(ctx, &domain.StockItem{
		SkuID:     skuID,
		SKU:       sku,
		Quantity:  quantity,
		Reserved:  0,
		UpdatedAt: s.now(),
	})
}

func (s *ProductSearchService) enrichVariantsStock(ctx context.Context, p *domain.Product) {
	if p == nil || len(p.Variants) == 0 {
		return
	}
	s.enrichVariantListStock(ctx, p.Variants)
}

func (s *ProductSearchService) enrichVariantListStock(ctx context.Context, variants []domain.Variant) {
	if s.inventory == nil || len(variants) == 0 {
		for i := range variants {
			variants[i].AvailableQty = 0
			variants[i].InStock = false
		}
		return
	}
	ids := make([]string, 0, len(variants))
	for _, v := range variants {
		if v.SkuID != "" {
			ids = append(ids, v.SkuID)
		}
	}
	items, err := s.inventory.GetItems(ctx, ids)
	if err != nil {
		for i := range variants {
			variants[i].AvailableQty = 0
			variants[i].InStock = false
		}
		return
	}
	for i := range variants {
		applyStockItem(&variants[i], items[variants[i].SkuID])
	}
}

func (s *ProductSearchService) applyStockToVariant(ctx context.Context, v *domain.Variant) {
	if v == nil {
		return
	}
	if s.inventory == nil || v.SkuID == "" {
		v.AvailableQty = 0
		v.InStock = false
		return
	}
	item, err := s.inventory.GetItem(ctx, v.SkuID)
	if err != nil {
		v.AvailableQty = 0
		v.InStock = false
		return
	}
	applyStockItem(v, item)
}

func applyStockItem(v *domain.Variant, item *domain.StockItem) {
	if v == nil {
		return
	}
	if item == nil {
		v.AvailableQty = 0
		v.InStock = false
		return
	}
	avail := item.Available()
	if avail < 0 {
		avail = 0
	}
	v.AvailableQty = avail
	v.InStock = avail > 0
}
