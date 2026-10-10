// Package stackarchives exports stacks as archives and creates stacks from
// them (#313).
//
// An export (stack.export, a manager-executed job) stops the stack, reads
// its project directory and the selected local volumes from its agent with
// the migration transfer (migration.send: verified PAX tar per part) into
// one archive file in the manager's data directory (format.go), starts the
// stack again and keeps the archive for download for a day.
//
// An import is an upload of such an archive (streamed to the data
// directory and validated on the way), a preview against the chosen
// environment and a stack.import_archive job: the stack record exists from
// the request on; the project directory goes through migration.receive and
// migration.commit into a new directory of the environment's stacks
// volume, the definition is read back (its first revision), then each
// volume is received as the volume Compose creates for the stack under its
// (possibly new) name, and the stack is deployed when asked. Until the
// stack keeps its files, a failure removes what the job wrote and forgets
// the stack.
package stackarchives

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/transfer"
)

// Agents reaches environments' agents (*agents.Hub).
type Agents interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
	OpenStream(ctx context.Context, environmentID, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error)
	Online(environmentID string) bool
	EnvironmentServes(environmentID, name string) bool
	EnvironmentHasFeature(environmentID, feature string) bool
}

// Environments reads environments (*agents.Service).
type Environments interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
}

// Jobs is the job engine (*jobs.Engine).
type Jobs interface {
	Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error)
	Get(ctx context.Context, id string) (domain.Job, error)
	List(ctx context.Context, f domain.JobFilter) ([]domain.Job, error)
	OnFinish(kind domain.JobKind, h jobs.FinishHook)
	RegisterManagerExecutor(x jobexec.Executor) error
}

// Stacks is the stack service (*stacks.Service).
type Stacks interface {
	Get(ctx context.Context, id string) (domain.Stack, error)
	FindByName(ctx context.Context, environmentID, name string) (domain.Stack, error)
	Protection(ctx context.Context, st domain.Stack) (*protocol.Protection, error)
	Deploy(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackDeployOptions) (domain.Job, error)
	CheckArchiveName(ctx context.Context, env, name, own string) error
	ReserveArchiveStack(ctx context.Context, r domain.StackFromArchive) (domain.Stack, error)
	AttachArchiveJob(ctx context.Context, stackID string, j domain.Job) (domain.Stack, error)
	DropArchiveStack(ctx context.Context, stackID string) error
	RecordArchiveStack(ctx context.Context, stackID string, p authz.Principal) (domain.Stack, error)
	ForgetArchiveStack(ctx context.Context, db bun.IDB, stackID string, j domain.Job) error
}

// Defaults.
const (
	// DefaultRetention is how long an export and an unused upload are kept.
	DefaultRetention = 24 * time.Hour
	// MaxUploadsPerUser bounds the uploads a user keeps at once.
	MaxUploadsPerUser = 5
	// spaceMargin is kept free on the manager's disk.
	spaceMargin = 256 << 20
)

// Options configures the service.
type Options struct {
	Clock        clock.Clock
	Logger       *slog.Logger
	Agents       Agents
	Environments Environments
	Jobs         Jobs
	Stacks       Stacks
	// Authorizer re-checks the initiator's other capabilities when a job
	// runs.
	Authorizer authz.Authorizer
	// Dir holds the archives (<data dir>/stack-archives).
	Dir string
	// MaxSize bounds an archive (DOCKER_MANAGER_STACK_ARCHIVE_MAX_MB).
	MaxSize int64
	// Limiter is the migrations' shared bandwidth cap (nil: unlimited).
	Limiter *transfer.Limiter
	// ManagerVersion is written into exported manifests.
	ManagerVersion string
	// Retention overrides DefaultRetention.
	Retention time.Duration
	// RequestTimeout bounds agent requests (default 2 min); StopTimeout
	// stopping and starting the stack (default 15 min); ReconnectWait the
	// wait for a disconnected agent (default 2 min); PartAttempts the
	// attempts per part (default 3).
	RequestTimeout time.Duration
	StopTimeout    time.Duration
	ReconnectWait  time.Duration
	PartAttempts   int
	// FreeBytes reports a directory's free bytes (default statfs; -1
	// unknown).
	FreeBytes func(dir string) int64
}

// Service exports and imports stack archives.
type Service struct {
	opts Options
	clk  clock.Clock
	log  *slog.Logger

	mu      sync.Mutex
	uploads map[string]*Upload // by ID
	inUse   map[string]string  // upload ID -> the stack created from it
	// pending are the uploads being written (ID -> owner); reserved the
	// bytes uploads and exports being written claim of the free space.
	pending  map[string]string
	reserved int64
	// evicting are uploads set aside to make room for one being stored.
	evicting map[string]bool
}

// New creates the service, its directories, and registers the executors
// and finish hooks of stack.export and stack.import_archive. Leftovers of
// interrupted exports and uploads are removed; complete uploads are read
// back.
func New(o Options) (*Service, error) {
	if o.Agents == nil || o.Environments == nil || o.Jobs == nil || o.Stacks == nil || o.Dir == "" {
		return nil, errors.New("stackarchives: Agents, Environments, Jobs, Stacks and Dir are required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.MaxSize <= 0 {
		o.MaxSize = 10 << 30
	}
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
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
	if o.FreeBytes == nil {
		o.FreeBytes = freeBytes
	}
	o.Authorizer = authz.OrDenyAll(o.Authorizer)
	s := &Service{opts: o, clk: o.Clock, log: o.Logger.With("component", "stackarchives"),
		uploads: map[string]*Upload{}, inUse: map[string]string{}, pending: map[string]string{},
		evicting: map[string]bool{}}
	for _, d := range []string{s.exportsDir(), s.uploadsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("stackarchives: %w", err)
		}
	}
	if err := s.loadUploads(); err != nil {
		return nil, err
	}
	s.removePartials()
	for _, x := range []jobexec.Executor{s.exportExecutor(), s.importExecutor()} {
		if err := o.Jobs.RegisterManagerExecutor(x); err != nil {
			return nil, err
		}
	}
	o.Jobs.OnFinish(jobspec.StackExport, s.onExportFinished)
	o.Jobs.OnFinish(jobspec.StackImportArchive, s.onImportFinished)
	return s, nil
}

// MaxSize is the archive size limit in bytes.
func (s *Service) MaxSize() int64 { return s.opts.MaxSize }

// Retention is how long exports and unused uploads are kept.
func (s *Service) Retention() time.Duration { return s.opts.Retention }

func (s *Service) exportsDir() string { return filepath.Join(s.opts.Dir, "exports") }
func (s *Service) uploadsDir() string { return filepath.Join(s.opts.Dir, "uploads") }

func (s *Service) now() time.Time { return s.clk.Now().UTC().Truncate(time.Microsecond) }

// partSuffix marks files being written.
const partSuffix = ".part"

// removePartials removes files an interrupted export or upload left (the
// jobs of a previous run end interrupted when the manager restarts).
func (s *Service) removePartials() {
	for _, d := range []string{s.exportsDir(), s.uploadsDir()} {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), partSuffix) {
				_ = os.Remove(filepath.Join(d, e.Name()))
			}
		}
	}
}

// Run removes expired exports and uploads every hour until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := s.clk.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.Sweep()
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
	}
}

// Sweep removes expired exports and uploads (uploads an import uses are
// kept until it ends).
func (s *Service) Sweep() {
	now := s.clk.Now()
	entries, _ := os.ReadDir(s.exportsDir())
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || strings.HasSuffix(e.Name(), partSuffix) {
			continue
		}
		if now.Sub(info.ModTime()) >= s.opts.Retention {
			if err := os.Remove(filepath.Join(s.exportsDir(), e.Name())); err == nil {
				s.log.Info("removed an expired stack archive", "file", e.Name())
			}
		}
	}
	s.mu.Lock()
	var expired []string
	for id, u := range s.uploads {
		if _, used := s.inUse[id]; !used && !now.Before(u.ExpiresAt) {
			expired = append(expired, id)
		}
	}
	s.mu.Unlock()
	for _, id := range expired {
		s.removeUpload(id)
	}
}

// free is dir's free space less what writes in progress claimed (-1
// unknown).
func (s *Service) free(dir string) int64 {
	f := s.opts.FreeBytes(dir)
	if f < 0 {
		return f
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(f-s.reserved, 0)
}

// reserve claims n bytes of dir's free space (keeping spaceMargin free)
// until release; concurrent uploads and exports cannot overcommit the
// disk the database lives on.
func (s *Service) reserve(dir string, n int64) (func(), error) {
	f := s.opts.FreeBytes(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if f >= 0 && s.reserved+n+spaceMargin > f {
		return nil, fmt.Errorf("%w (%s free, %s claimed by other transfers)", ErrNoSpace, humanize.Bytes(f), humanize.Bytes(s.reserved))
	}
	s.reserved += n
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.reserved -= n
			s.mu.Unlock()
		})
	}, nil
}

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

// sourceSupported reports whether an environment's agent can be exported
// from (the migration requests and streams).
func (s *Service) sourceSupported(env string) bool {
	for _, r := range []string{protocol.ReqMigrationPreview, protocol.ReqMigrationStop, protocol.ReqMigrationStart} {
		if !s.opts.Agents.EnvironmentServes(env, r) {
			return false
		}
	}
	return true
}

// principalOf is a job's initiator.
func principalOf(j domain.Job) authz.Principal {
	switch j.Origin {
	case domain.OriginAPIToken:
		return authz.Principal{Kind: authz.KindAPIToken, UserID: j.InitiatorUserID, TokenID: j.InitiatorTokenID}
	case domain.OriginManual:
		return authz.Principal{Kind: authz.KindUser, UserID: j.InitiatorUserID}
	}
	return authz.Service()
}

// Check is a capability on a resource.
type Check struct {
	Capability string
	Resource   authz.Resource
}

// authorize re-checks the job initiator's capabilities when the job runs.
func (s *Service) authorize(ctx context.Context, jobID string, checks []Check) error {
	j, err := s.opts.Jobs.Get(ctx, jobID)
	if err != nil {
		return err
	}
	p := principalOf(j)
	if p.IsService() {
		return nil
	}
	c := authz.For(ctx, s.opts.Authorizer, p)
	for _, ch := range checks {
		if !c.Can(ch.Capability, ch.Resource).Allowed {
			return &classed{class: domain.ErrorAuthorizationRevoked, err: fmt.Errorf("the initiator no longer holds %s", ch.Capability),
				recovery: "Nothing was kept. Ask an administrator for the grant, then start again."}
		}
	}
	return nil
}

// classed is a step failure with its own class and recovery guidance.
type classed struct {
	class, recovery string
	err             error
}

func (c *classed) Error() string      { return c.err.Error() }
func (c *classed) Unwrap() error      { return c.err }
func (c *classed) ErrorClass() string { return c.class }
func (c *classed) Recovery() string   { return c.recovery }

// Job error classes.
const (
	ClassBlocked        = "stack_archive_blocked"
	ClassTransferFailed = "transfer_failed"
	ClassTooLarge       = "archive_too_large"
	ClassStopFailed     = "stack_stop_failed"
	ClassStartFailed    = "stack_start_failed"
	ClassDeployFailed   = "deploy_failed"
	ClassInvalid        = "invalid_definition"
	ClassArchiveGone    = "archive_unavailable"
)

// waitOnline waits (bounded by ReconnectWait) until env's agent is online.
func (s *Service) waitOnline(ctx context.Context, env string) bool {
	deadline := s.clk.Now().Add(s.opts.ReconnectWait)
	t := s.clk.NewTicker(time.Second)
	defer t.Stop()
	for {
		if s.opts.Agents.Online(env) {
			return true
		}
		if !s.clk.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C():
		}
	}
}

// lostSession reports whether err is a lost agent session (a part is
// retried once the agent is back).
func lostSession(err error) bool {
	return errors.Is(err, jobs.ErrAgentOffline) || errors.Is(err, streammux.ErrSessionClosed)
}

// interrupted ends a job whose agent stayed away.
func (s *Service) interrupted(env, what string, cause error, recovery string) error {
	return &classed{class: domain.ErrorAgentOffline,
		err: fmt.Errorf("the environment's agent disconnected during %s and did not return within %s: %w (%w)", what, s.opts.ReconnectWait,
			jobexec.ErrStepInterrupted, cause),
		recovery: recovery}
}

// agentFailure classifies a failed request to an agent.
func agentFailure(err error, class, recovery string) error {
	if errors.Is(err, jobs.ErrAgentOffline) || errors.Is(err, protocol.ErrRequestTimeout) {
		return &classed{class: domain.ErrorAgentOffline, err: fmt.Errorf("the agent: %w (%w)", jobexec.ErrStepInterrupted, err),
			recovery: "The environment's agent is unavailable. " + recovery}
	}
	return &classed{class: class, err: fmt.Errorf("the agent: %w", err), recovery: recovery}
}

// limited applies the bandwidth cap to reads.
type limited struct {
	ctx context.Context
	r   interface{ Read([]byte) (int, error) }
	l   *transfer.Limiter
}

func (l *limited) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if n > 0 {
		if werr := l.l.Wait(l.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}
