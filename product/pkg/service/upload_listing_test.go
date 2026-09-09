package service_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"strings"
	"testing"

	"github.com/elug3/dupli1/product/pkg/domain"
	"github.com/elug3/dupli1/product/pkg/imaging"
	"github.com/elug3/dupli1/product/pkg/infra/memory"
	"github.com/elug3/dupli1/product/pkg/service"
)

type memImageStore struct {
	objects map[string][]byte
	types   map[string]string
}

func (s *memImageStore) Upload(ctx context.Context, objectKey string, r io.Reader, size int64, contentType string) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if s.objects == nil {
		s.objects = map[string][]byte{}
		s.types = map[string]string{}
	}
	s.objects[objectKey] = data
	s.types[objectKey] = contentType
	return "https://cdn.test/" + objectKey, nil
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 180, G: 140, B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUploadVariantImageWritesListingThumb(t *testing.T) {
	store := memory.NewProductStore()
	store.Products = []domain.Product{
		{ID: "BOT-001", Name: "Cassette", Status: "active", Price: 2500},
	}
	store.Variants = []domain.Variant{
		{SKU: "BOT-001", ProductID: "BOT-001", Status: "active"},
	}
	images := &memImageStore{}
	svc := service.NewProductSearchService(store, images)

	data := jpegBytes(t, 1200, 1200)
	v, err := svc.UploadVariantImage(t.Context(), "BOT-001", "BOT-001", bytes.NewReader(data), int64(len(data)), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.ImageURLs) != 1 || len(v.ListingImageURLs) != 1 {
		t.Fatalf("urls image=%v listing=%v", v.ImageURLs, v.ListingImageURLs)
	}
	if !strings.HasSuffix(v.ListingImageURLs[0], imaging.ListingThumbSuffix) {
		t.Fatalf("listing url = %q", v.ListingImageURLs[0])
	}
	if len(images.objects) != 2 {
		t.Fatalf("want 2 uploaded objects, got %d keys=%v", len(images.objects), keys(images.objects))
	}
	var listingKey string
	for k := range images.objects {
		if strings.HasSuffix(k, imaging.ListingThumbSuffix) {
			listingKey = k
		}
	}
	if listingKey == "" {
		t.Fatal("missing listing object key")
	}
	if images.types[listingKey] != imaging.ListingContentType {
		t.Fatalf("listing content-type = %q", images.types[listingKey])
	}

	p, err := svc.GetPublicProduct(t.Context(), "BOT-001")
	if err != nil {
		t.Fatal(err)
	}
	if p.DefaultListingImageURL == "" || p.DefaultImageURL == "" {
		t.Fatalf("defaults image=%q listing=%q", p.DefaultImageURL, p.DefaultListingImageURL)
	}
	if p.DefaultListingImageURL != v.ListingImageURLs[0] {
		t.Fatalf("default listing = %q want %q", p.DefaultListingImageURL, v.ListingImageURLs[0])
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
