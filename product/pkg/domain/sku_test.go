package domain

import "testing"

func TestBuildSKU(t *testing.T) {
	sku, err := BuildSKU(SKUParts{
		BrandCode:   "BOT",
		StyleCode:   "CAS001",
		ColorCode:   "BLK",
		EditionCode: "V",
		SizeCode:    "MED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sku != "BOT_CAS001_BLK_V_MED" {
		t.Fatalf("got %q", sku)
	}
}

func TestBuildSKUWithoutEdition(t *testing.T) {
	sku, err := BuildSKU(SKUParts{
		BrandCode: "PR",
		StyleCode: "1BA457",
		ColorCode: "F0032",
		SizeCode:  "YO0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sku != "PR_1BA457_F0032_YO0" {
		t.Fatalf("got %q", sku)
	}
}

func TestBuildSKUDeterministic(t *testing.T) {
	parts := SKUParts{BrandCode: "bot", StyleCode: "cas001", ColorCode: "blk", EditionCode: "v", SizeCode: "med"}
	a, err := BuildSKU(parts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildSKU(parts)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a != "BOT_CAS001_BLK_V_MED" {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}
}

func TestParseSKU(t *testing.T) {
	p, err := ParseSKU("PR_1BA457_F0032_V_YO0")
	if err != nil {
		t.Fatal(err)
	}
	if p.BrandCode != "PR" || p.StyleCode != "1BA457" || p.ColorCode != "F0032" || p.EditionCode != "V" || p.SizeCode != "YO0" {
		t.Fatalf("unexpected parts: %+v", p)
	}

	p4, err := ParseSKU("BOT_CAS001_BLK_MED")
	if err != nil {
		t.Fatal(err)
	}
	if p4.EditionCode != "" || p4.SizeCode != "MED" {
		t.Fatalf("unexpected 4-segment parse: %+v", p4)
	}
}

func TestValidateBrandCode(t *testing.T) {
	if !ValidBrandCode("PR") || !ValidBrandCode("BOT") {
		t.Fatal("expected valid brand codes")
	}
	if ValidBrandCode("P") || ValidBrandCode("PRADA") || ValidBrandCode("b1") {
		t.Fatal("expected invalid brand codes")
	}
}

func TestBrandCodeFromName(t *testing.T) {
	if got := BrandCodeFromName("Bottega Veneta"); got != "BOT" {
		t.Fatalf("got %q", got)
	}
	if got := BrandCodeFromName("Prada"); got != "PR" {
		t.Fatalf("got %q", got)
	}
	if got := BrandCodeFromName("Gucci"); got != "GUC" {
		t.Fatalf("got %q", got)
	}
	if got := BrandCodeFromName("Dupli1 Studio"); got != "DUP" {
		t.Fatalf("got %q", got)
	}
}

func TestColorAndSizeFromName(t *testing.T) {
	if got := ColorCodeFromName("Black"); got != "BLK" {
		t.Fatalf("color got %q", got)
	}
	if got := ColorCodeFromName("F0032"); got != "F0032" {
		t.Fatalf("numeric color got %q", got)
	}
	if got := SizeCodeFromName("Medium"); got != "MED" {
		t.Fatalf("size got %q", got)
	}
	if got := SizeCodeFromName("M"); got != "M" {
		t.Fatalf("size M got %q", got)
	}
}

func TestStyleCodeFromProductID(t *testing.T) {
	if got := StyleCodeFromProductID("BOT-001"); got != "S001" {
		t.Fatalf("got %q", got)
	}
	if got := StyleCodeFromProductID("GUC-12"); got != "S012" {
		t.Fatalf("got %q", got)
	}
}

func TestComposeVariantSKULuxury(t *testing.T) {
	v := &Variant{Color: "Black", Size: "Medium", EditionCode: "V"}
	sku := ComposeVariantSKU("BOT-001", "BOT", "CAS001", v)
	if sku != "BOT_CAS001_BLK_V_MED" {
		t.Fatalf("got %q (codes: color=%q size=%q)", sku, v.ColorCode, v.SizeCode)
	}
}

func TestComposeVariantSKULegacyFallback(t *testing.T) {
	v := &Variant{Color: "Green", Size: "M"}
	sku := ComposeVariantSKU("BOT-001", "", "", v)
	if sku != "BOT-001-GRE-MXX" {
		t.Fatalf("got %q", sku)
	}
}

func TestAssignProductCodes(t *testing.T) {
	p := Product{ID: "BOT-001", Brand: "Bottega Veneta"}
	AssignProductCodes(&p)
	if p.BrandCode != "BOT" || p.StyleCode != "S001" {
		t.Fatalf("got brand=%q style=%q", p.BrandCode, p.StyleCode)
	}

	ulidProduct := Product{ID: "01JAY6Z9K3F8QW1G7H2T5X0ABC", Brand: "Bottega Veneta", StyleCode: "CAS001"}
	AssignProductCodes(&ulidProduct)
	if ulidProduct.BrandCode != "BOT" || ulidProduct.StyleCode != "CAS001" {
		t.Fatalf("ulid product codes: brand=%q style=%q", ulidProduct.BrandCode, ulidProduct.StyleCode)
	}
	if err := RequireProductSKUCodes(&Product{Brand: "Bottega Veneta"}); err == nil {
		t.Fatal("expected error without styleCode for ULID-era create")
	}
}
