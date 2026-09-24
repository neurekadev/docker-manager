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
	if !strings.Contains(string(b), "DockYard") {
		t.Fatalf("index.html does not mention DockYard")
	}
	if built {
		if _, err := fs.Stat(ui, "_app"); err != nil {
			t.Fatalf("real build lacks _app: %v", err)
		}
	}
}
