package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// The updates setup (#20, #240): one row.

type updateSettingsRow struct {
	bun.BaseModel       `bun:"table:update_settings"`
	Singleton           int       `bun:"singleton,pk"`
	ID                  string    `bun:"id,notnull"`
	ExcludeEnvironments string    `bun:"exclude_environments,notnull"`
	ExcludeStacks       string    `bun:"exclude_stacks,notnull"`
	ExcludeContainers   string    `bun:"exclude_containers,notnull"`
	CheckCron           string    `bun:"check_cron,notnull"`
	CheckTimeZone       string    `bun:"check_time_zone,notnull"`
	CheckEnabled        int       `bun:"check_enabled,notnull"`
	RunCron             string    `bun:"run_cron,notnull"`
	RunTimeZone         string    `bun:"run_time_zone,notnull"`
	RunEnabled          int       `bun:"run_enabled,notnull"`
	RunWindow           string    `bun:"run_window,notnull"`
	WaitTimeoutSeconds  int       `bun:"wait_timeout_seconds,notnull"`
	Revision            int64     `bun:"revision,notnull"`
	UpdatedAt           time.Time `bun:"updated_at,notnull"`
}

func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (r updateSettingsRow) toDomain() (domain.UpdateSetup, error) {
	s := domain.UpdateSetup{ID: r.ID,
		Check:              domain.UpdateSchedule{Cron: r.CheckCron, TimeZone: r.CheckTimeZone, Enabled: r.CheckEnabled == 1},
		Run:                domain.UpdateSchedule{Cron: r.RunCron, TimeZone: r.RunTimeZone, Enabled: r.RunEnabled == 1},
		WaitTimeoutSeconds: r.WaitTimeoutSeconds, Revision: r.Revision, UpdatedAt: r.UpdatedAt.UTC()}
	for _, l := range []struct {
		raw string
		to  *[]string
	}{{r.ExcludeEnvironments, &s.ExcludeEnvironments}, {r.ExcludeStacks, &s.ExcludeStacks}, {r.ExcludeContainers, &s.ExcludeContainers}} {
		if err := json.Unmarshal([]byte(l.raw), l.to); err != nil {
			return s, fmt.Errorf("store: update settings exclusions: %w", err)
		}
		*l.to = orEmpty(*l.to)
	}
	if r.RunWindow != "" {
		var w windowDoc
		if err := json.Unmarshal([]byte(r.RunWindow), &w); err != nil {
			return s, fmt.Errorf("store: update settings window: %w", err)
		}
		s.Window = &domain.UpdateWindow{Days: w.Days, Start: w.Start, End: w.End}
	}
	return s, nil
}

// GetUpdateSetup returns the updates setup.
func GetUpdateSetup(ctx context.Context, db bun.IDB) (domain.UpdateSetup, error) {
	var row updateSettingsRow
	if err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx); err != nil {
		return domain.UpdateSetup{}, fmt.Errorf("store: get update setup: %w", err)
	}
	return row.toDomain()
}

// UpdateUpdateSetup writes s (all but its ID) when the revision still
// matches.
func UpdateUpdateSetup(ctx context.Context, db bun.IDB, s domain.UpdateSetup, expectRevision int64) error {
	window := ""
	if s.Window != nil {
		window = mustJSON(windowDoc{Days: s.Window.Days, Start: s.Window.Start, End: s.Window.End})
	}
	res, err := db.NewUpdate().Model((*updateSettingsRow)(nil)).
		Set("exclude_environments = ?", mustJSON(orEmpty(s.ExcludeEnvironments))).
		Set("exclude_stacks = ?", mustJSON(orEmpty(s.ExcludeStacks))).
		Set("exclude_containers = ?", mustJSON(orEmpty(s.ExcludeContainers))).
		Set("check_cron = ?", s.Check.Cron).Set("check_time_zone = ?", s.Check.TimeZone).Set("check_enabled = ?", b2i(s.Check.Enabled)).
		Set("run_cron = ?", s.Run.Cron).Set("run_time_zone = ?", s.Run.TimeZone).Set("run_enabled = ?", b2i(s.Run.Enabled)).
		Set("run_window = ?", window).Set("wait_timeout_seconds = ?", s.WaitTimeoutSeconds).
		Set("revision = ?", s.Revision).Set("updated_at = ?", s.UpdatedAt.UTC()).
		Where("singleton = 1").Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update update setup: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionMismatch
	}
	return nil
}
