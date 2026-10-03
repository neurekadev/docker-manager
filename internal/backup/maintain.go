package backup

import (
	"context"
	"slices"
	"time"

	"github.com/neurekadev/docker-manager/internal/restic"
)

// Refusal is a classed job failure (jobexec.ClassedError) for backup
// executors: a stable class, a message and recovery guidance.
type Refusal struct {
	Class    string
	Message  string
	Guidance string
}

func (r *Refusal) Error() string { return r.Message }

// ErrorClass implements jobexec.ClassedError.
func (r *Refusal) ErrorClass() string { return r.Class }

// Recovery implements jobexec.ClassedError.
func (r *Refusal) Recovery() string { return r.Guidance }

// Refuse returns a *Refusal.
func Refuse(class, message, guidance string) error {
	return &Refusal{Class: class, Message: message, Guidance: guidance}
}

// RetentionResult is what ApplyRetention did.
type RetentionResult struct {
	Forgotten []string
	Kept      int
}

// RetentionScope selects the snapshots retention judges: those of
// PolicyID, or with AnyPolicy every snapshot a backup run took (it carries
// a policy tag), whichever policy took it.
type RetentionScope struct {
	PolicyID  string
	AnyPolicy bool
}

func (r RetentionScope) covers(tags []string) bool {
	p := PolicyOf(tags)
	if r.AnyPolicy {
		return p != ""
	}
	return p == r.PolicyID
}

// ApplyRetention forgets the snapshots of scope in one location that rules
// do not keep, and every snapshot of the expired (deleted) items (the same
// decision as the preview: Plan, then Expire), plus the manifests of the
// covered sets that have no data left there.
func ApplyRetention(ctx context.Context, repo restic.Repo, scope RetentionScope, rules RetentionRules, expire []string, tz string) (RetentionResult, error) {
	out := RetentionResult{Forgotten: []string{}}
	if rules.Empty() && len(expire) == 0 {
		return out, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	all, err := repo.Snapshots(ctx, restic.SnapshotFilter{})
	if err != nil {
		return out, err
	}
	var cands []RetentionSnapshot
	for _, sn := range all {
		if !scope.covers(sn.Tags) || sn.HasTag(TagManifest) {
			continue
		}
		cands = append(cands, RetentionSnapshot{ID: sn.ID, Time: sn.Time, Item: ItemOf(sn.Tags)})
	}
	plan := Plan(rules, cands, loc).Expire(expire)
	remove := plan.Remove()
	out.Kept = plan.Kept()
	policySets := map[string]bool{}
	keptSets := map[string]bool{}
	for _, sn := range all {
		if sn.HasTag(TagManifest) {
			continue
		}
		if scope.covers(sn.Tags) {
			policySets[SetOf(sn.Tags)] = true
		}
		if !slices.Contains(remove, sn.ID) {
			keptSets[SetOf(sn.Tags)] = true
		}
	}
	for _, sn := range all {
		set := SetOf(sn.Tags)
		if sn.HasTag(TagManifest) && policySets[set] && !keptSets[set] {
			remove = append(remove, sn.ID)
		}
	}
	if len(remove) == 0 {
		return out, nil
	}
	if err := repo.Forget(ctx, remove); err != nil {
		return out, err
	}
	out.Forgotten = remove
	return out, nil
}

// Prune prunes a repository and reports the raw size it reclaimed and
// the size afterwards (nil when it could not be measured).
func Prune(ctx context.Context, repo restic.Repo) (int64, *restic.Stats, error) {
	before, _ := repo.Stats(ctx)
	if err := repo.Prune(ctx); err != nil {
		return 0, nil, err
	}
	after, err := repo.Stats(ctx)
	if err != nil {
		return 0, nil, nil
	}
	if before.TotalSize < after.TotalSize {
		return 0, &after, nil
	}
	return before.TotalSize - after.TotalSize, &after, nil
}

// statsTimeout bounds MeasureStats.
const statsTimeout = 10 * time.Minute

// MeasureStats measures a repository after a job (#10): restic stats
// reads the index and directory metadata, never file contents. It returns
// nil when the size could not be measured; that never fails the job.
func MeasureStats(ctx context.Context, repo restic.Repo) *restic.Stats {
	ctx, cancel := context.WithTimeout(ctx, statsTimeout)
	defer cancel()
	st, err := repo.Stats(ctx)
	if err != nil {
		return nil
	}
	return &st
}

// MaxListedSnapshots bounds the snapshots a verification reports.
const MaxListedSnapshots = 200

// VerifyResult is what Verify found.
type VerifyResult struct {
	ReadData  bool
	Damaged   bool
	Snapshots []restic.Snapshot
}

// Verify checks a repository and lists its Docker Manager snapshots (bounded).
// A damaged repository returns the check's error (restic
// CodeRepositoryDamaged) with Damaged set.
func Verify(ctx context.Context, repo restic.Repo, subset string) (VerifyResult, error) {
	out := VerifyResult{ReadData: subset != ""}
	if _, err := repo.Check(ctx, restic.CheckRequest{ReadDataSubset: subset}); err != nil {
		out.Damaged = restic.IsCode(err, restic.CodeRepositoryDamaged)
		return out, err
	}
	snaps, err := repo.Snapshots(ctx, restic.SnapshotFilter{})
	if err != nil {
		return out, err
	}
	for _, sn := range snaps {
		if len(out.Snapshots) >= MaxListedSnapshots {
			break
		}
		if sn.HasTag(TagDockerManager) || sn.HasTag(TagManagerState) {
			out.Snapshots = append(out.Snapshots, sn)
		}
	}
	return out, nil
}
