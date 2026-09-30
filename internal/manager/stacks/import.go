package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// ImportCopy imports a discovered project whose directory lies outside
// the stack roots by copying that whole directory (Compose files and
// everything next to them) from the agent's import mount into a new
// directory <projectName> of the stacks volume: a stack.import job stops
// the project, copies and verifies it, recreates its containers from the
// copy and starts what ran before (#7). Docker Manager's own project (#32)
// is copied while it runs and nothing restarts (the agent decides and
// never stops it; the stack's next deploy moves it onto the copy). The
// stack record exists from the request on (it is the job's target); the
// finish hook records the copy as the applied revision, or forgets the
// stack when the import failed before the project switched to the copy
// (nothing changed then). The original directory is only read. A project
// without containers (discovered through its Compose file) is copied and
// switched to without stopping, recreating or starting anything; it needs
// an agent announcing FeatureStackImportContainerless and ends undeployed.
func (s *Service) ImportCopy(ctx context.Context, principal authz.Principal, r domain.StackImport, jr domain.StackJobRequest) (domain.Stack, domain.Job, error) {
	if !protocol.ValidProjectName(r.ProjectName) {
		return domain.Stack{}, domain.Job{}, &domain.InputError{Field: "projectName", Message: "must be a Compose project name"}
	}
	if err := checkMeta(r.DisplayName, r.Meta); err != nil {
		return domain.Stack{}, domain.Job{}, err
	}
	if jr.TimeoutSeconds < 0 || jr.TimeoutSeconds > 3600 {
		return domain.Stack{}, domain.Job{}, &domain.InputError{Field: "timeoutSeconds", Message: "must be between 0 and 3600"}
	}
	if _, err := s.activeEnvironment(ctx, r.EnvironmentID); err != nil {
		return domain.Stack{}, domain.Job{}, err
	}
	if _, err := store.FindStackByName(ctx, s.db, r.EnvironmentID, r.ProjectName); err == nil {
		return domain.Stack{}, domain.Job{}, domain.ErrStackNameTaken
	} else if !errors.Is(err, domain.ErrStackNotFound) {
		return domain.Stack{}, domain.Job{}, err
	}
	fh, ok := s.opts.Agents.(interface {
		EnvironmentHasFeature(environmentID, feature string) bool
	})
	if !ok || !fh.EnvironmentHasFeature(r.EnvironmentID, protocol.FeatureStackImportCopy) {
		if !s.Online(ctx, r.EnvironmentID) {
			return domain.Stack{}, domain.Job{}, agentError(jobs.ErrAgentOffline)
		}
		return domain.Stack{}, domain.Job{}, &domain.StackError{Code: domain.StackErrEnvironmentUnsupported,
			Message: "the environment's agent cannot import projects by copy yet; upgrade it"}
	}
	projects, err := s.discovered(ctx, r.EnvironmentID)
	if err != nil {
		return domain.Stack{}, domain.Job{}, err
	}
	p, ok := projects[r.ProjectName]
	switch {
	case !ok:
		return domain.Stack{}, domain.Job{}, &domain.StackError{Code: domain.StackErrProjectNotFound,
			Message: fmt.Sprintf("the Docker Engine has no Compose project named %q", r.ProjectName)}
	case p.Adoptable:
		return domain.Stack{}, domain.Job{}, &domain.StackError{Code: domain.StackErrNotCopyable,
			Message: "the project already lies in the stacks volume or a registered stack root: adopt it in place instead"}
	case !p.Copyable:
		return domain.Stack{}, domain.Job{}, &domain.StackError{Code: domain.StackErrNotCopyable, Message: p.Reason}
	case p.Containerless && !fh.EnvironmentHasFeature(r.EnvironmentID, protocol.FeatureStackImportContainerless):
		return domain.Stack{}, domain.Job{}, &domain.StackError{Code: domain.StackErrEnvironmentUnsupported,
			Message: "the environment's agent cannot import projects without containers by copy yet; update the agent"}
	}
	now := s.now()
	st := domain.Stack{ID: ids.New(), EnvironmentID: r.EnvironmentID, Name: r.ProjectName, DisplayName: r.DisplayName, Meta: r.Meta,
		Root: domain.StackRootStacks, Dir: r.ProjectName, Origin: domain.StackOriginImported, Status: domain.StackStopped,
		ConfigFiles: relativeTo(p.WorkingDir, p.ConfigFiles), EnvFiles: relativeTo(p.WorkingDir, p.EnvFiles),
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	var states []domain.StackServiceState
	if p.Containerless {
		st.Status = domain.StackUndeployed // nothing runs, nothing will be deployed
	} else {
		for _, sv := range p.Services {
			states = append(states, domain.StackServiceState{Service: sv.Name, Containers: sv.Containers, Running: sv.Running})
			if sv.Running > 0 {
				st.Status = domain.StackDeployed
			}
		}
	}
	s.setEngine(&st, states)
	if err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error { return store.InsertStack(ctx, tx, &st) }); err != nil {
		return domain.Stack{}, domain.Job{}, err
	}
	src := p.SourceDir
	if src == "" {
		src = p.WorkingDir
	}
	in := protocol.StackJobInput{StackID: st.ID, Stack: Ref(st), TimeoutSeconds: jr.TimeoutSeconds,
		Import: &protocol.StackImportSource{WorkingDir: src, Containerless: p.Containerless}}
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.StackImport, Principal: principal, EnvironmentID: st.EnvironmentID,
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: st.ID}}, Input: in, IdempotencyKey: jr.IdempotencyKey})
	if err != nil {
		if derr := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error { return store.DeleteStack(ctx, tx, st.ID) }); derr != nil {
			s.log.Error("stack import: forget the stack after a refused job", "stack_id", st.ID, "error", derr)
		}
		return domain.Stack{}, domain.Job{}, err
	}
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetStack(ctx, tx, st.ID)
		if err != nil {
			return err
		}
		cur.LastJobID, cur.LastJobKind, cur.UpdatedAt = j.ID, j.Kind, s.now()
		st = cur
		return store.UpdateStack(ctx, tx, &cur)
	})
	if errors.Is(err, domain.ErrStackNotFound) {
		return st, j, nil // it failed before switching and was forgotten already
	}
	if err != nil {
		return st, j, err
	}
	s.log.Info("stack import by copy started", "stack_id", st.ID, "environment_id", st.EnvironmentID, "project", st.Name, "job_id", j.ID)
	s.publish(EventCreated, st, map[string]string{"origin": domain.StackOriginImported, "jobId": j.ID})
	return st, j, nil
}

// onImportFinished follows a stack.import: the copy becomes the applied
// revision (a successful import deployed exactly those bytes); a failure
// after the switch leaves the stack failed on its copy; a failure before
// it forgets the stack (nothing changed on the host). A containerless
// import deployed nothing: the copy is the observed revision only and the
// stack stays undeployed.
func (s *Service) onImportFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	st, err := store.GetStack(ctx, db, stackTarget(j))
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	out, ok := s.output(j)
	switched := ok && out.Import != nil && out.Import.Switched
	if j.State != domain.JobSucceeded && !switched {
		return s.forget(ctx, db, st, j, "stack import did not switch to the copy; the stack is forgotten")
	}
	now := s.now()
	containerless := importedContainerless(j)
	var rev *domain.RevisionRef
	switch {
	case ok && out.Sources != nil && containerless:
		// Nothing was deployed: the copy's files are the stack's first
		// (observed) revision, and none is applied.
		r, err := s.observe(ctx, db, &st, *out.Sources, domain.RevisionExternal, initiator(j))
		if err != nil {
			return err
		}
		if r != nil {
			rev = r.Ref()
		} else if st.Observed != nil {
			same := *st.Observed
			rev = &same
		}
	case ok && out.Sources != nil:
		if rev, err = s.deployedRevision(ctx, db, &st, *out.Sources, j); err != nil {
			return err
		}
	}
	if ok {
		st.PreviousState = statesFrom(out.Before)
		if len(out.Services) > 0 {
			st.Services, st.Binds = servicesFrom(out.Services), bindsFrom(out.Binds)
			importLabelMeta(&st, out.Services)
		}
		if out.After != nil {
			s.setEngine(&st, statesFrom(out.After))
		}
	}
	switch {
	case j.State == domain.JobSucceeded && containerless:
		// Imported without containers: it waits for its first deploy, which
		// reuses the project's volumes (same project name).
		st.Status = domain.StackUndeployed
		st.Failed, st.Images = nil, nil
	case j.State == domain.JobSucceeded:
		st.Status = domain.StackStopped
		if ok && out.Import != nil && len(out.Import.WasRunning) > 0 {
			st.Status = domain.StackDeployed
		}
		st.Failed, st.Images = nil, nil
		st.AppliedAt = &now
		if rev != nil {
			st.Applied = rev
		}
		for _, i := range out.Images {
			st.Images = append(st.Images, domain.StackImage{Service: i.Service, Image: i.Image, ImageID: i.ImageID,
				Digest: i.Digest, Platform: i.Platform, Build: i.Build})
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
	s.log.Info("stack import by copy finished", "stack_id", st.ID, "project", st.Name, "state", j.State)
	s.publish(EventUpdated, st, map[string]string{"jobId": j.ID, "kind": string(j.Kind), "state": string(j.State)})
	return nil
}

// importedContainerless reports whether a stack.import copied a project
// that had no containers (its input's import.containerless).
func importedContainerless(j domain.Job) bool {
	var in protocol.StackJobInput
	if err := json.Unmarshal(j.Input, &in); err != nil || in.Import == nil {
		return false
	}
	return in.Import.Containerless
}

// forget deletes a stack record, its revisions and the permission rules
// naming it or its services (in the job's transaction).
func (s *Service) forget(ctx context.Context, db bun.IDB, st domain.Stack, j domain.Job, msg string) error {
	if err := store.DeleteStack(ctx, db, st.ID); err != nil {
		return err
	}
	now := s.now()
	if _, err := store.DeleteResourceRules(ctx, db, catalog.TypeStack, "", st.ID, now); err != nil {
		return err
	}
	for _, sv := range st.Services {
		if _, err := store.DeleteResourceRules(ctx, db, catalog.TypeService, "", authz.ServiceID(st.ID, sv.Name), now); err != nil {
			return err
		}
	}
	s.log.Info(msg, "stack_id", st.ID, "environment_id", st.EnvironmentID, "project", st.Name, "job_id", j.ID)
	s.publish(EventRemoved, st, map[string]string{"jobId": j.ID})
	return nil
}
