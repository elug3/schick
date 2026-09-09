// Package imaging builds listing-size JPEG thumbnails for product uploads.
// Output is JPEG (not WebP) so product builds stay CGO_ENABLED=0 for Alpine.
package imaging

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/gif"
	_ "image/png"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ListingMaxWidth is the longest edge for category/home listing cards.
const ListingMaxWidth = 600

// ListingThumbSuffix is appended to the original object key for the listing JPEG.
// Example: {productId}/{sku}/{uuid}.w600.jpg
const ListingThumbSuffix = ".w600.jpg"

// ListingJPEGQuality is the JPEG encoder quality (1–100).
const ListingJPEGQuality = 82

// ListingContentType is the Content-Type for listing thumbs.
const ListingContentType = "image/jpeg"

// ListingObjectKey returns the sibling listing thumb key for an original object key.
func ListingObjectKey(originalKey string) string {
	return strings.TrimLeft(originalKey, "/") + ListingThumbSuffix
}

// ListingJPEG decodes image bytes, scales so the longest edge is at most
// ListingMaxWidth (never upscales), and encodes a JPEG. contentType is optional
// (image.Decode sniffs format).
func ListingJPEG(data []byte, contentType string) ([]byte, error) {
	_ = contentType
	if len(data) == 0 {
		return nil, fmt.Errorf("empty image")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	dst := fitMax(src, ListingMaxWidth)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: ListingJPEGQuality}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

func fitMax(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxEdge {
		return src
	}
	scale := float64(maxEdge) / float64(longest)
	nw := int(float64(w)*scale + 0.5)
	nh := int(float64(h)*scale + 0.5)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}
