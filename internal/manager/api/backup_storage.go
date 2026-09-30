package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
)

// Storage history of the backup repositories (#10): what they stored over
// time, from a sample of each location's measured size after every backup
// and prune. Aggregated over the repositories the caller sees in full
// (backup_repository.read: the capability of the storage figures); the
// response names no repository.

const (
	// backupStorageMaxRange is the longest history one request reads
	// (samples are kept for two years).
	backupStorageMaxRange = 731 * 24 * time.Hour
	// backupStorageHourlyUpTo is the longest range with hourly points;
	// longer ranges get one point per day.
	backupStorageHourlyUpTo = 8 * 24 * time.Hour
	backupStorageDefault    = 30 * 24 * time.Hour
)

// BackupStorageHistory is the storage of the repositories over time.
type BackupStorageHistory struct {
	From        time.Time   `json:"from"`
	To          time.Time   `json:"to"`
	StepSeconds int         `json:"stepSeconds" example:"86400" doc:"Spacing of the points: 3600 (ranges up to 8 days) or 86400."`
	Timestamps  []time.Time `json:"timestamps" doc:"from, every whole UTC hour or day (stepSeconds) in between, and to."`
	// Values align with timestamps; null until a location was measured.
	StoredBytes []*int64 `json:"storedBytes" doc:"Stored at the destinations (after deduplication and compression) at each timestamp: the sum of every location's latest measurement at or before it. null before the first measurement."`
	// UncompressedBytes is the same data before compression.
	UncompressedBytes []*int64 `json:"uncompressedBytes" doc:"The same data before compression, summed the same way. null before the first measurement."`
}

type backupStorageHistoryInput struct {
	From          time.Time `query:"from" doc:"Range start (RFC 3339; default: 30 days before to)."`
	To            time.Time `query:"to" doc:"Range end (RFC 3339; default and latest: now)."`
	EnvironmentID string    `query:"environmentId" maxLength:"64" doc:"Only the locations holding this environment's data (leaves out the manager state)."`
}

type backupStorageHistoryOutput struct{ Body BackupStorageHistory }

// backupStorageStep is the spacing of a history's points.
func backupStorageStep(span time.Duration) time.Duration {
	if span <= backupStorageHourlyUpTo {
		return time.Hour
	}
	return 24 * time.Hour
}

// backupStorageRange applies the defaults and limits to a requested range.
func backupStorageRange(from, to, now time.Time) (time.Time, time.Time, error) {
	now = now.UTC()
	if to.IsZero() || to.After(now) {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-backupStorageDefault)
	}
	from, to = from.UTC(), to.UTC()
	if !from.Before(to) {
		return from, to, Invalid("invalid storage history range", Field("query.from", "must be before to (and before now)"))
	}
	if to.Sub(from) > backupStorageMaxRange {
		return from, to, Invalid("invalid storage history range", Field("query.from", "at most 731 days before to"))
	}
	return from, to, nil
}

func (h *backupsAPI) storageHistory(ctx context.Context, in *backupStorageHistoryInput) (*backupStorageHistoryOutput, error) {
	svc, c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	from, to, err := backupStorageRange(in.From, in.To, h.deps.clock().Now())
	if err != nil {
		return nil, err
	}
	readable := map[string]bool{} // one check per repository, not per sample
	q := domain.BackupStorageQuery{From: from, To: to, Step: backupStorageStep(to.Sub(from)),
		Repository: func(id string) bool {
			ok, seen := readable[id]
			if !seen {
				ok = c.Can(string(CapBackupRepositoryRead), backupRepositoryResource(id)).Allowed
				readable[id] = ok
			}
			return ok
		}}
	if in.EnvironmentID != "" {
		q.Scope = backup.EnvironmentScope(in.EnvironmentID)
	}
	points, err := svc.StorageHistory(ctx, q)
	if err != nil {
		return nil, Internal(err)
	}
	return &backupStorageHistoryOutput{Body: newBackupStorageHistory(from, to, q.Step, points)}, nil
}

func newBackupStorageHistory(from, to time.Time, step time.Duration, points []domain.BackupStoragePoint) BackupStorageHistory {
	out := BackupStorageHistory{From: from, To: to, StepSeconds: int(step / time.Second), Timestamps: make([]time.Time, 0, len(points)),
		StoredBytes: make([]*int64, 0, len(points)), UncompressedBytes: make([]*int64, 0, len(points))}
	for _, p := range points {
		out.Timestamps = append(out.Timestamps, p.At)
		if !p.Known {
			out.StoredBytes, out.UncompressedBytes = append(out.StoredBytes, nil), append(out.UncompressedBytes, nil)
			continue
		}
		stored, uncompressed := p.SizeBytes, p.UncompressedBytes
		out.StoredBytes, out.UncompressedBytes = append(out.StoredBytes, &stored), append(out.UncompressedBytes, &uncompressed)
	}
	return out
}

func registerBackupStorage(a huma.API, h *backupsAPI) {
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-backup-storage-history", Method: http.MethodGet, Path: BasePath + "/backup-storage/history",
			Summary: "Get the storage history of the backup repositories",
			Description: "What the repositories stored over time: a point at from, at every whole UTC hour (ranges up to 8 days) or " +
				"day in between and at to, each the sum over locations of their latest measured size at or before it (measured " +
				"after every backup and prune; a location keeps its last value until the next measurement, a removed repository " +
				"counts zero from its removal). Only repositories the caller may read in full (backup_repository.read) count. " +
				"Default range: the last 30 days; at most 731 days.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRepositoryRead, Scope: ScopeResource,
	}, h.storageHistory)
}
