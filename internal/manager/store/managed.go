package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Recreate specifications of Docker Manager-managed standalone containers (#6).
// The spec column holds a sealed envelope; internal/manager/resources seals
// and opens it.

type managedContainerRow struct {
	bun.BaseModel `bun:"table:managed_containers"`

	ID            string    `bun:"id,pk"`
	EnvironmentID string    `bun:"environment_id,notnull"`
	Name          string    `bun:"name,notnull"`
	CreateJobID   string    `bun:"create_job_id,notnull"`
	Spec          string    `bun:"spec,notnull"`
	Start         bool      `bun:"start,notnull"`
	Revision      int64     `bun:"revision,notnull"`
	CreatedAt     time.Time `bun:"created_at,notnull"`
	UpdatedAt     time.Time `bun:"updated_at,notnull"`
}

func (r managedContainerRow) toDomain() (domain.ManagedContainer, string) {
	return domain.ManagedContainer{ID: r.ID, EnvironmentID: r.EnvironmentID, Name: r.Name, CreateJobID: r.CreateJobID, Start: r.Start,
		Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}, r.Spec
}

// ErrManagedContainerNotFound means no recreate specification has the ID.
var ErrManagedContainerNotFound = errors.New("store: managed container not found")

// InsertManagedContainer stores a new specification with its sealed spec.
func InsertManagedContainer(ctx context.Context, db bun.IDB, m domain.ManagedContainer, sealedSpec string) error {
	row := managedContainerRow{ID: m.ID, EnvironmentID: m.EnvironmentID, Name: m.Name, CreateJobID: m.CreateJobID, Spec: sealedSpec,
		Start: m.Start, Revision: 1, CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.CreatedAt.UTC()}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert managed container: %w", err)
	}
	return nil
}

// GetManagedContainer returns a specification and its sealed spec.
func GetManagedContainer(ctx context.Context, db bun.IDB, id string) (domain.ManagedContainer, string, error) {
	var row managedContainerRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ManagedContainer{}, "", ErrManagedContainerNotFound
	}
	if err != nil {
		return domain.ManagedContainer{}, "", fmt.Errorf("store: get managed container: %w", err)
	}
	m, spec := row.toDomain()
	return m, spec, nil
}

// ListManagedContainers returns an environment's specifications (all
// environments when environmentID is empty), oldest first.
func ListManagedContainers(ctx context.Context, db bun.IDB, environmentID string) ([]domain.ManagedContainer, error) {
	var rows []managedContainerRow
	q := db.NewSelect().Model(&rows).OrderExpr("created_at, id")
	if environmentID != "" {
		q = q.Where("environment_id = ?", environmentID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list managed containers: %w", err)
	}
	out := make([]domain.ManagedContainer, 0, len(rows))
	for _, r := range rows {
		m, _ := r.toDomain()
		out = append(out, m)
	}
	return out, nil
}

// UpdateManagedContainerSpec replaces the sealed spec and bumps the
// revision.
func UpdateManagedContainerSpec(ctx context.Context, db bun.IDB, id, sealedSpec string, now time.Time) error {
	res, err := db.NewUpdate().Model((*managedContainerRow)(nil)).Set("spec = ?", sealedSpec).Set("revision = revision + 1").
		Set("updated_at = ?", now.UTC()).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update managed container: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrManagedContainerNotFound
	}
	return nil
}

// SetManagedContainerJob records the create job of a specification.
func SetManagedContainerJob(ctx context.Context, db bun.IDB, id, jobID string) error {
	if _, err := db.NewUpdate().Model((*managedContainerRow)(nil)).Set("create_job_id = ?", jobID).Where("id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("store: set managed container job: %w", err)
	}
	return nil
}

// DeleteManagedContainer removes a specification (idempotent).
func DeleteManagedContainer(ctx context.Context, db bun.IDB, id string) error {
	if _, err := db.NewDelete().Model((*managedContainerRow)(nil)).Where("id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("store: delete managed container: %w", err)
	}
	return nil
}
