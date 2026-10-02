package store

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Registry pull limits (#217): the last limit a registry reported to the
// manager's checks, per registry host and credential (connection ID, ''
// for anonymous access).

type registryPullLimitRow struct {
	bun.BaseModel `bun:"table:registry_pull_limits"`

	Host          string     `bun:"host,pk"`
	ConnectionID  string     `bun:"registry_connection_id,pk"`
	Limit         *int64     `bun:"pull_limit"`
	Remaining     *int64     `bun:"remaining"`
	WindowSeconds *int64     `bun:"window_seconds"`
	ResetAt       *time.Time `bun:"reset_at"`
	ObservedAt    *time.Time `bun:"observed_at"`
	CheckedAt     time.Time  `bun:"checked_at,notnull"`
	LimitedUntil  *time.Time `bun:"limited_until"`
	LastLimitedAt *time.Time `bun:"last_limited_at"`
}

func (r registryPullLimitRow) toDomain() domain.RegistryPullLimit {
	p := domain.RegistryPullLimit{Host: r.Host, ConnectionID: r.ConnectionID, Limit: r.Limit, Remaining: r.Remaining,
		ResetAt: utcPtr(r.ResetAt), ObservedAt: utcPtr(r.ObservedAt), CheckedAt: r.CheckedAt.UTC(),
		LimitedUntil: utcPtr(r.LimitedUntil), LastLimitedAt: utcPtr(r.LastLimitedAt)}
	if r.WindowSeconds != nil {
		p.Window = time.Duration(*r.WindowSeconds) * time.Second
	}
	return p
}

// RecordRegistryPullLimit stores one observation of a registry's answer
// (p.CheckedAt). The limit columns change only when the answer reported a
// limit (p.ObservedAt set), so a later answer without limit headers keeps
// the last reported limit; LimitedUntil is replaced by every answer (nil
// unless it was a 429), LastLimitedAt only by a 429. Writes are monotonic:
// an observation older than the stored row changes nothing (applied false),
// so a slow check never overwrites a newer answer.
func RecordRegistryPullLimit(ctx context.Context, db bun.IDB, p domain.RegistryPullLimit) (applied bool, err error) {
	row := registryPullLimitRow{Host: p.Host, ConnectionID: p.ConnectionID, Limit: p.Limit, Remaining: p.Remaining,
		ResetAt: utcPtr(p.ResetAt), ObservedAt: utcPtr(p.ObservedAt), CheckedAt: p.CheckedAt.UTC(),
		LimitedUntil: utcPtr(p.LimitedUntil), LastLimitedAt: utcPtr(p.LastLimitedAt)}
	if p.Window > 0 {
		s := int64(p.Window / time.Second)
		row.WindowSeconds = &s
	}
	// Unqualified columns name the stored row (the insert has a table alias).
	reported := func(col string) string {
		return col + " = CASE WHEN EXCLUDED.observed_at IS NULL THEN " + col + " ELSE EXCLUDED." + col + " END"
	}
	// Timestamps are UTC text in one format with trailing zeros trimmed
	// ("...05+00:00", "...05.5+00:00"), so they order as text.
	res, err := db.NewInsert().Model(&row).
		On("CONFLICT (host, registry_connection_id) DO UPDATE").
		Set(reported("pull_limit")).Set(reported("remaining")).Set(reported("window_seconds")).Set(reported("reset_at")).
		Set(reported("observed_at")).
		Set("checked_at = EXCLUDED.checked_at").
		Set("limited_until = EXCLUDED.limited_until").
		Set("last_limited_at = COALESCE(EXCLUDED.last_limited_at, last_limited_at)").
		Where("EXCLUDED.checked_at >= checked_at").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: record registry pull limit: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: record registry pull limit: %w", err)
	}
	return n > 0, nil
}

// ListRegistryPullLimits returns every stored pull limit by host, anonymous
// access first.
func ListRegistryPullLimits(ctx context.Context, db bun.IDB) ([]domain.RegistryPullLimit, error) {
	var rows []registryPullLimitRow
	if err := db.NewSelect().Model(&rows).Order("host ASC", "registry_connection_id ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list registry pull limits: %w", err)
	}
	out := make([]domain.RegistryPullLimit, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// DeleteRegistryPullLimits removes the pull limits of a connection (it was
// deleted, or its credential replaced).
func DeleteRegistryPullLimits(ctx context.Context, db bun.IDB, connectionID string) error {
	if connectionID == "" {
		return nil
	}
	if _, err := db.NewDelete().Model((*registryPullLimitRow)(nil)).Where("registry_connection_id = ?", connectionID).Exec(ctx); err != nil {
		return fmt.Errorf("store: delete registry pull limits: %w", err)
	}
	return nil
}
