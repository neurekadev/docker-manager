// Package files is the manager side of the scoped file manager (#15): it
// resolves a stack's file root, relays the files.* requests and the
// files.download/files.upload byte streams to the environment's agent,
// enqueues the files.* jobs, and tells the stack workstream (#7) when
// Docker Manager changed a stack's Compose sources. Template drafts are
// served locally by the template service's internal/fsroot instance
// (template.files.* jobs run on the manager). It implements
// api.FilesService.
//
// It does not authorize: the API layer (#17) checks the caller's
// capabilities before calling it, and the job engine re-checks jobs. The
// agent confines every path to the scope root (internal/agent/files).
// File contents are never logged here.
package files

import (
	"bytes"
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

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/fsroot"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/agents"
	"github.com/neurekadev/docker-manager/internal/manager/api"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
)

// StackRoot is where a stack's files live.
type StackRoot struct {
	EnvironmentID string
	// Dir is the project directory (absolute host path, identical inside
	// the agent, #28).
	Dir string
	// DefinitionFiles are the stack's declared definition files (Compose,
	// override and env files), relative to Dir, clean and slash-separated
	// (api.FileRoot.Definition).
	DefinitionFiles []string
}

// StackRoots resolves a stack's file root. The Compose stack workstream
// (#7) implements it; return domain.ErrFileScopeNotFound for unknown
// stacks. Until it is wired, stack scopes answer 404.
type StackRoots interface {
	StackFileRoot(ctx context.Context, stackID string) (StackRoot, error)
}

// SourceObserver is told when Docker Manager changed a stack's Compose sources
// (its definition files, see api.FileRoot.IsDefinition) through the file
// manager. #7 records a new stack
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

// Templates serves template drafts (*templates.Service).
type Templates interface {
	// Exists returns domain.ErrTemplateNotFound for unknown templates.
	Exists(ctx context.Context, id string) error
	// Files is the file service of the drafts.
	Files() *fsroot.Service
	// CheckQuota refuses adding bytes beyond the template size limit
	// (*domain.TemplateTooLargeError).
	CheckQuota(ctx context.Context, id string, adding int64) error
	// Writing holds off publications while a file changes.
	Writing(id string) func()
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
	// Limits are the configured file manager limits
	// (DOCKER_MANAGER_FILES_*); zero fields are the agents' built-in
	// defaults (DefaultLimits).
	Limits domain.FileLimits
}

// DefaultLimits are the file manager limits without configuration: the
// agents' built-in ones.
func DefaultLimits() domain.FileLimits {
	d := fsroot.DefaultLimits()
	return domain.FileLimits{Edit: d.MaxInline, Upload: d.MaxUpload, Download: d.MaxDownload, ExtractBytes: d.MaxExtractBytes,
		ExtractRatio: d.MaxExtractRatio, ArchiveEntries: d.MaxArchiveEntries}
}

// withDefaults fills the zero fields of l with DefaultLimits.
func withDefaults(l domain.FileLimits) domain.FileLimits {
	d := DefaultLimits()
	if l.Edit <= 0 {
		l.Edit = d.Edit
	}
	if l.Upload <= 0 {
		l.Upload = d.Upload
	}
	if l.Download <= 0 {
		l.Download = d.Download
	}
	if l.ExtractBytes <= 0 {
		l.ExtractBytes = d.ExtractBytes
	}
	if l.ExtractRatio <= 0 {
		l.ExtractRatio = d.ExtractRatio
	}
	if l.ArchiveEntries <= 0 {
		l.ArchiveEntries = d.ArchiveEntries
	}
	return l
}

// Service implements api.FilesService.
type Service struct {
	opts Options
	log  *slog.Logger

	mu        sync.Mutex
	stacks    StackRoots
	observer  SourceObserver
	templates Templates
	watcher   *Watcher
	closed    bool
	watchers  sync.WaitGroup
	stop      chan struct{}
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
	o.Limits = withDefaults(o.Limits)
	return &Service{opts: o, log: o.Logger.With("component", "files"), stacks: o.Stacks, observer: o.Observer, stop: make(chan struct{})}
}

// SetStacks installs the stack root resolver and source observer (#7
// calls it during startup).
func (s *Service) SetStacks(r StackRoots, o SourceObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stacks, s.observer = r, o
}

// SetTemplates installs the template draft service.
func (s *Service) SetTemplates(t Templates) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = t
}

func (s *Service) tmpl() Templates {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.templates
}

// TemplateRoot resolves a template's draft (domain.ErrFileScopeNotFound).
func (s *Service) TemplateRoot(ctx context.Context, templateID string) (api.FileRoot, error) {
	t := s.tmpl()
	if t == nil {
		return api.FileRoot{}, domain.ErrFileScopeNotFound
	}
	if err := t.Exists(ctx, templateID); err != nil {
		if errors.Is(err, domain.ErrTemplateNotFound) {
			return api.FileRoot{}, domain.ErrFileScopeNotFound
		}
		return api.FileRoot{}, err
	}
	return api.FileRoot{Scope: protocol.FileScope{Kind: protocol.ScopeTemplate, ID: templateID}}, nil
}

// local returns the draft file service for template roots (nil for agent
// roots).
func (s *Service) local(r api.FileRoot) (Templates, error) {
	if r.Scope.Kind != protocol.ScopeTemplate {
		return nil, nil
	}
	t := s.tmpl()
	if t == nil {
		return nil, domain.ErrFileScopeNotFound
	}
	return t, nil
}

// localErr maps fsroot errors like agent errors.
func localErr(err error) error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return &domain.FileError{Code: pe.Code, Message: pe.Message}
	}
	return err
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
	return api.FileRoot{Scope: protocol.FileScope{Kind: protocol.ScopeStack, ID: stackID, Dir: r.Dir}, EnvironmentID: r.EnvironmentID,
		Definition: r.DefinitionFiles, NoFollow: s.hasFeature(r.EnvironmentID, protocol.FeatureStackFilesNoFollow)}, nil
}

// hasFeature reports whether the environment's connected agent announced
// a capabilities feature.
func (s *Service) hasFeature(environmentID, feature string) bool {
	fh, ok := s.opts.Agents.(interface {
		EnvironmentHasFeature(environmentID, feature string) bool
	})
	return ok && fh.EnvironmentHasFeature(environmentID, feature)
}

// Limits returns the file manager limits in effect for a root: the
// configured ones for agents that apply them (protocol.FeatureFileLimits);
// for older agents the agents' built-in defaults, except that uploads may
// only be lowered (the manager checks them) and the edit limit holds for
// every agent (the manager reads and saves larger files through the
// streams); for template drafts the template size limits.
func (s *Service) Limits(r api.FileRoot) domain.FileLimits {
	cfg := s.opts.Limits
	if r.Scope.Kind == protocol.ScopeTemplate {
		t := s.tmpl()
		if t == nil {
			return cfg
		}
		fl := t.Files().Limits()
		return domain.FileLimits{Edit: fl.MaxInline, Upload: min(cfg.Upload, fl.MaxUpload), Download: fl.MaxDownload,
			ExtractBytes: fl.MaxExtractBytes, ExtractRatio: fl.MaxExtractRatio, ArchiveEntries: fl.MaxArchiveEntries}
	}
	if s.appliesLimits(r) {
		return cfg
	}
	d := DefaultLimits()
	d.Edit, d.Upload = cfg.Edit, min(cfg.Upload, d.Upload)
	return d
}

// appliesLimits reports whether the root's agent applies the limits the
// manager sends (protocol.FeatureFileLimits).
func (s *Service) appliesLimits(r api.FileRoot) bool {
	return r.Scope.Kind != protocol.ScopeTemplate && s.hasFeature(r.EnvironmentID, protocol.FeatureFileLimits)
}

// agentLimits are the limits sent with an operation on the root: the
// configured ones for agents that apply them, nil otherwise (they keep
// their defaults).
func (s *Service) agentLimits(r api.FileRoot) *protocol.FileLimits {
	if !s.appliesLimits(r) {
		return nil
	}
	l := s.opts.Limits
	return &protocol.FileLimits{MaxUpload: l.Upload, MaxDownload: l.Download, MaxExtractBytes: l.ExtractBytes,
		MaxExtractRatio: l.ExtractRatio, MaxArchiveEntries: l.ArchiveEntries}
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
	if t, err := s.local(r); t != nil || err != nil {
		if err != nil {
			return protocol.FilesListOutput{}, err
		}
		out, err := t.Files().List(ctx, in)
		return out, localErr(err)
	}
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
	if t, err := s.local(r); t != nil || err != nil {
		if err != nil {
			return protocol.FileEntry{}, err
		}
		out, err := t.Files().Stat(ctx, protocol.FilesStatInput{Scope: r.Scope, Path: p, ETag: etag})
		return out, localErr(err)
	}
	return request[protocol.FileEntry](ctx, s, r, protocol.ReqFilesStat, protocol.FilesStatInput{Scope: r.Scope, Path: p, ETag: etag})
}

// Read reads a bounded slice of a regular file: at most in.Length bytes
// (the edit limit; 0: the agent's inline limit). Agents return at most
// protocol.MaxInlineContent per files.read (one frame); the rest of a
// longer slice comes through a raw files.download stream.
func (s *Service) Read(ctx context.Context, r api.FileRoot, in protocol.FilesReadInput) (protocol.FilesReadOutput, error) {
	in.Scope = r.Scope
	if t, err := s.local(r); t != nil || err != nil {
		if err != nil {
			return protocol.FilesReadOutput{}, err
		}
		out, err := t.Files().Read(ctx, in)
		return out, localErr(err)
	}
	want := in.Length
	if want > protocol.MaxInlineContent {
		in.Length = protocol.MaxInlineContent
	}
	out, err := request[protocol.FilesReadOutput](ctx, s, r, protocol.ReqFilesRead, in)
	if err != nil || want <= protocol.MaxInlineContent || !out.Truncated || out.Binary {
		return out, err
	}
	return s.readRest(ctx, r, in, want, out)
}

// readRest completes a read beyond the inline limit: the bytes after first
// up to want (or the end of the file) through a raw download. The file must
// not change meanwhile (same size and modification time before and after);
// otherwise the read fails with conflict and the caller opens it again.
func (s *Service) readRest(ctx context.Context, r api.FileRoot, in protocol.FilesReadInput, want int64, first protocol.FilesReadOutput) (protocol.FilesReadOutput, error) {
	off := in.Offset + int64(len(first.Data))
	n := min(want-int64(len(first.Data)), first.Entry.Size-off)
	if n <= 0 || s.opts.Agents == nil {
		return first, nil
	}
	dl := protocol.FilesDownloadInput{Scope: r.Scope, Paths: []string{in.Path}, Format: protocol.FormatRaw, Offset: off, Length: n}
	if s.appliesLimits(r) {
		// Reading for the editor is not a download: the download limit
		// does not apply.
		dl.Limits = &protocol.FileLimits{MaxDownload: protocol.MaxFileLimitBytes}
	}
	st, err := s.opts.Agents.OpenStream(ctx, r.EnvironmentID, protocol.StreamFilesDownload, dl, streammux.OpenOptions{})
	if err != nil {
		return first, agentErr(err)
	}
	rest, err := io.ReadAll(io.LimitReader(st, n))
	if err != nil {
		st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
		var fe *domain.FileError
		if err = agentErr(err); errors.As(err, &fe) && fe.Code == protocol.CodeTooLarge {
			// An older agent refuses files over its download limit: the
			// first bytes stay a truncated read.
			return first, nil
		}
		return first, err
	}
	_ = st.CloseWrite()
	cur, err := request[protocol.FileEntry](ctx, s, r, protocol.ReqFilesStat, protocol.FilesStatInput{Scope: r.Scope, Path: in.Path})
	if err != nil {
		return first, err
	}
	if int64(len(rest)) != n || cur.Size != first.Entry.Size || !cur.ModTime.Equal(first.Entry.ModTime) {
		return first, &domain.FileError{Code: protocol.CodeConflict, Message: in.Path + " changed while it was read; open it again"}
	}
	out := first
	out.Data = append(slices.Clip(first.Data), rest...)
	out.Truncated = off+n < first.Entry.Size
	out.Binary = fsroot.IsBinary(out.Data, out.Truncated)
	return out, nil
}

// Write replaces or creates a file (inline content).
func (s *Service) Write(ctx context.Context, r api.FileRoot, in protocol.FilesWriteInput) (protocol.FileEntry, error) {
	in.Scope = r.Scope
	if t, err := s.local(r); t != nil || err != nil {
		if err != nil {
			return protocol.FileEntry{}, err
		}
		if err := t.CheckQuota(ctx, r.Scope.ID, int64(len(in.Data))); err != nil {
			return protocol.FileEntry{}, err
		}
		defer t.Writing(r.Scope.ID)()
		out, err := t.Files().Write(ctx, in)
		return out, localErr(err)
	}
	if err := s.validateSource(ctx, r, in.Path, in.Data); err != nil {
		return protocol.FileEntry{}, err
	}
	var e protocol.FileEntry
	var err error
	if len(in.Data) > protocol.MaxInlineContent {
		conflict := ""
		if in.Overwrite {
			conflict = protocol.ConflictOverwrite
		}
		e, err = s.writeStream(ctx, r, in.Path, in.Data, in.IfMatch, in.CreateOnly, conflict)
	} else {
		e, err = request[protocol.FileEntry](ctx, s, r, protocol.ReqFilesWrite, in)
	}
	if err == nil {
		s.sourcesChanged(ctx, r, e.Path)
	}
	return e, err
}

// writeStream saves content above the agents' inline limit (a raised edit
// limit) through a files.upload stream with the same precondition: the
// agent writes a temporary file, re-checks and renames it like files.write.
func (s *Service) writeStream(ctx context.Context, r api.FileRoot, rel string, data []byte, ifMatch []string, createOnly bool, conflict string) (protocol.FileEntry, error) {
	dir, name := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}
	in := protocol.FilesUploadInput{Scope: r.Scope, Dir: dir, Name: name, Size: int64(len(data)), IfMatch: ifMatch, CreateOnly: createOnly,
		Conflict: conflict}
	if s.appliesLimits(r) {
		// The edit limit (checked by the caller) applies, not the upload
		// limit; agents without the feature allow far more anyway.
		in.Limits = &protocol.FileLimits{MaxUpload: in.Size}
	}
	res, err := s.upload(ctx, r, in, bytes.NewReader(data))
	return res.Entry, err
}

// Mkdir creates a directory or a new file.
func (s *Service) Mkdir(ctx context.Context, r api.FileRoot, in protocol.FilesMkdirInput) (protocol.FileEntry, error) {
	in.Scope = r.Scope
	if t, err := s.local(r); t != nil || err != nil {
		if err != nil {
			return protocol.FileEntry{}, err
		}
		if err := t.CheckQuota(ctx, r.Scope.ID, int64(len(in.Data))); err != nil {
			return protocol.FileEntry{}, err
		}
		defer t.Writing(r.Scope.ID)()
		out, err := t.Files().Mkdir(ctx, in)
		return out, localErr(err)
	}
	if in.Type == protocol.FileTypeFile {
		if err := s.validateSource(ctx, r, in.Path, in.Data); err != nil {
			return protocol.FileEntry{}, err
		}
		if len(in.Data) > protocol.MaxInlineContent {
			e, err := s.writeStream(ctx, r, in.Path, in.Data, nil, true, "")
			if err == nil {
				s.sourcesChanged(ctx, r, e.Path)
			}
			return e, err
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
	if t, err := s.local(r); t != nil || err != nil {
		if err != nil {
			return protocol.FilesPreviewOutput{}, err
		}
		out, err := t.Files().Preview(ctx, in)
		return out, localErr(err)
	}
	if in.Operation == protocol.FileOpExtract {
		in.Limits = s.agentLimits(r)
	}
	return request[protocol.FilesPreviewOutput](ctx, s, r, protocol.ReqFilesConflictPreview, in)
}

// Download opens a files.download stream. The caller reads it (the
// agent's failure arrives as a Read error; map it with FileStreamError)
// and must end it with CloseWrite or Abort. ctx ending aborts it.
func (s *Service) Download(ctx context.Context, r api.FileRoot, in protocol.FilesDownloadInput) (api.ByteStream, error) {
	in.Scope = r.Scope
	if t, err := s.local(r); t != nil || err != nil {
		if err != nil {
			return nil, err
		}
		return localDownload(ctx, t, in), nil
	}
	if s.opts.Agents == nil {
		return nil, domain.ErrFileAgentOffline
	}
	in.Limits = s.agentLimits(r)
	st, err := s.opts.Agents.OpenStream(ctx, r.EnvironmentID, protocol.StreamFilesDownload, in, streammux.OpenOptions{})
	return st, agentErr(err)
}

// StreamError maps an error read from a download stream.
func (s *Service) StreamError(err error) error { return localErr(agentErr(err)) }

// pipeStream is a local download: fsroot writes into the pipe while the
// handler reads it.
type pipeStream struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (p pipeStream) CloseWrite() error { p.cancel(); return p.Close() }

func (p pipeStream) Abort(_, _, _ string) {
	p.cancel()
	_ = p.CloseWithError(context.Canceled)
}

func localDownload(ctx context.Context, t Templates, in protocol.FilesDownloadInput) api.ByteStream {
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		// A nil error closes the pipe with EOF.
		_ = pw.CloseWithError(t.Files().Download(ctx, in, pw))
	}()
	return pipeStream{PipeReader: pr, cancel: cancel}
}

// Upload streams body (exactly in.Size bytes) into a file and returns the
// agent's result. Backpressure: body is read only as fast as the agent
// grants stream credit.
func (s *Service) Upload(ctx context.Context, r api.FileRoot, in protocol.FilesUploadInput, body io.Reader) (protocol.FilesUploadResult, error) {
	var out protocol.FilesUploadResult
	in.Scope = r.Scope
	if t, err := s.local(r); t != nil || err != nil {
		if err != nil {
			return out, err
		}
		if err := t.CheckQuota(ctx, r.Scope.ID, in.Size); err != nil {
			return out, err
		}
		defer t.Writing(r.Scope.ID)()
		out, err := t.Files().Upload(ctx, in, io.LimitReader(body, in.Size))
		return out, localErr(err)
	}
	in.Limits = s.agentLimits(r)
	out, err := s.upload(ctx, r, in, body)
	if err == nil && !out.Skipped {
		s.sourcesChanged(ctx, r, out.Entry.Path)
	}
	return out, err
}

// upload streams body (exactly in.Size bytes) to the root's agent.
func (s *Service) upload(ctx context.Context, r api.FileRoot, in protocol.FilesUploadInput, body io.Reader) (protocol.FilesUploadResult, error) {
	var out protocol.FilesUploadResult
	if s.opts.Agents == nil {
		return out, domain.ErrFileAgentOffline
	}
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
	if r.Scope.Kind == protocol.ScopeTemplate {
		// Template drafts are small: one exclusive lock on the template,
		// no per-path locks (those belong to an environment).
		tk, ok := jobspec.TemplateFilesKind(kind)
		if !ok {
			return domain.Job{}, fmt.Errorf("files: %s has no template kind", kind)
		}
		j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: tk, Principal: p,
			Targets: []domain.JobTarget{{Type: domain.TargetTemplate, ID: r.Scope.ID}}, Input: in, IdempotencyKey: key})
		return j, err
	}
	if kind == jobspec.FilesArchive || kind == jobspec.FilesExtract {
		in.Limits = s.agentLimits(r)
	}
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
	if changed := jobSourcePaths(r, kind, in); created && r.Scope.Kind == protocol.ScopeStack && len(changed) > 0 {
		s.watch(r, j.ID, changed)
	}
	return j, nil
}

// jobSourcePaths are the paths of a job that are or hold a definition
// file of the stack and that the job may change: deleted or moved
// sources, copy and move destinations, an extraction's destination
// directory (its entries are unknown), an archive file created.
func jobSourcePaths(r api.FileRoot, kind domain.JobKind, in protocol.FilesJobInput) []string {
	var out []string
	add := func(p string) {
		if r.HoldsDefinition(p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	target := func(p string) string {
		if in.Name != "" {
			return path.Join(in.Destination, in.Name)
		}
		return path.Join(in.Destination, path.Base(p))
	}
	switch kind {
	case jobspec.FilesDelete:
		for _, p := range in.Paths {
			add(p)
		}
	case jobspec.FilesMove:
		for _, p := range in.Paths {
			add(p)
			add(target(p))
		}
	case jobspec.FilesCopy:
		for _, p := range in.Paths {
			add(target(p))
		}
	case jobspec.FilesExtract:
		add(in.Destination)
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
		if r.IsDefinition(p) {
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
