package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Storage history of backup locations (#10): a sample every time a
// location's size is measured, at most one per location and UTC hour.

// BackupStorageRetention is how long storage samples are kept. The newest
// older sample of each location stays, so its value still carries into
// the kept range.
const BackupStorageRetention = 730 * 24 * time.Hour

type backupStorageSampleRow struct {
	bun.BaseModel `bun:"table:backup_storage_samples"`

	RepositoryID      string    `bun:"repository_id,pk"`
	Scope             string    `bun:"scope,pk"`
	Hour              int64     `bun:"hour,pk"`
	At                time.Time `bun:"at,notnull"`
	SizeBytes         int64     `bun:"size_bytes,notnull"`
	UncompressedBytes int64     `bun:"uncompressed_bytes,notnull"`
}

// appendBackupStorageSample records a location's size at s.At (replacing
// a sample of the same location and hour) and prunes expired samples.
func appendBackupStorageSample(ctx context.Context, db bun.IDB, s domain.BackupStorageSample) error {
	at := s.At.UTC()
	row := backupStorageSampleRow{RepositoryID: s.RepositoryID, Scope: s.Scope, Hour: at.Unix() / 3600, At: at,
		SizeBytes: max(0, s.SizeBytes), UncompressedBytes: max(0, s.UncompressedBytes)}
	if _, err := db.NewInsert().Model(&row).
		On("CONFLICT (repository_id, scope, hour) DO UPDATE").
		Set("at = EXCLUDED.at, size_bytes = EXCLUDED.size_bytes, uncompressed_bytes = EXCLUDED.uncompressed_bytes").
		Exec(ctx); err != nil {
		return fmt.Errorf("store: append backup storage sample: %w", err)
	}
	return PruneBackupStorageSamples(ctx, db, at.Add(-BackupStorageRetention))
}

// EndBackupStorage appends a zero sample at now to every location of a
// removed repository that has samples, so its storage stops counting from
// its removal on (the samples before stay). Call it in the removal's
// transaction.
func EndBackupStorage(ctx context.Context, db bun.IDB, repositoryID string, now time.Time) error {
	var scopes []string
	if err := db.NewSelect().Model((*backupStorageSampleRow)(nil)).ColumnExpr("DISTINCT scope").
		Where("repository_id = ?", repositoryID).Order("scope ASC").Scan(ctx, &scopes); err != nil {
		return fmt.Errorf("store: list backup storage scopes: %w", err)
	}
	for _, scope := range scopes {
		if err := appendBackupStorageSample(ctx, db, domain.BackupStorageSample{RepositoryID: repositoryID, Scope: scope, At: now}); err != nil {
			return err
		}
	}
	return nil
}

// PruneBackupStorageSamples removes samples older than cutoff except the
// newest older one of each location (the value carried into the kept
// range); that one goes too when it is zero and nothing newer follows (a
// removed repository or an emptied location).
func PruneBackupStorageSamples(ctx context.Context, db bun.IDB, cutoff time.Time) error {
	cutoff = cutoff.UTC()
	// Two statements: the first never deletes a location's newest older
	// sample, so the maximum it compares with stays put while it runs.
	if _, err := db.NewRaw(`DELETE FROM backup_storage_samples WHERE at < ? AND at < (SELECT max(o.at) FROM backup_storage_samples AS o
			WHERE o.repository_id = backup_storage_samples.repository_id AND o.scope = backup_storage_samples.scope AND o.at < ?)`,
		cutoff, cutoff).Exec(ctx); err != nil {
		return fmt.Errorf("store: prune backup storage samples: %w", err)
	}
	if _, err := db.NewRaw(`DELETE FROM backup_storage_samples WHERE at < ? AND size_bytes = 0 AND uncompressed_bytes = 0
			AND NOT EXISTS (SELECT 1 FROM backup_storage_samples AS n WHERE n.repository_id = backup_storage_samples.repository_id
				AND n.scope = backup_storage_samples.scope AND n.at > backup_storage_samples.at)`,
		cutoff).Exec(ctx); err != nil {
		return fmt.Errorf("store: prune removed backup storage samples: %w", err)
	}
	return nil
}

// ListBackupStorageSamples returns the samples at or before to that a
// history from `from` needs: those in [from, to] and each location's
// newest sample before from, oldest first.
func ListBackupStorageSamples(ctx context.Context, db bun.IDB, from, to time.Time, scope string) ([]domain.BackupStorageSample, error) {
	from, to = from.UTC(), to.UTC()
	q := `SELECT s.repository_id, s.scope, s.hour, s.at, s.size_bytes, s.uncompressed_bytes
		FROM backup_storage_samples AS s
		WHERE s.at <= ? AND (s.at >= ? OR s.at = (SELECT max(o.at) FROM backup_storage_samples AS o
			WHERE o.repository_id = s.repository_id AND o.scope = s.scope AND o.at < ?))`
	args := []any{to, from, from}
	if scope != "" {
		q += ` AND s.scope = ?`
		args = append(args, scope)
	}
	q += ` ORDER BY s.at ASC, s.repository_id ASC, s.scope ASC`
	var rows []backupStorageSampleRow
	if err := db.NewRaw(q, args...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: list backup storage samples: %w", err)
	}
	out := make([]domain.BackupStorageSample, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.BackupStorageSample{RepositoryID: r.RepositoryID, Scope: r.Scope, At: r.At.UTC(),
			SizeBytes: r.SizeBytes, UncompressedBytes: r.UncompressedBytes})
	}
	return out, nil
}

// BackupStorageHistory returns the storage at q.From, at every q.Step
// boundary (UTC) after it and at q.To: the sum over the selected locations
// of each one's latest sample at or before that time.
func BackupStorageHistory(ctx context.Context, db bun.IDB, q domain.BackupStorageQuery) ([]domain.BackupStoragePoint, error) {
	samples, err := ListBackupStorageSamples(ctx, db, q.From, q.To, q.Scope)
	if err != nil {
		return nil, err
	}
	if q.Repository != nil {
		kept := samples[:0]
		for _, s := range samples {
			if q.Repository(s.RepositoryID) {
				kept = append(kept, s)
			}
		}
		samples = kept
	}
	return storagePoints(samples, storageTimes(q.From, q.To, q.Step)), nil
}

// storageTimes returns from, every multiple of step (whole UTC hours or
// days for steps that divide a day) strictly between from and to, and to.
func storageTimes(from, to time.Time, step time.Duration) []time.Time {
	from, to = from.UTC(), to.UTC()
	if to.Before(from) {
		return nil
	}
	out := []time.Time{from}
	if step > 0 {
		for t := from.Truncate(step).Add(step); t.Before(to); t = t.Add(step) {
			if t.After(from) {
				out = append(out, t)
			}
		}
	}
	if to.After(from) {
		out = append(out, to)
	}
	return out
}

// storagePoints carries each location's latest sample forward: the point
// at t sums the newest sample at or before t of every location seen so
// far. times must be ascending; samples may come in any order.
func storagePoints(samples []domain.BackupStorageSample, times []time.Time) []domain.BackupStoragePoint {
	sorted := make([]domain.BackupStorageSample, len(samples))
	copy(sorted, samples)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	type loc struct{ repo, scope string }
	latest := map[loc]domain.BackupStorageSample{}
	out := make([]domain.BackupStoragePoint, 0, len(times))
	next := 0
	for _, t := range times {
		for next < len(sorted) && !sorted[next].At.After(t) {
			s := sorted[next]
			latest[loc{s.RepositoryID, s.Scope}] = s
			next++
		}
		p := domain.BackupStoragePoint{At: t, Known: len(latest) > 0}
		for _, s := range latest {
			p.SizeBytes += s.SizeBytes
			p.UncompressedBytes += s.UncompressedBytes
		}
		out = append(out, p)
	}
	return out
}
