package domain

// MergeUpdate returns a copy of the variant with any non-zero-value fields
// from incoming applied on top. Used by UpdateVariant so a partial request
// body (e.g. color-only) can't silently blank out size/status/images —
// omitted fields keep their current value instead of being overwritten with
// the JSON zero value. Identity fields (SkuID, SKU, ProductID, CreatedAt)
// and price fields (owned by the parent product) are never taken from incoming.
func (existing Variant) MergeUpdate(incoming Variant) Variant {
	merged := existing
	if incoming.Color != "" {
		merged.Color = incoming.Color
	}
	if incoming.Size != "" {
		merged.Size = incoming.Size
	}
	if incoming.ColorCode != "" {
		merged.ColorCode = incoming.ColorCode
	}
	if incoming.EditionCode != "" {
		merged.EditionCode = incoming.EditionCode
	}
	if incoming.SizeCode != "" {
		merged.SizeCode = incoming.SizeCode
	}
	if incoming.Status != "" {
		merged.Status = incoming.Status
	}
	if len(incoming.ImageURLs) > 0 {
		merged.ImageURLs = incoming.ImageURLs
	}
	if len(incoming.ListingImageURLs) > 0 {
		merged.ListingImageURLs = incoming.ListingImageURLs
	}
	// Dimensions: nil means omit (keep existing); non-nil replaces
	// (including {} which NormalizeDimensions treats as clear).
	if incoming.Dimensions != nil {
		merged.Dimensions = incoming.Dimensions
	}
	// Price / OfficialPrice stay on the parent product.
	merged.Price = existing.Price
	merged.OfficialPrice = existing.OfficialPrice
	return merged
}

// MergeUpdate returns a copy of the product with non-zero / non-empty fields
// from incoming applied on top. Used by UpdateProduct so a partial body
// (e.g. style-only) cannot wipe price, name, or other omitted fields with
// JSON zero values. BrandCode / StyleCode / counters / timestamps are never
// taken from incoming.
func (existing Product) MergeUpdate(incoming Product) Product {
	merged := existing
	if incoming.Name != "" {
		merged.Name = incoming.Name
	}
	if incoming.Description != "" {
		merged.Description = incoming.Description
	}
	if incoming.Brand != "" {
		merged.Brand = incoming.Brand
	}
	if incoming.Material != "" {
		merged.Material = incoming.Material
	}
	if incoming.Category != "" {
		merged.Category = incoming.Category
	}
	if incoming.SubCategory != "" {
		merged.SubCategory = incoming.SubCategory
	}
	if incoming.Style != "" {
		merged.Style = incoming.Style
	}
	if incoming.Target != "" {
		merged.Target = incoming.Target
	}
	if incoming.Status != "" {
		merged.Status = incoming.Status
	}
	if incoming.Capacity != "" {
		merged.Capacity = incoming.Capacity
	}
	if incoming.Tags != nil {
		merged.Tags = incoming.Tags
	}
	if incoming.Attributes != nil {
		merged.Attributes = incoming.Attributes
	}
	if incoming.Price != 0 {
		merged.Price = incoming.Price
	}
	if incoming.OfficialPrice != 0 {
		merged.OfficialPrice = incoming.OfficialPrice
	}
	// Identity / denormalized counters stay on the existing row.
	merged.ID = existing.ID
	merged.BrandCode = existing.BrandCode
	merged.StyleCode = existing.StyleCode
	merged.CreatedAt = existing.CreatedAt
	merged.UpdatedAt = existing.UpdatedAt
	merged.CreatedBy = existing.CreatedBy
	merged.ViewCount = existing.ViewCount
	merged.SoldCount = existing.SoldCount
	merged.WishlistCount = existing.WishlistCount
	return merged
}

// ApplyParentPrice copies the parent product's price onto a variant for API
// responses (cart/order still read price from the variant JSON).
func (v *Variant) ApplyParentPrice(p Product) {
	if v == nil {
		return
	}
	v.Price = p.Price
	v.OfficialPrice = p.OfficialPrice
}

// EnrichFromVariants fills summary and legacy display fields from variants.
// Price / OfficialPrice stay on the parent. When includeVariants is false,
// Variants is left empty (list/search cards).
func (p *Product) EnrichFromVariants(variants []Variant, includeVariants bool) {
	if includeVariants {
		stamped := make([]Variant, len(variants))
		for i := range variants {
			stamped[i] = variants[i]
			stamped[i].ApplyParentPrice(*p)
		}
		p.Variants = stamped
	} else {
		p.Variants = nil
	}

	colors := make([]string, 0)
	sizes := make([]string, 0)
	colorSeen := map[string]bool{}
	sizeSeen := map[string]bool{}
	var defaultVariant *Variant

	for i := range variants {
		v := &variants[i]
		if v.Status != "" && v.Status != "active" {
			continue
		}
		if defaultVariant == nil {
			defaultVariant = v
		}
		if v.Color != "" && !colorSeen[v.Color] {
			colorSeen[v.Color] = true
			colors = append(colors, v.Color)
		}
		if v.Size != "" && !sizeSeen[v.Size] {
			sizeSeen[v.Size] = true
			sizes = append(sizes, v.Size)
		}
	}

	p.AvailableColors = colors
	p.AvailableSizes = sizes
	if defaultVariant != nil {
		p.Color = defaultVariant.Color
		p.ImageURLs = defaultVariant.ImageURLs
		if len(defaultVariant.ImageURLs) > 0 {
			p.DefaultImageURL = defaultVariant.ImageURLs[0]
		}
		if len(defaultVariant.ListingImageURLs) > 0 {
			p.DefaultListingImageURL = defaultVariant.ListingImageURLs[0]
		}
	}
}
