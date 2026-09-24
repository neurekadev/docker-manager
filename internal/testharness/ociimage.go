package testharness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"time"
)

// OCIImage is a minimal single-layer image built in memory, for pushing to
// the registry fixture without a Docker CLI.
type OCIImage struct {
	Layer        []byte // gzip-compressed tar
	LayerDigest  string
	DiffID       string
	Config       []byte
	ConfigDigest string
	Manifest     []byte
	Digest       string // manifest digest
}

// OCIManifestType is the media type of OCIImage.Manifest.
const OCIManifestType = "application/vnd.oci.image.manifest.v1+json"

// NewTestImage builds a linux/<goarch> image whose only file is
// /dockyard-fixture.txt with the given content. Timestamps are fixed, so
// equal inputs give equal digests.
func NewTestImage(goarch, content string) (*OCIImage, error) {
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := tw.WriteHeader(&tar.Header{Name: "dockyard-fixture.txt", Mode: 0o644, Size: int64(len(content)), ModTime: epoch, Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.ModTime = epoch
	if _, err := zw.Write(tarBuf.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	img := &OCIImage{Layer: gz.Bytes()}
	img.LayerDigest = "sha256:" + PayloadHash(img.Layer)
	img.DiffID = "sha256:" + PayloadHash(tarBuf.Bytes())

	cfg := map[string]any{
		"architecture": goarch,
		"os":           "linux",
		"created":      epoch.Format(time.RFC3339),
		"config":       map[string]any{"Cmd": []string{"/dockyard-fixture.txt"}},
		"rootfs":       map[string]any{"type": "layers", "diff_ids": []string{img.DiffID}},
	}
	var err error
	if img.Config, err = json.Marshal(cfg); err != nil {
		return nil, err
	}
	img.ConfigDigest = "sha256:" + PayloadHash(img.Config)
	man := map[string]any{
		"schemaVersion": 2,
		"mediaType":     OCIManifestType,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    img.ConfigDigest,
			"size":      len(img.Config),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest":    img.LayerDigest,
			"size":      len(img.Layer),
		}},
	}
	if img.Manifest, err = json.Marshal(man); err != nil {
		return nil, err
	}
	img.Digest = "sha256:" + PayloadHash(img.Manifest)
	return img, nil
}

// PushOCIImage pushes img as registryURL/<repo>:<tag> through the
// distribution HTTP API with basic auth (monolithic blob uploads).
func PushOCIImage(ctx context.Context, hc *http.Client, registryURL, user, password, repo, tag string, img *OCIImage) error {
	base, err := url.Parse(registryURL)
	if err != nil {
		return err
	}
	do := func(method, target, contentType string, body []byte, want int) (http.Header, error) {
		u, err := base.Parse(target)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(user, password)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode != want {
			return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, u.Path, resp.StatusCode, b)
		}
		return resp.Header, nil
	}
	for _, blob := range []struct {
		digest string
		data   []byte
	}{{img.LayerDigest, img.Layer}, {img.ConfigDigest, img.Config}} {
		h, err := do(http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "", nil, http.StatusAccepted)
		if err != nil {
			return err
		}
		loc, err := base.Parse(h.Get("Location"))
		if err != nil {
			return fmt.Errorf("upload location: %w", err)
		}
		q := loc.Query()
		q.Set("digest", blob.digest)
		loc.RawQuery = q.Encode()
		if _, err := do(http.MethodPut, loc.String(), "application/octet-stream", blob.data, http.StatusCreated); err != nil {
			return err
		}
	}
	_, err = do(http.MethodPut, "/v2/"+repo+"/manifests/"+tag, OCIManifestType, img.Manifest, http.StatusCreated)
	return err
}
