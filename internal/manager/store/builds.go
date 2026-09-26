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

// Build definitions and build records (#33).

// sourceDoc is the stored JSON form of a domain.BuildSource.
type sourceDoc struct {
	GitURL                string            `json:"gitUrl"`
	Ref                   string            `json:"ref,omitempty"`
	ContextPath           string            `json:"contextPath,omitempty"`
	Dockerfile            string            `json:"dockerfile,omitempty"`
	Target                string            `json:"target,omitempty"`
	BuildArgs             map[string]string `json:"buildArgs,omitempty"`
	Tags                  []string          `json:"tags"`
	Platform              string            `json:"platform,omitempty"`
	NoCache               bool              `json:"noCache,omitempty"`
	Pull                  bool              `json:"pull,omitempty"`
	GitCredentialID       string            `json:"gitCredentialId,omitempty"`
	RegistryConnectionIDs []string          `json:"registryConnectionIds,omitempty"`
	TimeoutSeconds        int               `json:"timeoutSeconds,omitempty"`
}

func encodeSource(s domain.BuildSource) (string, error) {
	b, err := json.Marshal(sourceDoc(s))
	return string(b), err
}

func decodeSource(s string) (domain.BuildSource, error) {
	var d sourceDoc
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return domain.BuildSource{}, fmt.Errorf("store: build definition source: %w", err)
	}
	return domain.BuildSource(d), nil
}

type buildDefinitionRow struct {
	bun.BaseModel `bun:"table:build_definitions"`

	ID            string    `bun:"id,pk"`
	EnvironmentID string    `bun:"environment_id,notnull"`
	Name          string    `bun:"name,notnull"`
	NameKey       string    `bun:"name_key,notnull"`
	Description   string    `bun:"description,notnull"`
	Source        string    `bun:"source,notnull"`
	LastBuildID   string    `bun:"last_build_id,notnull"`
	Revision      int64     `bun:"revision,notnull"`
	CreatedAt     time.Time `bun:"created_at,notnull"`
	UpdatedAt     time.Time `bun:"updated_at,notnull"`
}

func fromBuildDefinition(d *domain.BuildDefinition) (buildDefinitionRow, error) {
	src, err := encodeSource(d.Source)
	if err != nil {
		return buildDefinitionRow{}, err
	}
	return buildDefinitionRow{ID: d.ID, EnvironmentID: d.EnvironmentID, Name: d.Name, NameKey: NameKey(d.Name), Description: d.Description,
		Source: src, LastBuildID: d.LastBuildID, Revision: d.Revision, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC()}, nil
}

func (r buildDefinitionRow) toDomain() (domain.BuildDefinition, error) {
	src, err := decodeSource(r.Source)
	if err != nil {
		return domain.BuildDefinition{}, err
	}
	return domain.BuildDefinition{ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name, Description: r.Description, Source: src,
		LastBuildID: r.LastBuildID, Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}, nil
}

// InsertBuildDefinition stores a definition.
func InsertBuildDefinition(ctx context.Context, db bun.IDB, d *domain.BuildDefinition) error {
	row, err := fromBuildDefinition(d)
	if err != nil {
		return err
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "build_definitions.environment_id") {
			return domain.ErrBuildDefinitionNameTaken
		}
		return fmt.Errorf("store: insert build definition: %w", err)
	}
	return nil
}

// UpdateBuildDefinition writes d when the revision still matches.
func UpdateBuildDefinition(ctx context.Context, db bun.IDB, d *domain.BuildDefinition, expectRevision int64) error {
	row, err := fromBuildDefinition(d)
	if err != nil {
		return err
	}
	res, err := db.NewUpdate().Model(&row).WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "build_definitions.environment_id") {
			return domain.ErrBuildDefinitionNameTaken
		}
		return fmt.Errorf("store: update build definition: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetBuildDefinition(ctx, db, d.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// SetDefinitionLastBuild records the latest run of a definition (no
// revision change).
func SetDefinitionLastBuild(ctx context.Context, db bun.IDB, id, buildID string) error {
	if _, err := db.NewUpdate().Model((*buildDefinitionRow)(nil)).Set("last_build_id = ?", buildID).Where("id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("store: set last build: %w", err)
	}
	return nil
}

// DeleteBuildDefinition removes a definition when the revision matches.
func DeleteBuildDefinition(ctx context.Context, db bun.IDB, id string, expectRevision int64) error {
	res, err := db.NewDelete().Model((*buildDefinitionRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete build definition: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetBuildDefinition(ctx, db, id); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// GetBuildDefinition returns one definition.
func GetBuildDefinition(ctx context.Context, db bun.IDB, id string) (domain.BuildDefinition, error) {
	var row buildDefinitionRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BuildDefinition{}, domain.ErrBuildDefinitionNotFound
	}
	if err != nil {
		return domain.BuildDefinition{}, fmt.Errorf("store: get build definition: %w", err)
	}
	return row.toDomain()
}

// ListBuildDefinitions returns an environment's definitions in ID order.
func ListBuildDefinitions(ctx context.Context, db bun.IDB, environmentID, afterID string, limit int) ([]domain.BuildDefinition, error) {
	var rows []buildDefinitionRow
	q := db.NewSelect().Model(&rows).Where("environment_id = ?", environmentID).Order("id ASC")
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list build definitions: %w", err)
	}
	out := make([]domain.BuildDefinition, 0, len(rows))
	for _, r := range rows {
		d, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

type imageBuildRow struct {
	bun.BaseModel `bun:"table:image_builds"`

	ID              string     `bun:"id,pk"`
	EnvironmentID   string     `bun:"environment_id,notnull"`
	JobID           string     `bun:"job_id,notnull"`
	DefinitionID    string     `bun:"definition_id,notnull"`
	GitURL          string     `bun:"git_url,notnull"`
	Ref             string     `bun:"ref,notnull"`
	ContextPath     string     `bun:"context_path,notnull"`
	Dockerfile      string     `bun:"dockerfile,notnull"`
	Target          string     `bun:"target,notnull"`
	Tags            string     `bun:"tags,notnull"`
	Platform        string     `bun:"platform,notnull"`
	NoCache         int        `bun:"no_cache,notnull"`
	Pull            int        `bun:"pull,notnull"`
	BuildArgKeys    string     `bun:"build_arg_keys,notnull"`
	GitCredentialID string     `bun:"git_credential_id,notnull"`
	RegistryIDs     string     `bun:"registry_ids,notnull"`
	Status          string     `bun:"status,notnull"`
	ResolvedCommit  string     `bun:"resolved_commit,notnull"`
	ResolvedRef     string     `bun:"resolved_ref,notnull"`
	ImageID         string     `bun:"image_id,notnull"`
	ErrorClass      string     `bun:"error_class,notnull"`
	ErrorMessage    string     `bun:"error_message,notnull"`
	InitiatorUserID string     `bun:"initiator_user_id,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull"`
	StartedAt       *time.Time `bun:"started_at"`
	FinishedAt      *time.Time `bun:"finished_at"`
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func fromImageBuild(b *domain.ImageBuild) imageBuildRow {
	return imageBuildRow{
		ID: b.ID, EnvironmentID: b.EnvironmentID, JobID: b.JobID, DefinitionID: b.DefinitionID, GitURL: b.GitURL, Ref: b.Ref,
		ContextPath: b.ContextPath, Dockerfile: b.Dockerfile, Target: b.Target, Tags: jsonList(b.Tags), Platform: b.Platform,
		NoCache: b2i(b.NoCache), Pull: b2i(b.Pull), BuildArgKeys: jsonList(b.BuildArgKeys), GitCredentialID: b.GitCredentialID,
		RegistryIDs: jsonList(b.RegistryConnectionIDs), Status: string(b.Status), ResolvedCommit: b.ResolvedCommit, ResolvedRef: b.ResolvedRef,
		ImageID: b.ImageID, ErrorClass: b.ErrorClass, ErrorMessage: b.ErrorMessage, InitiatorUserID: b.InitiatorUserID,
		CreatedAt: b.CreatedAt.UTC(), StartedAt: utcPtr(b.StartedAt), FinishedAt: utcPtr(b.FinishedAt),
	}
}

func (r imageBuildRow) toDomain() domain.ImageBuild {
	var tags, keys, regs []string
	_ = json.Unmarshal([]byte(r.Tags), &tags)
	_ = json.Unmarshal([]byte(r.BuildArgKeys), &keys)
	_ = json.Unmarshal([]byte(r.RegistryIDs), &regs)
	return domain.ImageBuild{
		ID: r.ID, EnvironmentID: r.EnvironmentID, JobID: r.JobID, DefinitionID: r.DefinitionID, GitURL: r.GitURL, Ref: r.Ref,
		ContextPath: r.ContextPath, Dockerfile: r.Dockerfile, Target: r.Target, Tags: tags, Platform: r.Platform, NoCache: r.NoCache == 1,
		Pull: r.Pull == 1, BuildArgKeys: keys, GitCredentialID: r.GitCredentialID, RegistryConnectionIDs: regs,
		Status: domain.ImageBuildStatus(r.Status), ResolvedCommit: r.ResolvedCommit, ResolvedRef: r.ResolvedRef, ImageID: r.ImageID,
		ErrorClass: r.ErrorClass, ErrorMessage: r.ErrorMessage, InitiatorUserID: r.InitiatorUserID, CreatedAt: r.CreatedAt.UTC(),
		StartedAt: utcPtr(r.StartedAt), FinishedAt: utcPtr(r.FinishedAt),
	}
}

// InsertImageBuild stores a build record.
func InsertImageBuild(ctx context.Context, db bun.IDB, b *domain.ImageBuild) error {
	row := fromImageBuild(b)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert image build: %w", err)
	}
	return nil
}

// UpdateImageBuildOutcome writes the job-derived outcome columns.
func UpdateImageBuildOutcome(ctx context.Context, db bun.IDB, b *domain.ImageBuild) error {
	row := fromImageBuild(b)
	_, err := db.NewUpdate().Model(&row).Column("status", "resolved_commit", "resolved_ref", "image_id", "error_class", "error_message",
		"started_at", "finished_at").WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update image build: %w", err)
	}
	return nil
}

// GetImageBuild returns one build record.
func GetImageBuild(ctx context.Context, db bun.IDB, id string) (domain.ImageBuild, error) {
	var row imageBuildRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ImageBuild{}, domain.ErrImageBuildNotFound
	}
	if err != nil {
		return domain.ImageBuild{}, fmt.Errorf("store: get image build: %w", err)
	}
	return row.toDomain(), nil
}

// ImageBuildByJob returns the build record of a job.
func ImageBuildByJob(ctx context.Context, db bun.IDB, jobID string) (domain.ImageBuild, error) {
	var row imageBuildRow
	err := db.NewSelect().Model(&row).Where("job_id = ?", jobID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ImageBuild{}, domain.ErrImageBuildNotFound
	}
	if err != nil {
		return domain.ImageBuild{}, fmt.Errorf("store: get image build: %w", err)
	}
	return row.toDomain(), nil
}

// ListImageBuilds returns an environment's builds, newest first (IDs are
// UUIDv7), before beforeID; definitionID filters.
func ListImageBuilds(ctx context.Context, db bun.IDB, environmentID, definitionID, beforeID string, limit int) ([]domain.ImageBuild, error) {
	var rows []imageBuildRow
	q := db.NewSelect().Model(&rows).Where("environment_id = ?", environmentID).Order("id DESC")
	if definitionID != "" {
		q = q.Where("definition_id = ?", definitionID)
	}
	if beforeID != "" {
		q = q.Where("id < ?", beforeID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list image builds: %w", err)
	}
	out := make([]domain.ImageBuild, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}
