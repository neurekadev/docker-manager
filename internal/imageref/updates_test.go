package imageref

import (
	"strings"
	"testing"
)

func TestExplicitTag(t *testing.T) {
	pin := "@sha256:" + strings.Repeat("a", 64)
	for s, want := range map[string]bool{
		"nginx": false, "nginx:1.25": true, "nginx" + pin: false, "nginx:1" + pin: true,
		"registry.lan:5000/app": false, "registry.lan:5000/app:2": true, "::bad": false,
	} {
		if got := ExplicitTag(s); got != want {
			t.Errorf("ExplicitTag(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestDigestFor(t *testing.T) {
	d := "sha256:" + strings.Repeat("b", 64)
	for _, c := range []struct {
		ref     string
		digests []string
		want    string
	}{
		{"nginx:1", []string{"nginx@" + d}, d},
		{"docker.io/library/nginx:1", []string{"nginx@" + d}, d},
		{"index.docker.io/nginx:1", []string{"docker.io/library/nginx@" + d}, d},
		{"ghcr.io/acme/app:1", []string{"nginx@" + d}, ""},
		{"ghcr.io/acme/app:1", []string{"ghcr.io/acme/app@" + d}, d},
		{"ghcr.io/acme/app@" + d, nil, d},
		{"registry.lan:5000/app:1", []string{"bad", "registry.lan:5000/app@" + d}, d},
		{"not a ref", []string{"nginx@" + d}, ""},
	} {
		if got := DigestFor(c.ref, c.digests); got != c.want {
			t.Errorf("DigestFor(%q, %v) = %q, want %q", c.ref, c.digests, got, c.want)
		}
	}
}

func TestDigestsFor(t *testing.T) {
	a, b := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	got := DigestsFor("nginx:1", []string{"nginx@" + a, "other@" + b, "docker.io/library/nginx@" + b})
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("DigestsFor = %v", got)
	}
	if DigestsFor("::", []string{"nginx@" + a}) != nil {
		t.Fatal("invalid reference")
	}
}

func TestSameTag(t *testing.T) {
	d := "@sha256:" + strings.Repeat("c", 64)
	if !SameTag("nginx:1", "docker.io/library/nginx:1") || !SameTag("nginx", "nginx:latest") {
		t.Error("normalized spellings must match")
	}
	if SameTag("nginx:1", "nginx:2") || SameTag("nginx:1", "nginx:1"+d) || SameTag("ghcr.io/a/b:1", "ghcr.io/a/c:1") {
		t.Error("different tags, repositories or pinned references must not match")
	}
}

func TestVersionTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"1": true, "1.25.3": true, "v2.0.1-alpine": true, "2024.06.1": true, "1.2_rc": true, "V3": true,
		"latest": false, "main": false, "stable": false, "edge": false, "v": false, "": false, "1a": false, "alpine-3": false,
	} {
		if got := VersionTag(tag); got != want {
			t.Errorf("VersionTag(%q) = %v, want %v", tag, got, want)
		}
	}
}
