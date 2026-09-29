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

// Environment migrations (#35).

type environmentMigrationStack struct {
	StackID     string `json:"stackId"`
	Name        string `json:"name"`
	MigrationID string `json:"migrationId,omitempty"`
	State       string `json:"state"`
}

type environmentMigrationDetail struct {
	Groups   [][]string                  `json:"groups"`
	Stacks   []environmentMigrationStack `json:"stacks"`
	Networks []string                    `json:"networks,omitempty"`
}

type environmentMigrationRow struct {
	bun.BaseModel `bun:"table:environment_migrations"`

	ID                  string     `bun:"id,pk"`
	SourceEnvironmentID string     `bun:"source_environment_id,notnull"`
	TargetEnvironmentID string     `bun:"target_environment_id,notnull"`
	State               string     `bun:"state,notnull"`
	Detail              string     `bun:"detail,notnull"`
	CreatedAt           time.Time  `bun:"created_at,notnull"`
	UpdatedAt           time.Time  `bun:"updated_at,notnull"`
	FinishedAt          *time.Time `bun:"finished_at"`
}

func fromEnvironmentMigration(m *domain.EnvironmentMigration) (environmentMigrationRow, error) {
	d := environmentMigrationDetail{Groups: m.Groups, Networks: m.Networks, Stacks: make([]environmentMigrationStack, 0, len(m.Stacks))}
	if d.Groups == nil {
		d.Groups = [][]string{}
	}
	for _, s := range m.Stacks {
		d.Stacks = append(d.Stacks, environmentMigrationStack{StackID: s.StackID, Name: s.Name, MigrationID: s.MigrationID, State: string(s.State)})
	}
	b, err := json.Marshal(d)
	if err != nil {
		return environmentMigrationRow{}, err
	}
	return environmentMigrationRow{ID: m.ID, SourceEnvironmentID: m.SourceEnvironmentID, TargetEnvironmentID: m.TargetEnvironmentID,
		State: string(m.State), Detail: string(b), CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC(), FinishedAt: utcPtr(m.FinishedAt)}, nil
}

func (r environmentMigrationRow) toDomain() (domain.EnvironmentMigration, error) {
	var d environmentMigrationDetail
	if err := json.Unmarshal([]byte(r.Detail), &d); err != nil {
		return domain.EnvironmentMigration{}, fmt.Errorf("store: environment migration detail: %w", err)
	}
	m := domain.EnvironmentMigration{ID: r.ID, SourceEnvironmentID: r.SourceEnvironmentID, TargetEnvironmentID: r.TargetEnvironmentID,
		State: domain.MigrationState(r.State), Groups: d.Groups, Networks: d.Networks, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		FinishedAt: utcPtr(r.FinishedAt), Stacks: make([]domain.EnvironmentMigrationStack, 0, len(d.Stacks))}
	if m.Groups == nil {
		m.Groups = [][]string{}
	}
	for _, s := range d.Stacks {
		m.Stacks = append(m.Stacks, domain.EnvironmentMigrationStack{StackID: s.StackID, Name: s.Name, MigrationID: s.MigrationID,
			State: domain.EnvironmentStackState(s.State)})
	}
	return m, nil
}

// InsertEnvironmentMigration stores a new environment migration record.
func InsertEnvironmentMigration(ctx context.Context, db bun.IDB, m *domain.EnvironmentMigration) error {
	r, err := fromEnvironmentMigration(m)
	if err != nil {
		return err
	}
	if _, err := db.NewInsert().Model(&r).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert environment migration: %w", err)
	}
	return nil
}

// UpdateEnvironmentMigration replaces an environment migration record.
func UpdateEnvironmentMigration(ctx context.Context, db bun.IDB, m *domain.EnvironmentMigration) error {
	r, err := fromEnvironmentMigration(m)
	if err != nil {
		return err
	}
	res, err := db.NewUpdate().Model(&r).WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update environment migration: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrEnvironmentMigrationNotFound
	}
	return nil
}

// GetEnvironmentMigration returns an environment migration.
func GetEnvironmentMigration(ctx context.Context, db bun.IDB, id string) (domain.EnvironmentMigration, error) {
	var r environmentMigrationRow
	err := db.NewSelect().Model(&r).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.EnvironmentMigration{}, domain.ErrEnvironmentMigrationNotFound
	}
	if err != nil {
		return domain.EnvironmentMigration{}, fmt.Errorf("store: get environment migration: %w", err)
	}
	return r.toDomain()
}

// ListEnvironmentMigrations returns the latest migrations away from an
// environment, newest first (at most limit; 0: all).
func ListEnvironmentMigrations(ctx context.Context, db bun.IDB, sourceEnvironmentID string, limit int) ([]domain.EnvironmentMigration, error) {
	var rows []environmentMigrationRow
	q := db.NewSelect().Model(&rows).Where("source_environment_id = ?", sourceEnvironmentID).Order("created_at DESC", "id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list environment migrations: %w", err)
	}
	out := make([]domain.EnvironmentMigration, 0, len(rows))
	for _, r := range rows {
		m, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
