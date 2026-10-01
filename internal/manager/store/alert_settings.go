package store

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Alert thresholds: the singleton alert_settings row (the defaults) and
// alert_threshold_overrides (per environment; NULL keeps the default).

type alertSettingsRow struct {
	bun.BaseModel `bun:"table:alert_settings"`

	Singleton           int       `bun:"singleton,pk"`
	TemperatureWarning  int       `bun:"temperature_warning,notnull"`
	TemperatureCritical int       `bun:"temperature_critical,notnull"`
	DiskSpaceWarning    int       `bun:"disk_space_warning,notnull"`
	DiskSpaceCritical   int       `bun:"disk_space_critical,notnull"`
	MemoryWarning       int       `bun:"memory_warning,notnull"`
	MemoryCritical      int       `bun:"memory_critical,notnull"`
	Revision            int64     `bun:"revision,notnull"`
	UpdatedAt           time.Time `bun:"updated_at,notnull"`
}

type alertThresholdOverrideRow struct {
	bun.BaseModel `bun:"table:alert_threshold_overrides"`

	EnvironmentID       string `bun:"environment_id,pk"`
	TemperatureWarning  *int   `bun:"temperature_warning"`
	TemperatureCritical *int   `bun:"temperature_critical"`
	DiskSpaceWarning    *int   `bun:"disk_space_warning"`
	DiskSpaceCritical   *int   `bun:"disk_space_critical"`
	MemoryWarning       *int   `bun:"memory_warning"`
	MemoryCritical      *int   `bun:"memory_critical"`
}

// GetAlertSettings returns the thresholds and the overrides of the
// environments that still exist.
func GetAlertSettings(ctx context.Context, db bun.IDB) (domain.AlertSettings, error) {
	var row alertSettingsRow
	if err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx); err != nil {
		return domain.AlertSettings{}, fmt.Errorf("store: read alert settings: %w", err)
	}
	var rows []alertThresholdOverrideRow
	if err := db.NewSelect().Model(&rows).Order("environment_id ASC").Scan(ctx); err != nil {
		return domain.AlertSettings{}, fmt.Errorf("store: read alert threshold overrides: %w", err)
	}
	out := domain.AlertSettings{
		Thresholds: domain.AlertThresholds{
			TemperatureWarning: row.TemperatureWarning, TemperatureCritical: row.TemperatureCritical,
			DiskSpaceWarning: row.DiskSpaceWarning, DiskSpaceCritical: row.DiskSpaceCritical,
			MemoryWarning: row.MemoryWarning, MemoryCritical: row.MemoryCritical,
		},
		Revision: row.Revision, UpdatedAt: row.UpdatedAt.UTC(),
	}
	for _, r := range rows {
		out.Overrides = append(out.Overrides, domain.AlertThresholdOverride{
			EnvironmentID: r.EnvironmentID, TemperatureWarning: r.TemperatureWarning, TemperatureCritical: r.TemperatureCritical,
			DiskSpaceWarning: r.DiskSpaceWarning, DiskSpaceCritical: r.DiskSpaceCritical,
			MemoryWarning: r.MemoryWarning, MemoryCritical: r.MemoryCritical,
		})
	}
	return out, nil
}

// ReplaceAlertSettings writes the thresholds and replaces every override
// if the settings are still at revision (domain.ErrRevisionConflict
// otherwise), bumping it. Values must already be validated; run it in a
// transaction.
func ReplaceAlertSettings(ctx context.Context, db bun.IDB, revision int64, s domain.AlertSettings, now time.Time) error {
	t := s.Thresholds
	res, err := db.NewUpdate().Model((*alertSettingsRow)(nil)).
		Set("temperature_warning = ?", t.TemperatureWarning).Set("temperature_critical = ?", t.TemperatureCritical).
		Set("disk_space_warning = ?", t.DiskSpaceWarning).Set("disk_space_critical = ?", t.DiskSpaceCritical).
		Set("memory_warning = ?", t.MemoryWarning).Set("memory_critical = ?", t.MemoryCritical).
		Set("revision = revision + 1").Set("updated_at = ?", now.UTC()).
		Where("singleton = 1 AND revision = ?", revision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update alert settings: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionConflict
	}
	if _, err := db.NewDelete().Model((*alertThresholdOverrideRow)(nil)).Where("1 = 1").Exec(ctx); err != nil {
		return fmt.Errorf("store: clear alert threshold overrides: %w", err)
	}
	if len(s.Overrides) == 0 {
		return nil
	}
	rows := make([]alertThresholdOverrideRow, 0, len(s.Overrides))
	for _, o := range s.Overrides {
		rows = append(rows, alertThresholdOverrideRow{
			EnvironmentID: o.EnvironmentID, TemperatureWarning: o.TemperatureWarning, TemperatureCritical: o.TemperatureCritical,
			DiskSpaceWarning: o.DiskSpaceWarning, DiskSpaceCritical: o.DiskSpaceCritical,
			MemoryWarning: o.MemoryWarning, MemoryCritical: o.MemoryCritical,
		})
	}
	if _, err := db.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert alert threshold overrides: %w", err)
	}
	return nil
}
