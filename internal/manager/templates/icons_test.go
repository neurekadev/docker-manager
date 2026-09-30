package templates

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.White)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDetectIconAcceptsSupportedImages(t *testing.T) {
	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"png":         {pngOf(t, 64, 64), IconPNG},
		"gif":         {[]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"), IconGIF},
		"webp":        {append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 8)...), IconWebP},
		"svg":         {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0h1v1z"/></svg>`), IconSVG},
		"svg xml+bom": {[]byte("\xef\xbb\xbf<?xml version=\"1.0\"?>\n<!-- logo -->\n<svg viewBox=\"0 0 1 1\"></svg>"), IconSVG},
	} {
		t.Run(name, func(t *testing.T) {
			icon, err := DetectIcon(tc.data)
			if err != nil || icon.MediaType != tc.want || icon.Size != int64(len(tc.data)) || len(icon.SHA256) != 64 {
				t.Fatalf("DetectIcon = %+v, %v; want %s", icon, err, tc.want)
			}
		})
	}
}

func TestDetectIconRefusesEverythingElse(t *testing.T) {
	for name, tc := range map[string]struct {
		data     []byte
		tooLarge bool
	}{
		"empty":       {nil, false},
		"too large":   {append([]byte("<svg>"), make([]byte, domain.MaxTemplateIcon)...), true},
		"huge pixels": {pngOf(t, 2000, 10), false},
		"html":        {[]byte("<html><svg></svg></html>"), false},
		"script":      {[]byte(`<svg><script>alert(1)</script></svg>`), false},
		"handler url": {[]byte(`<svg><a href="javascript:alert(1)"/></svg>`), false},
		"doctype":     {[]byte(`<!DOCTYPE svg [<!ENTITY x "y">]><svg/>`), false},
		"foreign":     {[]byte(`<svg><foreignObject/></svg>`), false},
		"not utf-8":   {[]byte("<svg>\xff</svg>"), false},
		"svgfoo":      {[]byte("<svgfoo/>"), false},
		"broken png":  {[]byte("\x89PNG\r\n\x1a\nnope"), false},
	} {
		t.Run(name, func(t *testing.T) {
			var ie *domain.TemplateIconError
			_, err := DetectIcon(tc.data)
			if !errors.As(err, &ie) || ie.TooLarge != tc.tooLarge {
				t.Fatalf("DetectIcon error = %v (tooLarge %v)", err, tc.tooLarge)
			}
			if strings.Contains(err.Error(), string(tc.data)) && len(tc.data) > 0 {
				t.Fatal("the error echoes the icon bytes")
			}
		})
	}
}

func TestLinkInside(t *testing.T) {
	for _, tc := range []struct {
		path, target string
		want         bool
	}{
		{"a", "b", true},
		{"dir/a", "../b", true},
		{"dir/a", "../../b", false},
		{"a", "/etc/passwd", false},
		{"a", "C:/x", false},
		{"a", `..\b`, false},
		{"a", "", false},
	} {
		if got := linkInside(tc.path, tc.target); got != tc.want {
			t.Errorf("linkInside(%q, %q) = %v, want %v", tc.path, tc.target, got, tc.want)
		}
	}
}

func TestIsDefinitionFile(t *testing.T) {
	for p, want := range map[string]bool{"compose.yaml": true, "docker-compose.yml": true, "compose.prod.yaml": true, ".env": true,
		"config/compose.yaml": false, "app.env": false, "README.md": false} {
		if got := IsDefinitionFile(p); got != want {
			t.Errorf("IsDefinitionFile(%q) = %v, want %v", p, got, want)
		}
	}
}
