package stacks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Definition files a stack can be created or imported with. Other files
// (service env_files, build contexts, bind-mounted data) are added through
// the file manager (#15).
var definitionNames = []string{
	"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml",
	"compose.override.yaml", "compose.override.yml", "docker-compose.override.yaml", "docker-compose.override.yml",
	".env",
}

// Display metadata bounds.
const (
	MaxDisplayName = 128
	MaxDescription = 1024
)

// checkDefinition validates a submitted definition's shape.
func checkDefinition(d domain.StackDefinition) error {
	if !protocol.ValidProjectName(d.Name) {
		return &domain.InputError{Field: "name", Message: "must be a Compose project name: 1-63 lower-case letters, digits, '-' and '_', starting with a letter or digit"}
	}
	hasCompose := false
	for _, f := range d.Files {
		if !slices.Contains(definitionNames, f.Path) {
			return &domain.InputError{Field: "files", Message: fmt.Sprintf("%s is not a definition file (%s)", f.Path, strings.Join(definitionNames, ", "))}
		}
		if !utf8.Valid(f.Content) {
			return &domain.InputError{Field: "files", Message: f.Path + " is not valid UTF-8"}
		}
		if !strings.Contains(f.Path, "override") && f.Path != ".env" {
			hasCompose = true
		}
	}
	if !hasCompose {
		return &domain.InputError{Field: "compose", Message: "a Compose file is required"}
	}
	if err := protocol.ValidateSources(sourceFiles(d.Files)); err != nil {
		return &domain.StackError{Code: domain.StackErrDefinitionTooLarge, Message: err.Error()}
	}
	return nil
}

// sourceFiles converts submitted files to the protocol form.
func sourceFiles(in []domain.StackFile) []protocol.SourceFile {
	out := make([]protocol.SourceFile, 0, len(in))
	for _, f := range in {
		out = append(out, protocol.SourceFile{Path: f.Path, Content: f.Content, SHA256: protocol.FileHash(f.Content), Size: int64(len(f.Content))})
	}
	return out
}

func newRef(name string) protocol.ProjectRef {
	return protocol.ProjectRef{Root: protocol.RootStacks, Dir: name, ProjectName: name}
}

func (s *Service) activeEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	env, err := s.opts.Environments.GetEnvironment(ctx, id)
	if err != nil {
		return env, err
	}
	if env.Status != domain.EnvironmentActive {
		return env, domain.ErrEnvironmentArchived
	}
	return env, nil
}

// Validate validates a definition on the environment's agent without side
// effects (nothing is written).
func (s *Service) Validate(ctx context.Context, d domain.StackDefinition) (domain.StackValidation, error) {
	if err := checkDefinition(d); err != nil {
		return domain.StackValidation{}, err
	}
	if _, err := s.activeEnvironment(ctx, d.EnvironmentID); err != nil {
		return domain.StackValidation{}, err
	}
	var out protocol.ComposeValidateOutput
	err := s.call(ctx, d.EnvironmentID, protocol.ReqComposeValidate, protocol.ComposeValidateInput{Stack: newRef(d.Name), Files: sourceFiles(d.Files)}, &out)
	return validationOf(out), err
}

// ValidateStack validates an existing stack's definition as it is on disk,
// in its own project directory: its Compose files, override, .env and
// env_files resolve exactly as a deploy loads them (the agent's
// compose.validate without files). Read-only: nothing is written and no
// revision is recorded. Findings are the result; only an unreachable agent
// (StackErrOffline, ...) is an error.
func (s *Service) ValidateStack(ctx context.Context, st domain.Stack) (domain.StackValidation, error) {
	var out protocol.ComposeValidateOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqComposeValidate, protocol.ComposeValidateInput{Stack: Ref(st)}, &out); err != nil {
		return domain.StackValidation{}, err
	}
	return validationOf(out), nil
}

// validationOf converts the agent's validation output.
func validationOf(v protocol.ComposeValidateOutput) domain.StackValidation {
	out := domain.StackValidation{Valid: v.Valid, ProjectName: v.ProjectName, Errors: issues(v.Errors), Warnings: issues(v.Warnings),
		Binds: bindsFrom(v.Binds), Services: []domain.StackServiceInfo{}}
	defs := servicesFrom(v.Services)
	for i, sv := range v.Services {
		out.Services = append(out.Services, domain.StackServiceInfo{StackServiceDef: defs[i], Profiles: sv.Profiles,
			Meta: domain.DisplayMeta{Description: sv.Description}})
	}
	return out
}

func issues(in []protocol.ComposeIssue) []domain.StackIssue {
	out := []domain.StackIssue{}
	for _, i := range in {
		out = append(out, domain.StackIssue{Code: i.Code, Message: i.Message, Service: i.Service})
	}
	return out
}

func invalidDefinition(v protocol.ComposeValidateOutput) error {
	e := &domain.StackError{Code: domain.StackErrInvalidDefinition, Message: "the Compose definition is invalid"}
	for _, i := range v.Errors {
		e.Issues = append(e.Issues, domain.StackIssue{Code: i.Code, Message: i.Message, Service: i.Service})
	}
	return e
}

// discovered returns the environment's Compose projects by name.
func (s *Service) discovered(ctx context.Context, environmentID string) (map[string]protocol.DiscoveredProject, error) {
	var out protocol.ComposeDiscoverOutput
	if err := s.call(ctx, environmentID, protocol.ReqComposeDiscover, struct{}{}, &out); err != nil {
		return nil, err
	}
	m := map[string]protocol.DiscoveredProject{}
	for _, p := range out.Projects {
		m[p.Name] = p
	}
	return m, nil
}

func checkMeta(displayName string, m domain.DisplayMeta) error {
	switch {
	case utf8.RuneCountInString(displayName) > MaxDisplayName:
		return &domain.InputError{Field: "displayName", Message: fmt.Sprintf("at most %d characters", MaxDisplayName)}
	case utf8.RuneCountInString(m.Description) > MaxDescription:
		return &domain.InputError{Field: "description", Message: fmt.Sprintf("at most %d characters", MaxDescription)}
	}
	return nil
}

// Create validates the definition on the agent, writes it into a new
// project directory of the environment's stacks volume (never over an
// existing one) and records it as the first revision. It does not deploy.
func (s *Service) Create(ctx context.Context, p authz.Principal, r domain.StackCreate) (domain.Stack, domain.StackValidation, error) {
	var v protocol.ComposeValidateOutput
	if err := checkDefinition(r.StackDefinition); err != nil {
		return domain.Stack{}, validationOf(v), err
	}
	if err := checkMeta(r.DisplayName, r.Meta); err != nil {
		return domain.Stack{}, validationOf(v), err
	}
	links, err := domain.NormalizeLinks(r.Links)
	if err != nil {
		return domain.Stack{}, validationOf(v), err
	}
	if _, err := s.activeEnvironment(ctx, r.EnvironmentID); err != nil {
		return domain.Stack{}, validationOf(v), err
	}
	if _, err := store.FindStackByName(ctx, s.db, r.EnvironmentID, r.Name); err == nil {
		return domain.Stack{}, validationOf(v), domain.ErrStackNameTaken
	} else if !errors.Is(err, domain.ErrStackNotFound) {
		return domain.Stack{}, validationOf(v), err
	}
	// A Compose project of that name already on the Engine would be taken
	// over by the first deploy: import it instead. (A containerless one is
	// only a folder: the write below refuses its directory if it is ours.)
	projects, err := s.discovered(ctx, r.EnvironmentID)
	if err != nil {
		return domain.Stack{}, validationOf(v), err
	}
	if dp, ok := projects[r.Name]; ok && !dp.Containerless {
		return domain.Stack{}, validationOf(v), &domain.StackError{Code: domain.StackErrProjectExists,
			Message: fmt.Sprintf("the Docker Engine already runs a Compose project named %q; import it instead", r.Name)}
	}
	ref := newRef(r.Name)
	if err := s.call(ctx, r.EnvironmentID, protocol.ReqComposeValidate, protocol.ComposeValidateInput{Stack: ref, Files: sourceFiles(r.Files)}, &v); err != nil {
		return domain.Stack{}, validationOf(v), err
	}
	if !v.Valid {
		return domain.Stack{}, validationOf(v), invalidDefinition(v)
	}
	var w protocol.ComposeWriteOutput
	if err := s.call(ctx, r.EnvironmentID, protocol.ReqComposeWrite, protocol.ComposeWriteInput{Stack: ref, Mode: protocol.WriteCreate, Files: sourceFiles(r.Files)}, &w); err != nil {
		if isCode(err, domain.StackErrDefinitionChanged) {
			return domain.Stack{}, validationOf(v), &domain.StackError{Code: domain.StackErrDirectoryExists,
				Message: fmt.Sprintf("the stacks volume already has a directory %q; nothing was overwritten (import the project or choose another name)", r.Name)}
		}
		return domain.Stack{}, validationOf(v), err
	}
	now := s.now()
	st := domain.Stack{ID: ids.New(), EnvironmentID: r.EnvironmentID, Name: r.Name, DisplayName: r.DisplayName, Meta: r.Meta,
		Links: links, Root: domain.StackRootStacks, Dir: r.Name, Origin: domain.StackOriginCreated, Status: domain.StackUndeployed,
		Services: servicesFrom(v.Services), Binds: bindsFrom(v.Binds), SourceBuild: builds(v.Services), EngineState: domain.EngineStateMissing,
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	importLabelMeta(&st, v.Services)
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := store.InsertStack(ctx, tx, &st); err != nil {
			return err
		}
		if _, err := s.observe(ctx, tx, &st, w.Snapshot, domain.RevisionEditor, p); err != nil {
			return err
		}
		return store.UpdateStack(ctx, tx, &st)
	})
	if err != nil {
		return domain.Stack{}, validationOf(v), err
	}
	s.log.Info("stack created", "stack_id", st.ID, "environment_id", st.EnvironmentID, "project", st.Name, "hash", w.Snapshot.Hash)
	s.publish(EventCreated, st, nil)
	return st, validationOf(v), nil
}

// Discovered lists the environment's Compose projects (read-only), with
// the Docker Manager stack managing each one, if any.
func (s *Service) Discovered(ctx context.Context, environmentID string) ([]domain.DiscoveredStack, error) {
	if _, err := s.activeEnvironment(ctx, environmentID); err != nil {
		return nil, err
	}
	var out protocol.ComposeDiscoverOutput
	if err := s.call(ctx, environmentID, protocol.ReqComposeDiscover, struct{}{}, &out); err != nil {
		return nil, err
	}
	managed, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: environmentID})
	if err != nil {
		return nil, err
	}
	list := make([]domain.DiscoveredStack, 0, len(out.Projects))
	for _, p := range out.Projects {
		d := domain.DiscoveredStack{Name: p.Name, WorkingDir: p.WorkingDir, ConfigFiles: p.ConfigFiles, Root: p.Root, Dir: p.Dir,
			Adoptable: p.Adoptable, Copyable: p.Copyable && !p.Adoptable, SourceDir: p.SourceDir, Reason: p.Reason, Protected: p.Protected,
			Containerless: p.Containerless, Volumes: p.Volumes}
		for _, sv := range p.Services {
			d.Services = append(d.Services, domain.DiscoveredService{Name: sv.Name, Image: sv.Image, Containers: sv.Containers, Running: sv.Running})
		}
		// Managed: a stack of that project name, or (a folder whose project
		// name differs from the stack's) a stack in that very folder.
		if st, ok := managedAs(managed, p); ok {
			d.StackID = st.ID
			d.Adoptable, d.Copyable, d.Reason = false, false, "already managed by Docker Manager"
		}
		list = append(list, d)
	}
	return list, nil
}

// managedAs finds the stack managing a discovered project among an
// environment's stacks: the one with its project name, else, for a
// containerless project in a stack root, the one whose project directory
// is that folder (its Compose file names another project than the stack).
func managedAs(stacks []domain.Stack, p protocol.DiscoveredProject) (domain.Stack, bool) {
	for _, st := range stacks {
		if st.Name == p.Name {
			return st, true
		}
	}
	if !p.Containerless || p.Root == "" {
		return domain.Stack{}, false
	}
	for _, st := range stacks {
		if st.Root == p.Root && st.RootPath == p.RootPath && st.Dir == p.Dir {
			return st, true
		}
	}
	return domain.Stack{}, false
}

// Import adopts a discovered project. In place, the real files in its
// directory become the first revision; with an explicit source, the source
// is written into a new directory of the stacks volume. Labels never
// reconstruct a source, and nothing existing is overwritten: a project
// already managed or a directory that exists is a conflict.
func (s *Service) Import(ctx context.Context, principal authz.Principal, r domain.StackImport) (domain.Stack, error) {
	if !protocol.ValidProjectName(r.ProjectName) {
		return domain.Stack{}, &domain.InputError{Field: "projectName", Message: "must be a Compose project name"}
	}
	if err := checkMeta(r.DisplayName, r.Meta); err != nil {
		return domain.Stack{}, err
	}
	if _, err := s.activeEnvironment(ctx, r.EnvironmentID); err != nil {
		return domain.Stack{}, err
	}
	if _, err := store.FindStackByName(ctx, s.db, r.EnvironmentID, r.ProjectName); err == nil {
		return domain.Stack{}, domain.ErrStackNameTaken
	} else if !errors.Is(err, domain.ErrStackNotFound) {
		return domain.Stack{}, err
	}
	projects, err := s.discovered(ctx, r.EnvironmentID)
	if err != nil {
		return domain.Stack{}, err
	}
	p, ok := projects[r.ProjectName]
	if !ok {
		return domain.Stack{}, &domain.StackError{Code: domain.StackErrProjectNotFound,
			Message: fmt.Sprintf("the Docker Engine has no Compose project named %q", r.ProjectName)}
	}
	now := s.now()
	st := domain.Stack{ID: ids.New(), EnvironmentID: r.EnvironmentID, Name: r.ProjectName, DisplayName: r.DisplayName, Meta: r.Meta,
		Origin: domain.StackOriginImported, Status: domain.StackDeployed, Revision: 1, CreatedAt: now, UpdatedAt: now}
	var states []domain.StackServiceState
	if p.Containerless {
		// Nothing was ever deployed from these files: like a created stack,
		// it waits for its first deploy (which reuses the project's volumes).
		st.Status = domain.StackUndeployed
	} else {
		for _, sv := range p.Services {
			states = append(states, domain.StackServiceState{Service: sv.Name, Containers: sv.Containers, Running: sv.Running})
		}
	}
	s.setEngine(&st, states)
	var snap protocol.SourceSnapshot
	source := domain.RevisionExternal
	var v protocol.ComposeValidateOutput
	if len(r.Files) == 0 {
		if !p.Adoptable {
			return domain.Stack{}, &domain.StackError{Code: domain.StackErrNotAdoptable, Message: p.Reason}
		}
		if p.Containerless {
			// Its Compose file may name another project than the stack of
			// that folder: the folder is taken all the same.
			managed, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: r.EnvironmentID})
			if err != nil {
				return domain.Stack{}, err
			}
			if other, ok := managedAs(managed, p); ok {
				return domain.Stack{}, &domain.StackError{Code: domain.StackErrNotAdoptable,
					Message: fmt.Sprintf("its folder is the project folder of the stack %s already", other.Name)}
			}
		}
		st.Root, st.RootPath, st.Dir = p.Root, p.RootPath, p.Dir
		st.ConfigFiles, st.EnvFiles = relativeTo(p.WorkingDir, p.ConfigFiles), relativeTo(p.WorkingDir, p.EnvFiles)
		var read protocol.ComposeReadOutput
		if err := s.call(ctx, r.EnvironmentID, protocol.ReqComposeRead, protocol.ComposeReadInput{Stack: Ref(st)}, &read); err != nil {
			return domain.Stack{}, err
		}
		if read.Missing {
			return domain.Stack{}, &domain.StackError{Code: domain.StackErrNotAdoptable,
				Message: "the project directory has no Compose file any more; import it with an explicit Compose source"}
		}
		snap = read.Snapshot
		// The project runs already: a definition that no longer loads is
		// imported anyway (it shows as invalid until fixed).
		if err := s.call(ctx, r.EnvironmentID, protocol.ReqComposeValidate, protocol.ComposeValidateInput{Stack: Ref(st)}, &v); err != nil {
			return domain.Stack{}, err
		}
	} else {
		if err := checkDefinition(domain.StackDefinition{EnvironmentID: r.EnvironmentID, Name: r.ProjectName, Files: r.Files}); err != nil {
			return domain.Stack{}, err
		}
		st.Root, st.Dir = domain.StackRootStacks, r.ProjectName
		if err := s.call(ctx, r.EnvironmentID, protocol.ReqComposeValidate, protocol.ComposeValidateInput{Stack: Ref(st), Files: sourceFiles(r.Files)}, &v); err != nil {
			return domain.Stack{}, err
		}
		if !v.Valid {
			return domain.Stack{}, invalidDefinition(v)
		}
		var w protocol.ComposeWriteOutput
		if err := s.call(ctx, r.EnvironmentID, protocol.ReqComposeWrite, protocol.ComposeWriteInput{Stack: Ref(st), Mode: protocol.WriteCreate, Files: sourceFiles(r.Files)}, &w); err != nil {
			if isCode(err, domain.StackErrDefinitionChanged) {
				return domain.Stack{}, &domain.StackError{Code: domain.StackErrDirectoryExists,
					Message: fmt.Sprintf("the stacks volume already has a directory %q; nothing was overwritten", r.ProjectName)}
			}
			return domain.Stack{}, err
		}
		snap, source = w.Snapshot, domain.RevisionEditor
	}
	if v.Valid {
		st.Services, st.Binds, st.SourceBuild = servicesFrom(v.Services), bindsFrom(v.Binds), builds(v.Services)
		importLabelMeta(&st, v.Services)
	}
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := store.InsertStack(ctx, tx, &st); err != nil {
			return err
		}
		if _, err := s.observe(ctx, tx, &st, snap, source, principal); err != nil {
			return err
		}
		return store.UpdateStack(ctx, tx, &st)
	})
	if err != nil {
		return domain.Stack{}, err
	}
	s.log.Info("stack imported", "stack_id", st.ID, "environment_id", st.EnvironmentID, "project", st.Name,
		"in_place", len(r.Files) == 0, "hash", snap.Hash)
	s.publish(EventCreated, st, map[string]string{"origin": domain.StackOriginImported})
	return st, nil
}

// relativeTo makes label paths relative to the project directory.
func relativeTo(dir string, files []string) []string {
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimPrefix(f, dir+"/"))
	}
	return out
}

// Update applies a metadata patch when expectRevision is current.
func (s *Service) Update(ctx context.Context, id string, expectRevision int64, p domain.StackPatch) (domain.Stack, error) {
	var st domain.Stack
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if st, err = store.GetStack(ctx, tx, id); err != nil {
			return err
		}
		if st.Revision != expectRevision {
			return domain.ErrStackRevisionStale
		}
		if p.DisplayName != nil {
			st.DisplayName = strings.TrimSpace(*p.DisplayName)
		}
		if p.Description != nil {
			st.Meta.Description = strings.TrimSpace(*p.Description)
		}
		if err := checkMeta(st.DisplayName, st.Meta); err != nil {
			return err
		}
		if p.Links != nil {
			if st.Links, err = domain.NormalizeLinks(*p.Links); err != nil {
				return err
			}
		}
		if st.ServiceMeta == nil {
			st.ServiceMeta = map[string]domain.DisplayMeta{}
		}
		for name, m := range p.Services {
			if name == "" || len(name) > 128 {
				return &domain.InputError{Field: "services", Message: "invalid service name"}
			}
			if err := checkMeta("", m); err != nil {
				return &domain.InputError{Field: "services." + name, Message: err.(*domain.InputError).Message} //nolint:errorlint // checkMeta returns *InputError
			}
			if m == (domain.DisplayMeta{}) {
				delete(st.ServiceMeta, name)
			} else {
				st.ServiceMeta[name] = m
			}
		}
		st.Revision++
		st.UpdatedAt = s.now()
		return store.UpdateStack(ctx, tx, &st)
	})
	if err != nil {
		return domain.Stack{}, err
	}
	s.publish(EventUpdated, st, map[string]string{"change": "metadata"})
	return st, nil
}
