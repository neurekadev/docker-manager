package templates

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Drafts in manager-state backups (template registry): published versions
// live in the database and are backed up with it; drafts are files in
// <data dir>/templates/<id>/draft and travel as one tar.gz next to the
// database snapshot. Only directories, regular files and symlinks inside a
// draft are written; restoring validates every name the same way.

// DraftsArchiveName is the drafts' file in a manager-state snapshot.
const DraftsArchiveName = "templates.tar.gz"

// DraftsDir returns the directory holding every template's directory.
func DraftsDir(dataDir string) string { return filepath.Join(dataDir, "templates") }

// WriteDrafts writes every draft below templatesDir as a tar.gz to w
// (entries <id>/draft/...). A missing directory writes an empty archive.
func WriteDrafts(w io.Writer, templatesDir string) error {
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	ids, err := os.ReadDir(templatesDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range ids {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		draft := filepath.Join(templatesDir, e.Name(), "draft")
		root, err := os.OpenRoot(draft)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fi, err := root.Lstat(p)
			if err != nil {
				return err
			}
			name := path.Join(e.Name(), "draft", p)
			h := &tar.Header{Name: name, Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime(), Format: tar.FormatPAX}
			switch {
			case fi.IsDir():
				h.Typeflag, h.Name = tar.TypeDir, name+"/"
				return tw.WriteHeader(h)
			case fi.Mode()&fs.ModeSymlink != 0:
				target, err := root.Readlink(p)
				if err != nil || !linkInside(p, target) {
					return nil
				}
				h.Typeflag, h.Linkname = tar.TypeSymlink, target
				return tw.WriteHeader(h)
			case fi.Mode().IsRegular():
				f, err := root.Open(p)
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				h.Typeflag, h.Size = tar.TypeReg, fi.Size()
				if err := tw.WriteHeader(h); err != nil {
					return err
				}
				_, err = io.CopyN(tw, f, fi.Size())
				return err
			}
			return nil
		})
		_ = root.Close()
		if err != nil {
			return fmt.Errorf("templates: back up the draft of %s: %w", e.Name(), err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// RestoreDrafts replaces templatesDir with the drafts of r; the current
// directory moves to keepDir (the restore's pre-restore copy).
func RestoreDrafts(r io.Reader, templatesDir, keepDir string) error {
	next := templatesDir + ".restoring"
	_ = os.RemoveAll(next)
	if err := os.MkdirAll(next, 0o700); err != nil {
		return err
	}
	if err := extractDrafts(r, next); err != nil {
		_ = os.RemoveAll(next)
		return err
	}
	if _, err := os.Stat(templatesDir); err == nil {
		if _, err := os.Stat(keepDir); err == nil {
			// A repeated apply: the original drafts were kept already.
			if err := os.RemoveAll(templatesDir); err != nil {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(keepDir), 0o700); err != nil {
				return err
			}
			if err := os.Rename(templatesDir, keepDir); err != nil {
				return fmt.Errorf("keep the replaced template drafts: %w", err)
			}
		}
	}
	return os.Rename(next, templatesDir)
}

func extractDrafts(r io.Reader, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("templates: the drafts archive is damaged: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("templates: the drafts archive is damaged: %w", err)
		}
		name, ok := cleanMember(h.Name)
		parts := strings.SplitN(name, "/", 3)
		if !ok || len(parts) < 2 || !validID(parts[0]) || parts[1] != "draft" {
			continue
		}
		if parent := path.Dir(name); parent != "." {
			if err := root.MkdirAll(parent, 0o700); err != nil {
				return err
			}
		}
		mode := h.FileInfo().Mode().Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o700); err != nil {
				return err
			}
			_ = root.Chmod(name, mode|0o700)
		case tar.TypeReg:
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode|0o600)
			if err != nil {
				return err
			}
			_, err = io.CopyN(f, tr, h.Size)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			_ = root.Chtimes(name, time.Now(), h.ModTime)
		case tar.TypeSymlink:
			if len(parts) == 3 && linkInside(parts[2], h.Linkname) {
				if err := root.Symlink(h.Linkname, name); err != nil {
					return err
				}
			}
		}
	}
}

// validID reports whether a directory name can be a template ID.
func validID(s string) bool {
	return s != "" && len(s) <= 64 && protocol.ValidFileName(s) && !strings.HasPrefix(s, ".") && !strings.Contains(s, ".")
}
