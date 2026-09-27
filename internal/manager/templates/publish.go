package templates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"
	"go.yaml.in/yaml/v4"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/fsroot"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// A published version is a canonical tar.gz of the draft: entries in
// lexical path order with "." first, PAX headers, owner 0:0, the version's
// time as every modification time, permission bits only (no setuid,
// setgid or sticky bits), and only directories, regular files with one
// hard link and symlinks whose target stays inside the template. The same
// draft therefore always yields the same bytes.

// composeRE matches Compose files at the root (compose.yaml, override
// files, docker-compose.*).
var composeRE = regexp.MustCompile(`^(docker-)?compose(\.[^/]+)?\.ya?ml$`)

// mainCompose are the file names Compose loads by default.
var mainCompose = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// IsDefinitionFile reports whether a template path is one of its Compose
// sources (a Compose file or .env at the root).
func IsDefinitionFile(p string) bool {
	return !strings.Contains(p, "/") && (p == ".env" || composeRE.MatchString(p))
}

// archiveInfo describes a written archive.
type archiveInfo struct {
	contentSize int64
	entries     int
	definition  []domain.TemplateFile
}

func defErr(format string, args ...any) error {
	return &domain.TemplateDefinitionError{Message: fmt.Sprintf(format, args...)}
}

// writeArchive packs dir canonically into w within the limits.
func writeArchive(ctx context.Context, dir string, maxSize int64, maxEntries int, mod time.Time, w io.Writer) (archiveInfo, error) {
	var info archiveInfo
	root, err := os.OpenRoot(dir)
	if err != nil {
		return info, fmt.Errorf("templates: open the draft: %w", err)
	}
	defer func() { _ = root.Close() }()
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return info, err
	}
	tw := tar.NewWriter(gz)
	mod = mod.UTC().Truncate(time.Second)
	hdr := func(name string, typ byte, mode int64) *tar.Header {
		return &tar.Header{Name: name, Typeflag: typ, Mode: mode, ModTime: mod, Format: tar.FormatPAX}
	}
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info.entries++
		if info.entries > maxEntries+1 {
			return &domain.TemplateTooLargeError{Message: fmt.Sprintf("a template holds at most %d files and directories", maxEntries)}
		}
		fi, err := root.Lstat(p)
		if err != nil {
			return err
		}
		perm := int64(fi.Mode().Perm())
		switch {
		case fi.IsDir():
			name := p + "/"
			if p == "." {
				name = "./"
			}
			return tw.WriteHeader(hdr(name, tar.TypeDir, perm))
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := root.Readlink(p)
			if err != nil {
				return err
			}
			if !linkInside(p, target) {
				return defErr("%s is a symlink that leaves the template; replace it with the file itself", p)
			}
			h := hdr(p, tar.TypeSymlink, 0o777)
			h.Linkname = target
			return tw.WriteHeader(h)
		case fi.Mode().IsRegular():
			f, err := root.Open(p)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			ofi, err := f.Stat()
			if err != nil {
				return err
			}
			if !os.SameFile(fi, ofi) {
				return defErr("%s changed while the version was written; publish again", p)
			}
			if fsroot.OpenLinks(f, ofi) > 1 {
				return defErr("%s has several hard links; replace it with a copy", p)
			}
			info.contentSize += ofi.Size()
			if info.contentSize > maxSize {
				return &domain.TemplateTooLargeError{Message: fmt.Sprintf("a template holds at most %d MiB", maxSize>>20)}
			}
			h := hdr(p, tar.TypeReg, perm)
			h.Size = ofi.Size()
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if n, err := io.Copy(tw, io.LimitReader(f, ofi.Size())); err != nil || n != ofi.Size() {
				if err == nil {
					err = defErr("%s changed while the version was written; publish again", p)
				}
				return err
			}
			if IsDefinitionFile(p) {
				info.definition = append(info.definition, domain.TemplateFile{Path: p, Size: ofi.Size()})
			}
			return nil
		}
		return defErr("%s is not a regular file, directory or symlink (devices, sockets and pipes cannot be published)", p)
	})
	if err != nil {
		return info, err
	}
	info.entries-- // the root itself
	if err := tw.Close(); err != nil {
		return info, err
	}
	return info, gz.Close()
}

// linkInside reports whether a symlink at p with target stays inside the
// template (relative, not climbing above the root).
func linkInside(p, target string) bool {
	if target == "" || path.IsAbs(target) || strings.Contains(target, "\\") || (len(target) > 1 && target[1] == ':') {
		return false
	}
	joined := path.Join(path.Dir(p), target)
	return joined != ".." && !strings.HasPrefix(joined, "../")
}

// checkDefinition refuses a draft without a default Compose file or with
// a Compose file that pins the project name (a stack created from it
// could not be named freely).
func checkDefinition(ctx context.Context, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("templates: open the draft: %w", err)
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.ContainsFunc(mainCompose, func(n string) bool { return slices.Contains(names, n) }) {
		return defErr("the template needs a compose.yaml (or compose.yml, docker-compose.yaml, docker-compose.yml) at its root")
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !composeRE.MatchString(e.Name()) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := root.ReadFile(e.Name())
		if err != nil {
			return err
		}
		if err := checkComposeFile(e.Name(), b); err != nil {
			return err
		}
	}
	return nil
}

// checkComposeFile checks one Compose file's top level (the manager never
// interprets Compose beyond this; the agent validates at creation).
func checkComposeFile(name string, b []byte) error {
	if !utf8.Valid(b) {
		return defErr("%s is not UTF-8 text", name)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return defErr("%s is not valid YAML", name)
	}
	if len(doc.Content) == 0 {
		return nil
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return defErr("%s must be a YAML mapping (services: ...)", name)
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "name" {
			return defErr("%s sets a top-level name:, which fixes the project name; remove it so each stack gets its own name", name)
		}
	}
	return nil
}

// Publish freezes the draft as a new version. Publishing a version of a
// public template needs the acknowledgement that every file, .env
// included, becomes public.
func (s *Service) Publish(ctx context.Context, id, label, notes string, acknowledged bool, userID string) (domain.TemplateVersion, error) {
	label = strings.TrimSpace(label)
	if !domain.ValidTemplateLabel(label) {
		return domain.TemplateVersion{}, fieldErr("label", "1 to 32 letters, digits and . + _ - (for example 1.2.0)")
	}
	if utf8.RuneCountInString(notes) > domain.MaxTemplateVersionNote {
		return domain.TemplateVersion{}, fieldErr("notes", "at most 4096 characters")
	}
	t, err := store.GetTemplate(ctx, s.db, id)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	if t.Visibility == domain.TemplatePublic && !acknowledged {
		return domain.TemplateVersion{}, domain.ErrTemplatePublicAckRequired
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	dir := s.draftDir(id)
	if err := checkDefinition(ctx, dir); err != nil {
		return domain.TemplateVersion{}, err
	}
	now := s.opts.Clock.Now().UTC()
	var buf bytes.Buffer
	info, err := writeArchive(ctx, dir, s.opts.MaxSize, s.opts.MaxEntries, now, &buf)
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	sum := sha256.Sum256(buf.Bytes())
	v := domain.TemplateVersion{ID: ids.New(), TemplateID: id, Label: label, Notes: notes, ArchiveSHA256: hex.EncodeToString(sum[:]),
		ArchiveSize: int64(buf.Len()), ContentSize: info.contentSize, Entries: info.entries, Definition: info.definition,
		PublishedByUserID: userID, CreatedAt: now}
	sealed, err := s.opts.Keyring.Seal(buf.Bytes(), archiveContext(v.ID))
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("templates: seal the archive: %w", err)
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		n, err := store.NextTemplateVersion(ctx, tx, id)
		if err != nil {
			return err
		}
		v.Number = n
		return store.InsertTemplateVersion(ctx, tx, &v, sealed)
	})
	if err != nil {
		return domain.TemplateVersion{}, err
	}
	audit.SetDetail(ctx, "versionNumber", v.Number)
	audit.SetDetail(ctx, "versionLabel", v.Label)
	audit.SetDetail(ctx, "archiveSha256", v.ArchiveSHA256)
	audit.SetDetail(ctx, "entryCount", v.Entries)
	return v, nil
}
