package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Stack and stack revision persistence (#7). internal/manager/stacks owns
// every change; revision contents arrive sealed (secrets.Keyring) and are
// stored opaquely.

type stackRow struct {
	bun.BaseModel `bun:"table:stacks"`

	ID                 string     `bun:"id,pk"`
	EnvironmentID      string     `bun:"environment_id,notnull"`
	Name               string     `bun:"name,notnull"`
	DisplayName        string     `bun:"display_name,notnull"`
	Description        string     `bun:"description,notnull"`
	Icon               string     `bun:"icon,notnull"`
	ServiceMeta        string     `bun:"service_meta,notnull"`
	Root               string     `bun:"root,notnull"`
	RootPath           string     `bun:"root_path,notnull"`
	Dir                string     `bun:"dir,notnull"`
	ConfigFiles        string     `bun:"config_files,notnull"`
	EnvFiles           string     `bun:"env_files,notnull"`
	Origin             string     `bun:"origin,notnull"`
	Status             string     `bun:"status,notnull"`
	AppliedRevisionID  string     `bun:"applied_revision_id,notnull"`
	AppliedSeq         int64      `bun:"applied_seq,notnull"`
	AppliedHash        string     `bun:"applied_hash,notnull"`
	AppliedAt          *time.Time `bun:"applied_at,nullzero"`
	ObservedRevisionID string     `bun:"observed_revision_id,notnull"`
	ObservedSeq        int64      `bun:"observed_seq,notnull"`
	ObservedHash       string     `bun:"observed_hash,notnull"`
	ObservedAt         *time.Time `bun:"observed_at,nullzero"`
	FailedRevisionID   string     `bun:"failed_revision_id,notnull"`
	FailedSeq          int64      `bun:"failed_seq,notnull"`
	FailedHash         string     `bun:"failed_hash,notnull"`
	Images             string     `bun:"images,notnull"`
	Services           string     `bun:"services,notnull"`
	Binds              string     `bun:"binds,notnull"`
	PreviousState      string     `bun:"previous_state,notnull"`
	EngineState        string     `bun:"engine_state,notnull"`
	EngineServices     string     `bun:"engine_services,notnull"`
	EngineObservedAt   *time.Time `bun:"engine_observed_at,nullzero"`
	LastJobID          string     `bun:"last_job_id,notnull"`
	LastJobKind        string     `bun:"last_job_kind,notnull"`
	// The template version the stack was created from ('' / 0: none).
	TemplateInstanceID   string    `bun:"template_instance_id,notnull"`
	TemplateID           string    `bun:"template_id,notnull"`
	TemplateName         string    `bun:"template_name,notnull"`
	TemplateVersion      int       `bun:"template_version,notnull"`
	TemplateVersionLabel string    `bun:"template_version_label,notnull"`
	Revision             int64     `bun:"revision,notnull"`
	CreatedAt            time.Time `bun:"created_at,notnull"`
	UpdatedAt            time.Time `bun:"updated_at,notnull"`
}

// JSON column shapes (stable storage format, independent of domain field
// names).
type metaJSON struct {
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

type dependencyJSON struct {
	Service   string `json:"service"`
	Condition string `json:"condition"`
	Required  bool   `json:"required"`
	Restart   bool   `json:"restart,omitempty"`
}

type serviceDefJSON struct {
	Name       string           `json:"name"`
	Image      string           `json:"image"`
	Build      bool             `json:"build,omitempty"`
	DependsOn  []dependencyJSON `json:"dependsOn,omitempty"`
	PullPolicy string           `json:"pullPolicy,omitempty"`
}

type imageJSON struct {
	Service       string     `json:"service"`
	Image         string     `json:"image"`
	ImageID       string     `json:"imageId,omitempty"`
	Digest        string     `json:"digest,omitempty"`
	Platform      string     `json:"platform,omitempty"`
	Build         bool       `json:"build,omitempty"`
	PulledImageID string     `json:"pulledImageId,omitempty"`
	PulledDigest  string     `json:"pulledDigest,omitempty"`
	PulledAt      *time.Time `json:"pulledAt,omitempty"`
}

type bindJSON struct {
	Service  string `json:"service"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	RelPath  string `json:"relPath,omitempty"`
	External bool   `json:"external,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

type serviceStateJSON struct {
	Service    string   `json:"service"`
	Containers int      `json:"containers"`
	Running    int      `json:"running"`
	ImageIDs   []string `json:"imageIds,omitempty"`
}

type stackFileJSON struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func refOf(id string, seq int64, hash string) *domain.RevisionRef {
	if id == "" {
		return nil
	}
	return &domain.RevisionRef{ID: id, Seq: seq, Hash: hash}
}

func refFields(r *domain.RevisionRef) (string, int64, string) {
	if r == nil {
		return "", 0, ""
	}
	return r.ID, r.Seq, r.Hash
}

func fromStack(s *domain.Stack) stackRow {
	meta := map[string]metaJSON{}
	for k, v := range s.ServiceMeta {
		meta[k] = metaJSON(v)
	}
	services := make([]serviceDefJSON, 0, len(s.Services))
	for _, sv := range s.Services {
		d := serviceDefJSON{Name: sv.Name, Image: sv.Image, Build: sv.Build, PullPolicy: sv.PullPolicy}
		for _, dep := range sv.DependsOn {
			d.DependsOn = append(d.DependsOn, dependencyJSON(dep))
		}
		services = append(services, d)
	}
	images := make([]imageJSON, 0, len(s.Images))
	for _, i := range s.Images {
		images = append(images, imageJSON(i))
	}
	binds := make([]bindJSON, 0, len(s.Binds))
	for _, b := range s.Binds {
		binds = append(binds, bindJSON(b))
	}
	r := stackRow{
		ID: s.ID, EnvironmentID: s.EnvironmentID, Name: s.Name, DisplayName: s.DisplayName, Description: s.Meta.Description,
		Icon: s.Meta.Icon, ServiceMeta: mustJSON(meta), Root: s.Root, RootPath: s.RootPath, Dir: s.Dir,
		ConfigFiles: mustJSON(nonNil(s.ConfigFiles)), EnvFiles: mustJSON(nonNil(s.EnvFiles)), Origin: s.Origin,
		Status: string(s.Status), AppliedAt: utcPtr(s.AppliedAt), ObservedAt: utcPtr(s.ObservedAt),
		Images: mustJSON(images), Services: mustJSON(services), Binds: mustJSON(binds),
		PreviousState: mustJSON(statesJSON(s.PreviousState)), EngineState: string(s.EngineState),
		EngineServices: mustJSON(statesJSON(s.EngineServices)), EngineObservedAt: utcPtr(s.EngineObservedAt),
		LastJobID: s.LastJobID, LastJobKind: string(s.LastJobKind), Revision: s.Revision,
		CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC(),
	}
	if r.EngineState == "" {
		r.EngineState = string(domain.EngineStateUnknown)
	}
	r.AppliedRevisionID, r.AppliedSeq, r.AppliedHash = refFields(s.Applied)
	r.ObservedRevisionID, r.ObservedSeq, r.ObservedHash = refFields(s.Observed)
	r.FailedRevisionID, r.FailedSeq, r.FailedHash = refFields(s.Failed)
	if t := s.Template; t != nil {
		r.TemplateInstanceID, r.TemplateID, r.TemplateName = t.InstanceID, t.TemplateID, t.Name
		r.TemplateVersion, r.TemplateVersionLabel = t.Version, t.VersionLabel
	}
	return r
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func statesJSON(states []domain.StackServiceState) []serviceStateJSON {
	out := make([]serviceStateJSON, 0, len(states))
	for _, s := range states {
		out = append(out, serviceStateJSON(s))
	}
	return out
}

func statesOf(raw string) []domain.StackServiceState {
	var in []serviceStateJSON
	_ = json.Unmarshal([]byte(raw), &in)
	var out []domain.StackServiceState
	for _, s := range in {
		out = append(out, domain.StackServiceState(s))
	}
	return out
}

func (r stackRow) toDomain() domain.Stack {
	s := domain.Stack{
		ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name, DisplayName: r.DisplayName,
		Meta: domain.DisplayMeta{Description: r.Description, Icon: r.Icon}, ServiceMeta: map[string]domain.DisplayMeta{},
		Root: r.Root, RootPath: r.RootPath, Dir: r.Dir, Origin: r.Origin, Status: domain.StackDeploymentStatus(r.Status),
		Applied: refOf(r.AppliedRevisionID, r.AppliedSeq, r.AppliedHash), AppliedAt: utcPtr(r.AppliedAt),
		Observed: refOf(r.ObservedRevisionID, r.ObservedSeq, r.ObservedHash), ObservedAt: utcPtr(r.ObservedAt),
		Failed:        refOf(r.FailedRevisionID, r.FailedSeq, r.FailedHash),
		PreviousState: statesOf(r.PreviousState), EngineState: domain.StackEngineState(r.EngineState),
		EngineServices: statesOf(r.EngineServices), EngineObservedAt: utcPtr(r.EngineObservedAt),
		LastJobID: r.LastJobID, LastJobKind: domain.JobKind(r.LastJobKind), Revision: r.Revision,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if r.TemplateID != "" {
		s.Template = &domain.StackTemplateRef{InstanceID: r.TemplateInstanceID, TemplateID: r.TemplateID, Name: r.TemplateName,
			Version: r.TemplateVersion, VersionLabel: r.TemplateVersionLabel}
	}
	var meta map[string]metaJSON
	_ = json.Unmarshal([]byte(r.ServiceMeta), &meta)
	for k, v := range meta {
		s.ServiceMeta[k] = domain.DisplayMeta(v)
	}
	_ = json.Unmarshal([]byte(r.ConfigFiles), &s.ConfigFiles)
	_ = json.Unmarshal([]byte(r.EnvFiles), &s.EnvFiles)
	var services []serviceDefJSON
	_ = json.Unmarshal([]byte(r.Services), &services)
	for _, sv := range services {
		d := domain.StackServiceDef{Name: sv.Name, Image: sv.Image, Build: sv.Build, PullPolicy: sv.PullPolicy}
		for _, dep := range sv.DependsOn {
			d.DependsOn = append(d.DependsOn, domain.StackDependency(dep))
		}
		s.Services = append(s.Services, d)
	}
	var images []imageJSON
	_ = json.Unmarshal([]byte(r.Images), &images)
	for _, i := range images {
		s.Images = append(s.Images, domain.StackImage(i))
	}
	var binds []bindJSON
	_ = json.Unmarshal([]byte(r.Binds), &binds)
	for _, b := range binds {
		s.Binds = append(s.Binds, domain.StackBind(b))
	}
	return s
}

// stackConflict maps unique violations of the stacks table.
func stackConflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: stacks.") {
		return domain.ErrStackNameTaken
	}
	return err
}

// InsertStack stores a new stack (ErrStackNameTaken when the environment
// already has a stack with its project name or directory).
func InsertStack(ctx context.Context, db bun.IDB, s *domain.Stack) error {
	r := fromStack(s)
	if _, err := db.NewInsert().Model(&r).Exec(ctx); err != nil {
		if errors.Is(stackConflict(err), domain.ErrStackNameTaken) {
			return domain.ErrStackNameTaken
		}
		return fmt.Errorf("store: insert stack: %w", err)
	}
	return nil
}

// UpdateStack replaces a stack's row.
func UpdateStack(ctx context.Context, db bun.IDB, s *domain.Stack) error {
	r := fromStack(s)
	res, err := db.NewUpdate().Model(&r).WherePK().Exec(ctx)
	if err != nil {
		// A stack moved to another environment (#35) may collide there.
		if errors.Is(stackConflict(err), domain.ErrStackNameTaken) {
			return domain.ErrStackNameTaken
		}
		return fmt.Errorf("store: update stack: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrStackNotFound
	}
	return nil
}

// DeleteStack deletes a stack and its revisions.
func DeleteStack(ctx context.Context, db bun.IDB, id string) error {
	if _, err := db.NewDelete().TableExpr("stacks").Where("id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("store: delete stack: %w", err)
	}
	return nil
}

// GetStack returns a stack.
func GetStack(ctx context.Context, db bun.IDB, id string) (domain.Stack, error) {
	var r stackRow
	err := db.NewSelect().Model(&r).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Stack{}, domain.ErrStackNotFound
	}
	if err != nil {
		return domain.Stack{}, fmt.Errorf("store: get stack: %w", err)
	}
	return r.toDomain(), nil
}

// FindStackByName returns the stack with a project name in an environment.
func FindStackByName(ctx context.Context, db bun.IDB, environmentID, name string) (domain.Stack, error) {
	var r stackRow
	err := db.NewSelect().Model(&r).Where("environment_id = ? AND name = ?", environmentID, name).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Stack{}, domain.ErrStackNotFound
	}
	if err != nil {
		return domain.Stack{}, fmt.Errorf("store: find stack: %w", err)
	}
	return r.toDomain(), nil
}

// ListStacks returns stacks in ID order after f.AfterID.
func ListStacks(ctx context.Context, db bun.IDB, f domain.StackFilter) ([]domain.Stack, error) {
	var rows []stackRow
	q := db.NewSelect().Model(&rows).Order("id ASC")
	if f.EnvironmentID != "" {
		q = q.Where("environment_id = ?", f.EnvironmentID)
	}
	if f.AfterID != "" {
		q = q.Where("id > ?", f.AfterID)
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list stacks: %w", err)
	}
	out := make([]domain.Stack, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

type stackRevisionRow struct {
	bun.BaseModel `bun:"table:stack_revisions"`

	ID             string    `bun:"id,pk"`
	StackID        string    `bun:"stack_id,notnull"`
	Seq            int64     `bun:"seq,notnull"`
	Hash           string    `bun:"hash,notnull"`
	Source         string    `bun:"source,notnull"`
	AuthorUserID   string    `bun:"author_user_id,notnull"`
	AuthorTokenID  string    `bun:"author_token_id,notnull"`
	JobID          string    `bun:"job_id,notnull"`
	RestoredFrom   string    `bun:"restored_from,notnull"`
	Files          string    `bun:"files,notnull"`
	Content        string    `bun:"content,notnull"`
	ContentOmitted int       `bun:"content_omitted,notnull"`
	CreatedAt      time.Time `bun:"created_at,notnull"`
}

func (r stackRevisionRow) toDomain() domain.StackRevision {
	rev := domain.StackRevision{ID: r.ID, StackID: r.StackID, Seq: r.Seq, Hash: r.Hash, Source: domain.RevisionSource(r.Source),
		AuthorUserID: r.AuthorUserID, AuthorTokenID: r.AuthorTokenID, JobID: r.JobID, RestoredFrom: r.RestoredFrom,
		ContentOmitted: r.ContentOmitted == 1, CreatedAt: r.CreatedAt.UTC()}
	var files []stackFileJSON
	_ = json.Unmarshal([]byte(r.Files), &files)
	for _, f := range files {
		rev.Files = append(rev.Files, domain.StackFile{Path: f.Path, SHA256: f.SHA256, Size: f.Size})
	}
	return rev
}

// NextStackRevisionSeq returns the next revision number of a stack.
func NextStackRevisionSeq(ctx context.Context, db bun.IDB, stackID string) (int64, error) {
	var maxSeq sql.NullInt64
	if err := db.NewSelect().TableExpr("stack_revisions").ColumnExpr("MAX(seq)").Where("stack_id = ?", stackID).Scan(ctx, &maxSeq); err != nil {
		return 0, fmt.Errorf("store: next revision: %w", err)
	}
	return maxSeq.Int64 + 1, nil
}

// InsertStackRevision stores an immutable revision; sealed is its sealed
// content ("" when not captured).
func InsertStackRevision(ctx context.Context, db bun.IDB, rev *domain.StackRevision, sealed string) error {
	files := make([]stackFileJSON, 0, len(rev.Files))
	for _, f := range rev.Files {
		files = append(files, stackFileJSON{Path: f.Path, SHA256: f.SHA256, Size: f.Size})
	}
	r := stackRevisionRow{ID: rev.ID, StackID: rev.StackID, Seq: rev.Seq, Hash: rev.Hash, Source: string(rev.Source),
		AuthorUserID: rev.AuthorUserID, AuthorTokenID: rev.AuthorTokenID, JobID: rev.JobID, RestoredFrom: rev.RestoredFrom,
		Files: mustJSON(files), Content: sealed, ContentOmitted: b2i(rev.ContentOmitted), CreatedAt: rev.CreatedAt.UTC()}
	if _, err := db.NewInsert().Model(&r).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert stack revision: %w", err)
	}
	return nil
}

// GetStackRevision returns a revision of a stack and its sealed content.
func GetStackRevision(ctx context.Context, db bun.IDB, stackID, id string) (domain.StackRevision, string, error) {
	var r stackRevisionRow
	err := db.NewSelect().Model(&r).Where("stack_id = ? AND id = ?", stackID, id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.StackRevision{}, "", domain.ErrStackRevisionNotFound
	}
	if err != nil {
		return domain.StackRevision{}, "", fmt.Errorf("store: get stack revision: %w", err)
	}
	return r.toDomain(), r.Content, nil
}

// FindStackRevisionByHash returns the newest revision of a stack with a
// hash whose content was captured, and its sealed content.
func FindStackRevisionByHash(ctx context.Context, db bun.IDB, stackID, hash string) (domain.StackRevision, string, error) {
	var r stackRevisionRow
	err := db.NewSelect().Model(&r).Where("stack_id = ? AND hash = ? AND content_omitted = 0", stackID, hash).
		Order("seq DESC").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.StackRevision{}, "", domain.ErrStackRevisionNotFound
	}
	if err != nil {
		return domain.StackRevision{}, "", fmt.Errorf("store: find stack revision: %w", err)
	}
	return r.toDomain(), r.Content, nil
}

// ListStackRevisions returns a stack's revisions, newest first, with
// sequence numbers below beforeSeq (0: from the newest).
func ListStackRevisions(ctx context.Context, db bun.IDB, stackID string, beforeSeq int64, limit int) ([]domain.StackRevision, error) {
	var rows []stackRevisionRow
	q := db.NewSelect().Model(&rows).Where("stack_id = ?", stackID).Order("seq DESC")
	if beforeSeq > 0 {
		q = q.Where("seq < ?", beforeSeq)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list stack revisions: %w", err)
	}
	out := make([]domain.StackRevision, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}
