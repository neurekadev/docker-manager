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

type environmentUpdatePolicyRow struct {
	bun.BaseModel      `bun:"table:environment_update_policies"`
	ID                 string    `bun:"id,pk"`
	EnvironmentID      string    `bun:"environment_id,notnull"`
	Name               string    `bun:"name,notnull"`
	ExcludeStacks      string    `bun:"exclude_stacks,notnull"`
	ExcludeContainers  string    `bun:"exclude_containers,notnull"`
	CheckCron          string    `bun:"check_cron,notnull"`
	CheckTimeZone      string    `bun:"check_time_zone,notnull"`
	CheckEnabled       int       `bun:"check_enabled,notnull"`
	RunCron            string    `bun:"run_cron,notnull"`
	RunTimeZone        string    `bun:"run_time_zone,notnull"`
	RunEnabled         int       `bun:"run_enabled,notnull"`
	RunWindow          string    `bun:"run_window,notnull"`
	WaitTimeoutSeconds int       `bun:"wait_timeout_seconds,notnull"`
	Revision           int64     `bun:"revision,notnull"`
	CreatedAt          time.Time `bun:"created_at,notnull"`
	UpdatedAt          time.Time `bun:"updated_at,notnull"`
}

func environmentUpdateRow(p domain.EnvironmentUpdatePolicy) environmentUpdatePolicyRow {
	stacks, containers := p.ExcludeStacks, p.ExcludeContainers
	if stacks == nil {
		stacks = []string{}
	}
	if containers == nil {
		containers = []string{}
	}
	window := ""
	if p.Window != nil {
		window = mustJSON(windowDoc{Days: p.Window.Days, Start: p.Window.Start, End: p.Window.End})
	}
	return environmentUpdatePolicyRow{ID: p.ID, EnvironmentID: p.EnvironmentID, Name: p.Name,
		ExcludeStacks: mustJSON(stacks), ExcludeContainers: mustJSON(containers),
		CheckCron: p.Check.Cron, CheckTimeZone: p.Check.TimeZone, CheckEnabled: b2i(p.Check.Enabled),
		RunCron: p.Run.Cron, RunTimeZone: p.Run.TimeZone, RunEnabled: b2i(p.Run.Enabled),
		RunWindow: window, WaitTimeoutSeconds: p.WaitTimeoutSeconds, Revision: p.Revision,
		CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC()}
}

func (r environmentUpdatePolicyRow) toDomain() domain.EnvironmentUpdatePolicy {
	p := domain.EnvironmentUpdatePolicy{ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name,
		Check:              domain.UpdateSchedule{Cron: r.CheckCron, TimeZone: r.CheckTimeZone, Enabled: r.CheckEnabled == 1},
		Run:                domain.UpdateSchedule{Cron: r.RunCron, TimeZone: r.RunTimeZone, Enabled: r.RunEnabled == 1},
		WaitTimeoutSeconds: r.WaitTimeoutSeconds, Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
	_ = json.Unmarshal([]byte(r.ExcludeStacks), &p.ExcludeStacks)
	_ = json.Unmarshal([]byte(r.ExcludeContainers), &p.ExcludeContainers)
	if r.RunWindow != "" {
		var w windowDoc
		if json.Unmarshal([]byte(r.RunWindow), &w) == nil {
			p.Window = &domain.UpdateWindow{Days: w.Days, Start: w.Start, End: w.End}
		}
	}
	return p
}

// ListEnvironmentUpdatePolicies returns every environment policy. An empty
// environment ID is the all-environments scope.
func ListEnvironmentUpdatePolicies(ctx context.Context, db bun.IDB) ([]domain.EnvironmentUpdatePolicy, error) {
	var rows []environmentUpdatePolicyRow
	if err := db.NewSelect().Model(&rows).Order("id ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list environment update policies: %w", err)
	}
	out := make([]domain.EnvironmentUpdatePolicy, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// GetEnvironmentUpdatePolicy returns one environment policy.
func GetEnvironmentUpdatePolicy(ctx context.Context, db bun.IDB, id string) (domain.EnvironmentUpdatePolicy, error) {
	var row environmentUpdatePolicyRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.EnvironmentUpdatePolicy{}, domain.ErrUpdatePolicyNotFound
	}
	if err != nil {
		return domain.EnvironmentUpdatePolicy{}, fmt.Errorf("store: get environment update policy: %w", err)
	}
	return row.toDomain(), nil
}

// InsertEnvironmentUpdatePolicy stores a new policy.
func InsertEnvironmentUpdatePolicy(ctx context.Context, db bun.IDB, p domain.EnvironmentUpdatePolicy) error {
	row := environmentUpdateRow(p)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "environment_update_policies.environment_id") {
			return domain.ErrUpdateScopeOverlap
		}
		return fmt.Errorf("store: insert environment update policy: %w", err)
	}
	return nil
}

// UpdateEnvironmentUpdatePolicy replaces a policy when its revision matches.
func UpdateEnvironmentUpdatePolicy(ctx context.Context, db bun.IDB, p domain.EnvironmentUpdatePolicy, revision int64) error {
	row := environmentUpdateRow(p)
	res, err := db.NewUpdate().Model(&row).WherePK().Where("revision = ?", revision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update environment update policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionMismatch
	}
	return nil
}

// DeleteEnvironmentUpdatePolicy removes a policy when its revision matches.
func DeleteEnvironmentUpdatePolicy(ctx context.Context, db bun.IDB, id string, revision int64) error {
	res, err := db.NewDelete().Model((*environmentUpdatePolicyRow)(nil)).Where("id = ?", id).Where("revision = ?", revision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete environment update policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionMismatch
	}
	return nil
}
