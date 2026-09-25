package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// Hash chain (#30).
//
// Each record's canonical form is the compact JSON object below, with
// members in this fixed order and every value a string or integer exactly
// as stored (targets and details are the stored canonical JSON texts,
// embedded as strings). Its hash is
//
//	hex(SHA-256("dockyard-audit-v1\n" + prevHash + "\n" + canonical))
//
// where prevHash is the preceding record's hash, or for the oldest
// retained record the purge anchor's hash ("" before the first purge).
// The chain state row keeps the anchor and the head (newest seq and hash),
// so VerifyChain detects modified, deleted, inserted, reordered and
// truncated records.

// canonicalVersion is part of every canonical form.
const canonicalVersion = 1

const hashDomain = "dockyard-audit-v1\n"

type canonicalRecord struct {
	V             int    `json:"v"`
	Seq           int64  `json:"seq"`
	ID            string `json:"id"`
	At            string `json:"at"`
	Category      string `json:"category"`
	Action        string `json:"action"`
	OperationID   string `json:"operationId"`
	ActorKind     string `json:"actorKind"`
	ActorUserID   string `json:"actorUserId"`
	ActorTokenID  string `json:"actorTokenId"`
	ActorAgentID  string `json:"actorAgentId"`
	ClientIP      string `json:"clientIp"`
	UserAgent     string `json:"userAgent"`
	EnvironmentID string `json:"environmentId"`
	Targets       string `json:"targets"`
	Outcome       string `json:"outcome"`
	ErrorClass    string `json:"errorClass"`
	JobID         string `json:"jobId"`
	RequestID     string `json:"requestId"`
	Details       string `json:"details"`
}

// Canonical returns the canonical form of rec (its Hash and PrevHash are
// not part of it).
func Canonical(rec *domain.AuditRecord) []byte {
	b, _ := json.Marshal(canonicalRecord{ // strings and integers only: cannot fail
		V: canonicalVersion, Seq: rec.Seq, ID: rec.ID, At: store.FormatAuditTime(rec.At), Category: string(rec.Category),
		Action: rec.Action, OperationID: rec.OperationID, ActorKind: string(rec.Actor.Kind), ActorUserID: rec.Actor.UserID,
		ActorTokenID: rec.Actor.TokenID, ActorAgentID: rec.Actor.AgentID, ClientIP: rec.ClientIP, UserAgent: rec.UserAgent,
		EnvironmentID: rec.EnvironmentID, Targets: store.MarshalAuditTargets(rec.Targets), Outcome: string(rec.Outcome),
		ErrorClass: rec.ErrorClass, JobID: rec.JobID, RequestID: rec.RequestID, Details: string(rec.Details),
	})
	return b
}

// ChainHash links a canonical record to its predecessor's hash.
func ChainHash(prevHash string, canonical []byte) string {
	h := sha256.New()
	h.Write([]byte(hashDomain))
	h.Write([]byte(prevHash))
	h.Write([]byte{'\n'})
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil))
}

// Problem is one chain verification failure.
type Problem struct {
	// Seq is the record (or expected position) concerned.
	Seq int64
	// Kind: "hash" (content changed), "link" (prev_hash does not match the
	// predecessor: a record was removed, inserted or reordered), "sequence"
	// (a position is missing or out of order), "head" (the newest records
	// were removed or the head was altered), "count" (the stored count does
	// not match).
	Kind   string
	Detail string
}

// VerifyReport is the result of VerifyChain (#34 diagnostics).
type VerifyReport struct {
	OK bool
	// Checked is the number of records verified.
	Checked int64
	// AnchorSeq/AnchorHash: the chain starts after this (purged) record.
	AnchorSeq  int64
	AnchorHash string
	// HeadSeq/HeadHash: the newest record at the start of verification.
	HeadSeq  int64
	HeadHash string
	// Problems lists the first failures found (at most maxProblems).
	Problems []Problem
}

const (
	maxProblems   = 100
	verifyBatch   = 1000
	verifyRetries = 3
)

// Verify checks the whole retained chain.
func (l *Log) Verify(ctx context.Context) (VerifyReport, error) { return VerifyChain(ctx, l.db) }

// VerifyChain recomputes every retained record's hash and link, from the
// purge anchor to the head. Records appended while it runs are ignored; a
// purge while it runs restarts the verification.
func VerifyChain(ctx context.Context, db bun.IDB) (VerifyReport, error) {
	for attempt := 0; ; attempt++ {
		rep, anchorMoved, err := verifyOnce(ctx, db)
		if err != nil || !anchorMoved || attempt == verifyRetries {
			return rep, err
		}
	}
}

func verifyOnce(ctx context.Context, db bun.IDB) (VerifyReport, bool, error) {
	chain, err := store.GetAuditChain(ctx, db)
	if err != nil {
		return VerifyReport{}, false, err
	}
	rep := VerifyReport{AnchorSeq: chain.AnchorSeq, AnchorHash: chain.AnchorHash, HeadSeq: chain.HeadSeq, HeadHash: chain.HeadHash}
	add := func(seq int64, kind, format string, args ...any) {
		if len(rep.Problems) < maxProblems {
			rep.Problems = append(rep.Problems, Problem{Seq: seq, Kind: kind, Detail: fmt.Sprintf(format, args...)})
		}
	}
	prevHash, nextSeq := chain.AnchorHash, chain.AnchorSeq+1
	var last *domain.AuditRecord
	after := int64(0)
	for {
		batch, err := store.ListAuditRecords(ctx, db, domain.AuditFilter{AfterSeq: after, BeforeSeq: chain.HeadSeq + 1, Ascending: true, Limit: verifyBatch})
		if err != nil {
			return rep, false, err
		}
		for i := range batch {
			rec := &batch[i]
			if rep.Checked == 0 && rec.Seq <= chain.AnchorSeq {
				add(rec.Seq, "sequence", "record %d is at or before the purge anchor %d", rec.Seq, chain.AnchorSeq)
			}
			if rec.Seq != nextSeq {
				if rep.Checked == 0 {
					// Was a purge committed after we read the anchor?
					if cur, err := store.GetAuditChain(ctx, db); err == nil && cur.AnchorSeq != chain.AnchorSeq {
						return rep, true, nil
					}
				}
				add(rec.Seq, "sequence", "expected record %d, found %d", nextSeq, rec.Seq)
			}
			if rec.PrevHash != prevHash {
				add(rec.Seq, "link", "prev_hash does not match the hash of the preceding record")
			}
			if got := ChainHash(rec.PrevHash, Canonical(rec)); got != rec.Hash {
				add(rec.Seq, "hash", "stored hash does not match the record's content")
			}
			prevHash, nextSeq, last = rec.Hash, rec.Seq+1, rec
			rep.Checked++
		}
		if len(batch) < verifyBatch {
			break
		}
		after = batch[len(batch)-1].Seq
	}
	switch {
	case last == nil && chain.HeadSeq != chain.AnchorSeq:
		add(chain.HeadSeq, "head", "no records found, but the head is record %d", chain.HeadSeq)
	case last != nil && (last.Seq != chain.HeadSeq || last.Hash != chain.HeadHash):
		add(chain.HeadSeq, "head", "the newest record is %d, the chain head says %d", last.Seq, chain.HeadSeq)
	case last == nil && chain.HeadHash != chain.AnchorHash:
		add(chain.HeadSeq, "head", "the head hash does not match the anchor of an empty chain")
	}
	if rep.Checked != chain.RecordCount {
		add(0, "count", "found %d records, the chain state counts %d", rep.Checked, chain.RecordCount)
	}
	rep.OK = len(rep.Problems) == 0
	return rep, false, nil
}

// Err returns domain.ErrAuditChainBroken with the first problem, or nil.
func (r VerifyReport) Err() error {
	if r.OK {
		return nil
	}
	p := r.Problems[0]
	return fmt.Errorf("%w: record %d: %s: %s (%d problems)", domain.ErrAuditChainBroken, p.Seq, p.Kind, p.Detail, len(r.Problems))
}
