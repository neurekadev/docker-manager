package prune

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Executor returns the prune.run executor: collect_candidates computes the
// plan and journals the candidates as the job output; delete_candidates
// revalidates and removes them one by one, journaling each result, and
// stops at an item boundary when cancellation is requested.
func (s *Service) Executor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.PruneRun, Steps: map[string]jobexec.StepFunc{
		"collect_candidates": s.collect,
		"delete_candidates":  s.deleteCandidates,
	}}
}

// stepError is a classified step failure (jobexec.ClassedError).
type stepError struct {
	class, msg, recovery string
	err                  error
}

func (e *stepError) Error() string      { return e.msg }
func (e *stepError) Unwrap() error      { return e.err }
func (e *stepError) ErrorClass() string { return e.class }
func (e *stepError) Recovery() string   { return e.recovery }

func engineStepErr(err error) error {
	code := engine.CodeOf(err)
	rec := "The Engine failed; the items processed so far are in the job result. Check that Docker is running on the host and run the policy again."
	return &stepError{class: string(code), msg: err.Error(), recovery: rec, err: err}
}

func (s *Service) input(sc *jobexec.StepContext) (protocol.PruneInput, error) {
	in, err := decodeInput(sc.Input)
	if err != nil {
		return in, &stepError{class: "invalid_input", msg: err.Error(),
			recovery: "The manager sent an input this agent does not understand; upgrade the agent."}
	}
	return in, nil
}

func (s *Service) collect(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.input(sc)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return engineStepErr(err)
	}
	sc.Progress(ctx, 2, "collecting candidates")
	p, err := s.plan(ctx, eng, in)
	if err != nil {
		return engineStepErr(err)
	}
	out := protocol.PruneRunOutput{Items: []protocol.PruneRunItem{}, Protected: p.count(protocol.PruneProtected),
		Excluded: p.count(protocol.PruneExcluded), Retained: p.count(protocol.PruneRetained)}
	for _, it := range p.candidates() {
		if len(out.Items) >= protocol.PruneRunItemsMax {
			out.Deferred++
			continue
		}
		out.Items = append(out.Items, protocol.PruneRunItem{Category: it.Category, ID: it.ID, Name: it.Name,
			Status: protocol.PruneItemPending, Bytes: it.Bytes})
	}
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	msg := fmt.Sprintf("%d candidates (%d protected, %d excluded, %d retained)", len(out.Items), out.Protected, out.Excluded, out.Retained)
	if out.Deferred > 0 {
		msg += fmt.Sprintf("; %d more are left for the next run", out.Deferred)
	}
	sc.Progress(ctx, 10, msg)
	return nil
}

func tally(out *protocol.PruneRunOutput) {
	out.Removed, out.Skipped, out.Failed, out.BytesReclaimed = 0, 0, 0, 0
	for _, it := range out.Items {
		switch it.Status {
		case protocol.PruneItemRemoved:
			out.Removed++
			if it.Bytes > 0 {
				out.BytesReclaimed += it.Bytes
			}
		case protocol.PruneItemSkipped:
			out.Skipped++
		case protocol.PruneItemFailed:
			out.Failed++
		}
	}
}

// fatal reports Engine errors that end the run instead of failing one
// item: the Engine is gone or the agent is shutting down.
func fatal(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	switch engine.CodeOf(err) {
	case engine.CodeEngineUnavailable, engine.CodeCanceled:
		return true
	}
	return false
}

func (s *Service) deleteCandidates(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.input(sc)
	if err != nil {
		return err
	}
	var out protocol.PruneRunOutput
	if raw := sc.Output(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return &stepError{class: "invalid_input", msg: "the collected candidates are unreadable",
				recovery: "Run the policy again; nothing was removed by this step."}
		}
	}
	eng, err := s.engine()
	if err != nil {
		return engineStepErr(err)
	}
	total := len(out.Items)
	for i := range out.Items {
		it := &out.Items[i]
		if it.Status != protocol.PruneItemPending {
			continue // done in an earlier attempt
		}
		if sc.CancelRequested() {
			tally(&out)
			if err := sc.SetOutput(ctx, out); err != nil {
				return err
			}
			return fmt.Errorf("prune stopped after %d of %d candidates: %w", i, total, jobexec.ErrStepCancelled)
		}
		sc.Progress(ctx, 10+90*i/max(total, 1), fmt.Sprintf("%s %s (%d/%d)", it.Category, it.Name, i+1, total))
		status, reason, reclaimed, err := s.removeOne(ctx, eng, in, *it)
		if err != nil {
			tally(&out)
			_ = sc.SetOutput(ctx, out)
			return engineStepErr(err)
		}
		it.Status, it.Reason = status, bound(reason)
		if status == protocol.PruneItemRemoved {
			it.Bytes = reclaimed
		}
		tally(&out)
		if err := sc.SetOutput(ctx, out); err != nil {
			return err
		}
		sc.Item(ctx, it.Category+" "+it.Name, itemStatus(status), itemMessage(*it))
	}
	sc.Progress(ctx, 100, fmt.Sprintf("removed %d, skipped %d, failed %d; about %d bytes reclaimed", out.Removed, out.Skipped, out.Failed,
		out.BytesReclaimed))
	return nil
}

func bound(s string) string {
	if len(s) > protocol.MaxPruneReasonLen {
		return s[:protocol.MaxPruneReasonLen]
	}
	return s
}

func itemStatus(status string) string {
	switch status {
	case protocol.PruneItemRemoved:
		return domain.ItemSucceeded
	case protocol.PruneItemFailed:
		return domain.ItemFailed
	}
	return domain.ItemSkipped
}

func itemMessage(it protocol.PruneRunItem) string {
	if it.Status == protocol.PruneItemRemoved {
		if it.Bytes > 0 {
			return fmt.Sprintf("removed (about %d bytes)", it.Bytes)
		}
		return "removed"
	}
	return it.Reason
}

// removeOne revalidates one candidate against a fresh read of the Engine
// and removes it. err is set only for failures that end the run (Engine
// unavailable, shutdown); other failures are the item's result.
func (s *Service) removeOne(ctx context.Context, eng engine.Engine, in protocol.PruneInput, it protocol.PruneRunItem) (status, reason string, bytes int64, err error) {
	r, ok := in.Rule(it.Category)
	if !ok {
		return protocol.PruneItemSkipped, "the rule is no longer part of the policy", 0, nil
	}
	f, err := s.gather(ctx, eng, in, false)
	if err != nil {
		return s.itemErr(ctx, err)
	}
	switch it.Category {
	case protocol.PruneStoppedContainers:
		return s.removeContainer(ctx, eng, f, r, it)
	case protocol.PruneDanglingImages, protocol.PruneUnusedImages:
		return s.removeImage(ctx, eng, f, r, it)
	case protocol.PruneUnusedNetworks:
		return s.removeNetwork(ctx, eng, f, r, it)
	case protocol.PruneAnonymousVolumes, protocol.PruneNamedVolumes:
		return s.removeVolume(ctx, eng, f, r, it)
	case protocol.PruneBuildCache:
		return s.removeBuildCache(ctx, eng, f, r, it)
	}
	return protocol.PruneItemSkipped, "unknown category", 0, nil
}

// itemErr turns an Engine error into the item's result (or a fatal error).
func (s *Service) itemErr(ctx context.Context, err error) (string, string, int64, error) {
	if fatal(ctx, err) {
		return "", "", 0, err
	}
	switch engine.CodeOf(err) {
	case engine.CodeNotFound:
		return protocol.PruneItemSkipped, "already removed", 0, nil
	case engine.CodeConflict:
		return protocol.PruneItemSkipped, "the Engine refused: in use (" + err.Error() + ")", 0, nil
	}
	return protocol.PruneItemFailed, err.Error(), 0, nil
}

func skipped(reason string) (string, string, int64, error) {
	return protocol.PruneItemSkipped, reason, 0, nil
}

// revalidated turns a fresh decision into a skip reason ("" = still a
// candidate).
func revalidated(decision, reason string) string {
	switch decision {
	case protocol.PruneRemove:
		return ""
	case protocol.PruneProtected:
		return "protected: " + reason
	case protocol.PruneExcluded:
		return "excluded since the plan: " + reason
	}
	return "no longer eligible: " + reason
}

func (s *Service) removeContainer(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule, it protocol.PruneRunItem) (string, string, int64, error) {
	i := slices.IndexFunc(f.containers, func(c engine.Container) bool { return c.ID == it.ID })
	if i < 0 {
		return skipped("already removed")
	}
	c := f.containers[i]
	d, err := eng.InspectContainer(ctx, c.ID)
	if err != nil {
		return s.itemErr(ctx, err)
	}
	c.State = d.State.Status
	fresh, ok := f.containerItem(r, c, d, false)
	if !ok {
		return skipped("now " + c.State)
	}
	if why := revalidated(fresh.Decision, fresh.Reason); why != "" {
		return skipped(why)
	}
	if err := protection.Check(f.set.Container(c.ID), protection.Remove, false); err != nil {
		return skipped(err.Error())
	}
	if err := eng.RemoveContainer(ctx, c.ID, engine.RemoveOptions{}); err != nil {
		return s.itemErr(ctx, err)
	}
	return protocol.PruneItemRemoved, "", it.Bytes, nil
}

func (s *Service) removeImage(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule, it protocol.PruneRunItem) (string, string, int64, error) {
	im, err := eng.InspectImage(ctx, it.ID)
	if err != nil {
		return s.itemErr(ctx, err)
	}
	if im.ID != it.ID {
		return skipped("the reference now names another image")
	}
	users, _, _ := usage(f.containers, nil)
	if u := users[im.ID]; len(u) > 0 {
		return skipped("now used by container " + u[0])
	}
	fresh, ok := f.imageItem(r, im.ID, im.RepoTags, im.RepoDigests, im.Labels, im.Created, im.Size)
	if !ok {
		return skipped("tagged since the plan")
	}
	if why := revalidated(fresh.Decision, fresh.Reason); why != "" {
		return skipped(why)
	}
	if err := protection.Check(f.set.Image(im.ID), protection.Remove, false); err != nil {
		return skipped(err.Error())
	}
	// Without force the Engine refuses an image a container uses, and an
	// image tagged in several repositories by ID: untag all but one
	// reference first (untagging never deletes a used image).
	tags := realTags(im.RepoTags)
	for i := 0; i+1 < len(tags); i++ {
		if _, err := eng.RemoveImage(ctx, tags[i], false, true); err != nil && engine.CodeOf(err) != engine.CodeNotFound {
			return s.itemErr(ctx, err)
		}
	}
	if _, err := eng.RemoveImage(ctx, im.ID, false, true); err != nil {
		return s.itemErr(ctx, err)
	}
	return protocol.PruneItemRemoved, "", it.Bytes, nil
}

func (s *Service) removeNetwork(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule, it protocol.PruneRunItem) (string, string, int64, error) {
	n, err := eng.InspectNetwork(ctx, it.ID)
	if err != nil {
		return s.itemErr(ctx, err)
	}
	_, _, used := usage(f.containers, nil)
	if u := append(used[n.Name], used[n.ID]...); len(u) > 0 {
		return skipped("now used by container " + u[0])
	}
	if attached(n, nil) {
		return skipped("now has attached containers")
	}
	fresh := f.networkItem(r, n)
	if why := revalidated(fresh.Decision, fresh.Reason); why != "" {
		return skipped(why)
	}
	if err := eng.RemoveNetwork(ctx, n.ID); err != nil {
		return s.itemErr(ctx, err)
	}
	return protocol.PruneItemRemoved, "", 0, nil
}

func (s *Service) removeVolume(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule, it protocol.PruneRunItem) (string, string, int64, error) {
	v, err := eng.InspectVolume(ctx, it.ID)
	if err != nil {
		return s.itemErr(ctx, err)
	}
	_, used, _ := usage(f.containers, nil)
	if u := used[v.Name]; len(u) > 0 {
		return skipped("now mounted by container " + u[0])
	}
	fresh, ok := f.volumeItem(r, v, it.Bytes)
	if !ok {
		return skipped("no longer an " + r.Category + " volume")
	}
	if why := revalidated(fresh.Decision, fresh.Reason); why != "" {
		return skipped(why)
	}
	if err := protection.Check(f.set.Volume(v.Name, v.Labels), protection.Remove, false); err != nil {
		return skipped(err.Error())
	}
	if err := eng.RemoveVolume(ctx, v.Name, false); err != nil {
		return s.itemErr(ctx, err)
	}
	return protocol.PruneItemRemoved, "", it.Bytes, nil
}

func (s *Service) removeBuildCache(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule, it protocol.PruneRunItem) (string, string, int64, error) {
	recs, err := eng.ListBuildCache(ctx)
	if err != nil {
		return s.itemErr(ctx, err)
	}
	i := slices.IndexFunc(recs, func(rec engine.BuildCacheRecord) bool { return rec.ID == it.ID })
	if i < 0 {
		return skipped("already removed")
	}
	rec := recs[i]
	if rec.InUse {
		return skipped("now in use by a build")
	}
	if !buildCacheEligible(r, rec) {
		return skipped("no longer eligible (shared or internal record)")
	}
	d, why := ruleDecision(r, rec.ID, nil, nil, buildCacheSince(rec), f.now)
	if reason := revalidated(d, why); reason != "" {
		return skipped(reason)
	}
	if limit := r.KeepStorageBytes; limit > 0 && cacheTotal(recs) <= limit {
		return skipped(fmt.Sprintf("the build cache is within the keep-storage cap (%d bytes)", limit))
	}
	res, err := eng.RemoveBuildCache(ctx, rec.ID, r.BuildCacheAll)
	if err != nil {
		return s.itemErr(ctx, err)
	}
	if !slices.Contains(res.Deleted, rec.ID) {
		return skipped("the Engine kept it (in use, shared or the parent of another record)")
	}
	return protocol.PruneItemRemoved, "", int64(min(res.SpaceReclaimed, uint64(1)<<62)), nil //nolint:gosec // bounded above
}
