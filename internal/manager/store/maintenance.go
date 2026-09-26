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

// Maintenance policies and defaults (#14).

// ruleDoc is the stored JSON form of a domain.MaintenanceRule.
type ruleDoc struct {
	Category         string   `json:"category"`
	Enabled          bool     `json:"enabled"`
	MinAgeSeconds    int64    `json:"minAgeSeconds"`
	IncludeLabels    []string `json:"includeLabels,omitempty"`
	ExcludeLabels    []string `json:"excludeLabels,omitempty"`
	Exclude          []string `json:"exclude,omitempty"`
	ContainerStates  []string `json:"containerStates,omitempty"`
	BuildCacheAll    bool     `json:"buildCacheAll,omitempty"`
	KeepStorageBytes int64    `json:"keepStorageBytes,omitempty"`
	VolumeOptIn      bool     `json:"volumeOptIn,omitempty"`
}

func encodeRules(rules []domain.MaintenanceRule) (string, error) {
	docs := make([]ruleDoc, 0, len(rules))
	for _, r := range rules {
		docs = append(docs, ruleDoc{Category: r.Category, Enabled: r.Enabled, MinAgeSeconds: int64(r.MinAge / time.Second),
			IncludeLabels: r.IncludeLabels, ExcludeLabels: r.ExcludeLabels, Exclude: r.Exclude, ContainerStates: r.ContainerStates,
			BuildCacheAll: r.BuildCacheAll, KeepStorageBytes: r.KeepStorageBytes, VolumeOptIn: r.VolumeOptIn})
	}
	b, err := json.Marshal(docs)
	return string(b), err
}

func decodeRules(s string) ([]domain.MaintenanceRule, error) {
	if s == "" {
		return nil, nil
	}
	var docs []ruleDoc
	if err := json.Unmarshal([]byte(s), &docs); err != nil {
		return nil, fmt.Errorf("store: maintenance rules: %w", err)
	}
	out := make([]domain.MaintenanceRule, 0, len(docs))
	for _, d := range docs {
		out = append(out, domain.MaintenanceRule{Category: d.Category, Enabled: d.Enabled, MinAge: time.Duration(d.MinAgeSeconds) * time.Second,
			IncludeLabels: d.IncludeLabels, ExcludeLabels: d.ExcludeLabels, Exclude: d.Exclude, ContainerStates: d.ContainerStates,
			BuildCacheAll: d.BuildCacheAll, KeepStorageBytes: d.KeepStorageBytes, VolumeOptIn: d.VolumeOptIn})
	}
	return out, nil
}

// runDoc is the stored JSON form of a domain.MaintenanceRunSummary.
type runDoc struct {
	JobID          string    `json:"jobId"`
	State          string    `json:"state"`
	Origin         string    `json:"origin"`
	FinishedAt     time.Time `json:"finishedAt"`
	Removed        int       `json:"removed"`
	Skipped        int       `json:"skipped"`
	Failed         int       `json:"failed"`
	Deferred       int       `json:"deferred"`
	BytesReclaimed int64     `json:"bytesReclaimed"`
}

type maintenancePolicyRow struct {
	bun.BaseModel `bun:"table:maintenance_policies"`

	ID              string    `bun:"id,pk"`
	EnvironmentID   string    `bun:"environment_id,notnull"`
	Name            string    `bun:"name,notnull"`
	NameKey         string    `bun:"name_key,notnull"`
	Description     string    `bun:"description,notnull"`
	Cron            string    `bun:"cron,notnull"`
	TimeZone        string    `bun:"time_zone,notnull"`
	ScheduleEnabled int       `bun:"schedule_enabled,notnull"`
	Rules           string    `bun:"rules,notnull"`
	LastRun         string    `bun:"last_run,notnull"`
	Revision        int64     `bun:"revision,notnull"`
	CreatedAt       time.Time `bun:"created_at,notnull"`
	UpdatedAt       time.Time `bun:"updated_at,notnull"`
}

func fromMaintenancePolicy(p *domain.MaintenancePolicy) (maintenancePolicyRow, error) {
	rules, err := encodeRules(p.Rules)
	if err != nil {
		return maintenancePolicyRow{}, err
	}
	row := maintenancePolicyRow{ID: p.ID, EnvironmentID: p.EnvironmentID, Name: p.Name, NameKey: NameKey(p.Name), Description: p.Description,
		Cron: p.Cron, TimeZone: p.TimeZone, ScheduleEnabled: b2i(p.ScheduleEnabled), Rules: rules, Revision: p.Revision,
		CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC()}
	if r := p.LastRun; r != nil {
		b, err := json.Marshal(runDoc{JobID: r.JobID, State: string(r.State), Origin: string(r.Origin), FinishedAt: r.FinishedAt.UTC(),
			Removed: r.Removed, Skipped: r.Skipped, Failed: r.Failed, Deferred: r.Deferred, BytesReclaimed: r.BytesReclaimed})
		if err != nil {
			return row, err
		}
		row.LastRun = string(b)
	}
	return row, nil
}

func (r maintenancePolicyRow) toDomain() (domain.MaintenancePolicy, error) {
	rules, err := decodeRules(r.Rules)
	if err != nil {
		return domain.MaintenancePolicy{}, err
	}
	p := domain.MaintenancePolicy{ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name, Description: r.Description, Cron: r.Cron,
		TimeZone: r.TimeZone, ScheduleEnabled: r.ScheduleEnabled == 1, Rules: rules, Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC()}
	if r.LastRun != "" {
		var d runDoc
		if err := json.Unmarshal([]byte(r.LastRun), &d); err != nil {
			return p, fmt.Errorf("store: maintenance last run: %w", err)
		}
		p.LastRun = &domain.MaintenanceRunSummary{JobID: d.JobID, State: domain.JobState(d.State), Origin: domain.JobOrigin(d.Origin),
			FinishedAt: d.FinishedAt.UTC(), Removed: d.Removed, Skipped: d.Skipped, Failed: d.Failed, Deferred: d.Deferred,
			BytesReclaimed: d.BytesReclaimed}
	}
	return p, nil
}

// InsertMaintenancePolicy stores a policy.
func InsertMaintenancePolicy(ctx context.Context, db bun.IDB, p *domain.MaintenancePolicy) error {
	row, err := fromMaintenancePolicy(p)
	if err != nil {
		return err
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "maintenance_policy_scope") || uniqueViolation(err, "maintenance_policies.environment_id") {
			return domain.ErrMaintenanceScopeOverlap
		}
		if uniqueViolation(err, "maintenance_policies.environment_id") {
			return domain.ErrMaintenancePolicyNameTaken
		}
		return fmt.Errorf("store: insert maintenance policy: %w", err)
	}
	return nil
}

// UpdateMaintenancePolicy writes p when the revision still matches (the
// last run summary is left alone).
func UpdateMaintenancePolicy(ctx context.Context, db bun.IDB, p *domain.MaintenancePolicy, expectRevision int64) error {
	row, err := fromMaintenancePolicy(p)
	if err != nil {
		return err
	}
	res, err := db.NewUpdate().Model(&row).
		Column("name", "name_key", "description", "cron", "time_zone", "schedule_enabled", "rules", "revision", "updated_at").
		WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "maintenance_policies.environment_id") {
			return domain.ErrMaintenancePolicyNameTaken
		}
		return fmt.Errorf("store: update maintenance policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetMaintenancePolicy(ctx, db, p.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// SetMaintenanceLastRun records the latest finished run of a policy (no
// revision change; a deleted policy is ignored).
func SetMaintenanceLastRun(ctx context.Context, db bun.IDB, policyID string, r domain.MaintenanceRunSummary) error {
	b, err := json.Marshal(runDoc{JobID: r.JobID, State: string(r.State), Origin: string(r.Origin), FinishedAt: r.FinishedAt.UTC(),
		Removed: r.Removed, Skipped: r.Skipped, Failed: r.Failed, Deferred: r.Deferred, BytesReclaimed: r.BytesReclaimed})
	if err != nil {
		return err
	}
	if _, err := db.NewUpdate().Model((*maintenancePolicyRow)(nil)).Set("last_run = ?", string(b)).Where("id = ?", policyID).Exec(ctx); err != nil {
		return fmt.Errorf("store: set maintenance last run: %w", err)
	}
	return nil
}

// DeleteMaintenancePolicy removes a policy when the revision matches.
func DeleteMaintenancePolicy(ctx context.Context, db bun.IDB, id string, expectRevision int64) error {
	res, err := db.NewDelete().Model((*maintenancePolicyRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete maintenance policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetMaintenancePolicy(ctx, db, id); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// GetMaintenancePolicy returns one policy.
func GetMaintenancePolicy(ctx context.Context, db bun.IDB, id string) (domain.MaintenancePolicy, error) {
	var row maintenancePolicyRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MaintenancePolicy{}, domain.ErrMaintenancePolicyNotFound
	}
	if err != nil {
		return domain.MaintenancePolicy{}, fmt.Errorf("store: get maintenance policy: %w", err)
	}
	return row.toDomain()
}

// ListMaintenancePolicies returns policies in ID order (every environment
// when environmentID is empty; limit <= 0: all).
func ListMaintenancePolicies(ctx context.Context, db bun.IDB, environmentID, afterID string, limit int) ([]domain.MaintenancePolicy, error) {
	var rows []maintenancePolicyRow
	q := db.NewSelect().Model(&rows).Order("id ASC")
	if environmentID != "" {
		q = q.Where("(environment_id = ? OR environment_id = '')", environmentID)
	}
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list maintenance policies: %w", err)
	}
	out := make([]domain.MaintenancePolicy, 0, len(rows))
	for _, r := range rows {
		p, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

type maintenanceDefaultsRow struct {
	bun.BaseModel `bun:"table:maintenance_defaults"`

	Singleton int       `bun:"singleton,pk"`
	Rules     string    `bun:"rules,notnull"`
	Revision  int64     `bun:"revision,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

// GetMaintenanceDefaults returns the stored default rules (nil: the
// shipped suggestions).
func GetMaintenanceDefaults(ctx context.Context, db bun.IDB) (domain.MaintenanceDefaults, error) {
	var row maintenanceDefaultsRow
	if err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx); err != nil {
		return domain.MaintenanceDefaults{}, fmt.Errorf("store: get maintenance defaults: %w", err)
	}
	rules, err := decodeRules(row.Rules)
	if err != nil {
		return domain.MaintenanceDefaults{}, err
	}
	return domain.MaintenanceDefaults{Rules: rules, Revision: row.Revision, UpdatedAt: row.UpdatedAt.UTC()}, nil
}

// UpdateMaintenanceDefaults replaces the default rules when the revision
// matches.
func UpdateMaintenanceDefaults(ctx context.Context, db bun.IDB, rules []domain.MaintenanceRule, expectRevision int64, now time.Time) error {
	enc, err := encodeRules(rules)
	if err != nil {
		return err
	}
	res, err := db.NewUpdate().Model((*maintenanceDefaultsRow)(nil)).Set("rules = ?", enc).Set("revision = revision + 1").
		Set("updated_at = ?", now.UTC()).Where("singleton = 1").Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update maintenance defaults: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionMismatch
	}
	return nil
}
