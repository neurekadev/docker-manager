package metrics

import (
	"context"
	"fmt"
	"time"
)

// InventoryRecord is the last Engine inventory of an environment, kept as
// the agent's JSON document (observe decodes it).
type InventoryRecord struct {
	EnvironmentID string
	Data          []byte
	CollectedAt   time.Time
	ReceivedAt    time.Time
}

// SaveInventory replaces an environment's inventory.
func (s *Store) SaveInventory(ctx context.Context, r InventoryRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO inventory (environment_id, data, collected_at, received_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (environment_id) DO UPDATE SET data = excluded.data, collected_at = excluded.collected_at, received_at = excluded.received_at`,
		r.EnvironmentID, string(r.Data), r.CollectedAt.UTC().Format(time.RFC3339Nano), r.ReceivedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("metrics: save inventory: %w", err)
	}
	return nil
}

// Inventories returns every stored inventory.
func (s *Store) Inventories(ctx context.Context) ([]InventoryRecord, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT environment_id, data, collected_at, received_at FROM inventory ORDER BY environment_id`)
	if err != nil {
		return nil, fmt.Errorf("metrics: read inventories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []InventoryRecord
	for rows.Next() {
		var r InventoryRecord
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
