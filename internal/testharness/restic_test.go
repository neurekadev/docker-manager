package testharness

import (
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"
)

// The test restic must be the one the images ship.
func TestResticPinMatchesDockerfiles(t *testing.T) {
	root, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"RESTIC_VERSION":      ResticVersion,
		"RESTIC_SHA256_AMD64": ResticSHA256AMD64,
		"RESTIC_SHA256_ARM64": ResticSHA256ARM64,
	}
	for _, df := range []string{"manager.Dockerfile", "agent.Dockerfile"} {
		data, err := os.ReadFile(filepath.Join(root, "deploy", "docker", df))
		if err != nil {
			t.Fatal(err)
		}
		for arg, v := range want {
			m := regexp.MustCompile(`(?m)^ARG ` + arg + `=(\S+)$`).FindStringSubmatch(string(data))
			if m == nil {
				t.Errorf("%s: ARG %s not found", df, arg)
				continue
			}
			if m[1] != v {
				t.Errorf("%s: %s=%s, testharness pins %s", df, arg, m[1], v)
			}
		}
	}
	if _, err := ResticSHA256("riscv64"); err == nil {
		t.Error("ResticSHA256(riscv64) succeeded")
	}
}

// bzip2 of "#!/bin/sh\necho restic-fixture\n" and its SHA-256.
const (
	sampleBz2Hex    = "425a68393141592653597b0e58660000025180001068029b619e402000228069b51ea620a64c4c8323340e6e1082bd4a7195d9d5a2091e1af8bb9229c28483d872c330"
	sampleBz2SHA256 = "4e969c24fddae545c0cb6e883388a43177c82b830275530363f2dca1d2469dbd"
)

func TestFetchVerifiedBzip2(t *testing.T) {
	archive, err := hex.DecodeString(sampleBz2Hex)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/tampered.bz2" {
			_, _ = w.Write(append([]byte{}, archive[:len(archive)-1]...))
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()

	dst := filepath.Join(dir, "restic")
	got, err := fetchVerifiedBzip2(t.Context(), srv.Client(), srv.URL+"/restic.bz2", sampleBz2SHA256, dst)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil || string(data) != "#!/bin/sh\necho restic-fixture\n" {
		t.Fatalf("extracted %q, %v", data, err)
	}

	// Cached: no second download.
	if _, err := fetchVerifiedBzip2(t.Context(), srv.Client(), srv.URL+"/restic.bz2", sampleBz2SHA256, dst); err != nil {
		t.Fatal(err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (cache hit)", n)
	}

	// Tampered download is rejected and not kept.
	bad := filepath.Join(dir, "bad")
	_, err = fetchVerifiedBzip2(t.Context(), srv.Client(), srv.URL+"/tampered.bz2", sampleBz2SHA256, bad)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("tampered: err = %v", err)
	}
	if _, err := os.Stat(bad + ".bz2"); !os.IsNotExist(err) {
		t.Errorf("tampered archive kept: %v", err)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Errorf("tampered binary extracted: %v", err)
	}
}
