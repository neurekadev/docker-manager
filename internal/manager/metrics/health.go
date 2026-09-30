package metrics

import (
	"context"
	"fmt"
	"time"
)

// HostHealthRecord is the last disk health report of an environment
// (#143), kept as the agent's JSON document (observe decodes it).
type HostHealthRecord struct {
	EnvironmentID string
	Data          []byte
	CollectedAt   time.Time
	ReceivedAt    time.Time
}

// SaveHostHealth replaces an environment's disk health report.
func (s *Store) SaveHostHealth(ctx context.Context, r HostHealthRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_health (environment_id, data, collected_at, received_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (environment_id) DO UPDATE SET data = excluded.data, collected_at = excluded.collected_at, received_at = excluded.received_at`,
		r.EnvironmentID, string(r.Data), r.CollectedAt.UTC().Format(time.RFC3339Nano), r.ReceivedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("metrics: save host health: %w", err)
	}
	return nil
}

// HostHealths returns every stored disk health report.
func (s *Store) HostHealths(ctx context.Context) ([]HostHealthRecord, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT environment_id, data, collected_at, received_at FROM host_health ORDER BY environment_id`)
	if err != nil {
		return nil, fmt.Errorf("metrics: read host health: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HostHealthRecord
	for rows.Next() {
		var r HostHealthRecord
		var data, collected, received string
		if err := rows.Scan(&r.EnvironmentID, &data, &collected, &received); err != nil {
			return nil, err
		}
		r.Data = []byte(data)
		r.CollectedAt, _ = time.Parse(time.RFC3339Nano, collected)
		r.ReceivedAt, _ = time.Parse(time.RFC3339Nano, received)
		out = append(out, r)
	}
	return out, rows.Err()
}
