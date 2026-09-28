// Package templates keeps the instance's stack templates (template
// registry): metadata, icons, the editable draft of each template (a
// directory in the manager's data directory, served to the file manager
// through internal/fsroot) and the immutable published versions (sealed
// canonical tar.gz archives in the database).
//
// It does not authorize: the API layer checks the template capabilities.
// Draft and archive contents (which include .env files) are never logged
// or audited.
package templates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/fsroot"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Defaults.
const (
	// DefaultMaxSize bounds a template's content (draft and versions).
	DefaultMaxSize = 32 << 20
	// DefaultMaxEntries bounds a template's files and directories.
	DefaultMaxEntries = 5000
)

// Options configures the service.
type Options struct {
	DB      *bun.DB
	Keyring *secrets.Keyring
	Clock   clock.Clock
	Logger  *slog.Logger
	// DataDir is the manager's data directory; drafts live in
	// <DataDir>/templates/<id>/draft.
	DataDir string
	// MaxSize and MaxEntries bound a template's content (defaults above).
	MaxSize    int64
	MaxEntries int
	// Bus receives file invalidations of drafts (live file views).
	Bus *events.Bus
	// Executors registers the template.files.* job executors (the job
	// engine's RegisterManagerExecutor); nil in focused tests.
	Executors func(jobexec.Executor) error
	// ForgetResource drops authorization state of deleted templates.
	ForgetResource func(ctx context.Context, ref authz.ResourceRef) (int, error)
	// InstanceID is this instance's ID (its own registry; adding it as a
	// registry is refused).
	InstanceID string
	// HTTPClient reads other registries (nil: a default client; tests
	// inject httptest clients).
	HTTPClient *http.Client
	// SyncInterval is how often added registries are synced (0: the
	// default; negative: never, for tests).
	SyncInterval time.Duration
}

// Service manages templates.
type Service struct {
	opts  Options
	db    *bun.DB
	log   *slog.Logger
	files *fsroot.Service

	// draftMu serializes publications and whole-draft changes with file
	// writes per template (write: publish/delete; read: file writes).
	mu      sync.Mutex
	drafts  map[string]*sync.RWMutex
	usageMu sync.Mutex
	usage   map[string]draftUsage

	client *RegistryClient
	syncer *syncer
}

// New returns the service, removes drafts of templates that no longer
// exist and registers the file job executors.
func New(ctx context.Context, o Options) (*Service, error) {
	if o.DB == nil || o.Keyring == nil || o.DataDir == "" {
		return nil, errors.New("templates: DB, Keyring and DataDir are required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.MaxSize <= 0 {
		o.MaxSize = DefaultMaxSize
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = DefaultMaxEntries
	}
	if o.SyncInterval == 0 {
		o.SyncInterval = DefaultSyncInterval
	}
	s := &Service{opts: o, db: o.DB, log: o.Logger, drafts: map[string]*sync.RWMutex{}, usage: map[string]draftUsage{},
		client: NewRegistryClient(o.HTTPClient)}
	s.files = fsroot.New(fsroot.Options{
		Resolve: s.resolve, Clock: o.Clock, Logger: o.Logger,
		Limits: fsroot.Limits{MaxUpload: o.MaxSize, MaxDownload: 4 * o.MaxSize, MaxArchiveEntries: o.MaxEntries,
			MaxExtractBytes: o.MaxSize, MaxWalk: 4 * o.MaxEntries},
		Kinds:      templateKinds,
		Invalidate: s.invalidate,
	})
	if err := os.MkdirAll(s.root(), 0o700); err != nil {
		return nil, fmt.Errorf("templates: create the templates directory: %w", err)
	}
	if err := s.sweep(ctx); err != nil {
		return nil, err
	}
	if o.Executors != nil {
		for _, x := range s.files.Executors() {
			if err := o.Executors(s.guardExecutor(x)); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

// root is the directory holding every template's directory.
func (s *Service) root() string { return filepath.Join(s.opts.DataDir, "templates") }

// draftDir is a template's draft directory.
func (s *Service) draftDir(id string) string { return filepath.Join(s.root(), id, "draft") }

// sweep removes template directories without a template (a crash between
// deleting the row and the directory, or a restored older database).
func (s *Service) sweep(ctx context.Context) error {
	ids, err := store.TemplateIDs(ctx, s.db)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, id := range ids {
		known[id] = true
	}
	entries, err := os.ReadDir(s.root())
	if err != nil {
		return fmt.Errorf("templates: read the templates directory: %w", err)
	}
	for _, e := range entries {
		if !known[e.Name()] {
			s.log.Info("removing the directory of a template that no longer exists", "template_id", e.Name())
			_ = os.RemoveAll(filepath.Join(s.root(), e.Name()))
		}
	}
	for _, id := range ids {
		if err := os.MkdirAll(s.draftDir(id), 0o700); err != nil {
			return fmt.Errorf("templates: create a draft directory: %w", err)
		}
	}
	return nil
}

func (s *Service) lock(id string) *sync.RWMutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.drafts[id]
	if l == nil {
		l = &sync.RWMutex{}
		s.drafts[id] = l
	}
	return l
}

func fieldErr(field, msg string) error { return &domain.FieldError{Field: field, Message: msg} }

func checkMeta(name, description string) error {
	if n := strings.TrimSpace(name); n == "" || utf8.RuneCountInString(n) > domain.MaxTemplateName {
		return fieldErr("name", "1 to 100 characters")
	}
	if utf8.RuneCountInString(description) > domain.MaxTemplateDescription {
		return fieldErr("description", "at most 1024 characters")
	}
	return nil
}

// starter is the draft of a new template.
const starter = `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
    restart: unless-stopped
`

// List returns templates in creation order after afterID (limit 0: all).
func (s *Service) List(ctx context.Context, afterID string, limit int) ([]domain.Template, error) {
	return store.ListTemplates(ctx, s.db, afterID, limit)
}

// Get returns one template.
func (s *Service) Get(ctx context.Context, id string) (domain.Template, error) {
	return store.GetTemplate(ctx, s.db, id)
}

// Create stores a private template with a starter draft (compose.yaml).
func (s *Service) Create(ctx context.Context, in domain.TemplateInput, userID string) (domain.Template, error) {
	in.Name = strings.TrimSpace(in.Name)
	if err := checkMeta(in.Name, in.Description); err != nil {
		return domain.Template{}, err
	}
	tags, err := domain.NormalizeTemplateTags(in.Tags)
	if err != nil {
		return domain.Template{}, err
	}
	now := s.opts.Clock.Now().UTC()
	t := domain.Template{ID: ids.New(), Name: in.Name, Description: in.Description, Tags: tags, Visibility: domain.TemplatePrivate,
		CreatedByUserID: userID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	dir := s.draftDir(t.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return domain.Template{}, fmt.Errorf("templates: create the draft: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(starter), 0o600); err != nil {
		_ = os.RemoveAll(filepath.Dir(dir))
		return domain.Template{}, fmt.Errorf("templates: write the starter draft: %w", err)
	}
	if err := store.InsertTemplate(ctx, s.db, &t); err != nil {
		_ = os.RemoveAll(filepath.Dir(dir))
		return domain.Template{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeTemplate, ID: t.ID})
	audit.SetDetail(ctx, "templateName", t.Name)
	return store.GetTemplate(ctx, s.db, t.ID)
}

// Update edits a template's name, description and tags.
func (s *Service) Update(ctx context.Context, id string, revision int64, p domain.TemplatePatch) (domain.Template, error) {
	before, err := store.GetTemplate(ctx, s.db, id)
	if err != nil {
		return domain.Template{}, err
	}
	if before.Revision != revision {
		return domain.Template{}, domain.ErrRevisionMismatch
	}
	after := before
	if p.Name != nil {
		after.Name = strings.TrimSpace(*p.Name)
	}
	if p.Description != nil {
		after.Description = *p.Description
	}
	if p.Tags != nil {
		if after.Tags, err = domain.NormalizeTemplateTags(*p.Tags); err != nil {
			return domain.Template{}, err
		}
	}
	if err := checkMeta(after.Name, after.Description); err != nil {
		return domain.Template{}, err
	}
	after.Revision, after.UpdatedAt = before.Revision+1, s.opts.Clock.Now().UTC()
	if err := store.UpdateTemplate(ctx, s.db, &after, revision); err != nil {
		return domain.Template{}, err
	}
	audit.SetDiff(ctx, auditView(before), auditView(after))
	return store.GetTemplate(ctx, s.db, id)
}

func auditView(t domain.Template) map[string]any {
	return map[string]any{"name": t.Name, "description": t.Description, "tags": t.Tags, "visibility": string(t.Visibility)}
}

// SetVisibility makes a template public or private. Making it public needs
// the acknowledgement that every file, .env included, becomes public.
func (s *Service) SetVisibility(ctx context.Context, id string, revision int64, v domain.TemplateVisibility, acknowledged bool) (domain.Template, error) {
	if !v.Valid() {
		return domain.Template{}, fieldErr("visibility", "private or public")
	}
	before, err := store.GetTemplate(ctx, s.db, id)
	if err != nil {
		return domain.Template{}, err
	}
	if before.Revision != revision {
		return domain.Template{}, domain.ErrRevisionMismatch
	}
	if v == domain.TemplatePublic && before.Visibility != domain.TemplatePublic && !acknowledged {
		return domain.Template{}, domain.ErrTemplatePublicAckRequired
	}
	after := before
	after.Visibility, after.Revision, after.UpdatedAt = v, before.Revision+1, s.opts.Clock.Now().UTC()
	if err := store.UpdateTemplate(ctx, s.db, &after, revision); err != nil {
		return domain.Template{}, err
	}
	audit.SetDiff(ctx, map[string]any{"visibility": string(before.Visibility)}, map[string]any{"visibility": string(v)})
	return store.GetTemplate(ctx, s.db, id)
}

// Delete removes a template, its icon, versions and draft. Stacks created
// from it keep working (their files are their own).
func (s *Service) Delete(ctx context.Context, id string, revision int64) error {
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	t, err := store.GetTemplate(ctx, s.db, id)
	if err != nil {
		return err
	}
	if err := store.DeleteTemplate(ctx, s.db, id, revision); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.root(), id)); err != nil {
		// The next start's sweep removes it.
		s.log.Warn("could not remove a deleted template's directory", "template_id", id, "error", err)
	}
	s.forgetUsage(id)
	audit.SetDetail(ctx, "templateName", t.Name)
	audit.SetDetail(ctx, "versionCount", t.Versions)
	if s.opts.ForgetResource != nil {
		_, _ = s.opts.ForgetResource(ctx, authz.ResourceRef{Type: catalog.TypeTemplate, ID: id})
	}
	return nil
}

// Exists reports domain.ErrTemplateNotFound for unknown templates.
func (s *Service) Exists(ctx context.Context, id string) error {
	_, err := store.GetTemplate(ctx, s.db, id)
	return err
}

// Icon returns a template's icon and bytes.
func (s *Service) Icon(ctx context.Context, id string) (domain.TemplateIcon, []byte, error) {
	return store.TemplateIconData(ctx, s.db, id)
}

// SetIcon replaces a template's icon after checking its bytes.
func (s *Service) SetIcon(ctx context.Context, id string, data []byte) (domain.Template, error) {
	if _, err := store.GetTemplate(ctx, s.db, id); err != nil {
		return domain.Template{}, err
	}
	icon, err := DetectIcon(data)
	if err != nil {
		return domain.Template{}, err
	}
	icon.UpdatedAt = s.opts.Clock.Now().UTC()
	if err := store.PutTemplateIcon(ctx, s.db, id, icon, data); err != nil {
		return domain.Template{}, err
	}
	audit.SetDetail(ctx, "iconSha256", icon.SHA256)
	audit.SetDetail(ctx, "iconMediaType", icon.MediaType)
	return store.GetTemplate(ctx, s.db, id)
}

// RemoveIcon removes a template's icon.
func (s *Service) RemoveIcon(ctx context.Context, id string) (domain.Template, error) {
	if err := store.DeleteTemplateIcon(ctx, s.db, id); err != nil {
		return domain.Template{}, err
	}
	return store.GetTemplate(ctx, s.db, id)
}

// Versions lists a template's versions, newest first.
func (s *Service) Versions(ctx context.Context, id string) ([]domain.TemplateVersion, error) {
	if _, err := store.GetTemplate(ctx, s.db, id); err != nil {
		return nil, err
	}
	return store.ListTemplateVersions(ctx, s.db, id)
}

// Version returns one version by number.
func (s *Service) Version(ctx context.Context, id string, number int) (domain.TemplateVersion, error) {
	return store.GetTemplateVersion(ctx, s.db, id, number)
}

// DeleteVersion removes a published version. Stacks created from it keep
// working; other managers stop seeing it at their next sync.
func (s *Service) DeleteVersion(ctx context.Context, id string, number int) error {
	v, err := store.GetTemplateVersion(ctx, s.db, id, number)
	if err != nil {
		return err
	}
	if err := store.DeleteTemplateVersion(ctx, s.db, id, number); err != nil {
		return err
	}
	audit.SetDetail(ctx, "versionNumber", v.Number)
	audit.SetDetail(ctx, "versionLabel", v.Label)
	return nil
}

// Archive returns a version's tar.gz bytes (the unsealed archive).
func (s *Service) Archive(ctx context.Context, id string, number int) (domain.TemplateVersion, []byte, error) {
	v, err := store.GetTemplateVersion(ctx, s.db, id, number)
	if err != nil {
		return v, nil, err
	}
	sealed, err := store.TemplateVersionArchive(ctx, s.db, id, number)
	if err != nil {
		return v, nil, err
	}
	b, err := s.opts.Keyring.Open(sealed, archiveContext(v.ID))
	if err != nil {
		return v, nil, fmt.Errorf("templates: open the archive of version %d: %w", number, err)
	}
	return v, b, nil
}

func archiveContext(versionID string) string { return "template_versions/" + versionID + "/archive" }
