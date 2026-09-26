package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAssetsHaveIndex(t *testing.T) {
	ui, built := Assets()
	b, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		t.Fatalf("index.html missing (real build: %v): %v", built, err)
	}
	if !strings.Contains(string(b), "Docker Manager") {
		t.Fatalf("index.html does not mention Docker Manager")
	}
	if built {
		// The real build is the installable PWA (#11): service worker, web app
		// manifest and its icons at the root, next to the app shell.
		for _, p := range []string{"_app", "service-worker.js", "manifest.webmanifest", "icons/pwa-192x192.png", "icons/pwa-512x512.png"} {
			if _, err := fs.Stat(ui, p); err != nil {
				t.Fatalf("real build lacks %s: %v", p, err)
			}
		}
		if !strings.Contains(string(b), `<link rel="manifest" href="/manifest.webmanifest"`) {
			t.Fatal("index.html does not link the web app manifest")
		}
	}
}
