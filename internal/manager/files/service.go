// Package files is the manager side of the scoped file manager (#15): it
// resolves a stack's file root, relays the files.* requests and the
// files.download/files.upload byte streams to the environment's agent,
// enqueues the files.* jobs, and tells the stack workstream (#7) when
// Docker Manager changed a stack's Compose sources. It implements
// api.FilesService.
//
// It does not authorize: the API layer (#17) checks the caller's
// capabilities before calling it, and the job engine re-checks jobs. The
// agent confines every path to the scope root (internal/agent/files).
// File contents are never logged here.
package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/agents"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/api"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
)

// StackRoot is where a stack's files live.
type StackRoot struct {
	EnvironmentID string
	// Dir is the project directory (absolute host path, identical inside
	// the agent, #28).
	Dir string
}

// StackRoots resolves a stack's file root. The Compose stack workstream
// (#7) implements it; return domain.ErrFileScopeNotFound for unknown
// stacks. Until it is wired, stack scopes answer 404.
type StackRoots interface {
	StackFileRoot(ctx context.Context, stackID string) (StackRoot, error)
}

// SourceObserver is told when Docker Manager changed a stack's Compose sources
// (compose.yaml, override files, .env at the project root; see
// api.IsDefinitionFile) through the file manager. #7 records a new stack
// revision and marks undeployed changes; it never deploys. Called after
// the change, never for contents; it must not block for long.
type SourceObserver interface {
	StackSourcesChanged(ctx context.Context, stackID string, paths []string)
}

// SourceValidator checks a stack's definition with new content for one of
// its files before the file manager writes it (#7): a save that would
// leave the definition invalid is refused with the validator's error
// (*domain.StackError invalid_definition). The observer may implement it.
type SourceValidator interface {
	ValidateSourceSave(ctx context.Context, stackID, path string, content []byte) error
}

// Agents is the session hub as used here (*agents.Hub).
type Agents interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
	OpenStream(ctx context.Context, environmentID, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error)
}

// Jobs is the job engine as used here (*jobs.Engine).
type Jobs interface {
	Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error)
	Get(ctx context.Context, id string) (domain.Job, error)
	Subscribe(jobID string) (<-chan struct{}, func())
}

// Options configures the service.
type Options struct {
	Agents Agents
	Jobs   Jobs
	// Stacks and Observer are optional until #7 provides them.
	Stacks   StackRoots
	Observer SourceObserver
	Logger   *slog.Logger
	// RequestTimeout bounds agent requests (default 60 s: previews and
	// listings of huge directories take time).
	RequestTimeout time.Duration
}

// Service implements api.FilesService.
type Service struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	stacks   StackRoots
	observer SourceObserver
	watcher  *Watcher
	closed   bool
	watchers sync.WaitGroup
	stop     chan struct{}
}

var _ api.FilesService = (*Service)(nil)

// New returns the service.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 60 * time.Second
	}
	return &Service{opts: o, log: o.Logger.With("component", "files"), stacks: o.Stacks, observer: o.Observer, stop: make(chan struct{})}
}

// SetStacks installs the stack root resolver and source observer (#7
// calls it during startup).
func (s *Service) SetStacks(r StackRoots, o SourceObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stacks, s.observer = r, o
}

// SetWatcher installs the file watcher (#23): volume listings keep the
// volume watched for a lease.
func (s *Service) SetWatcher(w *Watcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watcher = w
}

func (s *Service) deps() (StackRoots, SourceObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stacks, s.observer
}

// Close stops the job watchers of the source observer and waits for them.
func (s *Service) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stop)
	}
	s.mu.Unlock()
	s.watchers.Wait()
}

// StackRoot resolves a stack's file root (domain.ErrFileScopeNotFound).
func (s *Service) StackRoot(ctx context.Context, stackID string) (api.FileRoot, error) {
	stacks, _ := s.deps()
	if stacks == nil {
		return api.FileRoot{}, domain.ErrFileScopeNotFound
	}
	r, err := stacks.StackFileRoot(ctx, stackID)
	if err != nil {
		return api.FileRoot{}, err
	}
	return api.FileRoot{Scope: protocol.FileScope{Kind: protocol.ScopeStack, ID: stackID, Dir: r.Dir}, EnvironmentID: r.EnvironmentID}, nil
}

// agentErr maps hub and stream errors.
func agentErr(err error) error {
	var re *agents.RequestError
	var ce *streammux.CloseError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &re):
		return &domain.FileError{Code: re.Code, Message: re.Message}
	case errors.As(err, &ce) && !ce.Local && ce.Code != "":
		return &domain.FileError{Code: ce.Code, Message: ce.Message}
	case errors.Is(err, jobs.ErrAgentOffline), errors.Is(err, streammux.ErrSessionClosed):
		return domain.ErrFileAgentOffline
	case errors.Is(err, agents.ErrRequestTimeout):
		return domain.ErrFileAgentTimeout
	}
	return err
}

// request runs a files.* request and decodes its output.
func request[O any](ctx context.Context, s *Service, r api.FileRoot, name string, input any) (O, error) {
	var out O
	if s.opts.Agents == nil {
		return out, domain.ErrFileAgentOffline
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, r.EnvironmentID, name, input, s.opts.RequestTimeout)
	if err != nil {
		return out, agentErr(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("files: decode %s output: %w", name, err)
	}
	return out, nil
}

// List lists a directory page.
func (s *Service) List(ctx context.Context, r api.FileRoot, in protocol.FilesListInput) (protocol.FilesListOutput, error) {
	in.Scope = r.Scope
	s.mu.Lock()
	w := s.watcher
	s.mu.Unlock()
	if w != nil && r.Scope.Kind == protocol.ScopeVolume {
		w.Touch(r.EnvironmentID, r.Scope.ID) // an open volume view: watch it (#23)
	}
	return request[protocol.FilesListOutput](ctx, s, r, protocol.ReqFilesList, in)
}

// Stat returns one entry's metadata (with its content ETag when asked).
func (s *Service) Stat(ctx context.Context, r api.FileRoot, p string, etag bool) (protocol.FileEntry, error) {
	return request[protocol.FileEntry](ctx, s, r, protocol.ReqFilesStat, protocol.FilesStatInput{Scope: r.Scope, Path: p, ETag: etag})
}

// Read reads a bounded slice of a regular file.
func (s *Service) Read(ctx context.Context, r api.FileRoot, in protocol.FilesReadInput) (protocol.FilesReadOutput, error) {
	in.Scope = r.Scope
	return request[protocol.FilesReadOutput](ctx, s, r, protocol.ReqFilesRead, in)
}

// Write replaces or creates a file (inline content).
func (s *Service) Write(ctx context.Context, r api.FileRoot, in protocol.FilesWriteInput) (protocol.FileEntry, error) {
	in.Scope = r.Scope
	if err := s.validateSource(ctx, r, in.Path, in.Data); err != nil {
		return protocol.FileEntry{}, err
	}
	e, err := request[protocol.FileEntry](ctx, s, r, protocol.ReqFilesWrite, in)
	if err == nil {
		s.sourcesChanged(ctx, r, e.Path)
	}
	return e, err
}

// Mkdir creates a directory or a new file.
func (s *Service) Mkdir(ctx context.Context, r api.FileRoot, in protocol.FilesMkdirInput) (protocol.FileEntry, error) {
	in.Scope = r.Scope
	if in.Type == protocol.FileTypeFile {
		if err := s.validateSource(ctx, r, in.Path, in.Data); err != nil {
			return protocol.FileEntry{}, err
		}
	}
	e, err := request[protocol.FileEntry](ctx, s, r, protocol.ReqFilesMkdir, in)
	if err == nil {
		s.sourcesChanged(ctx, r, e.Path)
	}
	return e, err
}

// Preview lists conflicts and counts the impact of an operation.
func (s *Service) Preview(ctx context.Context, r api.FileRoot, in protocol.FilesPreviewInput) (protocol.FilesPreviewOutput, error) {
	in.Scope = r.Scope
	return request[protocol.FilesPreviewOutput](ctx, s, r, protocol.ReqFilesConflictPreview, in)
}

// Download opens a files.download stream. The caller reads it (the
// agent's failure arrives as a Read error; map it with FileStreamError)
// and must end it with CloseWrite or Abort. ctx ending aborts it.
func (s *Service) Download(ctx context.Context, r api.FileRoot, in protocol.FilesDownloadInput) (*streammux.Stream, error) {
	if s.opts.Agents == nil {
		return nil, domain.ErrFileAgentOffline
	}
	in.Scope = r.Scope
	st, err := s.opts.Agents.OpenStream(ctx, r.EnvironmentID, protocol.StreamFilesDownload, in, streammux.OpenOptions{})
	return st, agentErr(err)
}

// StreamError maps an error read from a download stream.
func (s *Service) StreamError(err error) error { return agentErr(err) }

// Upload streams body (exactly in.Size bytes) into a file and returns the
// agent's result. Backpressure: body is read only as fast as the agent
// grants stream credit.
func (s *Service) Upload(ctx context.Context, r api.FileRoot, in protocol.FilesUploadInput, body io.Reader) (protocol.FilesUploadResult, error) {
	var out protocol.FilesUploadResult
	if s.opts.Agents == nil {
		return out, domain.ErrFileAgentOffline
	}
	in.Scope = r.Scope
	st, err := s.opts.Agents.OpenStream(ctx, r.EnvironmentID, protocol.StreamFilesUpload, in, streammux.OpenOptions{MaxBytes: in.Size})
	if err != nil {
		return out, agentErr(err)
	}
	n, werr := io.Copy(st, io.LimitReader(body, in.Size))
	switch {
	case errors.Is(werr, streammux.ErrPeerClosed):
		// The agent finished early (a skipped name): its result follows.
	case werr != nil:
		if e := st.Err(); e != nil {
			return out, agentErr(e)
		}
		st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
		return out, fmt.Errorf("files: upload body: %w", werr)
	case n < in.Size:
		st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
		return out, fmt.Errorf("files: upload body: %w", io.ErrUnexpectedEOF)
	}
	if err := st.CloseWrite(); err != nil {
		return out, agentErr(err)
	}
	res, err := st.Result(ctx)
	if err != nil {
		return out, agentErr(err)
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return out, fmt.Errorf("files: decode upload result: %w", err)
	}
	if !out.Skipped {
		s.sourcesChanged(ctx, r, out.Entry.Path)
	}
	return out, nil
}

// virtualPath is a job lock path for a root-relative path: roots of
// different stacks and volumes never share lock names.
func virtualPath(sc protocol.FileScope, rel string) string {
	return path.Join("/", sc.Kind, sc.ID, rel)
}

// commonDir is the deepest path containing every path.
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return "."
	}
	parts := strings.Split(paths[0], "/")
	for _, p := range paths[1:] {
		q := strings.Split(p, "/")
		n := 0
		for n < len(parts) && n < len(q) && parts[n] == q[n] {
			n++
		}
		parts = parts[:n]
	}
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts, "/")
}

// StartJob enqueues a files.* job on the root. Targets: the root (stack
// or volume; its capability is what the engine checks and re-checks at
// dispatch), the deepest common path of the sources (read or changed) and
// the destination (written) as file_path locks.
func (s *Service) StartJob(ctx context.Context, r api.FileRoot, kind domain.JobKind, p authz.Principal, in protocol.FilesJobInput, key string) (domain.Job, error) {
	if s.opts.Jobs == nil {
		return domain.Job{}, errors.New("files: the job engine is not available")
	}
	in.Scope = r.Scope
	rootType := domain.TargetVolume
	if r.Scope.Kind == protocol.ScopeStack {
		rootType = domain.TargetStack
	}
	targets := []domain.JobTarget{{Type: rootType, ID: r.Scope.ID},
		{Type: domain.TargetPath, ID: virtualPath(r.Scope, commonDir(in.Paths))}}
	switch kind {
	case jobspec.FilesDelete, jobspec.FilesMetadata:
	default:
		targets = append(targets, domain.JobTarget{Type: domain.TargetDestinationPath, ID: virtualPath(r.Scope, in.Destination)})
	}
	j, created, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: kind, Principal: p, EnvironmentID: r.EnvironmentID,
		Targets: targets, Input: in, IdempotencyKey: key})
	if err != nil {
		return j, err
	}
	if changed := jobSourcePaths(kind, in); created && r.Scope.Kind == protocol.ScopeStack && len(changed) > 0 {
		s.watch(r, j.ID, changed)
	}
	return j, nil
}

// jobSourcePaths are the root-level definition paths a job may change
// ("." when an extraction writes into the root with unknown names).
func jobSourcePaths(kind domain.JobKind, in protocol.FilesJobInput) []string {
	var out []string
	add := func(p string) {
		if api.IsDefinitionFile(p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	target := func(p string) string {
		if in.Name != "" {
			return in.Name
		}
		return path.Base(p)
	}
	switch kind {
	case jobspec.FilesDelete:
		for _, p := range in.Paths {
			add(p)
		}
	case jobspec.FilesMove:
		for _, p := range in.Paths {
			add(p)
			if in.Destination == "." {
				add(target(p))
			}
		}
	case jobspec.FilesCopy:
		if in.Destination == "." {
			for _, p := range in.Paths {
				add(target(p))
			}
		}
	case jobspec.FilesExtract:
		if in.Destination == "." {
			out = append(out, ".")
		}
	case jobspec.FilesArchive:
		add(in.Destination)
	}
	return out
}

// sourcesChanged tells the observer about changed definition files.
// validateSource refuses writing content to a stack's definition file
// when the definition would no longer load (SourceValidator).
func (s *Service) validateSource(ctx context.Context, r api.FileRoot, path string, content []byte) error {
	_, observer := s.deps()
	v, ok := observer.(SourceValidator)
	if !ok || r.Scope.Kind != protocol.ScopeStack {
		return nil
	}
	return v.ValidateSourceSave(ctx, r.Scope.ID, path, content)
}

func (s *Service) sourcesChanged(ctx context.Context, r api.FileRoot, paths ...string) {
	_, observer := s.deps()
	if observer == nil || r.Scope.Kind != protocol.ScopeStack {
		return
	}
	var defs []string
	for _, p := range paths {
		if api.IsDefinitionFile(p) {
			defs = append(defs, p)
		}
	}
	if len(defs) > 0 {
		observer.StackSourcesChanged(context.WithoutCancel(ctx), r.Scope.ID, defs)
	}
}

// watch tells the observer once a job that may have changed Compose
// sources finished (any outcome but a cancellation before it started: a
// partial move may still have changed them). Watchers end with the job or
// when the service closes; changes missed then are caught by the file
// watcher (#23).
func (s *Service) watch(r api.FileRoot, jobID string, paths []string) {
	_, observer := s.deps()
	if observer == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.watchers.Add(1)
	s.mu.Unlock()
	changed, unsubscribe := s.opts.Jobs.Subscribe(jobID)
	go func() {
		defer s.watchers.Done()
		defer unsubscribe()
		ctx := context.Background()
		for {
			j, err := s.opts.Jobs.Get(ctx, jobID)
			if err != nil {
				return
			}
			if j.State.Terminal() {
				if j.State != domain.JobCancelled || j.StartedAt != nil {
					observer.StackSourcesChanged(ctx, r.Scope.ID, paths)
				}
				return
			}
			select {
			case <-changed:
			case <-s.stop:
				return
			}
		}
	}()
}
