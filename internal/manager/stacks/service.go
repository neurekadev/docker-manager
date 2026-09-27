// Package stacks is the manager side of Compose stacks (#7): the stack
// records, their immutable revisions, deployment intent and last observed
// Engine state, discovery and import, and the stack.* jobs.
//
// Source of truth (#25 Q1): the definition files on disk are
// authoritative. The manager records an immutable revision (content
// snapshot + hash, contents sealed at rest) at every deploy — from the
// bytes the agent actually deployed, reported in the job result — and
// whenever it observes a change: a stack creation or editor save, a file
// manager save (#15, RecordFileSave), an external edit (#23, RecordObserved,
// and the reconciliation after every agent reconnect), a revision restore.
// "Undeployed changes" is the newest observed revision differing from the
// last applied one. Restoring a revision writes its bytes back to disk
// (compare-and-set on the current hash) and only offers a deploy. Nothing
// is overwritten automatically in either direction, and automatic updates
// (#20) never touch the files.
package stacks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/agents"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Agents sends named requests to an environment's agent
// (*agents.Hub implements it).
type Agents interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
}

// Environments reads environments (*agents.Service implements it).
type Environments interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
}

// Jobs is the job engine as the stack service uses it (*jobs.Engine).
type Jobs interface {
	Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error)
	Get(ctx context.Context, id string) (domain.Job, error)
	OnFinish(kind domain.JobKind, h jobs.FinishHook)
}

// Registries selects the registry connection of an image reference and the
// connections offered to builds for their base images (#19, #33;
// *registries.Service implements it).
type Registries interface {
	Select(ctx context.Context, req domain.RegistrySelectRequest) (domain.RegistrySelection, error)
	BuildCredentials(ctx context.Context, environmentID string) (ids, ambiguous []string, err error)
	Usable(ctx context.Context, ids []string) error
}

// DefaultRequestTimeout bounds one agent request.
const DefaultRequestTimeout = 30 * time.Second

// Options configures the service.
type Options struct {
	DB           *bun.DB
	Clock        clock.Clock
	Logger       *slog.Logger
	Keyring      *secrets.Keyring
	Agents       Agents
	Environments Environments
	Jobs         Jobs
	Bus          *events.Bus
	// Registries selects registry connections for deploys (#19); nil
	// deploys anonymously.
	Registries Registries
	// Systems reads agents' reported roots (file manager roots, #15).
	Systems Systems
	// Protection reports whether a Compose project is Docker Manager's own
	// (#32; resources.Service); its deploy, stop, restart, down and removal
	// are refused. nil: no check (tests).
	Protection ProjectProtection
	// RequestTimeout bounds agent requests (default DefaultRequestTimeout).
	RequestTimeout time.Duration
}

// ProjectProtection is resources.Service.ProjectProtection (#32).
type ProjectProtection interface {
	ProjectProtection(ctx context.Context, env, project string) (*protocol.Protection, error)
}

// SetProtection installs the #32 check (the resource service is created
// after the stack service).
func (s *Service) SetProtection(p ProjectProtection) { s.opts.Protection = p }

// SetVolumeHolds installs the volumes a stack removal must keep although
// they carry the project's labels (a migrated stack's retained source,
// #35), by environment and Compose project.
func (s *Service) SetVolumeHolds(f func(ctx context.Context, environmentID, project string) ([]string, error)) {
	s.volumeHolds = f
}

// Service manages stacks.
type Service struct {
	opts Options
	db   *bun.DB
	clk  clock.Clock
	log  *slog.Logger

	volumeHolds func(ctx context.Context, environmentID, project string) ([]string, error)
}

// New creates the service and registers its job finish hooks.
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Keyring == nil || opts.Agents == nil || opts.Environments == nil || opts.Jobs == nil {
		return nil, errors.New("stacks: DB, Keyring, Agents, Environments and Jobs are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	s := &Service{opts: opts, db: opts.DB, clk: opts.Clock, log: opts.Logger}
	s.registerHooks()
	return s, nil
}

func (s *Service) now() time.Time { return s.clk.Now().UTC().Truncate(time.Microsecond) }

// Stack event types published on the bus (visibility: the stack, #17).
const (
	EventCreated  = events.StackCreated
	EventUpdated  = events.StackUpdated
	EventRemoved  = events.StackRemoved
	EventRevision = events.StackRevisionRecorded
)

func (s *Service) publish(typ string, st domain.Stack, attrs map[string]string) {
	s.opts.Bus.Publish(events.Event{Type: typ, ResourceType: events.ResourceStack, ResourceID: st.ID,
		EnvironmentID: st.EnvironmentID, Revision: st.Revision, Attributes: attrs})
}

// Get returns a stack.
func (s *Service) Get(ctx context.Context, id string) (domain.Stack, error) {
	return store.GetStack(ctx, s.db, id)
}

// List returns stacks in ID order.
func (s *Service) List(ctx context.Context, f domain.StackFilter) ([]domain.Stack, error) {
	return store.ListStacks(ctx, s.db, f)
}

// Online reports whether the stack's environment is online.
func (s *Service) Online(ctx context.Context, environmentID string) bool {
	env, err := s.opts.Environments.GetEnvironment(ctx, environmentID)
	return err == nil && env.Online && env.Status == domain.EnvironmentActive
}

// Ref builds the agent's reference of a stack's project.
func Ref(st domain.Stack) protocol.ProjectRef {
	return protocol.ProjectRef{Root: st.Root, RootPath: st.RootPath, Dir: st.Dir, ProjectName: st.Name,
		ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles}
}

// call sends a request to the environment's agent and decodes its output,
// mapping transport failures to StackErrors.
func (s *Service) call(ctx context.Context, environmentID, name string, in, out any) error {
	raw, err := s.opts.Agents.RequestEnvironment(ctx, environmentID, name, in, s.opts.RequestTimeout)
	if err != nil {
		return agentError(err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &domain.StackError{Code: domain.StackErrAgent, Message: "the agent answered with a malformed " + name + " response"}
	}
	return nil
}

func agentError(err error) error {
	var re *agents.RequestError
	switch {
	case errors.Is(err, jobs.ErrAgentOffline):
		return &domain.StackError{Code: domain.StackErrOffline, Message: "the environment's agent is offline; the stack's last known state is read-only until it reconnects"}
	case errors.Is(err, agents.ErrRequestTimeout):
		return &domain.StackError{Code: domain.StackErrAgentTimeout, Message: "the agent did not answer in time"}
	case errors.As(err, &re):
		switch re.Code {
		case protocol.CodeForbiddenPath:
			return &domain.StackError{Code: domain.StackErrRootUnavailable, Message: "the agent refuses the project directory: " + re.Message}
		case protocol.CodeConflict:
			return &domain.StackError{Code: domain.StackErrDefinitionChanged, Message: re.Message}
		case protocol.CodeTooLarge:
			return &domain.StackError{Code: domain.StackErrDefinitionTooLarge, Message: re.Message}
		case protocol.CodeUnsupportedRequest:
			return &domain.StackError{Code: domain.StackErrEnvironmentUnsupported, Message: "the environment's agent does not support stacks yet; upgrade it"}
		case protocol.CodeEngineUnavailable:
			return &domain.StackError{Code: domain.StackErrEngineUnavailable, Message: "the environment's agent cannot reach its Docker Engine"}
		}
		return &domain.StackError{Code: domain.StackErrAgent, Message: re.Message}
	}
	return err
}

// isCode reports whether err is a StackError with code.
func isCode(err error, code string) bool {
	var se *domain.StackError
	return errors.As(err, &se) && se.Code == code
}

// sealContext is the keyring context of a revision's content.
func sealContext(revisionID string) string { return "stack_revisions/" + revisionID + "/content" }

// contentJSON is the sealed plaintext of a revision: path -> base64 bytes.
type contentJSON map[string]string

// recordRevision stores snap as a new immutable revision of st inside db
// (the caller's transaction). Contents are sealed; a snapshot carrying
// hashes only reuses the content of an earlier revision with the same hash
// when there is one.
func (s *Service) recordRevision(ctx context.Context, db bun.IDB, st *domain.Stack, snap protocol.SourceSnapshot,
	source domain.RevisionSource, author authz.Principal, jobID, restoredFrom string) (domain.StackRevision, error) {
	seq, err := store.NextStackRevisionSeq(ctx, db, st.ID)
	if err != nil {
		return domain.StackRevision{}, err
	}
	rev := domain.StackRevision{ID: ids.New(), StackID: st.ID, Seq: seq, Hash: snap.Hash, Source: source,
		AuthorUserID: author.UserID, AuthorTokenID: author.TokenID, JobID: jobID, RestoredFrom: restoredFrom,
		ContentOmitted: snap.ContentOmitted, CreatedAt: s.now()}
	files := slices.Clone(snap.Files)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	content := contentJSON{}
	for _, f := range files {
		rev.Files = append(rev.Files, domain.StackFile{Path: f.Path, SHA256: f.SHA256, Size: f.Size})
		content[f.Path] = base64.StdEncoding.EncodeToString(f.Content)
	}
	var sealed string
	if snap.ContentOmitted {
		if prev, prevSealed, err := store.FindStackRevisionByHash(ctx, db, st.ID, snap.Hash); err == nil {
			plain, err := s.opts.Keyring.Open(prevSealed, sealContext(prev.ID))
			if err != nil {
				return domain.StackRevision{}, fmt.Errorf("stacks: open revision content: %w", err)
			}
			if sealed, err = s.opts.Keyring.Seal(plain, sealContext(rev.ID)); err != nil {
				return domain.StackRevision{}, err
			}
			rev.ContentOmitted = false
		}
	} else {
		plain, err := json.Marshal(content)
		if err != nil {
			return domain.StackRevision{}, err
		}
		if sealed, err = s.opts.Keyring.Seal(plain, sealContext(rev.ID)); err != nil {
			return domain.StackRevision{}, err
		}
	}
	if err := store.InsertStackRevision(ctx, db, &rev, sealed); err != nil {
		return domain.StackRevision{}, err
	}
	return rev, nil
}

// observe records snap as observed on disk when it differs from the newest
// observed revision (deduplicated by hash). It reports whether a revision
// was recorded.
func (s *Service) observe(ctx context.Context, db bun.IDB, st *domain.Stack, snap protocol.SourceSnapshot,
	source domain.RevisionSource, author authz.Principal) (*domain.StackRevision, error) {
	now := s.now()
	if st.Observed != nil && st.Observed.Hash == snap.Hash {
		st.ObservedAt = &now
		return nil, nil
	}
	rev, err := s.recordRevision(ctx, db, st, snap, source, author, "", "")
	if err != nil {
		return nil, err
	}
	st.Observed, st.ObservedAt = rev.Ref(), &now
	return &rev, nil
}

// Revisions lists a stack's revisions (newest first, metadata only).
func (s *Service) Revisions(ctx context.Context, stackID string, beforeSeq int64, limit int) ([]domain.StackRevision, error) {
	return store.ListStackRevisions(ctx, s.db, stackID, beforeSeq, limit)
}

// Revision returns a revision with its file contents (unsealed).
func (s *Service) Revision(ctx context.Context, stackID, revisionID string) (domain.StackRevision, error) {
	rev, sealed, err := store.GetStackRevision(ctx, s.db, stackID, revisionID)
	if err != nil {
		return rev, err
	}
	if sealed == "" {
		return rev, nil
	}
	plain, err := s.opts.Keyring.Open(sealed, sealContext(rev.ID))
	if err != nil {
		return rev, fmt.Errorf("stacks: open revision content: %w", err)
	}
	var content contentJSON
	if err := json.Unmarshal(plain, &content); err != nil {
		return rev, fmt.Errorf("stacks: decode revision content: %w", err)
	}
	for i, f := range rev.Files {
		b, err := base64.StdEncoding.DecodeString(content[f.Path])
		if err != nil {
			return rev, fmt.Errorf("stacks: decode revision content: %w", err)
		}
		rev.Files[i].Content = b
	}
	return rev, nil
}

// tx runs fn in a transaction.
func (s *Service) tx(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	return s.db.RunInTx(ctx, nil, fn)
}

// Root resolves where a stack's files live, for the file manager (#15) and
// backups (#10).
func (s *Service) Root(ctx context.Context, stackID string) (domain.StackRoot, error) {
	st, err := store.GetStack(ctx, s.db, stackID)
	if err != nil {
		return domain.StackRoot{}, err
	}
	r := domain.StackRoot{StackID: st.ID, EnvironmentID: st.EnvironmentID, ProjectName: st.Name, Root: st.Root,
		RootPath: st.RootPath, Dir: st.Dir}
	if st.Observed != nil {
		if rev, _, err := store.GetStackRevision(ctx, s.db, st.ID, st.Observed.ID); err == nil {
			for _, f := range rev.Files {
				r.DefinitionFiles = append(r.DefinitionFiles, f.Path)
			}
		}
	}
	return r, nil
}

// servicesFrom converts validated or deployed services.
func servicesFrom(in []protocol.ComposeService) []domain.StackServiceDef {
	out := make([]domain.StackServiceDef, 0, len(in))
	for _, s := range in {
		d := domain.StackServiceDef{Name: s.Name, Image: s.Image, Build: s.Build, PullPolicy: s.PullPolicy}
		for _, dep := range s.DependsOn {
			d.DependsOn = append(d.DependsOn, domain.StackDependency{Service: dep.Service, Condition: dep.Condition, Required: dep.Required, Restart: dep.Restart})
		}
		out = append(out, d)
	}
	return out
}

func bindsFrom(in []protocol.ComposeBind) []domain.StackBind {
	out := make([]domain.StackBind, 0, len(in))
	for _, b := range in {
		out = append(out, domain.StackBind{Service: b.Service, Source: b.Source, Target: b.Target, RelPath: b.RelPath,
			External: b.External, ReadOnly: b.ReadOnly})
	}
	return out
}

func statesFrom(in []protocol.ServiceState) []domain.StackServiceState {
	out := make([]domain.StackServiceState, 0, len(in))
	for _, s := range in {
		out = append(out, domain.StackServiceState{Service: s.Service, Containers: s.Containers, Running: s.Running, ImageIDs: s.ImageIDs})
	}
	return out
}

// importLabelMeta fills empty service display metadata from the
// dev.neureka.docker-manager.* labels (never overwriting the user's metadata).
func importLabelMeta(st *domain.Stack, services []protocol.ComposeService) {
	if st.ServiceMeta == nil {
		st.ServiceMeta = map[string]domain.DisplayMeta{}
	}
	for _, sv := range services {
		m := st.ServiceMeta[sv.Name]
		if m.Description == "" {
			m.Description = sv.Description
		}
		if m.Icon == "" {
			m.Icon = sv.Icon
		}
		if m != (domain.DisplayMeta{}) {
			st.ServiceMeta[sv.Name] = m
		}
	}
}

// engineStateOf summarizes observed service states.
func engineStateOf(states []domain.StackServiceState) domain.StackEngineState {
	total, running := 0, 0
	for _, s := range states {
		total += s.Containers
		running += s.Running
	}
	switch {
	case total == 0:
		return domain.EngineStateMissing
	case running == total:
		return domain.EngineStateRunning
	case running == 0:
		return domain.EngineStateStopped
	}
	return domain.EngineStatePartial
}

func (s *Service) setEngine(st *domain.Stack, states []domain.StackServiceState) {
	now := s.now()
	st.EngineServices = states
	st.EngineState = engineStateOf(states)
	st.EngineObservedAt = &now
}
