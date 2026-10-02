package audit

import (
	"context"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Retention (#30): records older than Options.Retention (default 365 days)
// are deleted, and when the retained records' canonical size exceeds
// Options.MaxBytes the oldest are deleted until it is back under 90% of
// the cap. Only a prefix of the chain is ever deleted: the purge moves the
// chain anchor to the last deleted record (its seq and hash) and appends an
// audit.purge record in the same transaction, so the purge is itself
// audited and the chain stays verifiable from the new oldest record.
// Manager-state backups copy the database and therefore the audit history.

// PurgeResult summarizes one Purge call.
type PurgeResult struct {
	// Batches is the number of purge transactions (one audit.purge record each).
	Batches int
	Deleted int64
	Bytes   int64
}

// maxBatchesPerPurge bounds one Purge call; the next run continues.
const maxBatchesPerPurge = 100

// Purge applies retention now. It runs as the manager service identity.
// It also writes the summaries of anonymous failures whose window ended
// with no record after it (anonymous.go).
func (l *Log) Purge(ctx context.Context) (PurgeResult, error) {
	var res PurgeResult
	if err := l.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return l.writeAnonSummaries(ctx, tx)
	}); err != nil {
		return res, err
	}
	for res.Batches < maxBatchesPerPurge {
		done := true
		err := l.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			chain, err := store.GetAuditChain(ctx, tx)
			if err != nil {
				return err
			}
			now := l.clock.Now()
			through, err := store.AuditPurgeCutoff(ctx, tx, now.Add(-l.opts.Retention))
			if err != nil {
				return err
			}
			reason := "retention"
			if chain.TotalBytes > l.opts.MaxBytes {
				bySize, err := store.AuditSizeCutoff(ctx, tx, chain.TotalBytes-l.opts.MaxBytes*9/10)
				if err != nil {
					return err
				}
				if bySize > through {
					through, reason = bySize, "size_cap"
				}
			}
			if through <= chain.AnchorSeq {
				return nil
			}
			if limit := chain.AnchorSeq + int64(l.opts.PurgeBatch); through > limit {
				through, done = limit, false
			}
			p, err := store.PurgeAuditThrough(ctx, tx, through)
			if err != nil {
				return err
			}
			res.Batches++
			res.Deleted += p.Deleted
			res.Bytes += p.Bytes
			return l.RecordTx(ctx, tx, domain.AuditEvent{
				At: now, Category: domain.AuditSystem, Action: ActionAuditPurge, Actor: ServiceActor(),
				Details: map[string]any{
					"reason": reason, "deletedRecords": p.Deleted, "deletedBytes": p.Bytes,
					"anchorSeq": p.ThroughSeq, "anchorHash": p.ThroughHash,
					"retentionDays": int64(l.opts.Retention / (24 * time.Hour)), "maxBytes": l.opts.MaxBytes,
				},
			})
		})
		if err != nil {
			return res, err
		}
		if done {
			return res, nil
		}
	}
	return res, nil
}

// Run purges at start and then every Options.PurgeInterval until ctx
// ends (manager service work).
func (l *Log) Run(ctx context.Context) error {
	for {
		if res, err := l.Purge(ctx); err != nil && ctx.Err() == nil {
			l.logger.Error("audit retention purge failed", "error", err)
		} else if res.Deleted > 0 {
			l.logger.Info("audit retention purge", "deleted_records", res.Deleted, "deleted_bytes", res.Bytes)
		}
		// A new timer after each purge: the next run is an interval after
		// this one finished (and tests can wait for the timer).
		t := l.clock.NewTimer(l.opts.PurgeInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C():
		}
	}
}
