package domain

import "strings"

// Brand is a master brand entity. Code is 2–3 uppercase letters and never changes.
type Brand struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Style is a master style (design family) scoped to a brand.
type Style struct {
	BrandCode string `json:"brandCode"`
	Code      string `json:"code"`
	Name      string `json:"name"`
}

// Color is a master color entity. Code is reused across all products.
type Color struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Size is a master size / capacity entity.
type Size struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Edition is a construction / manufacturing variant (SKU VariantCode segment).
// Named Edition in code to avoid confusion with product_variants (sellable rows).
type Edition struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Well-known brand seeds (code → display name).
var SeedBrands = []Brand{
	{Code: "DUP", Name: "Dupli1 Studio"},
	{Code: "PR", Name: "Prada"},
	{Code: "BOT", Name: "Bottega Veneta"},
	{Code: "LV", Name: "Louis Vuitton"},
	{Code: "CD", Name: "Dior"},
	{Code: "CH", Name: "Chanel"},
	{Code: "GUC", Name: "Gucci"},
	{Code: "HER", Name: "Hermes"},
	{Code: "CEL", Name: "Celine"},
	{Code: "BAL", Name: "Balenciaga"},
	{Code: "FEN", Name: "Fendi"},
	{Code: "YSL", Name: "Saint Laurent"},
	{Code: "BUR", Name: "Burberry"},
}

// Well-known color seeds.
var SeedColors = []Color{
	{Code: "BLK", Name: "Black"},
	{Code: "WHT", Name: "White"},
	{Code: "CRM", Name: "Cream"},
	{Code: "BRN", Name: "Brown"},
	{Code: "GRN", Name: "Green"},
	{Code: "RED", Name: "Red"},
	{Code: "BLU", Name: "Blue"},
	{Code: "BGE", Name: "Beige"},
	{Code: "NVY", Name: "Navy"},
	{Code: "PNK", Name: "Pink"},
	{Code: "GRY", Name: "Grey"},
	{Code: "GLD", Name: "Gold"},
	{Code: "SLV", Name: "Silver"},
	{Code: "ORG", Name: "Orange"},
	{Code: "YLW", Name: "Yellow"},
	{Code: "PRP", Name: "Purple"},
	{Code: "TAN", Name: "Tan"},
	{Code: "OLV", Name: "Olive"},
}

// Well-known size seeds (apparel + bag capacity shorthand).
var SeedSizes = []Size{
	{Code: "XXS", Name: "XXS"},
	{Code: "XS", Name: "XS"},
	{Code: "S", Name: "S"},
	{Code: "M", Name: "M"},
	{Code: "L", Name: "L"},
	{Code: "XL", Name: "XL"},
	{Code: "XXL", Name: "XXL"},
	{Code: "MIN", Name: "Mini"},
	{Code: "SML", Name: "Small"},
	{Code: "MED", Name: "Medium"},
	{Code: "LRG", Name: "Large"},
	{Code: "OS", Name: "One Size"},
}

// Well-known edition (VariantCode) seeds.
var SeedEditions = []Edition{
	{Code: "V", Name: "Standard"},
	{Code: "A", Name: "Alternate construction"},
	{Code: "R", Name: "Limited / special edition"},
}

// BrandCodeFromName resolves a display brand name to a seeded code when possible.
// Falls back to the first-word 2–3 letter prefix (same rules as legacy brandPrefix).
func BrandCodeFromName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, b := range SeedBrands {
		if strings.ToLower(b.Name) == lower {
			return b.Code
		}
		fields := strings.Fields(b.Name)
		if len(fields) > 0 && strings.HasPrefix(lower, strings.ToLower(fields[0])) {
			return b.Code
		}
	}
	return legacyBrandPrefix(trimmed)
}

// ColorCodeFromName resolves a display color name to a seeded code when possible.
func ColorCodeFromName(name string) string {
	code := NormalizeCode(name)
	if code == "" {
		return ""
	}
	for _, c := range SeedColors {
		if c.Code == code {
			return c.Code
		}
		if strings.EqualFold(c.Name, strings.TrimSpace(name)) {
			return c.Code
		}
	}
	// Already looks like a short code (e.g. F0032, BLK).
	if ValidSegmentCode(code) && len(code) <= 6 {
		return code
	}
	return OptionCode(name)
}

// SizeCodeFromName resolves a display size name to a seeded code when possible.
func SizeCodeFromName(name string) string {
	code := NormalizeCode(name)
	if code == "" {
		return ""
	}
	for _, s := range SeedSizes {
		if s.Code == code {
			return s.Code
		}
		if strings.EqualFold(s.Name, strings.TrimSpace(name)) {
			return s.Code
		}
	}
	if ValidSegmentCode(code) && len(code) <= 6 {
		return code
	}
	return OptionCode(name)
}

// EditionCodeFromName resolves a display edition name or code.
func EditionCodeFromName(name string) string {
	code := NormalizeCode(name)
	if code == "" {
		return ""
	}
	for _, e := range SeedEditions {
		if e.Code == code {
			return e.Code
		}
		if strings.EqualFold(e.Name, strings.TrimSpace(name)) {
			return e.Code
		}
	}
	if ValidSegmentCode(code) {
		return code
	}
	return ""
}

func legacyBrandPrefix(brand string) string {
	fields := strings.Fields(strings.TrimSpace(brand))
	word := "PRD"
	if len(fields) > 0 {
		word = fields[0]
	}
	runes := []rune(strings.ToUpper(word))
	if len(runes) > 3 {
		runes = runes[:3]
	}
	for len(runes) < 2 {
		runes = append(runes, 'X')
	}
	// Brand codes are 2–3 letters only.
	out := make([]rune, 0, 3)
	for _, r := range runes {
		if r >= 'A' && r <= 'Z' {
			out = append(out, r)
		}
		if len(out) == 3 {
			break
		}
	}
	for len(out) < 2 {
		out = append(out, 'X')
	}
	return string(out)
}
