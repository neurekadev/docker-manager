package templates

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Registries of other instances (template registry). Adding one reads its
// index right away; the service keeps the copy current with a sync every
// Options.SyncInterval (failed registries back off up to 6 h). Removing a
// registry drops its cached templates and icons; stacks created from them
// keep working and show their template's icon again when the registry is
// added back (same instance ID).

// DefaultSyncInterval is how often registries are synced.
const DefaultSyncInterval = 30 * time.Minute

// maxBackoff bounds the delay after failed syncs.
const maxBackoff = 6 * time.Hour

// Registries returns the added registries.
func (s *Service) Registries(ctx context.Context) ([]domain.TemplateRegistry, error) {
	return store.ListTemplateRegistries(ctx, s.db)
}

// Registry returns one registry.
func (s *Service) Registry(ctx context.Context, instanceID string) (domain.TemplateRegistry, error) {
	return store.GetTemplateRegistry(ctx, s.db, instanceID)
}

// InstanceID returns this instance's ID (its own registry).
func (s *Service) InstanceID() string { return s.opts.InstanceID }

// AddRegistry adds the registry at rawURL: its index is read now. The same
// instance under a new URL replaces the old URL (its stacks keep their
// icons); this instance itself is refused.
func (s *Service) AddRegistry(ctx context.Context, rawURL, userID string) (domain.TemplateRegistry, error) {
	base, err := NormalizeRegistryURL(rawURL)
	if err != nil {
		return domain.TemplateRegistry{}, err
	}
	if cur, err := store.FindTemplateRegistryByURL(ctx, s.db, base); err == nil {
		return cur, domain.ErrTemplateRegistryExists
	}
	idx, etag, _, err := s.client.FetchIndex(ctx, base, "")
	if err != nil {
		return domain.TemplateRegistry{}, err
	}
	if idx.InstanceID == s.opts.InstanceID {
		return domain.TemplateRegistry{}, domain.ErrTemplateRegistryIsSelf
	}
	now := s.opts.Clock.Now().UTC()
	r := domain.TemplateRegistry{InstanceID: idx.InstanceID, URL: base, Name: idx.Name, Status: domain.TemplateRegistryOK, ETag: etag,
		SyncedAt: &now, AttemptedAt: &now, AddedByUserID: userID, CreatedAt: now, UpdatedAt: now}
	if old, err := store.GetTemplateRegistry(ctx, s.db, idx.InstanceID); err == nil {
		r.CreatedAt, r.AddedByUserID = old.CreatedAt, old.AddedByUserID
		audit.SetDetail(ctx, "previousUrl", old.URL)
	}
	if err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.UpsertTemplateRegistry(ctx, tx, &r); err != nil {
			return err
		}
		return store.ReplaceRegistryTemplates(ctx, tx, r.InstanceID, entriesOf(idx))
	}); err != nil {
		return domain.TemplateRegistry{}, err
	}
	s.fetchIcons(ctx, r, idx)
	audit.AddTarget(ctx, domain.AuditTarget{Type: "template_registry", ID: r.InstanceID})
	audit.SetDetail(ctx, "registryUrl", base)
	audit.SetDetail(ctx, "templateCount", len(idx.Templates))
	s.changed(r.InstanceID)
	return store.GetTemplateRegistry(ctx, s.db, r.InstanceID)
}

// RemoveRegistry removes a registry and its cached templates and icons.
func (s *Service) RemoveRegistry(ctx context.Context, instanceID string) error {
	r, err := store.GetTemplateRegistry(ctx, s.db, instanceID)
	if err != nil {
		return err
	}
	if err := store.DeleteTemplateRegistry(ctx, s.db, instanceID); err != nil {
		return err
	}
	audit.SetDetail(ctx, "registryUrl", r.URL)
	audit.SetDetail(ctx, "templateCount", r.Templates)
	s.changed(instanceID)
	return nil
}

// SyncRegistry refreshes one registry now.
func (s *Service) SyncRegistry(ctx context.Context, instanceID string) (domain.TemplateRegistry, error) {
	r, err := store.GetTemplateRegistry(ctx, s.db, instanceID)
	if err != nil {
		return r, err
	}
	s.sync(ctx, r)
	return store.GetTemplateRegistry(ctx, s.db, instanceID)
}

// sync reads a registry's index (conditionally) and stores the outcome.
func (s *Service) sync(ctx context.Context, r domain.TemplateRegistry) {
	now := s.opts.Clock.Now().UTC()
	r.AttemptedAt, r.UpdatedAt = &now, now
	idx, etag, notModified, err := s.client.FetchIndex(ctx, r.URL, r.ETag)
	if err == nil && !notModified && idx.InstanceID != r.InstanceID {
		err = &RegistryError{Class: RegistryInvalid, Message: "another Docker Manager now answers at this address; remove the registry and add the address again"}
	}
	if err != nil {
		var re *RegistryError
		r.Status, r.ErrorClass, r.ErrorMessage = domain.TemplateRegistryError, RegistryUnreachable, "the registry could not be read"
		if errors.As(err, &re) {
			r.ErrorClass, r.ErrorMessage = re.Class, re.Message
		}
		r.Failures++
		if err := store.UpsertTemplateRegistry(ctx, s.db, &r); err != nil {
			s.log.Warn("could not record a registry sync", "registry", r.InstanceID, "error", err)
		}
		s.changed(r.InstanceID)
		return
	}
	r.Status, r.ErrorClass, r.ErrorMessage, r.Failures, r.SyncedAt = domain.TemplateRegistryOK, "", "", 0, &now
	if notModified {
		if err := store.UpsertTemplateRegistry(ctx, s.db, &r); err != nil {
			s.log.Warn("could not record a registry sync", "registry", r.InstanceID, "error", err)
		}
		return
	}
	r.Name, r.ETag = idx.Name, etag
	if err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.UpsertTemplateRegistry(ctx, tx, &r); err != nil {
			return err
		}
		return store.ReplaceRegistryTemplates(ctx, tx, r.InstanceID, entriesOf(idx))
	}); err != nil {
		s.log.Warn("could not store a registry sync", "registry", r.InstanceID, "error", err)
		return
	}
	s.fetchIcons(ctx, r, idx)
	s.changed(r.InstanceID)
}

// fetchIcons fetches icons that are new or changed (best effort: a
// template without its icon still works).
func (s *Service) fetchIcons(ctx context.Context, r domain.TemplateRegistry, idx RegistryIndex) {
	have, err := store.RegistryIconSums(ctx, s.db, r.InstanceID)
	if err != nil {
		return
	}
	for _, e := range idx.Templates {
		if e.Icon == nil || have[e.ID] == e.Icon.SHA256 {
			continue
		}
		icon, data, err := s.client.FetchIcon(ctx, r.URL, *e.Icon)
		if err != nil || icon.SHA256 != e.Icon.SHA256 {
			continue
		}
		if err := store.PutRegistryIcon(ctx, s.db, r.InstanceID, e.ID, icon, data); err != nil {
			s.log.Warn("could not store a registry icon", "registry", r.InstanceID, "error", err)
		}
	}
}

// entriesOf converts an index to cached templates.
func entriesOf(idx RegistryIndex) []domain.RegistryTemplate {
	out := make([]domain.RegistryTemplate, 0, len(idx.Templates))
	for _, e := range idx.Templates {
		t := domain.RegistryTemplate{RegistryID: idx.InstanceID, TemplateID: e.ID, Name: e.Name, Description: e.Description, Tags: e.Tags,
			UpdatedAt: e.UpdatedAt}
		for _, l := range e.Links {
			t.Links = append(t.Links, domain.Link(l))
		}
		if e.Icon != nil {
			t.IconSHA256, t.IconURL = e.Icon.SHA256, e.Icon.URL
		}
		for _, v := range e.Versions {
			t.Versions = append(t.Versions, domain.RegistryTemplateVersion{Number: v.Number, Label: v.Label, Notes: v.Notes,
				PublishedAt: v.PublishedAt, ArchiveSHA256: v.Archive.SHA256, ArchiveSize: v.Archive.Size, ContentSize: v.Archive.ContentSize,
				Entries: v.Archive.Entries, ArchiveURL: v.Archive.URL})
		}
		out = append(out, t)
	}
	return out
}

// changed tells live views that templates of a registry changed.
func (s *Service) changed(instanceID string) {
	if s.opts.Bus != nil {
		s.opts.Bus.Publish(events.Event{Type: events.ResourceChanged, ResourceType: "template_registry", ResourceID: instanceID,
			Attributes: map[string]string{"op": "update"}})
	}
}

// RegistryTemplates returns cached templates (registryID "": of every
// registry).
func (s *Service) RegistryTemplates(ctx context.Context, registryID string) ([]domain.RegistryTemplate, error) {
	return store.ListRegistryTemplates(ctx, s.db, registryID)
}

// RegistryTemplate returns one cached template.
func (s *Service) RegistryTemplate(ctx context.Context, registryID, templateID string) (domain.RegistryTemplate, error) {
	return store.GetRegistryTemplate(ctx, s.db, registryID, templateID)
}

// RegistryIcon returns a cached template's icon.
func (s *Service) RegistryIcon(ctx context.Context, registryID, templateID string) (domain.TemplateIcon, []byte, error) {
	return store.RegistryIconData(ctx, s.db, registryID, templateID)
}

// RegistryArchive downloads a registry template's version (checked against
// the cached index's digest).
func (s *Service) RegistryArchive(ctx context.Context, registryID, templateID string, version int) (domain.RegistryTemplate, domain.RegistryTemplateVersion, []byte, error) {
	r, err := store.GetTemplateRegistry(ctx, s.db, registryID)
	if err != nil {
		return domain.RegistryTemplate{}, domain.RegistryTemplateVersion{}, nil, err
	}
	t, err := store.GetRegistryTemplate(ctx, s.db, registryID, templateID)
	if err != nil {
		return t, domain.RegistryTemplateVersion{}, nil, err
	}
	v, ok := t.Version(version)
	if !ok {
		return t, v, nil, domain.ErrTemplateVersionNotFound
	}
	b, err := s.client.FetchArchive(ctx, r.URL, RegistryArchive{SHA256: v.ArchiveSHA256, Size: v.ArchiveSize, URL: v.ArchiveURL},
		s.opts.MaxSize*2)
	return t, v, b, err
}

// RegistryDefinition returns the Compose sources of a registry template's
// version.
func (s *Service) RegistryDefinition(ctx context.Context, registryID, templateID string, version int) (domain.RegistryTemplateVersion, []domain.TemplateFileContent, error) {
	_, v, b, err := s.RegistryArchive(ctx, registryID, templateID, version)
	if err != nil {
		return v, nil, err
	}
	files, err := DefinitionOf(b)
	return v, files, err
}

// due reports whether a registry should sync now (failed ones back off).
func (s *Service) due(r domain.TemplateRegistry, now time.Time) bool {
	if r.AttemptedAt == nil {
		return true
	}
	wait := s.opts.SyncInterval
	for i := 0; i < r.Failures && wait < maxBackoff; i++ {
		wait *= 2
	}
	return !now.Before(r.AttemptedAt.Add(min(wait, maxBackoff)))
}

// SyncDue syncs every registry whose interval (or backoff) passed.
func (s *Service) SyncDue(ctx context.Context) {
	rs, err := store.ListTemplateRegistries(ctx, s.db)
	if err != nil {
		s.log.Warn("could not list template registries", "error", err)
		return
	}
	now := s.opts.Clock.Now()
	for _, r := range rs {
		if ctx.Err() != nil {
			return
		}
		if s.due(r, now) {
			s.sync(ctx, r)
		}
	}
}

// syncer runs the periodic syncs.
type syncer struct {
	once sync.Once
	stop chan struct{}
	done chan struct{}
}

// StartSync starts the periodic registry syncs (a zero SyncInterval turns
// them off); Close stops them.
func (s *Service) StartSync() {
	if s.opts.SyncInterval <= 0 {
		return
	}
	s.syncer = &syncer{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.syncer.done)
		t := s.opts.Clock.NewTicker(min(s.opts.SyncInterval, 5*time.Minute))
		defer t.Stop()
		for {
			select {
			case <-s.syncer.stop:
				return
			case <-t.C():
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				s.SyncDue(ctx)
				cancel()
			}
		}
	}()
}

// Close stops the periodic syncs.
func (s *Service) Close() {
	if s.syncer == nil {
		return
	}
	s.syncer.once.Do(func() { close(s.syncer.stop) })
	<-s.syncer.done
}
