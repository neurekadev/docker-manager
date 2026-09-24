package testharness

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func readTar(t *testing.T, data []byte) map[string]*tar.Header {
	t.Helper()
	out := map[string]*tar.Header{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = h
	}
}

func TestWorkloadArchiveIsLoadable(t *testing.T) {
	bin := []byte("\x7fELF fake workload")
	archive, err := WorkloadArchive(bin, "arm64", WorkloadImage)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = b
	}
	var manifest []struct {
		Config   string
		RepoTags []string
		Layers   []string
	}
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil || len(manifest) != 1 {
		t.Fatalf("manifest %s: %v", files["manifest.json"], err)
	}
	m := manifest[0]
	if len(m.RepoTags) != 1 || m.RepoTags[0] != WorkloadImage || len(m.Layers) != 1 {
		t.Fatalf("manifest %+v", m)
	}
	var cfg struct {
		Architecture string
		OS           string
		Config       struct{ Entrypoint []string }
		RootFS       struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if err := json.Unmarshal(files[m.Config], &cfg); err != nil {
		t.Fatal(err)
	}
	layer := files[m.Layers[0]]
	sum := sha256.Sum256(layer)
	if cfg.Architecture != "arm64" || cfg.OS != "linux" || len(cfg.RootFS.DiffIDs) != 1 || cfg.RootFS.DiffIDs[0] != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("config %+v", cfg)
	}
	if cfg.Config.Entrypoint[0] != WorkloadBinary {
		t.Errorf("entrypoint %v", cfg.Config.Entrypoint)
	}
	entries := readTar(t, layer)
	if h := entries["workload"]; h == nil || h.Mode != 0o755 || h.Size != int64(len(bin)) {
		t.Errorf("workload entry %+v", h)
	}
	if h := entries["tmp/"]; h == nil || h.Typeflag != tar.TypeDir || h.Mode&0o1777 != 0o1777 {
		t.Errorf("tmp entry %+v", h)
	}
}

func TestBuildWorkloadIsStatic(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the workload")
	}
	bin, err := BuildWorkload(t.Context(), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(bin, []byte("\x7fELF")) {
		t.Fatal("not an ELF executable")
	}
}
