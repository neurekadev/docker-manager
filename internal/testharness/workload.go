package testharness

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// WorkloadImage is the image reference of the test workload (built from
// test/fixtures/workload) that Engine and Compose integration tests run.
const WorkloadImage = "dockyard-test/workload:1"

// WorkloadBinary is the path of the workload inside the image.
const WorkloadBinary = "/workload"

var (
	workloadMu    sync.Mutex
	workloadCache = map[string][]byte{}
)

// BuildWorkload compiles test/fixtures/workload statically for linux/goarch
// and returns the executable (cached per process).
func BuildWorkload(ctx context.Context, goarch string) ([]byte, error) {
	workloadMu.Lock()
	defer workloadMu.Unlock()
	if b, ok := workloadCache[goarch]; ok {
		return b, nil
	}
	root, err := RepoRoot()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "dockyard-workload-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "workload")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./test/fixtures/workload") //nolint:gosec // G204: fixed arguments
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build workload: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out) //nolint:gosec // temp file written above
	if err != nil {
		return nil, err
	}
	workloadCache[goarch] = b
	return b, nil
}

// WorkloadArchive returns a `docker save` style archive (loadable by every
// Engine version with ImageLoad) of a single-layer image holding the
// workload binary and an empty /tmp, tagged as ref.
func WorkloadArchive(binary []byte, goarch, ref string) ([]byte, error) {
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	for _, h := range []*tar.Header{
		{Name: "tmp/", Mode: 0o1777, Typeflag: tar.TypeDir, ModTime: epoch},
		{Name: "data/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: epoch},
	} {
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: "workload", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg, ModTime: epoch}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(binary); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	layerSum := sha256.Sum256(layer.Bytes())
	layerHex := hex.EncodeToString(layerSum[:])
	cfg, err := json.Marshal(map[string]any{
		"architecture": goarch,
		"os":           "linux",
		"created":      epoch.Format(time.RFC3339),
		"config": map[string]any{
			"Entrypoint": []string{WorkloadBinary},
			"Cmd":        []string{"serve"},
			"StopSignal": "SIGTERM",
			"Labels":     map[string]string{"dev.neureka.dockyard.test": "workload"},
		},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + layerHex}},
	})
	if err != nil {
		return nil, err
	}
	cfgSum := sha256.Sum256(cfg)
	cfgName := hex.EncodeToString(cfgSum[:]) + ".json"
	manifest, err := json.Marshal([]map[string]any{{
		"Config":   cfgName,
		"RepoTags": []string{ref},
		"Layers":   []string{layerHex + "/layer.tar"},
	}})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	aw := tar.NewWriter(&out)
	for _, f := range []struct {
		name string
		data []byte
	}{{cfgName, cfg}, {layerHex + "/layer.tar", layer.Bytes()}, {"manifest.json", manifest}} {
		if err := aw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data)), Typeflag: tar.TypeReg, ModTime: epoch}); err != nil {
			return nil, err
		}
		if _, err := aw.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := aw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
