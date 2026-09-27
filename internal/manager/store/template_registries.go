package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Template registry persistence (template registry): added registries,
// their cached templates and icons.

type templateRegistryRow struct {
	bun.BaseModel `bun:"table:template_registries"`

	InstanceID    string     `bun:"instance_id,pk"`
	URL           string     `bun:"url,notnull"`
	Name          string     `bun:"name,notnull"`
	Status        string     `bun:"status,notnull"`
	ErrorClass    string     `bun:"error_class,notnull"`
	ErrorMessage  string     `bun:"error_message,notnull"`
	ETag          string     `bun:"etag,notnull"`
	Failures      int        `bun:"failures,notnull"`
	SyncedAt      *time.Time `bun:"synced_at"`
	AttemptedAt   *time.Time `bun:"attempted_at"`
	AddedByUserID string     `bun:"added_by_user_id,notnull"`
	CreatedAt     time.Time  `bun:"created_at,notnull"`
	UpdatedAt     time.Time  `bun:"updated_at,notnull"`
}

type registryEntryRow struct {
	bun.BaseModel `bun:"table:template_registry_entries"`

	RegistryID  string    `bun:"registry_id,pk"`
	TemplateID  string    `bun:"template_id,pk"`
	Name        string    `bun:"name,notnull"`
	Description string    `bun:"description,notnull"`
	Tags        string    `bun:"tags,notnull"`
	Versions    string    `bun:"versions,notnull"`
	IconSHA256  string    `bun:"icon_sha256,notnull"`
	IconURL     string    `bun:"icon_url,notnull"`
	UpdatedAt   time.Time `bun:"updated_at,notnull"`
}

type registryIconRow struct {
	bun.BaseModel `bun:"table:template_registry_icons"`

	RegistryID string `bun:"registry_id,pk"`
	TemplateID string `bun:"template_id,pk"`
	MediaType  string `bun:"media_type,notnull"`
	SHA256     string `bun:"sha256,notnull"`
	Size       int64  `bun:"size,notnull"`
	Data       []byte `bun:"data,notnull"`
}

type registryVersionJSON struct {
	Number        int       `json:"number"`
	Label         string    `json:"label"`
	Notes         string    `json:"notes,omitempty"`
	PublishedAt   time.Time `json:"publishedAt"`
	ArchiveSHA256 string    `json:"archiveSha256"`
	ArchiveSize   int64     `json:"archiveSize"`
	ContentSize   int64     `json:"contentSize"`
	Entries       int       `json:"entries"`
	ArchiveURL    string    `json:"archiveUrl"`
}

func fromRegistry(r *domain.TemplateRegistry) templateRegistryRow {
	return templateRegistryRow{InstanceID: r.InstanceID, URL: r.URL, Name: r.Name, Status: string(r.Status), ErrorClass: r.ErrorClass,
		ErrorMessage: r.ErrorMessage, ETag: r.ETag, Failures: r.Failures, SyncedAt: utcPtr(r.SyncedAt), AttemptedAt: utcPtr(r.AttemptedAt),
		AddedByUserID: r.AddedByUserID, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func (r templateRegistryRow) toDomain() domain.TemplateRegistry {
	return domain.TemplateRegistry{InstanceID: r.InstanceID, URL: r.URL, Name: r.Name, Status: domain.TemplateRegistryStatus(r.Status),
		ErrorClass: r.ErrorClass, ErrorMessage: r.ErrorMessage, ETag: r.ETag, Failures: r.Failures, SyncedAt: utcPtr(r.SyncedAt),
		AttemptedAt: utcPtr(r.AttemptedAt), AddedByUserID: r.AddedByUserID, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func (r registryEntryRow) toDomain() domain.RegistryTemplate {
	t := domain.RegistryTemplate{RegistryID: r.RegistryID, TemplateID: r.TemplateID, Name: r.Name, Description: r.Description,
		IconSHA256: r.IconSHA256, IconURL: r.IconURL, UpdatedAt: r.UpdatedAt.UTC(), Tags: []string{}}
	_ = json.Unmarshal([]byte(r.Tags), &t.Tags)
	var vs []registryVersionJSON
	_ = json.Unmarshal([]byte(r.Versions), &vs)
	for _, v := range vs {
		t.Versions = append(t.Versions, domain.RegistryTemplateVersion(v))
	}
	return t
}

// UpsertTemplateRegistry stores a registry (a new one, or a known instance
// under a new URL or name).
func UpsertTemplateRegistry(ctx context.Context, db bun.IDB, r *domain.TemplateRegistry) error {
	row := fromRegistry(r)
	_, err := db.NewInsert().Model(&row).On("CONFLICT (instance_id) DO UPDATE").
		Set("url = EXCLUDED.url").Set("name = EXCLUDED.name").Set("status = EXCLUDED.status").
		Set("error_class = EXCLUDED.error_class").Set("error_message = EXCLUDED.error_message").Set("etag = EXCLUDED.etag").
		Set("failures = EXCLUDED.failures").Set("synced_at = EXCLUDED.synced_at").Set("attempted_at = EXCLUDED.attempted_at").
		Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "template_registries.url") {
			return domain.ErrTemplateRegistryExists
		}
		return fmt.Errorf("store: save template registry: %w", err)
	}
	return nil
}

// GetTemplateRegistry returns one registry with its template count.
func GetTemplateRegistry(ctx context.Context, db bun.IDB, instanceID string) (domain.TemplateRegistry, error) {
	var row templateRegistryRow
	err := db.NewSelect().Model(&row).Where("instance_id = ?", instanceID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TemplateRegistry{}, domain.ErrTemplateRegistryNotFound
	}
	if err != nil {
		return domain.TemplateRegistry{}, fmt.Errorf("store: get template registry: %w", err)
	}
	r := row.toDomain()
	n, err := db.NewSelect().Model((*registryEntryRow)(nil)).Where("registry_id = ?", instanceID).Count(ctx)
	if err != nil {
		return r, fmt.Errorf("store: count registry templates: %w", err)
	}
	r.Templates = n
	return r, nil
}

// FindTemplateRegistryByURL returns the registry with that origin.
func FindTemplateRegistryByURL(ctx context.Context, db bun.IDB, url string) (domain.TemplateRegistry, error) {
	var row templateRegistryRow
	err := db.NewSelect().Model(&row).Where("url = ?", url).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TemplateRegistry{}, domain.ErrTemplateRegistryNotFound
	}
	if err != nil {
		return domain.TemplateRegistry{}, fmt.Errorf("store: find template registry: %w", err)
	}
	return row.toDomain(), nil
}

// ListTemplateRegistries returns every registry by name with template
// counts.
func ListTemplateRegistries(ctx context.Context, db bun.IDB) ([]domain.TemplateRegistry, error) {
	var rows []templateRegistryRow
	if err := db.NewSelect().Model(&rows).Order("name ASC", "instance_id ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list template registries: %w", err)
	}
	var counts []struct {
		RegistryID string `bun:"registry_id"`
		N          int    `bun:"n"`
	}
	if err := db.NewSelect().Model((*registryEntryRow)(nil)).Column("registry_id").ColumnExpr("count(*) AS n").
		Group("registry_id").Scan(ctx, &counts); err != nil {
		return nil, fmt.Errorf("store: count registry templates: %w", err)
	}
	byID := map[string]int{}
	for _, c := range counts {
		byID[c.RegistryID] = c.N
	}
	out := make([]domain.TemplateRegistry, 0, len(rows))
	for _, r := range rows {
		d := r.toDomain()
		d.Templates = byID[r.InstanceID]
		out = append(out, d)
	}
	return out, nil
}

// DeleteTemplateRegistry removes a registry with its cached templates and
// icons.
func DeleteTemplateRegistry(ctx context.Context, db bun.IDB, instanceID string) error {
	res, err := db.NewDelete().Model((*templateRegistryRow)(nil)).Where("instance_id = ?", instanceID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete template registry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrTemplateRegistryNotFound
	}
	return nil
}

// ReplaceRegistryTemplates replaces a registry's cached templates (icons of
// templates that disappeared or whose icon changed go with them).
func ReplaceRegistryTemplates(ctx context.Context, db bun.IDB, registryID string, ts []domain.RegistryTemplate) error {
	keep := make([]string, 0, len(ts))
	for _, t := range ts {
		keep = append(keep, t.TemplateID)
		tags, err := json.Marshal(nonNil(t.Tags))
		if err != nil {
			return err
		}
		vs := make([]registryVersionJSON, 0, len(t.Versions))
		for _, v := range t.Versions {
			vs = append(vs, registryVersionJSON(v))
		}
		versions, err := json.Marshal(vs)
		if err != nil {
			return err
		}
		row := registryEntryRow{RegistryID: registryID, TemplateID: t.TemplateID, Name: t.Name, Description: t.Description, Tags: string(tags),
			Versions: string(versions), IconSHA256: t.IconSHA256, IconURL: t.IconURL, UpdatedAt: t.UpdatedAt.UTC()}
		if _, err := db.NewInsert().Model(&row).On("CONFLICT (registry_id, template_id) DO UPDATE").
			Set("name = EXCLUDED.name").Set("description = EXCLUDED.description").Set("tags = EXCLUDED.tags").
			Set("versions = EXCLUDED.versions").Set("icon_sha256 = EXCLUDED.icon_sha256").Set("icon_url = EXCLUDED.icon_url").
			Set("updated_at = EXCLUDED.updated_at").Exec(ctx); err != nil {
			return fmt.Errorf("store: save registry template: %w", err)
		}
		// An icon that no longer matches is dropped (the sync fetches the new one).
		if _, err := db.NewDelete().Model((*registryIconRow)(nil)).Where("registry_id = ?", registryID).
			Where("template_id = ?", t.TemplateID).Where("sha256 <> ?", t.IconSHA256).Exec(ctx); err != nil {
			return fmt.Errorf("store: drop a stale registry icon: %w", err)
		}
	}
	q := db.NewDelete().Model((*registryEntryRow)(nil)).Where("registry_id = ?", registryID)
	if len(keep) > 0 {
		q = q.Where("template_id NOT IN (?)", bun.List(keep))
	}
	if _, err := q.Exec(ctx); err != nil {
		return fmt.Errorf("store: drop removed registry templates: %w", err)
	}
	return nil
}

// ListRegistryTemplates returns cached templates (registryID "": of every
// registry), by name.
func ListRegistryTemplates(ctx context.Context, db bun.IDB, registryID string) ([]domain.RegistryTemplate, error) {
	var rows []registryEntryRow
	q := db.NewSelect().Model(&rows).Order("name ASC", "template_id ASC")
	if registryID != "" {
		q = q.Where("registry_id = ?", registryID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list registry templates: %w", err)
	}
	out := make([]domain.RegistryTemplate, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// GetRegistryTemplate returns one cached template.
func GetRegistryTemplate(ctx context.Context, db bun.IDB, registryID, templateID string) (domain.RegistryTemplate, error) {
	var row registryEntryRow
	err := db.NewSelect().Model(&row).Where("registry_id = ?", registryID).Where("template_id = ?", templateID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RegistryTemplate{}, domain.ErrTemplateNotFound
	}
	if err != nil {
		return domain.RegistryTemplate{}, fmt.Errorf("store: get registry template: %w", err)
	}
	return row.toDomain(), nil
}

// PutRegistryIcon stores a cached template's icon.
func PutRegistryIcon(ctx context.Context, db bun.IDB, registryID, templateID string, icon domain.TemplateIcon, data []byte) error {
	row := registryIconRow{RegistryID: registryID, TemplateID: templateID, MediaType: icon.MediaType, SHA256: icon.SHA256, Size: icon.Size, Data: data}
	if _, err := db.NewInsert().Model(&row).On("CONFLICT (registry_id, template_id) DO UPDATE").
		Set("media_type = EXCLUDED.media_type").Set("sha256 = EXCLUDED.sha256").Set("size = EXCLUDED.size").Set("data = EXCLUDED.data").
		Exec(ctx); err != nil {
		return fmt.Errorf("store: save registry icon: %w", err)
	}
	return nil
}

// RegistryIconData returns a cached template's icon.
func RegistryIconData(ctx context.Context, db bun.IDB, registryID, templateID string) (domain.TemplateIcon, []byte, error) {
	var row registryIconRow
	err := db.NewSelect().Model(&row).Where("registry_id = ?", registryID).Where("template_id = ?", templateID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TemplateIcon{}, nil, domain.ErrTemplateIconNotFound
	}
	if err != nil {
		return domain.TemplateIcon{}, nil, fmt.Errorf("store: get registry icon: %w", err)
	}
	return domain.TemplateIcon{MediaType: row.MediaType, SHA256: row.SHA256, Size: row.Size}, row.Data, nil
}

// RegistryIconSums returns the cached icon digests of a registry by
// template ID.
func RegistryIconSums(ctx context.Context, db bun.IDB, registryID string) (map[string]string, error) {
	var rows []registryIconRow
	if err := db.NewSelect().Model(&rows).Column("template_id", "sha256").Where("registry_id = ?", registryID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list registry icons: %w", err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.TemplateID] = r.SHA256
	}
	return out, nil
}
