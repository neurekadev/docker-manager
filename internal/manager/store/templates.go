package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Stack template persistence (template registry). Icon bytes and sealed
// version archives are never loaded by list queries; they are read
// through TemplateIconData and TemplateVersionArchive.

type templateRow struct {
	bun.BaseModel `bun:"table:templates"`

	ID              string    `bun:"id,pk"`
	Name            string    `bun:"name,notnull"`
	NameKey         string    `bun:"name_key,notnull"`
	Description     string    `bun:"description,notnull"`
	Tags            string    `bun:"tags,notnull"`
	Links           string    `bun:"links,notnull"`
	Visibility      string    `bun:"visibility,notnull"`
	VersionSeq      int       `bun:"version_seq,notnull"`
	CreatedByUserID string    `bun:"created_by_user_id,notnull"`
	Revision        int64     `bun:"revision,notnull"`
	CreatedAt       time.Time `bun:"created_at,notnull"`
	UpdatedAt       time.Time `bun:"updated_at,notnull"`
}

type templateIconRow struct {
	bun.BaseModel `bun:"table:template_icons"`

	TemplateID string    `bun:"template_id,pk"`
	MediaType  string    `bun:"media_type,notnull"`
	SHA256     string    `bun:"sha256,notnull"`
	Size       int64     `bun:"size,notnull"`
	Data       []byte    `bun:"data,notnull"`
	UpdatedAt  time.Time `bun:"updated_at,notnull"`
}

type templateVersionRow struct {
	bun.BaseModel `bun:"table:template_versions"`

	ID                string    `bun:"id,pk"`
	TemplateID        string    `bun:"template_id,notnull"`
	Number            int       `bun:"number,notnull"`
	Label             string    `bun:"label,notnull"`
	Notes             string    `bun:"notes,notnull"`
	Archive           string    `bun:"archive,notnull"`
	ArchiveSHA256     string    `bun:"archive_sha256,notnull"`
	ArchiveSize       int64     `bun:"archive_size,notnull"`
	ContentSize       int64     `bun:"content_size,notnull"`
	Entries           int       `bun:"entries,notnull"`
	Definition        string    `bun:"definition,notnull"`
	PublishedByUserID string    `bun:"published_by_user_id,notnull"`
	CreatedAt         time.Time `bun:"created_at,notnull"`
}

type templateFileJSON struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func fromTemplate(t *domain.Template) (templateRow, error) {
	tags := t.Tags
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return templateRow{}, err
	}
	return templateRow{ID: t.ID, Name: t.Name, NameKey: NameKey(t.Name), Description: t.Description, Tags: string(b),
		Links: linksJSON(t.Links), Visibility: string(t.Visibility), CreatedByUserID: t.CreatedByUserID, Revision: t.Revision,
		CreatedAt: t.CreatedAt.UTC(), UpdatedAt: t.UpdatedAt.UTC()}, nil
}

func (r templateRow) toDomain() domain.Template {
	t := domain.Template{ID: r.ID, Name: r.Name, Description: r.Description, Links: linksOf(r.Links), Visibility: domain.TemplateVisibility(r.Visibility),
		CreatedByUserID: r.CreatedByUserID, Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
	if err := json.Unmarshal([]byte(r.Tags), &t.Tags); err != nil || t.Tags == nil {
		t.Tags = []string{}
	}
	return t
}

func (r templateVersionRow) toDomain() domain.TemplateVersion {
	v := domain.TemplateVersion{ID: r.ID, TemplateID: r.TemplateID, Number: r.Number, Label: r.Label, Notes: r.Notes,
		ArchiveSHA256: r.ArchiveSHA256, ArchiveSize: r.ArchiveSize, ContentSize: r.ContentSize, Entries: r.Entries,
		PublishedByUserID: r.PublishedByUserID, CreatedAt: r.CreatedAt.UTC(), Definition: []domain.TemplateFile{}}
	var files []templateFileJSON
	if err := json.Unmarshal([]byte(r.Definition), &files); err == nil {
		for _, f := range files {
			v.Definition = append(v.Definition, domain.TemplateFile{Path: f.Path, Size: f.Size})
		}
	}
	return v
}

// InsertTemplate stores a new template.
func InsertTemplate(ctx context.Context, db bun.IDB, t *domain.Template) error {
	row, err := fromTemplate(t)
	if err != nil {
		return fmt.Errorf("store: encode template: %w", err)
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "templates.name_key") {
			return domain.ErrTemplateNameTaken
		}
		return fmt.Errorf("store: insert template: %w", err)
	}
	return nil
}

// UpdateTemplate writes the metadata of t when the stored revision still
// equals expectRevision.
func UpdateTemplate(ctx context.Context, db bun.IDB, t *domain.Template, expectRevision int64) error {
	row, err := fromTemplate(t)
	if err != nil {
		return fmt.Errorf("store: encode template: %w", err)
	}
	res, err := db.NewUpdate().Model(&row).Column("name", "name_key", "description", "tags", "links", "visibility", "revision", "updated_at").
		WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "templates.name_key") {
			return domain.ErrTemplateNameTaken
		}
		return fmt.Errorf("store: update template: %w", err)
	}
	return revisionChecked(ctx, db, res, t.ID)
}

func revisionChecked(ctx context.Context, db bun.IDB, res sql.Result, id string) error {
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetTemplate(ctx, db, id); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// DeleteTemplate removes a template (its icon and versions cascade) when
// the revision matches.
func DeleteTemplate(ctx context.Context, db bun.IDB, id string, expectRevision int64) error {
	res, err := db.NewDelete().Model((*templateRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete template: %w", err)
	}
	return revisionChecked(ctx, db, res, id)
}

// GetTemplate returns one template with its icon metadata and latest
// version.
func GetTemplate(ctx context.Context, db bun.IDB, id string) (domain.Template, error) {
	var row templateRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Template{}, domain.ErrTemplateNotFound
	}
	if err != nil {
		return domain.Template{}, fmt.Errorf("store: get template: %w", err)
	}
	out, err := decorateTemplates(ctx, db, []templateRow{row})
	if err != nil {
		return domain.Template{}, err
	}
	return out[0], nil
}

// ListTemplates returns templates in ID (creation) order after afterID, at
// most limit (0 = all), with icon metadata and latest versions.
func ListTemplates(ctx context.Context, db bun.IDB, afterID string, limit int) ([]domain.Template, error) {
	var rows []templateRow
	q := db.NewSelect().Model(&rows).Order("id ASC")
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list templates: %w", err)
	}
	return decorateTemplates(ctx, db, rows)
}

// decorateTemplates adds icon metadata, version counts and the latest
// version to template rows.
func decorateTemplates(ctx context.Context, db bun.IDB, rows []templateRow) ([]domain.Template, error) {
	out := make([]domain.Template, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	var icons []templateIconRow
	if err := db.NewSelect().Model(&icons).Column("template_id", "media_type", "sha256", "size", "updated_at").
		Where("template_id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list template icons: %w", err)
	}
	iconOf := map[string]*domain.TemplateIcon{}
	for _, i := range icons {
		iconOf[i.TemplateID] = &domain.TemplateIcon{MediaType: i.MediaType, SHA256: i.SHA256, Size: i.Size, UpdatedAt: i.UpdatedAt.UTC()}
	}
	var versions []templateVersionRow
	if err := db.NewSelect().Model(&versions).ExcludeColumn("archive").Where("template_id IN (?)", bun.List(ids)).
		Order("number ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list template versions: %w", err)
	}
	count := map[string]int{}
	latest := map[string]domain.TemplateVersion{}
	for _, v := range versions {
		count[v.TemplateID]++
		latest[v.TemplateID] = v.toDomain()
	}
	for _, r := range rows {
		t := r.toDomain()
		t.Icon, t.Versions = iconOf[r.ID], count[r.ID]
		if v, ok := latest[r.ID]; ok {
			t.Latest = &v
		}
		out = append(out, t)
	}
	return out, nil
}

// PutTemplateIcon replaces a template's icon.
func PutTemplateIcon(ctx context.Context, db bun.IDB, templateID string, icon domain.TemplateIcon, data []byte) error {
	row := templateIconRow{TemplateID: templateID, MediaType: icon.MediaType, SHA256: icon.SHA256, Size: icon.Size, Data: data,
		UpdatedAt: icon.UpdatedAt.UTC()}
	if _, err := db.NewInsert().Model(&row).On("CONFLICT (template_id) DO UPDATE").
		Set("media_type = EXCLUDED.media_type").Set("sha256 = EXCLUDED.sha256").Set("size = EXCLUDED.size").
		Set("data = EXCLUDED.data").Set("updated_at = EXCLUDED.updated_at").Exec(ctx); err != nil {
		return fmt.Errorf("store: put template icon: %w", err)
	}
	return nil
}

// DeleteTemplateIcon removes a template's icon (ErrTemplateIconNotFound
// when it has none).
func DeleteTemplateIcon(ctx context.Context, db bun.IDB, templateID string) error {
	res, err := db.NewDelete().Model((*templateIconRow)(nil)).Where("template_id = ?", templateID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete template icon: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrTemplateIconNotFound
	}
	return nil
}

// TemplateIconData returns a template's icon and its bytes.
func TemplateIconData(ctx context.Context, db bun.IDB, templateID string) (domain.TemplateIcon, []byte, error) {
	var row templateIconRow
	err := db.NewSelect().Model(&row).Where("template_id = ?", templateID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TemplateIcon{}, nil, domain.ErrTemplateIconNotFound
	}
	if err != nil {
		return domain.TemplateIcon{}, nil, fmt.Errorf("store: get template icon: %w", err)
	}
	return domain.TemplateIcon{MediaType: row.MediaType, SHA256: row.SHA256, Size: row.Size, UpdatedAt: row.UpdatedAt.UTC()}, row.Data, nil
}

// NextTemplateVersion reserves the next version number of a template.
func NextTemplateVersion(ctx context.Context, db bun.IDB, templateID string) (int, error) {
	var n int
	err := db.NewRaw("UPDATE templates SET version_seq = version_seq + 1 WHERE id = ? RETURNING version_seq", templateID).Scan(ctx, &n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, domain.ErrTemplateNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("store: next template version: %w", err)
	}
	return n, nil
}

// InsertTemplateVersion stores a published version with its sealed
// archive.
func InsertTemplateVersion(ctx context.Context, db bun.IDB, v *domain.TemplateVersion, sealedArchive string) error {
	files := make([]templateFileJSON, 0, len(v.Definition))
	for _, f := range v.Definition {
		files = append(files, templateFileJSON(f))
	}
	def, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("store: encode template definition: %w", err)
	}
	row := templateVersionRow{ID: v.ID, TemplateID: v.TemplateID, Number: v.Number, Label: v.Label, Notes: v.Notes, Archive: sealedArchive,
		ArchiveSHA256: v.ArchiveSHA256, ArchiveSize: v.ArchiveSize, ContentSize: v.ContentSize, Entries: v.Entries, Definition: string(def),
		PublishedByUserID: v.PublishedByUserID, CreatedAt: v.CreatedAt.UTC()}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "template_versions.template_id, template_versions.label") {
			return domain.ErrTemplateVersionLabelTaken
		}
		return fmt.Errorf("store: insert template version: %w", err)
	}
	return nil
}

// ListTemplateVersions returns a template's versions, newest first.
func ListTemplateVersions(ctx context.Context, db bun.IDB, templateID string) ([]domain.TemplateVersion, error) {
	var rows []templateVersionRow
	if err := db.NewSelect().Model(&rows).ExcludeColumn("archive").Where("template_id = ?", templateID).
		Order("number DESC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list template versions: %w", err)
	}
	out := make([]domain.TemplateVersion, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// GetTemplateVersion returns one version by number (without its archive).
func GetTemplateVersion(ctx context.Context, db bun.IDB, templateID string, number int) (domain.TemplateVersion, error) {
	var row templateVersionRow
	err := db.NewSelect().Model(&row).ExcludeColumn("archive").Where("template_id = ?", templateID).Where("number = ?", number).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TemplateVersion{}, domain.ErrTemplateVersionNotFound
	}
	if err != nil {
		return domain.TemplateVersion{}, fmt.Errorf("store: get template version: %w", err)
	}
	return row.toDomain(), nil
}

// TemplateVersionArchive returns a version's sealed archive.
func TemplateVersionArchive(ctx context.Context, db bun.IDB, templateID string, number int) (string, error) {
	var row templateVersionRow
	err := db.NewSelect().Model(&row).Column("archive").Where("template_id = ?", templateID).Where("number = ?", number).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrTemplateVersionNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: read template archive: %w", err)
	}
	return row.Archive, nil
}

// DeleteTemplateVersion removes one version.
func DeleteTemplateVersion(ctx context.Context, db bun.IDB, templateID string, number int) error {
	res, err := db.NewDelete().Model((*templateVersionRow)(nil)).Where("template_id = ?", templateID).Where("number = ?", number).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete template version: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrTemplateVersionNotFound
	}
	return nil
}

// TemplateIDs returns the IDs of every template (orphaned draft sweeps).
func TemplateIDs(ctx context.Context, db bun.IDB) ([]string, error) {
	var ids []string
	if err := db.NewSelect().Model((*templateRow)(nil)).Column("id").Scan(ctx, &ids); err != nil {
		return nil, fmt.Errorf("store: list template ids: %w", err)
	}
	return ids, nil
}
