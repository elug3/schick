package domain_test

import (
	"testing"

	"github.com/elug3/dupli1/product/pkg/domain"
)

func TestEnrichFromVariantsSummaries(t *testing.T) {
	p := domain.Product{ID: "BOT-001", Price: 2500, OfficialPrice: 3000}
	variants := []domain.Variant{
		{SKU: "BOT-001-GRN", ProductID: "BOT-001", Color: "Green", Status: "active", ImageURLs: []string{"green.jpg"}, ListingImageURLs: []string{"green.w600.jpg"}},
		{SKU: "BOT-001-BLK", ProductID: "BOT-001", Color: "Black", Status: "active", ImageURLs: []string{"black.jpg"}, ListingImageURLs: []string{"black.w600.jpg"}},
		{SKU: "BOT-001-RED", ProductID: "BOT-001", Color: "Red", Status: "draft"},
	}

	p.EnrichFromVariants(variants, true)

	if len(p.AvailableColors) != 2 || p.AvailableColors[0] != "Green" || p.AvailableColors[1] != "Black" {
		t.Fatalf("availableColors = %v", p.AvailableColors)
	}
	if p.Price != 2500 || p.OfficialPrice != 3000 {
		t.Fatalf("want price=2500 official=3000, got price=%v official=%v", p.Price, p.OfficialPrice)
	}
	if len(p.Variants) != 3 {
		t.Fatalf("want 3 variants, got %d", len(p.Variants))
	}
	if p.Variants[0].Price != 2500 || p.Variants[0].OfficialPrice != 3000 {
		t.Fatalf("variant should inherit parent price, got %+v", p.Variants[0])
	}
	if p.DefaultImageURL != "green.jpg" || p.Color != "Green" {
		t.Fatalf("default variant mirror: color=%q image=%q", p.Color, p.DefaultImageURL)
	}
	if p.DefaultListingImageURL != "green.w600.jpg" {
		t.Fatalf("defaultListingImageUrl = %q", p.DefaultListingImageURL)
	}
}

func TestEnrichFromVariantsListCardOmitsVariants(t *testing.T) {
	p := domain.Product{ID: "BOT-001", Price: 100}
	p.EnrichFromVariants([]domain.Variant{
		{SKU: "BOT-001-GRN", Color: "Green", Status: "active"},
	}, false)
	if p.Variants != nil {
		t.Fatalf("list card should omit variants, got %v", p.Variants)
	}
	if p.Price != 100 {
		t.Fatalf("price = %v, want 100", p.Price)
	}
}

func TestVariantMergeUpdate_PartialBodyKeepsOmittedFields(t *testing.T) {
	existing := domain.Variant{
		SKU:           "BOT-001-GRN",
		ProductID:     "BOT-001",
		Color:         "Green",
		Size:          "M",
		ColorCode:     "GRN",
		SizeCode:      "M",
		OfficialPrice: 3200,
		Price:         2600,
		Status:        "active",
		ImageURLs:     []string{"green.jpg"},
	}

	merged := existing.MergeUpdate(domain.Variant{Color: "Black", Price: 1, OfficialPrice: 2})
	if merged.Color != "Black" {
		t.Fatalf("color = %q, want Black", merged.Color)
	}
	if merged.Price != 2600 || merged.OfficialPrice != 3200 {
		t.Fatalf("price fields must stay from existing (parent-owned), got price=%v official=%v", merged.Price, merged.OfficialPrice)
	}
	if merged.Size != "M" || merged.Status != "active" || len(merged.ImageURLs) != 1 {
		t.Fatalf("omitted fields cleared: %+v", merged)
	}
}

func TestVariantMergeUpdate_FullBodyReplacesEverything(t *testing.T) {
	existing := domain.Variant{
		SKU: "BOT-001-GRN", Color: "Green", Size: "M", Price: 2600, Status: "draft",
		ImageURLs: []string{"old.jpg"},
	}

	merged := existing.MergeUpdate(domain.Variant{
		Color: "Black", Size: "L", Price: 2700, Status: "active",
		ImageURLs: []string{"new.jpg"},
	})

	if merged.Color != "Black" || merged.Size != "L" || merged.Status != "active" {
		t.Fatalf("got %+v", merged)
	}
	if merged.Price != 2600 {
		t.Fatalf("price must remain parent-owned existing value, got %v", merged.Price)
	}
	if len(merged.ImageURLs) != 1 || merged.ImageURLs[0] != "new.jpg" {
		t.Fatalf("imageUrls = %v", merged.ImageURLs)
	}
}

func TestProductMergeUpdate_StyleOnlyKeepsPrice(t *testing.T) {
	existing := domain.Product{
		ID: "BOT-001", Name: "Cassette", Brand: "Bottega", BrandCode: "BOT", StyleCode: "CAS001",
		Category: "bags", Style: "casual", Target: "women",
		Price: 2500, OfficialPrice: 3000, Status: "active",
		ViewCount: 7, SoldCount: 2, WishlistCount: 1,
		CreatedAt: "2026-01-01T00:00:00Z", CreatedBy: "admin",
	}
	merged := existing.MergeUpdate(domain.Product{Style: "evening"})
	if merged.Style != "evening" {
		t.Fatalf("style = %q, want evening", merged.Style)
	}
	if merged.Price != 2500 || merged.OfficialPrice != 3000 {
		t.Fatalf("price wiped by style-only update: price=%v official=%v", merged.Price, merged.OfficialPrice)
	}
	if merged.Name != "Cassette" || merged.BrandCode != "BOT" || merged.StyleCode != "CAS001" {
		t.Fatalf("identity/name cleared: %+v", merged)
	}
	if merged.ViewCount != 7 || merged.CreatedBy != "admin" {
		t.Fatalf("counters/audit cleared: %+v", merged)
	}
}

func TestProductMergeUpdate_ExplicitPriceChange(t *testing.T) {
	existing := domain.Product{ID: "BOT-001", Price: 2500, OfficialPrice: 3000, Name: "Cassette"}
	merged := existing.MergeUpdate(domain.Product{Price: 2800})
	if merged.Price != 2800 || merged.OfficialPrice != 3000 || merged.Name != "Cassette" {
		t.Fatalf("got price=%v official=%v name=%q", merged.Price, merged.OfficialPrice, merged.Name)
	}
}

