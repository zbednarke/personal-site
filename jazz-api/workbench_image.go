package main

import (
	"bytes"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Images attached to Workbench messages are sent to the model inline, so
// they are validated and kept small on upload: at most 1568 px on the long
// side (the size the model sees anyway), re-encoded as JPEG (PNG when the
// image has transparency). Small images in an accepted format pass through.

const (
	wbImageMaxSide   = 1568
	wbImageMaxPixels = 50_000_000
	wbImageKeepBytes = 1 << 20
)

func wbPrepareImage(body []byte) ([]byte, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, "", errors.New("unreadable image")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 12000 || cfg.Height > 12000 || cfg.Width*cfg.Height > wbImageMaxPixels {
		return nil, "", errors.New("image dimensions are too large")
	}
	small := cfg.Width <= wbImageMaxSide && cfg.Height <= wbImageMaxSide
	if small && len(body) <= wbImageKeepBytes && (format == "jpeg" || format == "png") {
		return body, "image/" + format, nil
	}
	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, "", errors.New("unreadable image")
	}
	w, h := cfg.Width, cfg.Height
	if !small {
		if w >= h {
			w, h = wbImageMaxSide, max(1, h*wbImageMaxSide/w)
		} else {
			w, h = max(1, w*wbImageMaxSide/h), wbImageMaxSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	if !dst.Opaque() {
		if err := png.Encode(&out, dst); err != nil {
			return nil, "", err
		}
		return out.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", err
	}
	return out.Bytes(), "image/jpeg", nil
}

var _ = gif.Decode // registers the GIF decoder
