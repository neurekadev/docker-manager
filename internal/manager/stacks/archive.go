package stacks

import (
	"context"
	"errors"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Stacks from archives (#313). The stack archive service
// (internal/manager/stackarchives) runs the stack.import_archive job; the
// stack record exists from the request on (it is the job's target, like an
// import by copy), its files arrive through the migration transfer
// (migration.receive into the stacks volume's staging area,
// migration.commit into the new project directory, never an existing
// one), and RecordArchiveStack records them as its first revision. A job
// that fails before its files are kept forgets the stack again.

// ReserveArchiveStack checks a stack created from an archive (name, display
// metadata, an active environment, no stack or running Compose project of
// that name) and records it, undeployed and without files yet.
func (s *Service) ReserveArchiveStack(ctx context.Context, r domain.StackFromArchive) (domain.Stack, error) {
	if !protocol.ValidProjectName(r.Name) {
		return domain.Stack{}, &domain.InputError{Field: "name", Message: "must be a Compose project name: 1-63 lower-case letters, digits, '-' and '_', starting with a letter or digit"}
	}
	if err := checkMeta(r.DisplayName, r.Meta); err != nil {
		return domain.Stack{}, err
	}
	if err := s.CheckArchiveName(ctx, r.EnvironmentID, r.Name, ""); err != nil {
		return domain.Stack{}, err
	}
	now := s.now()
	st := domain.Stack{ID: ids.New(), EnvironmentID: r.EnvironmentID, Name: r.Name, DisplayName: r.DisplayName, Meta: r.Meta,
		Root: domain.StackRootStacks, Dir: r.Name, Origin: domain.StackOriginCreated, Status: domain.StackUndeployed,
		ConfigFiles: r.ConfigFiles, EnvFiles: r.EnvFiles, EngineState: domain.EngineStateMissing,
		Links: domain.SanitizeLinks(r.Links), Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error { return store.InsertStack(ctx, tx, &st) }); err != nil {
		return domain.Stack{}, err
	}
	return st, nil
}

// CheckArchiveName refuses a name an archive cannot create a stack under
// in env: an inactive environment, a taken stack name (other than own, the
// stack reserved for the import), a Compose project of that name with
// containers.
func (s *Service) CheckArchiveName(ctx context.Context, env, name, own string) error {
	if _, err := s.activeEnvironment(ctx, env); err != nil {
		return err
	}
	if st, err := store.FindStackByName(ctx, s.db, env, name); err == nil && st.ID != own {
		return domain.ErrStackNameTaken
	} else if err != nil && !errors.Is(err, domain.ErrStackNotFound) {
		return err
	}
	projects, err := s.discovered(ctx, env)
	if err != nil {
		return err
	}
	if dp, ok := projects[name]; ok && !dp.Containerless {
		return &domain.StackError{Code: domain.StackErrProjectExists,
			Message: "a Compose project named " + name + " already runs on this environment; choose another name"}
	}
	return nil
}

// AttachArchiveJob records the job creating a reserved stack and announces
// the stack.
func (s *Service) AttachArchiveJob(ctx context.Context, stackID string, j domain.Job) (domain.Stack, error) {
	var st domain.Stack
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetStack(ctx, tx, stackID)
		if err != nil {
			return err
		}
		cur.LastJobID, cur.LastJobKind, cur.UpdatedAt = j.ID, j.Kind, s.now()
		st = cur
		return store.UpdateStack(ctx, tx, &cur)
	})
	if err != nil {
		return st, err
	}
	s.publish(EventCreated, st, map[string]string{"origin": "archive", "jobId": j.ID})
	return st, nil
}

// DropArchiveStack deletes a reserved stack whose job was refused.
func (s *Service) DropArchiveStack(ctx context.Context, stackID string) error {
	return s.tx(ctx, func(ctx context.Context, tx bun.Tx) error { return store.DeleteStack(ctx, tx, stackID) })
}

// RecordArchiveStack reads a reserved stack's committed files back: the
// definition must be valid and keep the stack's project name (a top-level
// name: pins it), and becomes the stack's first (observed) revision with
// its services. Nothing is deployed.
func (s *Service) RecordArchiveStack(ctx context.Context, stackID string, p authz.Principal) (domain.Stack, error) {
	st, err := store.GetStack(ctx, s.db, stackID)
	if err != nil {
		return st, err
	}
	var v protocol.ComposeValidateOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeValidate, protocol.ComposeValidateInput{Stack: Ref(st)}, &v); err != nil {
		return st, err
	}
	if !v.Valid {
		return st, invalidDefinition(v)
	}
	if v.ProjectName != "" && v.ProjectName != st.Name {
		return st, &domain.StackError{Code: domain.StackErrInvalidDefinition,
			Message: "the archive's Compose file sets the project name " + v.ProjectName + " (top-level name:); create the stack under that name"}
	}
	var read protocol.ComposeReadOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeRead, protocol.ComposeReadInput{Stack: Ref(st)}, &read); err != nil {
		return st, err
	}
	if read.Missing {
		return st, &domain.StackError{Code: domain.StackErrAgent, Message: "the archive's files could not be read back on the host"}
	}
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetStack(ctx, tx, stackID)
		if err != nil {
			return err
		}
		cur.Services, cur.Binds = servicesFrom(v.Services), bindsFrom(v.Binds)
		importLabelMeta(&cur, v.Services)
		if _, err := s.observe(ctx, tx, &cur, read.Snapshot, domain.RevisionExternal, p); err != nil {
			return err
		}
		setSourceBuild(&cur, builds(v.Services))
		cur.UpdatedAt = s.now()
		st = cur
		return store.UpdateStack(ctx, tx, &cur)
	})
	if err != nil {
		return st, err
	}
	s.publish(EventUpdated, st, map[string]string{"origin": "archive"})
	return st, nil
}

// ForgetArchiveStack deletes a stack whose archive import did not keep its
// files (inside the finishing job's transaction).
func (s *Service) ForgetArchiveStack(ctx context.Context, db bun.IDB, stackID string, j domain.Job) error {
	st, err := store.GetStack(ctx, db, stackID)
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.forget(ctx, db, st, j, "stack creation from an archive did not finish; the stack is forgotten")
}
