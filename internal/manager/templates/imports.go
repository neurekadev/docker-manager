package templates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Drafts filled from archives (template registry): duplicating a template
// version (this instance's or a registry's), restoring a draft to a
// version, and saving a stack's files as a template. The archive is
// extracted into a new directory next to the draft and swapped in only
// when it is complete, so a failure leaves the draft as it was.
//
// Extraction trusts nothing: only directories, regular files and symlinks
// that stay inside are written (hard links, devices and anything else are
// skipped), names are cleaned and must stay below the draft, permission
// bits only, and the template size and entry limits are counted while
// writing. Docker Manager's own skipped-entries note of a stack download
// is left out.

// extract writes the tar.gz from r into dir (an empty directory).
func (s *Service) extract(ctx context.Context, dir string, r io.Reader) (skipped int, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	zr, err := gzip.NewReader(r)
	if err != nil {
		return 0, &domain.TemplateDefinitionError{Message: "the files are not a tar.gz archive"}
	}
	tr := tar.NewReader(zr)
	var written int64
	entries := 0
	tooLarge := func() error {
		return &domain.TemplateTooLargeError{Message: fmt.Sprintf("a template holds at most %d MiB and %d files and directories "+
			"(DOCKER_MANAGER_TEMPLATE_MAX_SIZE_MB); leave out large folders such as data directories", s.opts.MaxSize>>20, s.opts.MaxEntries)}
	}
	for {
		if err := ctx.Err(); err != nil {
			return skipped, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return skipped, nil
		}
		if err != nil {
			return skipped, &domain.TemplateDefinitionError{Message: "the archive is damaged"}
		}
		name, ok := cleanMember(h.Name)
		if !ok {
			skipped++
			continue
		}
		if name == "." || name == protocol.SkippedListName {
			continue
		}
		if entries++; entries > s.opts.MaxEntries {
			return skipped, tooLarge()
		}
		if parent := path.Dir(name); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return skipped, fmt.Errorf("templates: create %s: %w", parent, err)
			}
		}
		perm := h.FileInfo().Mode().Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return skipped, fmt.Errorf("templates: create %s: %w", name, err)
			}
			_ = root.Chmod(name, perm|0o700)
		case tar.TypeReg:
			if written+h.Size > s.opts.MaxSize {
				return skipped, tooLarge()
			}
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm|0o600)
			if err != nil {
				return skipped, fmt.Errorf("templates: write %s: %w", name, err)
			}
			n, err := io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return skipped, fmt.Errorf("templates: write %s: %w", name, err)
			}
			written += n
		case tar.TypeSymlink:
			if !linkInside(name, h.Linkname) {
				skipped++
				continue
			}
			if err := root.Symlink(h.Linkname, name); err != nil {
				return skipped, fmt.Errorf("templates: link %s: %w", name, err)
			}
		default:
			skipped++
		}
	}
}

// cleanMember returns an archive member's path below the root.
func cleanMember(name string) (string, bool) {
	name = strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	if name == "" || name == "." {
		return ".", true
	}
	if strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) {
		return "", false
	}
	clean := path.Clean(name)
	if clean != name || clean == ".." || strings.HasPrefix(clean, "../") || !protocol.ValidRelativePath(clean) {
		return "", false
	}
	return clean, true
}

// replaceDraft fills a new draft with fill and swaps it in (the old draft
// is removed afterwards).
func (s *Service) replaceDraft(ctx context.Context, id string, fill func(dir string) error) error {
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	base := filepath.Join(s.root(), id)
	next, old := filepath.Join(base, "draft.next"), filepath.Join(base, "draft.old")
	_ = os.RemoveAll(next)
	_ = os.RemoveAll(old)
	if err := os.MkdirAll(next, 0o700); err != nil {
		return fmt.Errorf("templates: prepare the draft: %w", err)
	}
	if err := fill(next); err != nil {
		_ = os.RemoveAll(next)
		return err
	}
	draft := s.draftDir(id)
	if err := os.Rename(draft, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(next)
		return fmt.Errorf("templates: replace the draft: %w", err)
	}
	if err := os.Rename(next, draft); err != nil {
		_ = os.Rename(old, draft)
		return fmt.Errorf("templates: replace the draft: %w", err)
	}
	_ = os.RemoveAll(old)
	s.FilesChanged(id)
	return nil
}

// ImportDraft replaces a template's draft with the tar.gz from r.
func (s *Service) ImportDraft(ctx context.Context, id string, r io.Reader) error {
	if _, err := store.GetTemplate(ctx, s.db, id); err != nil {
		return err
	}
	var skipped int
	err := s.replaceDraft(ctx, id, func(dir string) error {
		var err error
		skipped, err = s.extract(ctx, dir, r)
		return err
	})
	if err == nil {
		audit.SetDetail(ctx, "skippedCount", skipped)
	}
	return err
}

// RestoreDraft replaces a template's draft with one of its versions.
func (s *Service) RestoreDraft(ctx context.Context, id string, number int) (domain.Template, error) {
	v, archive, err := s.Archive(ctx, id, number)
	if err != nil {
		return domain.Template{}, err
	}
	if err := s.ImportDraft(ctx, id, bytes.NewReader(archive)); err != nil {
		return domain.Template{}, err
	}
	audit.SetDetail(ctx, "versionNumber", v.Number)
	audit.SetDetail(ctx, "versionLabel", v.Label)
	return store.GetTemplate(ctx, s.db, id)
}

// CreateFrom creates a private template whose draft is the tar.gz from r
// (a duplicated version or a stack's files). A failure removes it again.
func (s *Service) CreateFrom(ctx context.Context, in domain.TemplateInput, r io.Reader, userID string) (domain.Template, error) {
	t, err := s.Create(ctx, in, userID)
	if err != nil {
		return t, err
	}
	if err := s.ImportDraft(ctx, t.ID, r); err != nil {
		if derr := s.Delete(context.WithoutCancel(ctx), t.ID, t.Revision); derr != nil {
			s.log.Warn("could not remove a template whose files failed", "template_id", t.ID, "error", derr)
		}
		return domain.Template{}, err
	}
	return store.GetTemplate(ctx, s.db, t.ID)
}

// Duplicate creates a private template from a version of this instance's
// template (registryID "" or this instance) or of an added registry's.
func (s *Service) Duplicate(ctx context.Context, in domain.TemplateInput, registryID, templateID string, version int, userID string) (domain.Template, error) {
	var archive []byte
	if registryID == "" || registryID == s.opts.InstanceID {
		_, b, err := s.Archive(ctx, templateID, version)
		if err != nil {
			return domain.Template{}, err
		}
		archive = b
	} else {
		_, _, b, err := s.RegistryArchive(ctx, registryID, templateID, version)
		if err != nil {
			return domain.Template{}, err
		}
		archive = b
	}
	audit.SetDetail(ctx, "sourceTemplateId", templateID)
	audit.SetDetail(ctx, "sourceVersion", version)
	return s.CreateFrom(ctx, in, bytes.NewReader(archive), userID)
}
