package stacks

import (
	"context"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// ImportCopy imports a discovered project whose directory lies outside
// the stack roots by copying that whole directory (Compose files and
// everything next to them) from the agent's import mount into a new
// directory <projectName> of the stacks volume: a stack.import job stops
// the project, copies and verifies it, recreates its containers from the
// copy and starts what ran before (#7). The stack record exists from the
// request on (it is the job's target); the finish hook records the copy
// as the applied revision, or forgets the stack when the import failed
// before the project switched to the copy (nothing changed then). The
// original directory is only read.
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
	}
	// The import stops the project while it copies it: never Docker
	// Manager's own (#32; the agent refuses it too).
	if s.opts.Protection != nil {
		prot, err := s.opts.Protection.ProjectProtection(ctx, r.EnvironmentID, r.ProjectName)
		if err != nil {
			return domain.Stack{}, domain.Job{}, err
		}
		if err := protection.Check(prot, protection.Stop, false); err != nil {
			return domain.Stack{}, domain.Job{}, &domain.DockerError{Code: domain.DockerProtected, Message: err.Error()}
		}
	}
	now := s.now()
	st := domain.Stack{ID: ids.New(), EnvironmentID: r.EnvironmentID, Name: r.ProjectName, DisplayName: r.DisplayName, Meta: r.Meta,
		Root: domain.StackRootStacks, Dir: r.ProjectName, Origin: domain.StackOriginImported, Status: domain.StackStopped,
		ConfigFiles: relativeTo(p.WorkingDir, p.ConfigFiles), EnvFiles: relativeTo(p.WorkingDir, p.EnvFiles),
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	var states []domain.StackServiceState
	for _, sv := range p.Services {
		states = append(states, domain.StackServiceState{Service: sv.Name, Containers: sv.Containers, Running: sv.Running})
		if sv.Running > 0 {
			st.Status = domain.StackDeployed
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
		Import: &protocol.StackImportSource{WorkingDir: src}}
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
// it forgets the stack (nothing changed on the host).
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
	var rev *domain.StackRevision
	if ok && out.Sources != nil {
		r, err := s.recordRevision(ctx, db, &st, *out.Sources, domain.RevisionDeploy, initiator(j), j.ID, "")
		if err != nil {
			return err
		}
		rev = &r
		st.Observed, st.ObservedAt = r.Ref(), &now
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
	switch j.State {
	case domain.JobSucceeded:
		st.Status = domain.StackStopped
		if ok && out.Import != nil && len(out.Import.WasRunning) > 0 {
			st.Status = domain.StackDeployed
		}
		st.Failed, st.Images = nil, nil
		st.AppliedAt = &now
		if rev != nil {
			st.Applied = rev.Ref()
		}
		for _, i := range out.Images {
			st.Images = append(st.Images, domain.StackImage{Service: i.Service, Image: i.Image, ImageID: i.ImageID,
				Digest: i.Digest, Platform: i.Platform, Build: i.Build})
		}
	default: // failed after the switch: deploy to finish
		st.Status = domain.StackFailed
		if rev != nil {
			st.Failed = rev.Ref()
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
