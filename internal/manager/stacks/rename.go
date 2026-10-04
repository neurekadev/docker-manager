package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Renaming a stack (#7) changes its Compose project name (domain.Stack.Name,
// also the project directory's name when the directory is named after the
// project). A stack.rename job on the agent stops the stack, moves its named
// volumes to the new project's names (their data follows), stops and
// recreates the containers outside the stack that mount them, renames the
// project directory, removes the old project's containers (the switch),
// creates the project under the new name and starts what ran. Before the
// switch every failure is undone; after it the stack lives under the new
// name even if the job failed. The stack keeps its ID, revisions, policies
// and permission rules.

// Manager-side rename warning codes.
const (
	// RenameWarnUndeployed: the rename recreates the stack from the files on
	// disk, which differ from the applied revision.
	RenameWarnUndeployed = "undeployed_changes"
)

// OnRenamed registers a hook that runs in the transaction that finishes a
// rename (the stack record carries the new name): services that keep
// Docker object names follow (resources.Service.StackRenamed rewrites the
// saved specifications of recreated standalone containers). Use db for
// every read and write.
func (s *Service) OnRenamed(h func(ctx context.Context, db bun.IDB, r domain.StackRenamed) error) {
	s.renamed = append(s.renamed, h)
}

// RenameDir is the project directory after renaming a stack to name: the
// current one renamed in place when it is named after the project,
// otherwise unchanged.
func RenameDir(st domain.Stack, name string) string {
	if path.Base(st.Dir) != st.Name {
		return st.Dir
	}
	return path.Join(path.Dir(st.Dir), name)
}

// renameRequest validates a rename of st to name and builds the agent's
// rename and the volumes the manager holds.
func (s *Service) renameRequest(ctx context.Context, st domain.Stack, name string) (protocol.StackRename, []string, error) {
	if !protocol.ValidProjectName(name) {
		return protocol.StackRename{}, nil, &domain.InputError{Field: "name",
			Message: "must be a Compose project name: 1-63 lower-case letters, digits, '-' and '_', starting with a letter or digit"}
	}
	if name == st.Name {
		return protocol.StackRename{}, nil, &domain.InputError{Field: "name", Message: "the stack already has this project name"}
	}
	if _, err := s.activeEnvironment(ctx, st.EnvironmentID); err != nil {
		return protocol.StackRename{}, nil, err
	}
	if err := s.nameFree(ctx, s.db, st, name, RenameDir(st, name)); err != nil {
		return protocol.StackRename{}, nil, err
	}
	fh, ok := s.opts.Agents.(interface {
		EnvironmentHasFeature(environmentID, feature string) bool
	})
	if !ok || !fh.EnvironmentHasFeature(st.EnvironmentID, protocol.FeatureStackRename) {
		if !s.Online(ctx, st.EnvironmentID) {
			return protocol.StackRename{}, nil, agentError(jobs.ErrAgentOffline)
		}
		return protocol.StackRename{}, nil, &domain.StackError{Code: domain.StackErrEnvironmentUnsupported,
			Message: "the environment's agent cannot rename stacks yet; upgrade it"}
	}
	r := protocol.StackRename{ProjectName: name, Dir: RenameDir(st, name)}
	if err := r.Validate(Ref(st)); err != nil {
		return protocol.StackRename{}, nil, &domain.InputError{Field: "name", Message: err.Error()}
	}
	var keep []string
	if s.volumeHolds != nil {
		var err error
		if keep, err = s.volumeHolds(ctx, st.EnvironmentID, st.Name); err != nil {
			return protocol.StackRename{}, nil, err
		}
	}
	return r, keep, nil
}

// nameFree reports ErrStackNameTaken when another stack of st's
// environment has the project name or the project directory.
func (s *Service) nameFree(ctx context.Context, db bun.IDB, st domain.Stack, name, dir string) error {
	others, err := store.ListStacks(ctx, db, domain.StackFilter{EnvironmentID: st.EnvironmentID})
	if err != nil {
		return err
	}
	for _, o := range others {
		if o.ID == st.ID {
			continue
		}
		if o.Name == name || (dir != st.Dir && o.Root == st.Root && o.RootPath == st.RootPath && o.Dir == dir) {
			return domain.ErrStackNameTaken
		}
	}
	return nil
}

// PreviewRename computes what renaming st to name does (nothing changes):
// the agent's plan (services that stop and start again, volumes and their
// new names, containers outside the stack that are recreated, blockers)
// plus the manager's warnings.
func (s *Service) PreviewRename(ctx context.Context, st domain.Stack, name string) (domain.StackRenamePlan, error) {
	r, keep, err := s.renameRequest(ctx, st, name)
	if err != nil {
		return domain.StackRenamePlan{}, err
	}
	var plan protocol.StackRenamePlan
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeRenamePreview,
		protocol.ComposeRenamePreviewInput{Stack: Ref(st), Rename: r, KeepVolumes: keep}, &plan); err != nil {
		return domain.StackRenamePlan{}, err
	}
	out := renamePlanOf(plan)
	if st.UndeployedChanges() {
		out.Warnings = append(out.Warnings, domain.StackIssue{Code: RenameWarnUndeployed,
			Message: "The rename recreates the stack from its current files: the undeployed changes will be applied."})
	}
	return out, nil
}

// Rename re-runs the preview (409 stack_rename_blocked with its blockers)
// and enqueues a stack.rename job. Docker Manager's own project (#32) is
// refused here and again by the agent.
func (s *Service) Rename(ctx context.Context, p authz.Principal, st domain.Stack, name string, jr domain.StackJobRequest) (domain.Job, error) {
	if jr.TimeoutSeconds < 0 || jr.TimeoutSeconds > 3600 {
		return domain.Job{}, &domain.InputError{Field: "timeoutSeconds", Message: "must be between 0 and 3600"}
	}
	r, keep, err := s.renameRequest(ctx, st, name)
	if err != nil {
		return domain.Job{}, err
	}
	if prot, err := s.Protection(ctx, st); err != nil {
		return domain.Job{}, err
	} else if prot != nil {
		return domain.Job{}, &domain.DockerError{Code: domain.DockerProtected,
			Message: "Docker Manager's own Compose project cannot be renamed: " + prot.Reason}
	}
	var plan protocol.StackRenamePlan
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeRenamePreview,
		protocol.ComposeRenamePreviewInput{Stack: Ref(st), Rename: r, KeepVolumes: keep}, &plan); err != nil {
		return domain.Job{}, err
	}
	if jr.MayRecreate != nil {
		for _, c := range containersRenamed(plan.Containers) {
			if !jr.MayRecreate(c) {
				// Never name a container the caller may not see (#17).
				plan.Blockers = append(plan.Blockers, protocol.ComposeIssue{Code: "container_not_permitted",
					Message: "a container outside the stack that uses volume " + strings.Join(c.Volumes, ", ") +
						" would be stopped and recreated, and you may not do that (it needs container.stop and container.remove on it)"})
			}
		}
	}
	if len(plan.Blockers) > 0 {
		return domain.Job{}, &domain.StackError{Code: domain.StackErrRenameBlocked,
			Message: "the rename has blockers; preview it for details", Issues: issues(plan.Blockers)}
	}
	jr.Services, jr.MayRecreate = nil, nil
	j, err := s.enqueue(ctx, p, st, jobspec.StackRename, jr, protocol.StackJobInput{Rename: &r, KeepVolumes: keep})
	if err != nil {
		return j, err
	}
	s.log.Info("stack rename started", "stack_id", st.ID, "environment_id", st.EnvironmentID, "from", st.Name, "to", name, "job_id", j.ID)
	return j, nil
}

func renamePlanOf(p protocol.StackRenamePlan) domain.StackRenamePlan {
	out := domain.StackRenamePlan{From: p.From, To: p.To, FromDir: p.FromDir, ToDir: p.ToDir, DeclaredName: p.DeclaredName,
		Running: p.Running, Blockers: issues(p.Blockers), Warnings: issues(p.Warnings)}
	for _, v := range p.Volumes {
		out.Volumes = append(out.Volumes, domain.StackRenameVolume{Key: v.Key, Name: v.Name, NewName: v.NewName,
			Service: v.Service, Target: v.Target, Action: v.Action})
	}
	out.Containers = containersRenamed(p.Containers)
	return out
}

func containersRenamed(in []protocol.StackRenameContainer) []domain.StackRenameContainer {
	var out []domain.StackRenameContainer
	for _, c := range in {
		out = append(out, domain.StackRenameContainer{ID: c.ID, Name: c.Name, Running: c.Running, Volumes: c.Volumes, NewID: c.NewID})
	}
	return out
}

// onRenameFinished follows a stack.rename: once the project switched (or the
// job succeeded) the stack record takes the new name and directory, the
// definition the agent created the project from is recorded (the applied
// revision when the job succeeded; after a failure past the switch the
// stack is failed and a deploy finishes it) and the OnRenamed hooks run. A
// rename that failed before the switch was undone by the agent: only the
// Engine state is refreshed.
func (s *Service) onRenameFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	st, err := store.GetStack(ctx, db, stackTarget(j))
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var in protocol.StackJobInput
	if err := json.Unmarshal(j.Input, &in); err != nil || in.Rename == nil {
		s.log.Warn("stack rename job has a malformed input", "job_id", j.ID)
		return nil
	}
	out, ok := s.output(j)
	switched := ok && out.Rename != nil && (out.Rename.Switched || j.State == domain.JobSucceeded)
	if !switched {
		if j.State == domain.JobSucceeded {
			s.log.Warn("stack rename succeeded without reporting it; the stack keeps its name", "stack_id", st.ID, "job_id", j.ID)
		}
		return s.onOperationFinished(ctx, db, j)
	}
	rep := out.Rename
	if rep.To != in.Rename.ProjectName || rep.ToDir != in.Rename.Dir {
		// The manager decided the name and directory; a report naming
		// others is logged and the requested ones are recorded.
		s.log.Warn("stack rename reported another name or directory than requested", "stack_id", st.ID, "job_id", j.ID)
	}
	from, fromDir := st.Name, st.Dir
	if st.Name != in.Rename.ProjectName {
		if err := s.nameFree(ctx, db, st, in.Rename.ProjectName, in.Rename.Dir); err != nil {
			// Returning the error would redeliver the result forever.
			s.log.Error("stack renamed on the host, but another stack took its new name or directory meanwhile; the record keeps the old name",
				"stack_id", st.ID, "job_id", j.ID, "error", err)
			return s.onOperationFinished(ctx, db, j)
		}
		st.Name, st.Dir = in.Rename.ProjectName, in.Rename.Dir
		st.Revision++
	}
	now := s.now()
	var rev *domain.RevisionRef
	if out.Sources != nil {
		if rev, err = s.deployedRevision(ctx, db, &st, *out.Sources, j); err != nil {
			return err
		}
	}
	st.PreviousState = statesFrom(out.Before)
	if len(out.Services) > 0 {
		st.Services, st.Binds = servicesFrom(out.Services), bindsFrom(out.Binds)
		if rev != nil {
			setSourceBuild(&st, builds(out.Services))
		}
	}
	if out.After != nil {
		s.setEngine(&st, statesFrom(out.After))
	}
	switch j.State {
	case domain.JobSucceeded:
		switch {
		case len(rep.Running) > 0:
			st.Status = domain.StackDeployed
		case out.After != nil && st.EngineState != domain.EngineStateMissing:
			st.Status = domain.StackStopped
		}
		st.Failed = nil
		if rev != nil {
			st.Applied, st.AppliedAt, st.Images = rev, &now, nil
			for _, i := range out.Images {
				st.Images = append(st.Images, domain.StackImage{Service: i.Service, Image: i.Image, ImageID: i.ImageID,
					Digest: i.Digest, Platform: i.Platform, Build: i.Build})
			}
		}
	default: // failed after the switch: deploy to finish
		st.Status = domain.StackFailed
		if rev != nil {
			st.Failed = rev
		}
	}
	st.UpdatedAt = now
	if err := store.UpdateStack(ctx, db, &st); err != nil {
		return err
	}
	ev := domain.StackRenamed{StackID: st.ID, EnvironmentID: st.EnvironmentID, From: from, To: st.Name, FromDir: fromDir, ToDir: st.Dir,
		Volumes: map[string]string{}, Containers: containersRenamed(rep.Containers)}
	for _, v := range rep.Volumes {
		if v.Done && v.NewName != "" && v.Name != "" && v.NewName != v.Name {
			ev.Volumes[v.Name] = v.NewName
		}
	}
	for _, h := range s.renamed {
		if err := h(ctx, db, ev); err != nil {
			return fmt.Errorf("stacks: rename hook: %w", err)
		}
	}
	s.log.Info("stack renamed", "stack_id", st.ID, "environment_id", st.EnvironmentID, "from", from, "to", st.Name, "state", j.State, "job_id", j.ID)
	s.publish(EventUpdated, st, map[string]string{"jobId": j.ID, "kind": string(j.Kind), "state": string(j.State), "change": "renamed"})
	return nil
}
