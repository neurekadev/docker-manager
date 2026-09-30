// Package migrations moves stacks and volumes between environments (#35).
//
// A migration is a manager-executed job (stack.migrate, volume.migrate)
// that relays data from the source agent to the destination agent over
// their existing sessions: agents only dial out (#27), so there is no
// agent-to-agent connection. The manager opens a migration.send stream on
// the source and a migration.receive stream on the destination per part
// (the project directory, each volume, locally built images) and copies
// between them with end-to-end backpressure, a bandwidth cap
// (DOCKER_MANAGER_MIGRATION_BANDWIDTH_LIMIT) and bounded memory (one stream
// window plus a copy buffer per part; nothing on disk). Every part is
// checksummed per chunk and as a whole (internal/transfer) and compared
// across source, manager and destination.
//
// A stack migration is cold: preview (blockers and warnings computed
// before anything stops) -> stop the source in reverse dependency order ->
// copy -> commit the new project directory -> move the stack record to
// the destination (cut-over; stack-scoped permission rules follow the
// stack ID) -> deploy it there as a stack.deploy job (dependency order and
// conditions, #7) -> finish. The source stays stopped and untouched until
// the user confirms its removal (stack.remove_source); a migration that
// stops before finishing puts the stack record back and restarts the
// source's previously running services (compensation start_source), and
// the destination's partial data is removed by the next migration of the
// same stack.
package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/regclient"
	"github.com/neurekadev/docker-manager/internal/manager/registries"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/transfer"
)

// Agents reaches environments' agents (*agents.Hub).
type Agents interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
	OpenStream(ctx context.Context, environmentID, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error)
	Online(environmentID string) bool
}

// Environments reads environments and their agents' capabilities
// (*agents.Service).
type Environments interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	EnvironmentSystem(ctx context.Context, id string) (domain.EnvironmentSystem, error)
}

// Jobs is the job engine (*jobs.Engine).
type Jobs interface {
	Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error)
	Get(ctx context.Context, id string) (domain.Job, error)
	Subscribe(jobID string) (<-chan struct{}, func())
	OnFinish(kind domain.JobKind, h jobs.FinishHook)
	RegisterManagerExecutor(x jobexec.Executor) error
	Cancel(ctx context.Context, id string) (domain.Job, error)
}

// Stacks is the stack service (*stacks.Service).
type Stacks interface {
	Get(ctx context.Context, id string) (domain.Stack, error)
	List(ctx context.Context, f domain.StackFilter) ([]domain.Stack, error)
	Place(ctx context.Context, db bun.IDB, stackID string, p domain.StackPlacement) (domain.Stack, error)
	Published(st domain.Stack, attrs map[string]string)
	Deploy(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackDeployOptions) (domain.Job, error)
	FindByName(ctx context.Context, environmentID, name string) (domain.Stack, error)
}

// Registries checks image references on registry connections
// (*registries.Service).
type Registries interface {
	Check(ctx context.Context, req registries.CheckRequest) (registries.CheckResult, error)
}

// Auditor records audit events inside a transaction (*audit.Log).
type Auditor interface {
	RecordTx(ctx context.Context, db bun.IDB, ev domain.AuditEvent) error
}

// ActionMigrationFinished is the audit action of a migration's outcome:
// source, destination, size, every part's checksum and the result.
const ActionMigrationFinished = "migration.finished"

// MovedHook is called in the transaction that completes a stack migration
// (policies that target the stack follow it: updates #20, backups #10).
type MovedHook func(ctx context.Context, db bun.IDB, stackID, fromEnvironment, toEnvironment string) error

// Options configures the service.
type Options struct {
	DB           *bun.DB
	Clock        clock.Clock
	Logger       *slog.Logger
	Agents       Agents
	Environments Environments
	Jobs         Jobs
	Stacks       Stacks
	// Registries checks pullability on the destination (nil: unchecked).
	Registries Registries
	// Permissions computes access changes (nil: unavailable).
	Permissions Permissions
	// Authorizer re-checks the destination's capabilities when the job
	// runs.
	Authorizer authz.Authorizer
	// Audit records each migration's outcome (nil: not recorded, tests).
	Audit   Auditor
	Catalog *catalog.Catalog
	// BandwidthLimit caps relays in bytes per second (0: unlimited); one
	// cap shared by every running migration.
	BandwidthLimit int64
	// RequestTimeout bounds preview requests (default 2 min); StopTimeout
	// the source stop and restart (default 15 min).
	RequestTimeout time.Duration
	StopTimeout    time.Duration
	// ReconnectWait is how long a transfer waits for a disconnected agent
	// before the job is interrupted (default 2 min); PartAttempts bounds
	// the attempts per part (default 3).
	ReconnectWait time.Duration
	PartAttempts  int
	// RelayBuffer overrides DefaultRelayBuffer (tests).
	RelayBuffer int
}

// Service runs migrations.
type Service struct {
	opts    Options
	db      *bun.DB
	clk     clock.Clock
	log     *slog.Logger
	limiter *transfer.Limiter
	cat     *catalog.Catalog

	mu    sync.Mutex
	moved []MovedHook
}

// New creates the service, registers the manager executors of
// stack.migrate, volume.migrate and environment.migrate and the finish
// hooks.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Agents == nil || o.Environments == nil || o.Jobs == nil || o.Stacks == nil {
		return nil, errors.New("migrations: DB, Agents, Environments, Jobs and Stacks are required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Catalog == nil {
		o.Catalog = catalog.Default()
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 2 * time.Minute
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = 15 * time.Minute
	}
	if o.ReconnectWait <= 0 {
		o.ReconnectWait = 2 * time.Minute
	}
	if o.PartAttempts <= 0 {
		o.PartAttempts = 3
	}
	o.Authorizer = authz.OrDenyAll(o.Authorizer)
	s := &Service{opts: o, db: o.DB, clk: o.Clock, log: o.Logger.With("component", "migrations"),
		limiter: transfer.NewLimiter(o.Clock, o.BandwidthLimit), cat: o.Catalog}
	for _, x := range []jobexec.Executor{s.stackExecutor(), s.volumeExecutor(), s.environmentExecutor()} {
		if err := o.Jobs.RegisterManagerExecutor(x); err != nil {
			return nil, err
		}
	}
	o.Jobs.OnFinish(jobspec.StackMigrate, s.onMigrationFinished)
	o.Jobs.OnFinish(jobspec.VolumeMigrate, s.onMigrationFinished)
	o.Jobs.OnFinish(jobspec.StackRemoveSource, s.onRemovalFinished)
	o.Jobs.OnFinish(jobspec.EnvironmentMigrate, s.onEnvironmentMigrationFinished)
	return s, nil
}

// OnStackMoved registers a hook run in the transaction that completes a
// stack migration (#10/#20 policies targeting the stack follow it).
func (s *Service) OnStackMoved(h MovedHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.moved = append(s.moved, h)
}

func (s *Service) now() time.Time { return s.clk.Now().UTC().Truncate(time.Microsecond) }

// Get returns a migration.
func (s *Service) Get(ctx context.Context, id string) (domain.Migration, error) {
	return store.GetMigration(ctx, s.db, id)
}

// StackMigrations lists a stack's migrations (newest first).
func (s *Service) StackMigrations(ctx context.Context, stackID string) ([]domain.Migration, error) {
	return store.ListStackMigrations(ctx, s.db, stackID)
}

// Errors of the service (the API maps them).
var (
	// ErrBlocked: the preview has blockers (BlockedError carries them).
	ErrBlocked = errors.New("migration blocked by its preflight check")
	// ErrNotCompleted: the source can be removed only after a completed
	// stack migration, once.
	ErrNotCompleted = errors.New("the migration is not completed")
	// ErrSourceRemoved: the source was already removed (or its removal
	// runs).
	ErrSourceRemoved = errors.New("the source was already removed")
	// ErrSourceInUse: a Docker Manager stack manages the source project again.
	ErrSourceInUse = errors.New("a Docker Manager stack manages the source project again")
)

// BlockedError is ErrBlocked with the plan.
type BlockedError struct{ Plan Plan }

func (e *BlockedError) Error() string {
	var codes []string
	for _, b := range e.Plan.Blockers {
		codes = append(codes, b.Code)
	}
	return ErrBlocked.Error() + ": " + strings.Join(codes, ", ")
}

// Unwrap returns ErrBlocked.
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// call sends a request and decodes its output.
func (s *Service) call(ctx context.Context, env, name string, in, out any, timeout time.Duration) error {
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, name, in, timeout)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("the agent answered %s with a malformed response", name)
	}
	return nil
}

// capabilities returns an environment's agent capabilities (zero value
// when unknown).
func (s *Service) capabilities(ctx context.Context, env string) protocol.CapabilitiesPayload {
	var c protocol.CapabilitiesPayload
	sys, err := s.opts.Environments.EnvironmentSystem(ctx, env)
	if err != nil || sys.Agent == nil || sys.Agent.Capabilities == "" {
		return c
	}
	_ = json.Unmarshal([]byte(sys.Agent.Capabilities), &c)
	return c
}

// migrationRequests are what an agent must serve to take part.
var migrationRequests = []string{protocol.ReqMigrationPreview, protocol.ReqMigrationStop, protocol.ReqMigrationStart,
	protocol.ReqMigrationCommit, protocol.ReqMigrationCleanup}

func supports(c protocol.CapabilitiesPayload, destination bool) bool {
	for _, r := range migrationRequests {
		if !slices.Contains(c.Requests, r) {
			return false
		}
	}
	kind := protocol.StreamMigrationSend
	if destination {
		kind = protocol.StreamMigrationReceive
	}
	return slices.Contains(c.Streams, kind)
}

// envCheck is the environment facts a preview needs.
type envCheck struct {
	online, supports, plainHTTP bool
}

func (s *Service) envState(ctx context.Context, env string, destination bool) (envCheck, error) {
	e, err := s.opts.Environments.GetEnvironment(ctx, env)
	if err != nil {
		return envCheck{}, err
	}
	if e.Status == domain.EnvironmentArchived {
		return envCheck{}, domain.ErrEnvironmentArchived
	}
	c := s.capabilities(ctx, env)
	return envCheck{online: e.Online && s.opts.Agents.Online(env), supports: supports(c, destination), plainHTTP: c.Transport.PlainHTTP}, nil
}

// StackRequest is a stack migration preview or start.
type StackRequest struct {
	TargetEnvironmentID string
	Selection           Selection
	// TimeoutSeconds is the stop grace period of the source's containers.
	TimeoutSeconds int
	IdempotencyKey string
}

// VolumeRequest is a volume migration preview or start.
type VolumeRequest struct {
	TargetEnvironmentID string
	TargetName          string
	// AcknowledgeCrashConsistency allows copying a volume in use.
	AcknowledgeCrashConsistency bool
	IdempotencyKey              string
}

// gathered is a preview with the facts behind it.
type gathered struct {
	plan   Plan
	source *protocol.MigrationSourceFacts
	target *protocol.MigrationDestinationFacts
}

// targetDir is the new project directory: the project name.
func targetDir(st domain.Stack) string { return st.Name }

// PreviewStack computes a stack migration's preview for caller p (owner:
// whether p is the instance owner, who sees every affected user).
func (s *Service) PreviewStack(ctx context.Context, p authz.Principal, owner bool, st domain.Stack, r StackRequest) (Plan, error) {
	g, err := s.previewStack(ctx, st, r, true)
	if err != nil {
		return Plan{}, err
	}
	if g.source != nil && g.source.Project != nil {
		checks := stackChecks(s.cat, st.ID, st.EnvironmentID, r.TargetEnvironmentID, g.source.Project, g.plan.Volumes)
		g.plan.Access = accessPreview(ctx, s.opts.Permissions, p, owner, checks)
	}
	return g.plan, nil
}

func (s *Service) previewStack(ctx context.Context, st domain.Stack, r StackRequest, measure bool) (gathered, error) {
	src, err := s.envState(ctx, st.EnvironmentID, false)
	if err != nil {
		return gathered{}, err
	}
	dst, err := s.envState(ctx, r.TargetEnvironmentID, true)
	if err != nil {
		return gathered{}, err
	}
	in := PreflightInput{Kind: domain.MigrationKindStack, StackID: st.ID, SourceEnvironmentID: st.EnvironmentID,
		TargetEnvironmentID: r.TargetEnvironmentID, SourceOnline: src.online, TargetOnline: dst.online,
		SourceSupports: src.supports, TargetSupports: dst.supports, SourcePlainHTTP: src.plainHTTP, TargetPlainHTTP: dst.plainHTTP,
		TargetDir: targetDir(st), Selection: r.Selection, BandwidthLimit: s.opts.BandwidthLimit, StopGraceSecond: int64(r.TimeoutSeconds)}
	if st.EnvironmentID == r.TargetEnvironmentID {
		return gathered{plan: Evaluate(in)}, nil
	}
	if _, err := s.opts.Stacks.FindByName(ctx, r.TargetEnvironmentID, st.Name); err == nil {
		in.StackNameTaken = true
	}
	leftovers, err := s.leftovers(ctx, r.TargetEnvironmentID, func(m domain.Migration) bool { return m.StackID == st.ID })
	if err != nil {
		return gathered{}, err
	}
	in.Leftovers = leftovers
	if src.online && src.supports && dst.online && dst.supports {
		var out protocol.MigrationPreviewOutput
		ref := protocol.ProjectRef{Root: st.Root, RootPath: st.RootPath, Dir: st.Dir, ProjectName: st.Name, ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles}
		if err := s.call(ctx, st.EnvironmentID, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{Role: protocol.RoleSource,
			Source: &protocol.MigrationSourceQuery{Stack: &ref, Measure: measure}}, &out, s.opts.RequestTimeout); err != nil {
			return gathered{}, agentErr("source", err)
		}
		in.Source = out.Source
		if in.Source == nil || in.Source.Project == nil {
			return gathered{}, errors.New("the source agent returned no project facts")
		}
		q := destinationQuery(in.Source.Project, in.TargetDir, r.Selection)
		out = protocol.MigrationPreviewOutput{}
		if err := s.call(ctx, r.TargetEnvironmentID, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{Role: protocol.RoleDestination,
			Destination: &q}, &out, s.opts.RequestTimeout); err != nil {
			return gathered{}, agentErr("destination", err)
		}
		in.Target = out.Destination
		in.Registry = s.registryChecks(ctx, st.ID, r.TargetEnvironmentID, in.Target.Platform, in.Source.Project, in.Target.ImagesPresent, r.Selection)
	}
	return gathered{plan: Evaluate(in), source: in.Source, target: in.Target}, nil
}

// destinationQuery lists what the stack would create on the destination.
func destinationQuery(p *protocol.MigrationProjectFacts, dir string, sel Selection) protocol.MigrationDestinationQuery {
	q := protocol.MigrationDestinationQuery{ProjectName: p.Name, Dir: dir}
	for _, s := range p.Services {
		q.ContainerNames = append(q.ContainerNames, s.ContainerNames...)
		q.Ports = append(q.Ports, s.Ports...)
		q.Images = append(q.Images, s.Image)
	}
	for _, v := range p.Volumes {
		switch {
		case v.External:
			q.ExternalVolumes = append(q.ExternalVolumes, v.Name)
		case v.Anonymous:
			if slices.Contains(sel.AnonymousVolumes, v.Name) {
				q.Volumes = append(q.Volumes, v.Name)
			}
		default:
			q.Volumes = append(q.Volumes, v.Name)
		}
	}
	for _, n := range p.Networks {
		if n.External {
			q.ExternalNetworks = append(q.ExternalNetworks, n.Name)
		} else {
			q.Networks = append(q.Networks, n.Name)
		}
	}
	slices.Sort(q.Images)
	q.Images = slices.Compact(q.Images)
	return q
}

// registryChecks checks the images the destination would pull (#19).
func (s *Service) registryChecks(ctx context.Context, stackID, env, platform string, p *protocol.MigrationProjectFacts, present []string, sel Selection) map[string]RegistryCheck {
	out := map[string]RegistryCheck{}
	if s.opts.Registries == nil {
		return out
	}
	for _, svc := range p.Services {
		if svc.Build || slices.Contains(present, svc.Image) || slices.Contains(sel.TransferImages, svc.Image) {
			continue
		}
		if _, done := out[svc.Image]; done || (svc.ImageID != "" && len(svc.RepoDigests) == 0) {
			continue
		}
		res, err := s.opts.Registries.Check(ctx, registries.CheckRequest{
			RegistrySelectRequest: domain.RegistrySelectRequest{Reference: svc.Image, EnvironmentID: env, StackID: stackID},
			Platform:              archOf(platform)})
		rc := RegistryCheck{SinglePlatform: err == nil && res.Result.PlatformDigest == res.Result.Digest}
		if res.Selection.Selected != nil {
			rc.ConnectionID = res.Selection.Selected.ID
		}
		if err != nil {
			rc.Class, rc.Message = registryClass(err), err.Error()
		}
		out[svc.Image] = rc
	}
	return out
}

func registryClass(err error) string {
	var amb *domain.AmbiguousRegistryError
	switch {
	case errors.As(err, &amb):
		return "ambiguous_registry_connection"
	case errors.Is(err, domain.ErrRegistryConnectionRevoked):
		return "registry_connection_revoked"
	}
	if c := regclient.ClassOf(err); c != "" {
		return c
	}
	return "registry_unavailable"
}

// leftovers returns earlier unsuccessful migrations to env matching fn.
func (s *Service) leftovers(ctx context.Context, env string, fn func(domain.Migration) bool) ([]domain.Migration, error) {
	all, err := store.PartialMigrations(ctx, s.db, env)
	if err != nil {
		return nil, err
	}
	var out []domain.Migration
	for _, m := range all {
		if fn(m) {
			out = append(out, m)
		}
	}
	return out, nil
}

// PreviewVolume computes a volume migration's preview.
func (s *Service) PreviewVolume(ctx context.Context, p authz.Principal, owner bool, env, volume string, r VolumeRequest) (Plan, error) {
	g, err := s.previewVolume(ctx, env, volume, r, true)
	if err != nil {
		return Plan{}, err
	}
	target := r.TargetName
	if target == "" {
		target = volume
	}
	g.plan.Access = accessPreview(ctx, s.opts.Permissions, p, owner, volumeChecks(s.cat, env, r.TargetEnvironmentID, volume, target))
	return g.plan, nil
}

func (s *Service) previewVolume(ctx context.Context, env, volume string, r VolumeRequest, measure bool) (gathered, error) {
	if r.TargetName != "" && !protocol.ValidDockerName(r.TargetName) {
		return gathered{}, &domain.InputError{Field: "targetName", Message: "invalid volume name"}
	}
	src, err := s.envState(ctx, env, false)
	if err != nil {
		return gathered{}, err
	}
	dst, err := s.envState(ctx, r.TargetEnvironmentID, true)
	if err != nil {
		return gathered{}, err
	}
	target := r.TargetName
	if target == "" {
		target = volume
	}
	in := PreflightInput{Kind: domain.MigrationKindVolume, SourceEnvironmentID: env, TargetEnvironmentID: r.TargetEnvironmentID,
		SourceOnline: src.online, TargetOnline: dst.online, SourceSupports: src.supports, TargetSupports: dst.supports,
		SourcePlainHTTP: src.plainHTTP, TargetPlainHTTP: dst.plainHTTP, BandwidthLimit: s.opts.BandwidthLimit,
		Selection: Selection{TargetName: r.TargetName, AcknowledgeCrashConsistency: r.AcknowledgeCrashConsistency}}
	if env == r.TargetEnvironmentID {
		return gathered{plan: Evaluate(in)}, nil
	}
	leftovers, err := s.leftovers(ctx, r.TargetEnvironmentID, func(m domain.Migration) bool {
		return m.Kind == domain.MigrationKindVolume && slices.Contains(m.VolumeTargets(), target)
	})
	if err != nil {
		return gathered{}, err
	}
	in.Leftovers = leftovers
	if src.online && src.supports && dst.online && dst.supports {
		var out protocol.MigrationPreviewOutput
		if err := s.call(ctx, env, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{Role: protocol.RoleSource,
			Source: &protocol.MigrationSourceQuery{Volume: volume, Measure: measure}}, &out, s.opts.RequestTimeout); err != nil {
			return gathered{}, agentErr("source", err)
		}
		in.Source = out.Source
		if in.Source == nil || in.Source.Volume == nil {
			return gathered{}, errors.New("the source agent returned no volume facts")
		}
		out = protocol.MigrationPreviewOutput{}
		if err := s.call(ctx, r.TargetEnvironmentID, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{Role: protocol.RoleDestination,
			Destination: &protocol.MigrationDestinationQuery{Volumes: []string{target}}}, &out, s.opts.RequestTimeout); err != nil {
			return gathered{}, agentErr("destination", err)
		}
		in.Target = out.Destination
	}
	return gathered{plan: Evaluate(in), source: in.Source, target: in.Target}, nil
}

// AgentError is a failed preview request to one side.
type AgentError struct {
	Side string
	Err  error
	// Offline: the agent is not connected; Timeout: it did not answer.
	Offline, Timeout bool
}

func (e *AgentError) Error() string { return "the " + e.Side + " agent: " + e.Err.Error() }

// Unwrap returns the transport error.
func (e *AgentError) Unwrap() error { return e.Err }

func agentErr(side string, err error) error {
	return &AgentError{Side: side, Err: err, Offline: errors.Is(err, jobs.ErrAgentOffline), Timeout: errors.Is(err, protocol.ErrRequestTimeout)}
}

// stackJobInput is the stack.migrate job input.
type stackJobInput struct {
	StackID        string                       `json:"stackId"`
	Target         string                       `json:"target"`
	Source         domain.MigrationSource       `json:"source"`
	ConfigFiles    []string                     `json:"configFiles,omitempty"`
	EnvFiles       []string                     `json:"envFiles,omitempty"`
	TargetDir      string                       `json:"targetDir"`
	Volumes        []domain.MigrationVolume     `json:"volumes,omitempty"`
	VolumeLabels   map[string]map[string]string `json:"volumeLabels,omitempty"`
	Images         []string                     `json:"images,omitempty"`
	Selection      Selection                    `json:"selection"`
	TimeoutSeconds int                          `json:"timeoutSeconds,omitempty"`
}

// StartStack re-runs the preview and, without blockers, enqueues the
// stack.migrate job (the migration ID is the job ID).
func (s *Service) StartStack(ctx context.Context, p authz.Principal, st domain.Stack, r StackRequest) (domain.Job, domain.Migration, error) {
	g, err := s.previewStack(ctx, st, r, true)
	if err != nil {
		return domain.Job{}, domain.Migration{}, err
	}
	if !g.plan.Allowed() {
		return domain.Job{}, domain.Migration{}, &BlockedError{Plan: g.plan}
	}
	in := stackJobInput{StackID: st.ID, Target: r.TargetEnvironmentID, TargetDir: g.plan.TargetDir, Selection: r.Selection,
		Source:      domain.MigrationSource{Root: st.Root, RootPath: st.RootPath, Dir: st.Dir, Project: st.Name},
		ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles, TimeoutSeconds: r.TimeoutSeconds, VolumeLabels: map[string]map[string]string{}}
	targets := []domain.JobTarget{{Type: domain.TargetStack, ID: st.ID}}
	for _, v := range g.plan.Volumes {
		if v.Action != VolumeCopy {
			continue
		}
		in.Volumes = append(in.Volumes, domain.MigrationVolume{Source: v.Source, Target: v.Target, Anonymous: v.Anonymous})
		if len(v.Labels) > 0 {
			in.VolumeLabels[v.Target] = v.Labels
		}
		targets = append(targets, domain.JobTarget{Type: domain.TargetVolume, ID: v.Source},
			domain.JobTarget{Type: domain.TargetVolume, ID: v.Target, EnvironmentID: r.TargetEnvironmentID})
	}
	for _, sp := range g.plan.Services {
		if sp.Action == ImageTransfer && !slices.Contains(in.Images, sp.Image) {
			in.Images = append(in.Images, sp.Image)
		}
	}
	j, created, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.StackMigrate, Principal: p, EnvironmentID: st.EnvironmentID,
		Targets: targets, Input: in, IdempotencyKey: r.IdempotencyKey})
	if err != nil {
		return domain.Job{}, domain.Migration{}, err
	}
	m := domain.Migration{ID: j.ID, Kind: domain.MigrationKindStack, StackID: st.ID, SourceEnvironmentID: st.EnvironmentID,
		TargetEnvironmentID: r.TargetEnvironmentID, Source: in.Source, TargetDir: in.TargetDir, Volumes: in.Volumes, Images: in.Images,
		State: domain.MigrationRunning, CreatedAt: s.now(), UpdatedAt: s.now()}
	if created {
		if err := s.ensureRecord(ctx, s.db, &m); err != nil {
			return j, m, err
		}
	} else if existing, err := store.GetMigration(ctx, s.db, j.ID); err == nil {
		m = existing
	}
	s.log.Info("stack migration queued", "migration_id", j.ID, "stack_id", st.ID, "from", st.EnvironmentID, "to", r.TargetEnvironmentID)
	return j, m, nil
}

// volumeJobInput is the volume.migrate job input.
type volumeJobInput struct {
	Volume string            `json:"volume"`
	Target string            `json:"target"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Ack    bool              `json:"acknowledgeCrashConsistency,omitempty"`
}

// StartVolume re-runs the preview and enqueues the volume.migrate job.
func (s *Service) StartVolume(ctx context.Context, p authz.Principal, env, volume string, r VolumeRequest) (domain.Job, domain.Migration, error) {
	g, err := s.previewVolume(ctx, env, volume, r, true)
	if err != nil {
		return domain.Job{}, domain.Migration{}, err
	}
	if !g.plan.Allowed() {
		return domain.Job{}, domain.Migration{}, &BlockedError{Plan: g.plan}
	}
	vp := g.plan.Volumes[0]
	in := volumeJobInput{Volume: volume, Target: r.TargetEnvironmentID, Name: vp.Target, Labels: vp.Labels, Ack: r.AcknowledgeCrashConsistency}
	j, created, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.VolumeMigrate, Principal: p, EnvironmentID: env,
		Targets: []domain.JobTarget{{Type: domain.TargetVolume, ID: volume},
			{Type: domain.TargetVolume, ID: vp.Target, EnvironmentID: r.TargetEnvironmentID}},
		Input: in, IdempotencyKey: r.IdempotencyKey})
	if err != nil {
		return domain.Job{}, domain.Migration{}, err
	}
	m := domain.Migration{ID: j.ID, Kind: domain.MigrationKindVolume, SourceEnvironmentID: env, TargetEnvironmentID: r.TargetEnvironmentID,
		Volumes: []domain.MigrationVolume{{Source: volume, Target: vp.Target}}, State: domain.MigrationRunning, CreatedAt: s.now(), UpdatedAt: s.now()}
	if created {
		if err := s.ensureRecord(ctx, s.db, &m); err != nil {
			return j, m, err
		}
	} else if existing, err := store.GetMigration(ctx, s.db, j.ID); err == nil {
		m = existing
	}
	s.log.Info("volume migration queued", "migration_id", j.ID, "volume", volume, "from", env, "to", r.TargetEnvironmentID)
	return j, m, nil
}

// ensureRecord inserts the record unless it exists: the executor and the
// request that queued its job may both create it, at the same time.
func (s *Service) ensureRecord(ctx context.Context, db bun.IDB, m *domain.Migration) error {
	if _, err := store.GetMigration(ctx, db, m.ID); err == nil {
		return nil
	}
	err := store.InsertMigration(ctx, db, m)
	if _, gerr := store.GetMigration(ctx, db, m.ID); err != nil && gerr == nil {
		return nil // the other one created it first
	}
	return err
}

// RemoveSource enqueues stack.remove_source for a completed migration the
// user confirms: the source project's containers and networks, the
// migrated volumes and the project directory are removed from the source.
func (s *Service) RemoveSource(ctx context.Context, p authz.Principal, stackID, migrationID, key string) (domain.Job, error) {
	m, err := store.GetMigration(ctx, s.db, migrationID)
	if err != nil {
		return domain.Job{}, err
	}
	if m.Kind != domain.MigrationKindStack || m.StackID != stackID {
		return domain.Job{}, domain.ErrMigrationNotFound
	}
	switch {
	case m.State == domain.MigrationSourceRemoved:
		return domain.Job{}, ErrSourceRemoved
	case m.State != domain.MigrationCompleted:
		return domain.Job{}, ErrNotCompleted
	}
	if m.RemovalJobID != "" {
		if j, err := s.opts.Jobs.Get(ctx, m.RemovalJobID); err == nil && !j.State.Terminal() {
			return j, nil
		}
	}
	if other, err := s.opts.Stacks.FindByName(ctx, m.SourceEnvironmentID, m.Source.Project); err == nil && other.ID != "" {
		return domain.Job{}, ErrSourceInUse
	}
	in := protocol.SourceRemovalInput{StackID: stackID, MigrationID: m.ID,
		Stack: protocol.ProjectRef{Root: m.Source.Root, RootPath: m.Source.RootPath, Dir: m.Source.Dir, ProjectName: m.Source.Project}}
	targets := []domain.JobTarget{{Type: domain.TargetStack, ID: stackID}}
	for _, v := range m.Volumes {
		if v.Copied {
			in.Volumes = append(in.Volumes, v.Source)
			targets = append(targets, domain.JobTarget{Type: domain.TargetVolume, ID: v.Source})
		}
	}
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.StackRemoveSource, Principal: p, EnvironmentID: m.SourceEnvironmentID,
		Targets: targets, Input: in, IdempotencyKey: key})
	if err != nil {
		return domain.Job{}, err
	}
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetMigration(ctx, tx, m.ID)
		if err != nil {
			return err
		}
		cur.RemovalJobID, cur.UpdatedAt = j.ID, s.now()
		return store.UpdateMigration(ctx, tx, &cur)
	})
	return j, err
}

func (s *Service) tx(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	return s.db.RunInTx(ctx, nil, fn)
}

// onMigrationFinished records a migration's outcome from its job.
func (s *Service) onMigrationFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	m, err := store.GetMigration(ctx, db, j.ID)
	if errors.Is(err, domain.ErrMigrationNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.now()
	switch {
	case j.State == domain.JobSucceeded:
		m.State = domain.MigrationCompleted
	case m.CutOver && m.Kind == domain.MigrationKindStack && m.State == domain.MigrationCompleted:
		// Completed in finalize; only the job's own bookkeeping failed.
	case j.State == domain.JobCancelled:
		m.State = domain.MigrationCancelled
	case j.State == domain.JobInterrupted:
		m.State = domain.MigrationInterrupted
	default:
		m.State = domain.MigrationFailed
	}
	m.UpdatedAt, m.FinishedAt = now, &now
	if err := store.UpdateMigration(ctx, db, &m); err != nil {
		return err
	}
	return s.auditOutcome(ctx, db, j, m)
}

// auditOutcome records a migration's outcome (#30): source and
// destination, what moved, the bytes and checksum of every verified part,
// and where the migration ended. Never contents or error messages.
func (s *Service) auditOutcome(ctx context.Context, db bun.IDB, j domain.Job, m domain.Migration) error {
	if s.opts.Audit == nil {
		return nil
	}
	outcome := domain.AuditFailure
	if m.State == domain.MigrationCompleted {
		outcome = domain.AuditSuccess
	}
	parts := make([]map[string]any, 0, len(m.Parts))
	for _, p := range m.Parts {
		parts = append(parts, map[string]any{"name": p.Name, "bytes": p.Bytes, "sha256": p.SHA256, "chunks": p.Chunks})
	}
	volumes := make([]map[string]any, 0, len(m.Volumes))
	for _, v := range m.Volumes {
		volumes = append(volumes, map[string]any{"source": v.Source, "target": v.Target, "copied": v.Copied})
	}
	details := map[string]any{"migrationId": m.ID, "kind": string(m.Kind), "sourceEnvironmentId": m.SourceEnvironmentID,
		"targetEnvironmentId": m.TargetEnvironmentID, "state": string(m.State), "bytes": m.Bytes, "parts": parts, "volumes": volumes,
		"cutOver": m.CutOver, "targetPartial": m.TargetPartial}
	targets := []domain.AuditTarget{{Type: "job", ID: j.ID}}
	if m.StackID != "" {
		details["stackId"], details["targetDirectory"] = m.StackID, m.TargetDir
		targets = append(targets, domain.AuditTarget{Type: catalog.TypeStack, ID: m.StackID})
	}
	for _, v := range m.Volumes {
		targets = append(targets, domain.AuditTarget{Type: catalog.TypeVolume, ID: v.Source, EnvironmentID: m.SourceEnvironmentID},
			domain.AuditTarget{Type: catalog.TypeVolume, ID: v.Target, EnvironmentID: m.TargetEnvironmentID})
	}
	return s.opts.Audit.RecordTx(ctx, db, domain.AuditEvent{At: s.clk.Now(), Category: domain.AuditOperations, Action: ActionMigrationFinished,
		Actor: audit.ActorFor(principalOf(j)), EnvironmentID: m.SourceEnvironmentID, Targets: targets, Outcome: outcome,
		ErrorClass: j.ErrorClass, JobID: j.ID, Details: details})
}

// onRemovalFinished records a confirmed source removal and deletes the
// exact permission rules naming the removed containers and volumes (#17:
// a later resource with the same name must not inherit them).
func (s *Service) onRemovalFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	var in protocol.SourceRemovalInput
	if err := json.Unmarshal(j.Input, &in); err != nil || in.MigrationID == "" {
		return nil //nolint:nilerr // malformed input: nothing to record
	}
	m, err := store.GetMigration(ctx, db, in.MigrationID)
	if err != nil {
		return nil //nolint:nilerr // the record is gone
	}
	if j.State != domain.JobSucceeded {
		return nil
	}
	now := s.now()
	m.State, m.UpdatedAt = domain.MigrationSourceRemoved, now
	if err := store.UpdateMigration(ctx, db, &m); err != nil {
		return err
	}
	var out protocol.SourceRemovalOutput
	_ = json.Unmarshal(j.ResultOutput, &out)
	for _, v := range out.RemovedVolumes {
		if _, err := store.DeleteResourceRules(ctx, db, catalog.TypeVolume, m.SourceEnvironmentID, v, now); err != nil {
			return err
		}
	}
	for _, c := range out.RemovedContainers {
		if _, err := store.DeleteResourceRules(ctx, db, catalog.TypeContainer, m.SourceEnvironmentID, c, now); err != nil {
			return err
		}
	}
	return nil
}
