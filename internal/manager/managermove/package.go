package managermove

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
)

// The handoff package is a tar of the parts below, in this order, then
// manifest.json with the length and SHA-256 of every part, streamed
// encrypted (crypt.go). The receiver stores each part under a fixed name
// and trusts none of them before the manifest matched.

// Package format.
const (
	PackageFormat  = "docker-manager-move"
	PackageVersion = 1

	PartState     = "state.json"
	PartDatabase  = "docker-manager.db"
	PartSealedKey = "secret-key.sealed"
	PartTemplates = "templates.tar.gz"
	PartManifest  = "manifest.json"
)

// packageParts are the parts in stream order (templates only when
// included).
var packageParts = []string{PartState, PartDatabase, PartSealedKey, PartTemplates}

// requiredParts must be in every package.
var requiredParts = []string{PartState, PartDatabase, PartSealedKey}

// partLimits bound the small parts a receiver accepts.
var partLimits = map[string]int64{PartState: 1 << 20, PartSealedKey: 64 << 10, PartManifest: 1 << 20}

// StateInfo is state.json of a handoff.
type StateInfo struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	MoveID     string `json:"moveId"`
	InstanceID string `json:"instanceId"`
	// Generation is the copy's generation (the old manager's plus one).
	Generation  int64             `json:"generation"`
	CreatedAt   time.Time         `json:"createdAt"`
	App         backup.AppInfo    `json:"app"`
	Schema      backup.SchemaInfo `json:"schema"`
	SecretKeyID string            `json:"secretKeyId"`
	// TemplatesIncluded: the template drafts are in templates.tar.gz.
	TemplatesIncluded bool `json:"templatesIncluded"`
}

// Manifest is manifest.json: every part with its length and SHA-256.
type Manifest struct {
	Format  string         `json:"format"`
	Version int            `json:"version"`
	Parts   []ManifestPart `json:"parts"`
}

// ManifestPart is one part of the manifest.
type ManifestPart struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Part returns the manifest entry of name.
func (m Manifest) Part(name string) (ManifestPart, bool) {
	for _, p := range m.Parts {
		if p.Name == name {
			return p, true
		}
	}
	return ManifestPart{}, false
}

// Size is the total size of the parts.
func (m Manifest) Size() int64 {
	var n int64
	for _, p := range m.Parts {
		n += p.Size
	}
	return n
}

// errPackage marks a handoff stream that is not a complete, intact
// package (retrying the transfer may help).
var errPackage = errors.New("the handoff package is incomplete or damaged")

func packageError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errPackage, fmt.Sprintf(format, args...))
}

// fileSum returns the size and SHA-256 of a file.
func fileSum(path string) (ManifestPart, error) {
	f, err := os.Open(path) //nolint:gosec // a part of a package below the data directory
	if err != nil {
		return ManifestPart{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return ManifestPart{}, err
	}
	return ManifestPart{Name: filepath.Base(path), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// buildManifest sums the parts present in dir.
func buildManifest(dir string) (Manifest, error) {
	m := Manifest{Format: PackageFormat, Version: PackageVersion}
	for _, name := range packageParts {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) && !slices.Contains(requiredParts, name) {
			continue
		}
		part, err := fileSum(p)
		if err != nil {
			return Manifest{}, err
		}
		m.Parts = append(m.Parts, part)
	}
	return m, nil
}

// writePackage streams the parts of dir listed in m, then the manifest.
func writePackage(w io.Writer, dir string, m Manifest, modTime time.Time) error {
	tw := tar.NewWriter(w)
	for _, p := range m.Parts {
		if err := writePart(tw, filepath.Join(dir, p.Name), p, modTime); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: PartManifest, Mode: 0o600, Size: int64(len(raw)), ModTime: modTime, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(raw); err != nil {
		return err
	}
	return tw.Close()
}

func writePart(tw *tar.Writer, path string, p ManifestPart, modTime time.Time) error {
	f, err := os.Open(path) //nolint:gosec // a part of a package below the data directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := tw.WriteHeader(&tar.Header{Name: p.Name, Mode: 0o600, Size: p.Size, ModTime: modTime, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	n, err := io.Copy(tw, io.LimitReader(f, p.Size))
	if err != nil {
		return err
	}
	if n != p.Size {
		return fmt.Errorf("the part %s changed while it was sent", p.Name)
	}
	return nil
}

// readPackage stores a handoff stream in dir (one file per part, under its
// fixed name) and checks every part against the manifest: the known parts
// only, each once, the manifest last, the required parts present, and
// every length and SHA-256 equal. progress receives the bytes read so far.
func readPackage(r io.Reader, dir string, progress func(int64)) (Manifest, error) {
	tr := tar.NewReader(r)
	got := map[string]ManifestPart{}
	var manifest *Manifest
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, packageError("the stream broke off (%v)", err)
		}
		if manifest != nil {
			return Manifest{}, packageError("%q follows the manifest", hdr.Name)
		}
		if hdr.Typeflag != tar.TypeReg {
			return Manifest{}, packageError("%q is not a regular file", hdr.Name)
		}
		name := hdr.Name
		if name == PartManifest {
			raw, err := io.ReadAll(io.LimitReader(tr, partLimits[PartManifest]+1))
			if err != nil {
				return Manifest{}, packageError("the manifest broke off (%v)", err)
			}
			if int64(len(raw)) > partLimits[PartManifest] {
				return Manifest{}, packageError("the manifest is too large")
			}
			var m Manifest
			if err := json.Unmarshal(raw, &m); err != nil || m.Format != PackageFormat || m.Version != PackageVersion {
				return Manifest{}, packageError("the manifest is unreadable")
			}
			manifest = &m
			continue
		}
		i := slices.Index(packageParts, name)
		if i < 0 {
			return Manifest{}, packageError("unexpected part %q", name)
		}
		if _, dup := got[name]; dup {
			return Manifest{}, packageError("the part %q is repeated", name)
		}
		if limit, ok := partLimits[name]; ok && hdr.Size > limit {
			return Manifest{}, packageError("the part %q is too large", name)
		}
		// The path is one of the fixed part names (checked above), never the
		// archive's entry name.
		path := filepath.Join(dir, packageParts[i]) //nolint:gosec // G305: packageParts is a constant allowlist
		part, err := storePart(tr, path, hdr.Size, func(n int64) {
			if progress != nil {
				progress(total + n)
			}
		})
		if err != nil {
			return Manifest{}, err
		}
		total += part.Size
		got[name] = part
	}
	if manifest == nil {
		return Manifest{}, packageError("the manifest is missing (the transfer ended early)")
	}
	for _, name := range requiredParts {
		if _, ok := manifest.Part(name); !ok {
			return Manifest{}, packageError("the manifest lacks %s", name)
		}
	}
	if len(manifest.Parts) != len(got) {
		return Manifest{}, packageError("the manifest lists %d parts, the stream carried %d", len(manifest.Parts), len(got))
	}
	for _, want := range manifest.Parts {
		have, ok := got[want.Name]
		if !ok {
			return Manifest{}, packageError("the part %s is missing", want.Name)
		}
		if have.Size != want.Size || have.SHA256 != want.SHA256 {
			return Manifest{}, packageError("the part %s does not match the manifest (length or SHA-256)", want.Name)
		}
	}
	return *manifest, nil
}

// storePart writes one part to path, returning its size and SHA-256.
func storePart(r io.Reader, path string, size int64, progress func(int64)) (ManifestPart, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // a fixed part name below the data directory
	if err != nil {
		return ManifestPart{}, err
	}
	h := sha256.New()
	var written int64
	n, cerr := io.Copy(io.MultiWriter(f, h, progressWriter{fn: progress, n: &written}), r)
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return ManifestPart{}, err
	}
	if cerr != nil {
		return ManifestPart{}, packageError("the part %s broke off (%v)", filepath.Base(path), cerr)
	}
	if n != size {
		return ManifestPart{}, packageError("the part %s is truncated", filepath.Base(path))
	}
	return ManifestPart{Name: filepath.Base(path), Size: n, SHA256: hexSum(h)}, nil
}

func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

// progressWriter reports the running byte count of a part.
type progressWriter struct {
	fn func(int64)
	n  *int64
}

func (p progressWriter) Write(b []byte) (int, error) {
	*p.n += int64(len(b))
	if p.fn != nil {
		p.fn(*p.n)
	}
	return len(b), nil
}
