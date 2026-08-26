package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"time"
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

// fullPageScreenshot stitches viewport screenshots into one full-height image.
func (s *server) fullPageScreenshot(ctx string) (string, error) {
	// Measure the document height.
	hRes, err := s.bidiCall("script.evaluate", map[string]any{
		"expression":   `({ sh: document.documentElement.scrollHeight, ih: window.innerHeight, w: window.innerWidth })`,
		"target":       map[string]any{"context": ctx},
		"awaitPromise": true,
	})
	if err != nil {
		return "", err
	}
	var hOut struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(hRes, &hOut); err != nil {
		return "", err
	}
	hv := unwrapRemoteVal(hOut.Result)
	hm, _ := hv.(map[string]any)
	scrollH := int(numF(hm, "sh"))
	viewH := int(numF(hm, "ih"))
	width := int(numF(hm, "w"))
	if width <= 0 {
		width = 1280
	}
	if scrollH <= viewH || viewH <= 0 {
		// Single viewport suffices.
		raw, err := s.bidiCall("browsingContext.captureScreenshot", map[string]any{"context": ctx})
		if err != nil {
			return "", err
		}
		var out struct {
			Data string `json:"data"`
		}
		_ = json.Unmarshal(raw, &out)
		return out.Data, nil
	}

	pieces := []image.Image{}
	y := 0
	for y < scrollH {
		// Scroll so the top of the viewport aligns with y.
		_, err := s.bidiCall("script.evaluate", map[string]any{
			"expression":   fmt.Sprintf(`window.scrollTo(0, %d)`, y),
			"target":       map[string]any{"context": ctx},
			"awaitPromise": true,
		})
		if err != nil {
			return "", err
		}
		// Small settle for scroll-driven lazy content.
		time.Sleep(50 * time.Millisecond)
		raw, err := s.bidiCall("browsingContext.captureScreenshot", map[string]any{"context": ctx})
		if err != nil {
			return "", err
		}
		var out struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", err
		}
		rawBytes, err := base64.StdEncoding.DecodeString(out.Data)
		if err != nil {
			return "", err
		}
		img, _, err := image.Decode(bytes.NewReader(rawBytes))
		if err != nil {
			return "", err
		}
		pieces = append(pieces, img)
		y += viewH
	}

	canvas := image.NewRGBA(image.Rect(0, 0, width, scrollH))
	drawY := 0
	for _, p := range pieces {
		draw.Draw(canvas, image.Rect(0, drawY, width, drawY+p.Bounds().Dy()), p, image.Point{}, draw.Src)
		drawY += p.Bounds().Dy()
	}
	// Trim to actual accumulated height.
	canvas = canvas.SubImage(image.Rect(0, 0, width, min(drawY, scrollH))).(*image.RGBA)
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func unwrapRemoteVal(v any) any {
	return unwrapRemote(parseRaw(v))
}

func parseRaw(v any) any {
	if r, ok := v.(json.RawMessage); ok {
		var out any
		if json.Unmarshal(r, &out) == nil {
			return out
		}
	}
	return v
}
