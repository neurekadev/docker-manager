package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
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
