package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
)

// Environment migrations (#35).

type migrationDetail struct {
	Source        domain.MigrationSource   `json:"source"`
	TargetDir     string                   `json:"targetDir,omitempty"`
	Volumes       []domain.MigrationVolume `json:"volumes,omitempty"`
	Images        []string                 `json:"images,omitempty"`
	Parts         []domain.MigrationPart   `json:"parts,omitempty"`
	SourceRunning []string                 `json:"sourceRunning,omitempty"`
}

type migrationRow struct {
	bun.BaseModel `bun:"table:migrations"`

	ID                  string     `bun:"id,pk"`
	Kind                string     `bun:"kind,notnull"`
	StackID             string     `bun:"stack_id,notnull"`
	Volume              string     `bun:"volume,notnull"`
	SourceEnvironmentID string     `bun:"source_environment_id,notnull"`
	TargetEnvironmentID string     `bun:"target_environment_id,notnull"`
	State               string     `bun:"state,notnull"`
	CutOver             bool       `bun:"cut_over,notnull"`
	TargetPartial       bool       `bun:"target_partial,notnull"`
	Bytes               int64      `bun:"bytes,notnull"`
	Detail              string     `bun:"detail,notnull"`
	DeployJobID         string     `bun:"deploy_job_id,notnull"`
	RemovalJobID        string     `bun:"removal_job_id,notnull"`
	CreatedAt           time.Time  `bun:"created_at,notnull"`
	UpdatedAt           time.Time  `bun:"updated_at,notnull"`
	FinishedAt          *time.Time `bun:"finished_at"`
}

func fromMigration(m *domain.Migration) (migrationRow, error) {
	d, err := json.Marshal(migrationDetail{Source: m.Source, TargetDir: m.TargetDir, Volumes: m.Volumes, Images: m.Images,
		Parts: m.Parts, SourceRunning: m.SourceRunning})
	if err != nil {
		return migrationRow{}, err
	}
	volume := ""
	if m.Kind == domain.MigrationKindVolume && len(m.Volumes) > 0 {
		volume = m.Volumes[0].Source
	}
	return migrationRow{ID: m.ID, Kind: string(m.Kind), StackID: m.StackID, Volume: volume,
		SourceEnvironmentID: m.SourceEnvironmentID, TargetEnvironmentID: m.TargetEnvironmentID, State: string(m.State),
		CutOver: m.CutOver, TargetPartial: m.TargetPartial, Bytes: m.Bytes, Detail: string(d), DeployJobID: m.DeployJobID,
		RemovalJobID: m.RemovalJobID, CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC(), FinishedAt: utcPtr(m.FinishedAt)}, nil
}

func (r migrationRow) toDomain() (domain.Migration, error) {
	var d migrationDetail
	if err := json.Unmarshal([]byte(r.Detail), &d); err != nil {
		return domain.Migration{}, fmt.Errorf("store: migration detail: %w", err)
	}
	return domain.Migration{ID: r.ID, Kind: domain.MigrationKind(r.Kind), StackID: r.StackID,
		SourceEnvironmentID: r.SourceEnvironmentID, TargetEnvironmentID: r.TargetEnvironmentID, Source: d.Source,
		TargetDir: d.TargetDir, Volumes: d.Volumes, Images: d.Images, Parts: d.Parts, SourceRunning: d.SourceRunning,
		State: domain.MigrationState(r.State), CutOver: r.CutOver, TargetPartial: r.TargetPartial, Bytes: r.Bytes,
		DeployJobID: r.DeployJobID, RemovalJobID: r.RemovalJobID, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		FinishedAt: utcPtr(r.FinishedAt)}, nil
}

// InsertMigration stores a new migration record.
func InsertMigration(ctx context.Context, db bun.IDB, m *domain.Migration) error {
	r, err := fromMigration(m)
	if err != nil {
		return err
	}
	if _, err := db.NewInsert().Model(&r).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert migration: %w", err)
	}
	return nil
}

// UpdateMigration replaces a migration record.
func UpdateMigration(ctx context.Context, db bun.IDB, m *domain.Migration) error {
	r, err := fromMigration(m)
	if err != nil {
		return err
	}
	res, err := db.NewUpdate().Model(&r).WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update migration: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrMigrationNotFound
	}
	return nil
}

// GetMigration returns a migration.
func GetMigration(ctx context.Context, db bun.IDB, id string) (domain.Migration, error) {
	var r migrationRow
	err := db.NewSelect().Model(&r).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Migration{}, domain.ErrMigrationNotFound
	}
	if err != nil {
		return domain.Migration{}, fmt.Errorf("store: get migration: %w", err)
	}
	return r.toDomain()
}

// ListStackMigrations returns a stack's migrations, newest first.
func ListStackMigrations(ctx context.Context, db bun.IDB, stackID string) ([]domain.Migration, error) {
	var rows []migrationRow
	if err := db.NewSelect().Model(&rows).Where("stack_id = ?", stackID).Order("created_at DESC", "id DESC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	return migrationsOf(rows)
}

// PartialMigrations returns the finished, unsuccessful migrations that may
// have left data on an environment (oldest first).
func PartialMigrations(ctx context.Context, db bun.IDB, targetEnvironmentID string) ([]domain.Migration, error) {
	var rows []migrationRow
	err := db.NewSelect().Model(&rows).Where("target_environment_id = ? AND target_partial = 1 AND state IN (?)", targetEnvironmentID,
		bun.List([]string{string(domain.MigrationFailed), string(domain.MigrationCancelled), string(domain.MigrationInterrupted)})).
		Order("created_at", "id").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: partial migrations: %w", err)
	}
	return migrationsOf(rows)
}

func migrationsOf(rows []migrationRow) ([]domain.Migration, error) {
	out := make([]domain.Migration, 0, len(rows))
	for _, r := range rows {
		m, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
