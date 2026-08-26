package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
)

// cropPNG decodes a base64 PNG, crops to (x,y,w,h), re-encodes as PNG, and
// returns base64. Clips the crop to the image bounds.
func cropPNG(b64 string, x, y, w, h int) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("screenshot decode: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("png decode: %w", err)
	}
	b := img.Bounds()
	if x < b.Min.X {
		x = b.Min.X
	}
	if y < b.Min.Y {
		y = b.Min.Y
	}
	if x+w > b.Max.X {
		w = b.Max.X - x
	}
	if y+h > b.Max.Y {
		h = b.Max.Y - y
	}
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("crop rectangle empty")
	}
	sub, ok := img.(interface {
		SubImage(r image.Rectangle) image.Image
	})
	var cropped image.Image
	if ok {
		cropped = sub.SubImage(image.Rect(x, y, x+w, y+h))
	} else {
		cropped = img
	}
	var out bytes.Buffer
	if err := png.Encode(&out, cropped); err != nil {
		return "", fmt.Errorf("png encode: %w", err)
	}
	return base64.StdEncoding.EncodeToString(out.Bytes()), nil
}
