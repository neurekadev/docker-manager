package testharness

import (
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

// Pinned restic for tests: the same release and SHA-256 sums as
// deploy/docker/{manager,agent}.Dockerfile (TestResticPinMatchesDockerfiles
// keeps them in sync). Bump all of them together.
const (
	ResticVersion      = "0.19.1"
	ResticSHA256AMD64  = "f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c"
	ResticSHA256ARM64  = "a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465"
	resticReleasesBase = "https://github.com/restic/restic/releases/download"
)

// EnvTestCache overrides the download cache directory.
const EnvTestCache = "DOCKYARD_TEST_CACHE"

// ResticSHA256 returns the pinned archive checksum for a GOARCH.
func ResticSHA256(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return ResticSHA256AMD64, nil
	case "arm64":
		return ResticSHA256ARM64, nil
	}
	return "", fmt.Errorf("restic: no pinned checksum for linux/%s", goarch)
}

// FetchRestic downloads the pinned restic for the current linux/GOARCH into
// the test cache (verifying the SHA-256 of the release archive) and returns
// the path of the executable. Cached binaries are reused; the archive is
// re-verified before every extraction.
func FetchRestic(ctx context.Context) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("restic fixture: linux only (running on %s)", runtime.GOOS)
	}
	sum, err := ResticSHA256(runtime.GOARCH)
	if err != nil {
		return "", err
	}
	dir, err := testCacheDir()
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/v%s/restic_%s_linux_%s.bz2", resticReleasesBase, ResticVersion, ResticVersion, runtime.GOARCH)
	return fetchVerifiedBzip2(ctx, http.DefaultClient, url, sum, filepath.Join(dir, "restic-"+ResticVersion+"-"+runtime.GOARCH))
}

func testCacheDir() (string, error) {
	dir := os.Getenv(EnvTestCache)
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "dockyard-test")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // G703: the cache dir is chosen by the developer
		return "", err
	}
	return dir, nil
}

// fetchVerifiedBzip2 downloads url to dst+".bz2" unless a copy with the
// expected checksum is cached, verifies sha256hex, and extracts it to dst
// (mode 0755).
func fetchVerifiedBzip2(ctx context.Context, client *http.Client, url, sha256hex, dst string) (string, error) {
	archive := dst + ".bz2"
	if err := verifyFile(archive, sha256hex); err != nil {
		if err := download(ctx, client, url, archive); err != nil {
			return "", err
		}
		if err := verifyFile(archive, sha256hex); err != nil {
			_ = os.Remove(archive)
			return "", err
		}
	}
	if fi, err := os.Stat(dst); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
		return dst, nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755) //nolint:gosec // G302: the restic executable must be executable
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, bzip2.NewReader(f)); err != nil { //nolint:gosec // G110: trusted, checksum-verified archive
		_ = out.Close()
		return "", fmt.Errorf("extract %s: %w", archive, err)
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dst, os.Rename(tmp, dst)
}

func download(ctx context.Context, client *http.Client, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return fmt.Errorf("download %s: %w", url, err)
	}
	return out.Close()
}

// ErrChecksumMismatch reports a downloaded file with an unexpected SHA-256.
var ErrChecksumMismatch = errors.New("checksum mismatch")

func verifyFile(path, sha256hex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sha256hex {
		return fmt.Errorf("%s: %w (got %s, want %s)", path, ErrChecksumMismatch, got, sha256hex)
	}
	return nil
}
