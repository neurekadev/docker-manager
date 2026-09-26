package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
)

// instanceRow is the database model for the singleton instance row.
type instanceRow struct {
	bun.BaseModel `bun:"table:instance"`

	Singleton int       `bun:"singleton,pk"`
	ID        string    `bun:"id,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
}

func (r instanceRow) toDomain() domain.Instance {
	return domain.Instance{ID: r.ID, CreatedAt: r.CreatedAt.UTC()}
}

// GetInstance returns the instance row, or ok=false when it does not exist yet.
func GetInstance(ctx context.Context, db bun.IDB) (domain.Instance, bool, error) {
	var row instanceRow
	err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Instance{}, false, nil
	}
	if err != nil {
		return domain.Instance{}, false, fmt.Errorf("store: read instance: %w", err)
	}
	return row.toDomain(), true, nil
}

// CreateInstance inserts the instance row. It fails if one already exists.
func CreateInstance(ctx context.Context, db bun.IDB, now time.Time) (domain.Instance, error) {
	row := instanceRow{Singleton: 1, ID: ids.New(), CreatedAt: now.UTC()}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return domain.Instance{}, fmt.Errorf("store: create instance: %w", err)
	}
	return row.toDomain(), nil
}

// instanceSettingsRow is the singleton row of editable instance settings.
type instanceSettingsRow struct {
	bun.BaseModel `bun:"table:instance_settings"`

	Singleton int       `bun:"singleton,pk"`
	Name      string    `bun:"name,notnull"`
	Revision  int64     `bun:"revision,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

// GetInstanceSettings returns the editable instance settings.
func GetInstanceSettings(ctx context.Context, db bun.IDB) (domain.InstanceSettings, error) {
	var row instanceSettingsRow
	if err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx); err != nil {
		return domain.InstanceSettings{}, fmt.Errorf("store: read instance settings: %w", err)
	}
	return domain.InstanceSettings{Name: row.Name, Revision: row.Revision, UpdatedAt: row.UpdatedAt.UTC()}, nil
}

// UpdateInstanceSettings applies a change if the settings are still at
// revision (domain.ErrRevisionConflict otherwise) and bumps the revision.
// Values must already be validated.
func UpdateInstanceSettings(ctx context.Context, db bun.IDB, revision int64, p domain.InstanceSettingsPatch, now time.Time) error {
	q := db.NewUpdate().Model((*instanceSettingsRow)(nil)).
		Set("revision = revision + 1").Set("updated_at = ?", now.UTC()).
		Where("singleton = 1 AND revision = ?", revision)
	if p.Name != nil {
		q = q.Set("name = ?", *p.Name)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update instance settings: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrRevisionConflict
	}
	return nil
}
