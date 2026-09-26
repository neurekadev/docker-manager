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

// Audit trail storage (#30). internal/manager/audit owns the rules
// (redaction, canonical form, hashing, retention); these functions only
// move rows. audit_events is append-only: triggers reject every UPDATE and
// every DELETE outside PurgeAuditThrough.

// AuditTimeLayout is the fixed-width UTC text form of audit timestamps; it
// sorts lexicographically in time order.
const AuditTimeLayout = "2006-01-02T15:04:05.000000Z"

// FormatAuditTime renders t in AuditTimeLayout (UTC, microseconds).
func FormatAuditTime(t time.Time) string { return t.UTC().Format(AuditTimeLayout) }

type auditRow struct {
	bun.BaseModel `bun:"table:audit_events"`

	Seq           int64  `bun:"seq,pk"`
	ID            string `bun:"id,notnull"`
	At            string `bun:"at,notnull"`
	Category      string `bun:"category,notnull"`
	Action        string `bun:"action,notnull"`
	OperationID   string `bun:"operation_id,notnull"`
	ActorKind     string `bun:"actor_kind,notnull"`
	ActorUserID   string `bun:"actor_user_id,notnull"`
	ActorTokenID  string `bun:"actor_token_id,notnull"`
	ActorAgentID  string `bun:"actor_agent_id,notnull"`
	ClientIP      string `bun:"client_ip,notnull"`
	UserAgent     string `bun:"user_agent,notnull"`
	EnvironmentID string `bun:"environment_id,notnull"`
	Targets       string `bun:"targets,notnull"`
	Outcome       string `bun:"outcome,notnull"`
	ErrorClass    string `bun:"error_class,notnull"`
	JobID         string `bun:"job_id,notnull"`
	RequestID     string `bun:"request_id,notnull"`
	Details       string `bun:"details,notnull"`
	Size          int64  `bun:"size,notnull"`
	PrevHash      string `bun:"prev_hash,notnull"`
	Hash          string `bun:"hash,notnull"`
}

type auditTargetJSON struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	EnvironmentID string `json:"environmentId,omitempty"`
}

// MarshalAuditTargets is the canonical JSON form of a target list (stored
// and hashed as is).
func MarshalAuditTargets(ts []domain.AuditTarget) string {
	out := make([]auditTargetJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, auditTargetJSON{Type: t.Type, ID: t.ID, EnvironmentID: t.EnvironmentID})
	}
	b, _ := json.Marshal(out) // strings only: cannot fail
	return string(b)
}

func unmarshalAuditTargets(s string) ([]domain.AuditTarget, error) {
	var in []auditTargetJSON
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, err
	}
	out := make([]domain.AuditTarget, 0, len(in))
	for _, t := range in {
		out = append(out, domain.AuditTarget{Type: t.Type, ID: t.ID, EnvironmentID: t.EnvironmentID})
	}
	return out, nil
}

func auditRowFrom(r *domain.AuditRecord, size int64) auditRow {
	return auditRow{
		Seq: r.Seq, ID: r.ID, At: FormatAuditTime(r.At), Category: string(r.Category), Action: r.Action,
		OperationID: r.OperationID, ActorKind: string(r.Actor.Kind), ActorUserID: r.Actor.UserID,
		ActorTokenID: r.Actor.TokenID, ActorAgentID: r.Actor.AgentID, ClientIP: r.ClientIP, UserAgent: r.UserAgent,
		EnvironmentID: r.EnvironmentID, Targets: MarshalAuditTargets(r.Targets), Outcome: string(r.Outcome),
		ErrorClass: r.ErrorClass, JobID: r.JobID, RequestID: r.RequestID, Details: string(r.Details), Size: size,
		PrevHash: r.PrevHash, Hash: r.Hash,
	}
}

func (r auditRow) record() (domain.AuditRecord, error) {
	at, err := time.Parse(AuditTimeLayout, r.At)
	if err != nil {
		return domain.AuditRecord{}, fmt.Errorf("store: audit record %d: timestamp: %w", r.Seq, err)
	}
	targets, err := unmarshalAuditTargets(r.Targets)
	if err != nil {
		return domain.AuditRecord{}, fmt.Errorf("store: audit record %d: targets: %w", r.Seq, err)
	}
	return domain.AuditRecord{
		Seq: r.Seq, ID: r.ID, At: at, Category: domain.AuditCategory(r.Category), Action: r.Action, OperationID: r.OperationID,
		Actor:    domain.AuditActor{Kind: domain.AuditActorKind(r.ActorKind), UserID: r.ActorUserID, TokenID: r.ActorTokenID, AgentID: r.ActorAgentID},
		ClientIP: r.ClientIP, UserAgent: r.UserAgent, EnvironmentID: r.EnvironmentID, Targets: targets,
		Outcome: domain.AuditOutcome(r.Outcome), ErrorClass: r.ErrorClass, JobID: r.JobID, RequestID: r.RequestID,
		Details: []byte(r.Details), PrevHash: r.PrevHash, Hash: r.Hash,
	}, nil
}

type auditChainRow struct {
	bun.BaseModel `bun:"table:audit_chain"`

	ID          int64  `bun:"id,pk"`
	AnchorSeq   int64  `bun:"anchor_seq,notnull"`
	AnchorHash  string `bun:"anchor_hash,notnull"`
	HeadSeq     int64  `bun:"head_seq,notnull"`
	HeadHash    string `bun:"head_hash,notnull"`
	RecordCount int64  `bun:"record_count,notnull"`
	TotalBytes  int64  `bun:"total_bytes,notnull"`
	Purging     int64  `bun:"purging,notnull"`
}

// AuditChain is the chain state: the purge anchor (the last deleted record,
// zero before the first purge), the head (the newest record) and totals.
type AuditChain struct {
	AnchorSeq   int64
	AnchorHash  string
	HeadSeq     int64
	HeadHash    string
	RecordCount int64
	TotalBytes  int64
}

// GetAuditChain reads the chain state.
func GetAuditChain(ctx context.Context, db bun.IDB) (AuditChain, error) {
	var r auditChainRow
	if err := db.NewSelect().Model(&r).Where("id = 1").Scan(ctx); err != nil {
		return AuditChain{}, fmt.Errorf("store: read audit chain: %w", err)
	}
	return AuditChain{AnchorSeq: r.AnchorSeq, AnchorHash: r.AnchorHash, HeadSeq: r.HeadSeq, HeadHash: r.HeadHash,
		RecordCount: r.RecordCount, TotalBytes: r.TotalBytes}, nil
}

// ErrAuditHeadMoved means another writer appended since the head was read.
var ErrAuditHeadMoved = errors.New("store: audit chain head moved")

// AppendAuditRecord inserts rec (whose Seq must be the head's successor and
// whose PrevHash the head's hash) and advances the head. Run it in the same
// transaction that read the head.
func AppendAuditRecord(ctx context.Context, db bun.IDB, rec *domain.AuditRecord, size int64) error {
	row := auditRowFrom(rec, size)
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert audit record: %w", err)
	}
	res, err := db.NewUpdate().Model((*auditChainRow)(nil)).
		Set("head_seq = ?", rec.Seq).Set("head_hash = ?", rec.Hash).
		Set("record_count = record_count + 1").Set("total_bytes = total_bytes + ?", size).
		Where("id = 1 AND head_seq = ? AND head_hash = ?", rec.Seq-1, rec.PrevHash).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: advance audit chain: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrAuditHeadMoved
	}
	return nil
}

// ListAuditRecords returns records matching f, newest first unless
// f.Ascending, at most f.Limit (default 100).
func ListAuditRecords(ctx context.Context, db bun.IDB, f domain.AuditFilter) ([]domain.AuditRecord, error) {
	var rows []auditRow
	q := db.NewSelect().Model(&rows)
	if len(f.ActorKinds) > 0 {
		q = q.Where("actor_kind IN (?)", bun.List(f.ActorKinds))
	}
	if f.ActorID != "" {
		q = q.Where("(actor_user_id = ? OR actor_token_id = ? OR actor_agent_id = ?)", f.ActorID, f.ActorID, f.ActorID)
	}
	if len(f.Actions) > 0 {
		q = q.Where("action IN (?)", bun.List(f.Actions))
	}
	if len(f.Categories) > 0 {
		q = q.Where("category IN (?)", bun.List(f.Categories))
	}
	if len(f.Outcomes) > 0 {
		q = q.Where("outcome IN (?)", bun.List(f.Outcomes))
	}
	if f.EnvironmentID != "" {
		q = q.Where(`(environment_id = ? OR EXISTS (SELECT 1 FROM json_each(?TableAlias.targets) AS t
			WHERE json_extract(t.value, '$.environmentId') = ?))`, f.EnvironmentID, f.EnvironmentID)
	}
	if f.Resource != nil {
		cond := `EXISTS (SELECT 1 FROM json_each(?TableAlias.targets) AS t
			WHERE json_extract(t.value, '$.type') = ? AND json_extract(t.value, '$.id') = ?)`
		if f.Resource.Type == "job" {
			q = q.Where("(job_id = ? OR "+cond+")", f.Resource.ID, f.Resource.Type, f.Resource.ID)
		} else {
			q = q.Where(cond, f.Resource.Type, f.Resource.ID)
		}
	}
	if f.JobID != "" {
		q = q.Where("job_id = ?", f.JobID)
	}
	if !f.Since.IsZero() {
		q = q.Where("at >= ?", FormatAuditTime(f.Since))
	}
	if !f.Until.IsZero() {
		q = q.Where("at < ?", FormatAuditTime(f.Until))
	}
	if f.AfterSeq > 0 {
		q = q.Where("seq > ?", f.AfterSeq)
	}
	if f.BeforeSeq > 0 {
		q = q.Where("seq < ?", f.BeforeSeq)
	}
	if f.Ascending {
		q = q.Order("seq ASC")
	} else {
		q = q.Order("seq DESC")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if err := q.Limit(limit).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list audit records: %w", err)
	}
	out := make([]domain.AuditRecord, 0, len(rows))
	for _, r := range rows {
		rec, err := r.record()
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// AuditPurgeCutoff returns the highest seq of the oldest prefix of records
// timestamped before cutoff: every record up to it is older, and the
// record after it is not (so a record with a skewed clock never pulls newer
// records into a purge). 0 means nothing to purge.
func AuditPurgeCutoff(ctx context.Context, db bun.IDB, cutoff time.Time) (int64, error) {
	var firstKept sql.NullInt64
	if err := db.NewRaw(`SELECT MIN(seq) FROM audit_events WHERE at >= ?`, FormatAuditTime(cutoff)).Scan(ctx, &firstKept); err != nil {
		return 0, fmt.Errorf("store: audit purge cutoff: %w", err)
	}
	if firstKept.Valid {
		var last sql.NullInt64
		if err := db.NewRaw(`SELECT MAX(seq) FROM audit_events WHERE seq < ?`, firstKept.Int64).Scan(ctx, &last); err != nil {
			return 0, fmt.Errorf("store: audit purge cutoff: %w", err)
		}
		return last.Int64, nil
	}
	var last sql.NullInt64
	if err := db.NewRaw(`SELECT MAX(seq) FROM audit_events`).Scan(ctx, &last); err != nil {
		return 0, fmt.Errorf("store: audit purge cutoff: %w", err)
	}
	return last.Int64, nil
}

// AuditSizeCutoff returns the smallest seq such that deleting every record
// up to it frees at least excess bytes (0 when excess <= 0 or nothing).
func AuditSizeCutoff(ctx context.Context, db bun.IDB, excess int64) (int64, error) {
	if excess <= 0 {
		return 0, nil
	}
	var seq sql.NullInt64
	err := db.NewRaw(`SELECT seq FROM (SELECT seq, SUM(size) OVER (ORDER BY seq) AS cum FROM audit_events)
		WHERE cum >= ? ORDER BY seq LIMIT 1`, excess).Scan(ctx, &seq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: audit size cutoff: %w", err)
	}
	if !seq.Valid {
		var last sql.NullInt64
		if err := db.NewRaw(`SELECT MAX(seq) FROM audit_events`).Scan(ctx, &last); err != nil {
			return 0, fmt.Errorf("store: audit size cutoff: %w", err)
		}
		return last.Int64, nil
	}
	return seq.Int64, nil
}

// AuditPurge is the result of PurgeAuditThrough.
type AuditPurge struct {
	Deleted int64
	Bytes   int64
	// ThroughSeq/ThroughHash are the new anchor (the last deleted record).
	ThroughSeq  int64
	ThroughHash string
}

// PurgeAuditThrough deletes every record with seq <= through and moves the
// anchor to the last deleted record, so the chain stays verifiable from the
// new oldest record. It holds the purging guard only inside the caller's
// transaction (the delete trigger rejects deletes without it).
func PurgeAuditThrough(ctx context.Context, db bun.IDB, through int64) (AuditPurge, error) {
	var last auditRow
	if err := db.NewSelect().Model(&last).Column("seq", "hash").Where("seq = ?", through).Scan(ctx); err != nil {
		return AuditPurge{}, fmt.Errorf("store: audit purge: read record %d: %w", through, err)
	}
	var agg struct {
		N     int64 `bun:"n"`
		Bytes int64 `bun:"bytes"`
	}
	if err := db.NewRaw(`SELECT COUNT(*) AS n, COALESCE(SUM(size), 0) AS bytes FROM audit_events WHERE seq <= ?`, through).Scan(ctx, &agg); err != nil {
		return AuditPurge{}, fmt.Errorf("store: audit purge: count: %w", err)
	}
	if _, err := db.NewUpdate().Model((*auditChainRow)(nil)).Set("purging = 1").Where("id = 1").Exec(ctx); err != nil {
		return AuditPurge{}, fmt.Errorf("store: audit purge: guard: %w", err)
	}
	if _, err := db.NewDelete().Model((*auditRow)(nil)).Where("seq <= ?", through).Exec(ctx); err != nil {
		return AuditPurge{}, fmt.Errorf("store: audit purge: delete: %w", err)
	}
	if _, err := db.NewUpdate().Model((*auditChainRow)(nil)).
		Set("purging = 0").Set("anchor_seq = ?", through).Set("anchor_hash = ?", last.Hash).
		Set("record_count = record_count - ?", agg.N).Set("total_bytes = total_bytes - ?", agg.Bytes).
		Where("id = 1").Exec(ctx); err != nil {
		return AuditPurge{}, fmt.Errorf("store: audit purge: anchor: %w", err)
	}
	return AuditPurge{Deleted: agg.N, Bytes: agg.Bytes, ThroughSeq: through, ThroughHash: last.Hash}, nil
}
