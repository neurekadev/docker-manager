package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// The maintenance setup (#14, #238).

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

type maintenanceSettingsRow struct {
	bun.BaseModel `bun:"table:maintenance_settings"`

	Singleton           int       `bun:"singleton,pk"`
	ID                  string    `bun:"id,notnull"`
	Enabled             int       `bun:"enabled,notnull"`
	Cron                string    `bun:"cron,notnull"`
	TimeZone            string    `bun:"time_zone,notnull"`
	Rules               string    `bun:"rules,notnull"`
	ExcludeEnvironments string    `bun:"exclude_environments,notnull"`
	LastRun             string    `bun:"last_run,notnull"`
	Revision            int64     `bun:"revision,notnull"`
	UpdatedAt           time.Time `bun:"updated_at,notnull"`
}

func (r maintenanceSettingsRow) toDomain() (domain.MaintenanceSetup, error) {
	rules, err := decodeRules(r.Rules)
	if err != nil {
		return domain.MaintenanceSetup{}, err
	}
	s := domain.MaintenanceSetup{ID: r.ID, Enabled: r.Enabled == 1, Cron: r.Cron, TimeZone: r.TimeZone, Rules: rules,
		ExcludeEnvironments: []string{}, Revision: r.Revision, UpdatedAt: r.UpdatedAt.UTC()}
	if r.ExcludeEnvironments != "" {
		if err := json.Unmarshal([]byte(r.ExcludeEnvironments), &s.ExcludeEnvironments); err != nil {
			return s, fmt.Errorf("store: maintenance excluded environments: %w", err)
		}
	}
	if r.LastRun != "" {
		var d runDoc
		if err := json.Unmarshal([]byte(r.LastRun), &d); err != nil {
			return s, fmt.Errorf("store: maintenance last run: %w", err)
		}
		s.LastRun = &domain.MaintenanceRunSummary{JobID: d.JobID, State: domain.JobState(d.State), Origin: domain.JobOrigin(d.Origin),
			FinishedAt: d.FinishedAt.UTC(), Removed: d.Removed, Skipped: d.Skipped, Failed: d.Failed, Deferred: d.Deferred,
			BytesReclaimed: d.BytesReclaimed}
	}
	return s, nil
}

// GetMaintenanceSetup returns the maintenance setup (stored rules as
// saved: empty means the shipped suggestions).
func GetMaintenanceSetup(ctx context.Context, db bun.IDB) (domain.MaintenanceSetup, error) {
	var row maintenanceSettingsRow
	if err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx); err != nil {
		return domain.MaintenanceSetup{}, fmt.Errorf("store: get maintenance setup: %w", err)
	}
	return row.toDomain()
}

// UpdateMaintenanceSetup writes s when the revision still matches (the
// ID and the last run summary are left alone).
func UpdateMaintenanceSetup(ctx context.Context, db bun.IDB, s *domain.MaintenanceSetup, expectRevision int64) error {
	rules, err := encodeRules(s.Rules)
	if err != nil {
		return err
	}
	excluded := s.ExcludeEnvironments
	if excluded == nil {
		excluded = []string{}
	}
	ex, err := json.Marshal(excluded)
	if err != nil {
		return err
	}
	res, err := db.NewUpdate().Model((*maintenanceSettingsRow)(nil)).Set("enabled = ?", b2i(s.Enabled)).Set("cron = ?", s.Cron).
		Set("time_zone = ?", s.TimeZone).Set("rules = ?", rules).Set("exclude_environments = ?", string(ex)).
		Set("revision = ?", s.Revision).Set("updated_at = ?", s.UpdatedAt.UTC()).
		Where("singleton = 1").Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update maintenance setup: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionMismatch
	}
	return nil
}

// SetMaintenanceLastRun records the latest finished run of the setup (no
// revision change; a job of another policy ID is ignored).
func SetMaintenanceLastRun(ctx context.Context, db bun.IDB, policyID string, r domain.MaintenanceRunSummary) error {
	b, err := json.Marshal(runDoc{JobID: r.JobID, State: string(r.State), Origin: string(r.Origin), FinishedAt: r.FinishedAt.UTC(),
		Removed: r.Removed, Skipped: r.Skipped, Failed: r.Failed, Deferred: r.Deferred, BytesReclaimed: r.BytesReclaimed})
	if err != nil {
		return err
	}
	if _, err := db.NewUpdate().Model((*maintenanceSettingsRow)(nil)).Set("last_run = ?", string(b)).Where("singleton = 1").
		Where("id = ?", policyID).Exec(ctx); err != nil {
		return fmt.Errorf("store: set maintenance last run: %w", err)
	}
	return nil
}
