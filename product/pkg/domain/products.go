package domain

// Variant is a sellable option (SKU) under a parent product style.
// There is no separate Sku{} type — dual identity lives here as SkuID + SKU.
// Target model folds these fields onto Product; see docs/product-structure-final-review.md.
type Variant struct {
	// SkuID is the canonical, immutable cross-service identifier (ULID).
	SkuID     string `json:"skuId,omitempty"`
	SKU       string `json:"sku"`
	ProductID string `json:"productId"`
	Color     string `json:"color"`
	Size      string `json:"size,omitempty"`
	// Normalized SKU segment codes (see docs/product-sku-system.md).
	ColorCode   string `json:"colorCode,omitempty"`
	EditionCode string `json:"editionCode,omitempty"` // optional VariantCode segment
	SizeCode    string `json:"sizeCode,omitempty"`
	// Dimensions is the physical size of this SKU in millimeters
	// (e.g. width 340 × height 220 × depth 80). Distinct from Size/SizeCode
	// letter labels (S/M/L). Optional; omitted when unknown.
	Dimensions *Dimensions `json:"dimensions,omitempty"`
	// Price and OfficialPrice are filled from the parent product on read.
	// They are not stored on the SKU row (price lives on Product).
	OfficialPrice float64  `json:"officialPrice,omitempty"`
	Price         float64  `json:"price,omitempty"`
	Status        string   `json:"status"` // "active" | "draft" | "archived"
	ImageURLs     []string `json:"imageUrls,omitempty"`
	// ListingImageURLs are ~600px JPEG thumbs parallel to ImageURLs (same index).
	// Used by category/home cards; full ImageURLs stay on the PDP gallery.
	ListingImageURLs []string `json:"listingImageUrls,omitempty"`
	// ProductName is the parent product's display name, populated on public variant reads.
	ProductName string `json:"productName,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	// AvailableQty is max(0, stock.quantity - stock.reserved). Response-only;
	// not stored on the variant row. See docs/product-stock-tracking-plan.md.
	AvailableQty int `json:"availableQty"`
	// InStock is true when AvailableQty > 0. Response-only.
	InStock bool `json:"inStock"`
}

// Product is a parent catalog style. Sellable options live on Variants.
// After the accepted flatten migration, Product becomes the sellable unit and
// shared copy moves to Style — see docs/product-structure-final-review.md.
type Product struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Brand       string `json:"brand"`
	BrandCode   string `json:"brandCode,omitempty"`
	StyleCode   string `json:"styleCode,omitempty"`
	Material    string `json:"material"`
	Category    string `json:"category"`
	// SubCategory is a bag type under category (handbags, tote, shoulder, cross, mini).
	SubCategory string `json:"subCategory,omitempty"`
	// Style is bag occasion / look (casual, evening, business, weekend, statement).
	// Distinct from StyleCode (SKU design-family master).
	Style string `json:"style,omitempty"`
	// Target is audience (all, men, women, kids).
	Target string `json:"target,omitempty"`
	// OfficialPrice is the reference / list price in KRW won (not charged).
	OfficialPrice float64 `json:"officialPrice,omitempty"`
	// Price is the actual sale price in KRW won after discounts (whole won).
	// Stored on the parent; all variants inherit this price for cart/order.
	Price    float64  `json:"price"`
	Status   string   `json:"status"` // "active" | "draft" | "archived"
	Capacity string   `json:"capacity,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// Attributes is a free-form string key/value memo for PDP display
	// (condition, care, authenticity notes, etc.). Not used for search,
	// pricing, or checkout. Managers write; storefront treats as read-only.
	Attributes map[string]string `json:"attributes,omitempty"`
	// ViewCount is unique guest PDP views (denormalized). Public on PDP and recs.
	ViewCount int64 `json:"viewCount"`
	// SoldCount is units committed from inventory reservations (denormalized).
	// Incremented when a reservation is committed (order ship → in_transit), not on payment.
	SoldCount int64 `json:"soldCount"`
	// WishlistCount is unique owners who wishlisted this parent (denormalized).
	WishlistCount int64  `json:"wishlistCount"`
	CreatedAt     string `json:"createdAt"`
	// UpdatedAt is server-set on every write (create and update); never taken
	// from an incoming request body.
	UpdatedAt string `json:"updatedAt,omitempty"`
	CreatedBy string `json:"createdBy,omitempty"`

	// Summary fields derived from variants (not separate storage).
	DefaultImageURL string `json:"defaultImageUrl,omitempty"`
	// DefaultListingImageURL is the listing-size JPEG for category/home cards
	// (from the default active variant's first listingImageUrls entry).
	DefaultListingImageURL string `json:"defaultListingImageUrl,omitempty"`
	AvailableColors        []string `json:"availableColors,omitempty"`
	AvailableSizes         []string `json:"availableSizes,omitempty"`
	Variants               []Variant `json:"variants,omitempty"`

	// Legacy display fields mirrored from the default active variant.
	Color string `json:"color,omitempty"`
	// Stock is legacy parent-level stock. Availability lives on variants
	// (availableQty/inStock) via stock_items. Omitted from API responses.
	Stock     int      `json:"-"`
	ImageURLs []string `json:"imageUrls,omitempty"`
}
