package testharness

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Fixture images are pinned by digest and stay in sync with the deployment
// files that use the same images.
func TestFixtureImagesPinned(t *testing.T) {
	pinned := regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)
	for _, img := range []string{RegistryImage, MinIOImage, CaddyImage} {
		if !pinned.MatchString(img) {
			t.Errorf("%s is not pinned as name:tag@sha256:digest", img)
		}
	}
	root, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, f := range []string{"deploy/compose/compose.yaml", "e2e/compose.yaml"} {
		if !strings.Contains(read(f), "image: "+CaddyImage+"\n") {
			t.Errorf("%s does not use CaddyImage %s", f, CaddyImage)
		}
	}
	goImage := regexp.MustCompile(`(?m)^ARG GO_IMAGE=(\S+)$`)
	want := goImage.FindStringSubmatch(read("deploy/docker/manager.Dockerfile"))
	got := goImage.FindStringSubmatch(read("e2e/echo.Dockerfile"))
	if want == nil || got == nil || want[1] != got[1] {
		t.Errorf("e2e/echo.Dockerfile GO_IMAGE %v differs from manager.Dockerfile %v", got, want)
	}
}
