package templates

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // DecodeConfig of GIF icons
	_ "image/jpeg" // DecodeConfig of JPEG icons
	_ "image/png"  // DecodeConfig of PNG icons
	"strings"
	"unicode/utf8"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Icon media types.
const (
	IconPNG  = "image/png"
	IconJPEG = "image/jpeg"
	IconGIF  = "image/gif"
	IconWebP = "image/webp"
	IconSVG  = "image/svg+xml"
)

// maxIconSide bounds a raster icon's width and height in pixels.
const maxIconSide = 1024

// svgRefused are SVG constructs icons may not contain: entity tricks and
// active content (icons are only ever rendered as images, which run no
// scripts, but a directly opened icon URL must not either).
var svgRefused = []string{"<!doctype", "<!entity", "<script", "javascript:", "<foreignobject", "<iframe", "<embed", "<object"}

// DetectIcon checks icon bytes and returns their description: PNG, JPEG,
// GIF, WebP or SVG, at most 256 KiB; raster icons at most 1024x1024 pixels.
// The type is detected from the bytes, never from a name or header.
func DetectIcon(data []byte) (domain.TemplateIcon, error) {
	if len(data) == 0 {
		return domain.TemplateIcon{}, &domain.TemplateIconError{Message: "the icon is empty"}
	}
	if len(data) > domain.MaxTemplateIcon {
		return domain.TemplateIcon{}, &domain.TemplateIconError{TooLarge: true, Message: "icons are limited to 256 KiB"}
	}
	mt := ""
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		mt = IconPNG
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		mt = IconJPEG
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		mt = IconGIF
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		mt = IconWebP
	default:
		if err := checkSVG(data); err != nil {
			return domain.TemplateIcon{}, err
		}
		mt = IconSVG
	}
	switch mt {
	case IconPNG, IconJPEG, IconGIF:
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return domain.TemplateIcon{}, &domain.TemplateIconError{Message: "the image cannot be read"}
		}
		if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxIconSide || cfg.Height > maxIconSide {
			return domain.TemplateIcon{}, &domain.TemplateIconError{Message: fmt.Sprintf("icons are at most %dx%d pixels", maxIconSide, maxIconSide)}
		}
	}
	sum := sha256.Sum256(data)
	return domain.TemplateIcon{MediaType: mt, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}, nil
}

var unsupported = &domain.TemplateIconError{Message: "use a PNG, JPEG, GIF, WebP or SVG image"}

// checkSVG accepts UTF-8 text whose first element is <svg> (after an XML
// declaration and comments) without refused constructs.
func checkSVG(data []byte) error {
	if !utf8.Valid(data) {
		return unsupported
	}
	s := strings.TrimPrefix(string(data), string(rune(0xFEFF)))
	lower := strings.ToLower(s)
	for _, r := range svgRefused {
		if strings.Contains(lower, r) {
			return &domain.TemplateIconError{Message: "SVG icons may not contain scripts, embedded documents or DOCTYPE/ENTITY declarations"}
		}
	}
	rest := strings.TrimSpace(lower)
	for {
		switch {
		case strings.HasPrefix(rest, "<?xml"):
			i := strings.Index(rest, "?>")
			if i < 0 {
				return unsupported
			}
			rest = strings.TrimSpace(rest[i+2:])
		case strings.HasPrefix(rest, "<!--"):
			i := strings.Index(rest, "-->")
			if i < 0 {
				return unsupported
			}
			rest = strings.TrimSpace(rest[i+3:])
		case strings.HasPrefix(rest, "<svg") && len(rest) > 4 && strings.ContainsRune(" \t\r\n>/", rune(rest[4])):
			return nil
		default:
			return unsupported
		}
	}
}
