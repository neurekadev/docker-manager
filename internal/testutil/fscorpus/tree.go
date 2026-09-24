package fscorpus

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Link kinds of TreeCase.
const (
	KindFile     = "file"
	KindSymlink  = "symlink"
	KindHardlink = "hardlink"
)

// TreeCase is one entry of an EscapeTree, relative to Root.
type TreeCase struct {
	Path string
	Kind string
	// Escapes reports whether following the entry leaves Root (or, for a
	// hardlink, whether it shares an inode with a file outside Root).
	Escapes bool
	Note    string
}

// EscapeTree is a directory tree for containment tests:
//
//	<base>/outside/secret.txt          content: Secret
//	<base>/root/                       the directory a consumer confines to
//	    inside.txt, dir/nested.txt     legitimate files
//	    link-inside -> inside.txt      legitimate relative symlink
//	    link-dir-inside -> dir         legitimate directory symlink
//	    escape-abs -> <base>/outside/secret.txt
//	    escape-rel -> ../outside/secret.txt
//	    escape-dir -> ../outside
//	    escape-chain -> chain-mid, chain-mid -> ../outside
//	    dir/escape-deep -> ../../outside/secret.txt
//	    loop-a -> loop-b, loop-b -> loop-a
//	    dangling -> does-not-exist
//	    hardlink-secret                hard link to outside/secret.txt
type EscapeTree struct {
	Base, Root, Outside string
	// Secret is the content of outside/secret.txt; a consumer that ever
	// returns it has escaped.
	Secret string
	Cases  []TreeCase
}

// ErrSymlinksUnavailable is returned when the platform refuses to create
// symlinks (Windows without developer mode or privilege).
var ErrSymlinksUnavailable = errors.New("fscorpus: symlinks unavailable on this platform")

// NewEscapeTree creates the tree under base (which must exist and be empty
// or new).
func NewEscapeTree(base string) (*EscapeTree, error) {
	secretBytes := make([]byte, 16)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, err
	}
	et := &EscapeTree{
		Base:    base,
		Root:    filepath.Join(base, "root"),
		Outside: filepath.Join(base, "outside"),
		Secret:  "OUTSIDE-SECRET-" + hex.EncodeToString(secretBytes),
	}
	for _, d := range []string{et.Outside, filepath.Join(et.Root, "dir")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	secret := filepath.Join(et.Outside, "secret.txt")
	files := map[string]string{
		secret:                               et.Secret,
		filepath.Join(et.Root, "inside.txt"): "inside\n",
		filepath.Join(et.Root, "dir", "nested.txt"): "nested\n",
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			return nil, err
		}
	}
	et.Cases = append(et.Cases,
		TreeCase{Path: "inside.txt", Kind: KindFile},
		TreeCase{Path: "dir/nested.txt", Kind: KindFile},
	)

	symlinks := []struct {
		path, target string
		escapes      bool
		note         string
	}{
		{"link-inside", "inside.txt", false, "relative link inside the root"},
		{"link-dir-inside", "dir", false, "directory link inside the root"},
		{"escape-abs", secret, true, "absolute target outside"},
		{"escape-rel", "../outside/secret.txt", true, "relative target outside"},
		{"escape-dir", "../outside", true, "directory link outside; escape-dir/secret.txt reads the secret"},
		{"chain-mid", "../outside", true, "second hop of escape-chain"},
		{"escape-chain", "chain-mid", true, "link to a link that escapes"},
		{"dir/escape-deep", "../../outside/secret.txt", true, "escape from a subdirectory"},
		{"loop-a", "loop-b", false, "symlink loop (must fail, not hang)"},
		{"loop-b", "loop-a", false, "symlink loop (must fail, not hang)"},
		{"dangling", "does-not-exist", false, "dangling link (must fail cleanly)"},
	}
	for _, s := range symlinks {
		if err := os.Symlink(filepath.FromSlash(s.target), filepath.Join(et.Root, filepath.FromSlash(s.path))); err != nil {
			if runtime.GOOS == "windows" {
				return nil, fmt.Errorf("%w: %v", ErrSymlinksUnavailable, err)
			}
			return nil, err
		}
		et.Cases = append(et.Cases, TreeCase{Path: s.path, Kind: KindSymlink, Escapes: s.escapes, Note: s.note})
	}
	et.Cases = append(et.Cases, TreeCase{Path: "escape-dir/secret.txt", Kind: KindSymlink, Escapes: true, Note: "file reached through an escaping directory link"})
	if err := os.Link(secret, filepath.Join(et.Root, "hardlink-secret")); err != nil {
		return nil, fmt.Errorf("hardlink: %w", err)
	}
	et.Cases = append(et.Cases, TreeCase{Path: "hardlink-secret", Kind: KindHardlink, Escapes: true,
		Note: "a regular file inside the root sharing its inode with outside/secret.txt; path containment cannot see it, check the link count"})
	return et, nil
}

// MustEscapeTree builds an EscapeTree in a temporary directory, skipping
// the test only on Windows hosts that cannot create symlinks (CI runs it
// on Linux).
func MustEscapeTree(t testing.TB) *EscapeTree {
	t.Helper()
	et, err := NewEscapeTree(t.TempDir())
	if errors.Is(err, ErrSymlinksUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return et
}

// Escaping returns the cases that must not be followed.
func (et *EscapeTree) Escaping() []TreeCase {
	var out []TreeCase
	for _, c := range et.Cases {
		if c.Escapes {
			out = append(out, c)
		}
	}
	return out
}
