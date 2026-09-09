package imaging_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/elug3/dupli1/product/pkg/imaging"
)

func solidJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 160, B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestListingJPEGDownscales(t *testing.T) {
	data := solidJPEG(t, 2000, 2000)
	out, err := imaging.ListingJPEG(data, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	b := decoded.Bounds()
	if b.Dx() != imaging.ListingMaxWidth || b.Dy() != imaging.ListingMaxWidth {
		t.Fatalf("got %dx%d, want %dx%d", b.Dx(), b.Dy(), imaging.ListingMaxWidth, imaging.ListingMaxWidth)
	}
}

func TestListingJPEGNoUpscale(t *testing.T) {
	data := solidJPEG(t, 400, 300)
	out, err := imaging.ListingJPEG(data, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	b := decoded.Bounds()
	if b.Dx() != 400 || b.Dy() != 300 {
		t.Fatalf("upscaled unexpectedly: %dx%d", b.Dx(), b.Dy())
	}
}

func TestListingJPEGPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 400))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	out, err := imaging.ListingJPEG(buf.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	b := decoded.Bounds()
	if b.Dx() != imaging.ListingMaxWidth || b.Dy() != 300 {
		t.Fatalf("got %dx%d", b.Dx(), b.Dy())
	}
}

func TestListingObjectKey(t *testing.T) {
	got := imaging.ListingObjectKey("BOT-001/sku/abc")
	want := "BOT-001/sku/abc" + imaging.ListingThumbSuffix
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestListingJPEGEmpty(t *testing.T) {
	if _, err := imaging.ListingJPEG(nil, ""); err == nil {
		t.Fatal("expected error")
	}
}
