// Package migration is the agent side of environment migration (#35): the
// migration.* requests (preview, stop, start, commit, cleanup), the
// migration.send / migration.receive streams and the stack.remove_source
// job executor.
//
// The manager's stack.migrate / volume.migrate jobs drive it. Data flows
// source agent -> manager -> destination agent as a PAX tar archive framed
// and checksummed by internal/transfer (per chunk and whole payload), on
// top of the stream layer's own byte count and SHA-256; the manager relays
// with end-to-end backpressure and never buffers a whole part.
//
// Containment (#15, #28): the source reads only the stack's project
// directory (inside a verified stack root) and the selected local volumes
// (inside the verified volume directory, never Docker Manager's own volumes); the
// destination writes only into the staging directory of the migration below
// its stacks volume (<stacks>/.docker-manager-migrations/<id>/project, moved to
// the new project directory by migration.commit, which never replaces an
// existing directory) and into volumes it creates itself, labeled with the
// migration ID. Every filesystem access goes through an os.Root opened on a
// verified root; symlinks are archived as links and never followed.
package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/protect"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/transfer"
)

// Deps are the agent's live components (nil while the Engine is not
// connected or the storage check has not run).
type Deps interface {
	Engine() engine.Engine
	Storage() *storage.Result
}

// Options configures a Service.
type Options struct {
	Deps Deps
	// Guard identifies Docker Manager's own resources (#32); nil identifies them
	// by their labels only.
	Guard *protect.Guard
	// Open opens verified roots (default OSOpener).
	Open Opener
	// FreeBytes reports the free bytes of a root's filesystem (default:
	// statfs; -1 unknown).
	FreeBytes func(dir string) int64
	Clock     clock.Clock
	Logger    *slog.Logger
	// WaitTimeout bounds each dependency-condition wait when the source is
	// started again (default lifecycle.DefaultWaitTimeout).
	WaitTimeout time.Duration
	// MeasureEntries and MeasureTimeout bound a preview's data scan.
	MeasureEntries int64
	MeasureTimeout time.Duration
}

// Service serves the migration requests, streams and executor.
type Service struct {
	opts Options
	log  *slog.Logger
}

// New returns a Service.
func New(o Options) *Service {
	if o.Open == nil {
		o.Open = OSOpener
	}
	if o.FreeBytes == nil {
		o.FreeBytes = freeBytes
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Guard == nil {
		o.Guard = protect.New(protect.Options{Logger: o.Logger})
	}
	if o.MeasureEntries <= 0 {
		o.MeasureEntries = 1_000_000
	}
	if o.MeasureTimeout <= 0 {
		o.MeasureTimeout = time.Minute
	}
	return &Service{opts: o, log: o.Logger.With("component", "migration")}
}

// Requests returns the request handlers (runtime.Options.Requests).
func (s *Service) Requests() map[string]session.RequestHandler {
	return map[string]session.RequestHandler{
		protocol.ReqMigrationPreview: s.preview,
		protocol.ReqMigrationStop:    s.stop,
		protocol.ReqMigrationStart:   s.start,
		protocol.ReqMigrationCommit:  s.commit,
		protocol.ReqMigrationCleanup: s.cleanup,
	}
}

// Streams returns the stream handlers (runtime.Options.Streams).
func (s *Service) Streams() map[string]session.StreamHandler {
	return map[string]session.StreamHandler{
		protocol.StreamMigrationSend:    s.send,
		protocol.StreamMigrationReceive: s.receive,
	}
}

func fail(code, format string, args ...any) error {
	return &session.HandlerError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func invalid(err error) error { return fail(protocol.CodeInvalidFrame, "%s", err.Error()) }

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fail(protocol.CodeInvalidFrame, "malformed input")
	}
	return v, nil
}

func (s *Service) engine() (engine.Engine, error) {
	if s.opts.Deps == nil || s.opts.Deps.Engine() == nil {
		return nil, fail(protocol.CodeEngineUnavailable, "the Docker Engine is not connected")
	}
	return s.opts.Deps.Engine(), nil
}

func (s *Service) storage() (*storage.Result, error) {
	var r *storage.Result
	if s.opts.Deps != nil {
		r = s.opts.Deps.Storage()
	}
	if r == nil {
		return nil, fail(protocol.CodeForbiddenPath, "the storage layout has not been verified yet")
	}
	return r, nil
}

// engineError maps adapter errors to protocol codes.
func engineError(err error) error {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he
	}
	switch engine.CodeOf(err) {
	case engine.CodeNotFound:
		return fail(protocol.CodeNotFound, "%s", err.Error())
	case engine.CodeConflict:
		return fail(protocol.CodeConflict, "%s", err.Error())
	case engine.CodeEngineUnavailable:
		return fail(protocol.CodeEngineUnavailable, "%s", err.Error())
	case engine.CodeCanceled:
		return fail(protocol.CodeCancelled, "%s", err.Error())
	}
	return fail(protocol.CodeEngineError, "%s", err.Error())
}

// stackRoot returns the verified root a project reference names.
func stackRoot(res *storage.Result, ref protocol.ProjectRef) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", invalid(err)
	}
	for _, r := range res.Roots {
		if !r.OK {
			continue
		}
		if (ref.Root == protocol.RootStacks && r.Kind == storage.KindStacks) ||
			(ref.Root == protocol.RootBind && r.Kind == storage.KindBind && r.Path == ref.RootPath) {
			return r.Path, nil
		}
	}
	return "", fail(protocol.CodeForbiddenPath, "the stack root is not a verified stack root of this agent")
}

// openProject opens a project directory inside its verified root.
func (s *Service) openProject(ref protocol.ProjectRef) (FS, string, error) {
	res, err := s.storage()
	if err != nil {
		return nil, "", err
	}
	root, err := stackRoot(res, ref)
	if err != nil {
		return nil, "", err
	}
	dir := path.Join(root, ref.Dir)
	if err := res.Allows(dir); err != nil {
		return nil, "", fail(protocol.CodeForbiddenPath, "%s", err.Error())
	}
	rfs, err := s.opts.Open(root)
	if err != nil {
		return nil, "", fail(protocol.CodeForbiddenPath, "cannot open the stack root")
	}
	defer func() { _ = rfs.Close() }()
	pfs, err := rfs.Sub(ref.Dir)
	if err != nil {
		return nil, "", fail(protocol.CodeNotFound, "the project directory %s does not exist", ref.Dir)
	}
	return pfs, dir, nil
}

// openStacks opens the stacks volume (destination writes).
func (s *Service) openStacks() (FS, string, error) {
	res, err := s.storage()
	if err != nil {
		return nil, "", err
	}
	if !res.StacksOK() {
		return nil, "", fail(protocol.CodeForbiddenPath, "the stacks volume did not pass the storage check")
	}
	rfs, err := s.opts.Open(res.StacksDir)
	if err != nil {
		return nil, "", fail(protocol.CodeForbiddenPath, "cannot open the stacks volume")
	}
	return rfs, res.StacksDir, nil
}

// protectedSet identifies Docker Manager's own resources now.
func (s *Service) protectedSet(ctx context.Context, eng engine.Engine) (*protect.Set, []engine.Container, error) {
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return nil, nil, engineError(err)
	}
	return s.opts.Guard.Identify(ctx, eng, cs), cs, nil
}

// openVolume opens the data directory of a supported local volume that is
// not Docker Manager's own. The directory is opened below the verified volume
// directory, never through the volume's reported path alone.
func (s *Service) openVolume(v engine.Volume, set *protect.Set) (FS, error) {
	res, err := s.storage()
	if err != nil {
		return nil, err
	}
	if p := set.Volume(v.Name, v.Labels); p != nil {
		return nil, fail(protocol.CodeUnsupportedVolume, "volume %s is Docker Manager's own (%s) and is never migrated", v.Name, p.Reason)
	}
	if acc := res.AccessFor(v); !acc.Supported {
		return nil, fail(protocol.CodeUnsupportedVolume, "%s", acc.Reason)
	}
	mp := path.Clean(strings.ReplaceAll(v.Mountpoint, "\\", "/"))
	if res.StacksDir != "" && (within(mp, res.StacksDir) || within(res.StacksDir, mp)) {
		return nil, fail(protocol.CodeUnsupportedVolume, "the stacks volume is migrated per stack, not as a volume")
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(mp, strings.TrimSuffix(res.VolumesDir, "/")), "/")
	if rel == "" || !protocol.ValidRelativePath(rel) {
		return nil, fail(protocol.CodeUnsupportedVolume, "volume data at %s is outside the volume directory", mp)
	}
	root, err := s.opts.Open(res.VolumesDir)
	if err != nil {
		return nil, fail(protocol.CodeUnsupportedVolume, "cannot open the volume directory")
	}
	defer func() { _ = root.Close() }()
	vfs, err := root.Sub(rel)
	if err != nil {
		return nil, fail(protocol.CodeNotFound, "the data directory of volume %s does not exist", v.Name)
	}
	return vfs, nil
}

func within(p, root string) bool {
	p, root = path.Clean(p), path.Clean(root)
	return root != "." && (p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/"))
}

// --- streams ---

// send streams one part (migration.send, source).
func (s *Service) send(ctx context.Context, st *streammux.Stream) error {
	in, err := decode[protocol.MigrationSendInput](st.Input())
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return invalid(err)
	}
	fw := transfer.NewWriter(st, in.ChunkSize)
	var stats ArchiveStats
	switch in.Part {
	case protocol.PartProject:
		pfs, _, err := s.openProject(*in.Stack)
		if err != nil {
			return err
		}
		defer func() { _ = pfs.Close() }()
		if stats, err = WriteTree(ctx, pfs, fw); err != nil {
			return s.transferError(err)
		}
	case protocol.PartVolume:
		eng, err := s.engine()
		if err != nil {
			return err
		}
		v, err := eng.InspectVolume(ctx, in.Volume)
		if err != nil {
			return engineError(err)
		}
		set, _, err := s.protectedSet(ctx, eng)
		if err != nil {
			return err
		}
		vfs, err := s.openVolume(v, set)
		if err != nil {
			return err
		}
		defer func() { _ = vfs.Close() }()
		if stats, err = WriteTree(ctx, vfs, fw); err != nil {
			return s.transferError(err)
		}
	case protocol.PartImage:
		eng, err := s.engine()
		if err != nil {
			return err
		}
		set, _, err := s.protectedSet(ctx, eng)
		if err != nil {
			return err
		}
		for _, ref := range in.Images {
			img, err := eng.InspectImage(ctx, ref)
			if err != nil {
				return engineError(err)
			}
			if set.Image(img.ID) != nil {
				return fail(protocol.CodeConflict, "image %s is Docker Manager's own and is never migrated", ref)
			}
		}
		rc, err := eng.SaveImage(ctx, in.Images)
		if err != nil {
			return engineError(err)
		}
		_, err = io.Copy(fw, rc)
		_ = rc.Close()
		if err != nil {
			return s.transferError(err)
		}
		stats.Entries = int64(len(in.Images))
	}
	if err := fw.Close(); err != nil {
		return s.transferError(err)
	}
	sum := fw.Summary()
	s.log.Info("migration part sent", "migration_id", in.MigrationID, "part", in.Part, "bytes", sum.Bytes, "sha256", sum.SHA256)
	return st.CloseWithResult(protocol.MigrationPartResult{Bytes: sum.Bytes, SHA256: sum.SHA256, Chunks: sum.Chunks,
		Entries: stats.Entries, Skipped: stats.Skipped, SkippedCount: stats.SkippedCount})
}

// transferError keeps stream and checksum failures distinguishable.
func (s *Service) transferError(err error) error {
	var he *session.HandlerError
	switch {
	case errors.As(err, &he):
		return he
	case errors.Is(err, transfer.ErrChecksum), errors.Is(err, streammux.ErrVerification):
		return fail(protocol.CodeDigestMismatch, "%s", err.Error())
	case errors.Is(err, ErrUnsafeArchive), errors.Is(err, transfer.ErrFormat):
		return fail(protocol.CodeInvalidFrame, "%s", err.Error())
	case errors.Is(err, ErrChanged):
		return fail(protocol.CodeConflict, "%s", err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fail(protocol.CodeCancelled, "%s", err.Error())
	case errors.Is(err, fs.ErrExist):
		return fail(protocol.CodeAlreadyExists, "%s", err.Error())
	}
	var ce *streammux.CloseError
	if errors.As(err, &ce) {
		return fail(protocol.CodeCancelled, "%s", err.Error())
	}
	return fail(protocol.CodeInternal, "%s", err.Error())
}

func stagingDir(id string) string { return protocol.MigrationStagingDir + "/" + id }

// receive writes one part (migration.receive, destination).
func (s *Service) receive(ctx context.Context, st *streammux.Stream) error {
	in, err := decode[protocol.MigrationReceiveInput](st.Input())
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return invalid(err)
	}
	var res protocol.MigrationPartResult
	switch in.Part {
	case protocol.PartProject:
		sfs, stacksDir, err := s.openStacks()
		if err != nil {
			return err
		}
		defer func() { _ = sfs.Close() }()
		staging := stagingDir(in.MigrationID)
		if err := sfs.MkdirAll(staging, 0o700); err != nil {
			return s.transferError(err)
		}
		// A retried part starts over.
		if err := sfs.RemoveAll(staging + "/project"); err != nil {
			return s.transferError(err)
		}
		if err := sfs.Mkdir(staging+"/project", 0o700); err != nil {
			return s.transferError(err)
		}
		dst, err := sfs.Sub(staging + "/project")
		if err != nil {
			return s.transferError(err)
		}
		defer func() { _ = dst.Close() }()
		if res, err = extractFramed(ctx, st, dst, s.opts.FreeBytes(stacksDir)); err != nil {
			return s.transferError(err)
		}
	case protocol.PartVolume:
		eng, err := s.engine()
		if err != nil {
			return err
		}
		v, err := s.prepareVolume(ctx, eng, in.MigrationID, *in.Volume)
		if err != nil {
			return err
		}
		set, _, err := s.protectedSet(ctx, eng)
		if err != nil {
			return err
		}
		dst, err := s.openVolume(v, set)
		if err != nil {
			return err
		}
		defer func() { _ = dst.Close() }()
		if err := clearRoot(dst); err != nil {
			return s.transferError(err)
		}
		volumesDir := ""
		if r, _ := s.storage(); r != nil {
			volumesDir = r.VolumesDir
		}
		if res, err = extractFramed(ctx, st, dst, s.opts.FreeBytes(volumesDir)); err != nil {
			return s.transferError(err)
		}
	case protocol.PartImage:
		eng, err := s.engine()
		if err != nil {
			return err
		}
		r := transfer.NewReader(st)
		if err := eng.LoadImage(ctx, r); err != nil {
			if errors.Is(err, transfer.ErrChecksum) || errors.Is(err, transfer.ErrFormat) || errors.Is(err, streammux.ErrVerification) {
				return s.transferError(err)
			}
			return engineError(err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			return s.transferError(err)
		}
		if !r.Done() {
			return fail(protocol.CodeDigestMismatch, "the image archive ended without its trailer")
		}
		for _, ref := range in.Images {
			if _, err := eng.InspectImage(ctx, ref); err != nil {
				return fail(protocol.CodeNotFound, "image %s is not on the Engine after loading the archive", ref)
			}
		}
		sum := r.Summary()
		res = protocol.MigrationPartResult{Bytes: sum.Bytes, SHA256: sum.SHA256, Chunks: sum.Chunks, Entries: int64(len(in.Images))}
	}
	s.log.Info("migration part received", "migration_id", in.MigrationID, "part", in.Part, "bytes", res.Bytes, "sha256", res.SHA256)
	return st.CloseWithResult(res)
}

// extractFramed verifies and extracts a framed tar stream.
func extractFramed(ctx context.Context, r io.Reader, dst FS, free int64) (protocol.MigrationPartResult, error) {
	fr := transfer.NewReader(r)
	var limit int64
	if free > 0 {
		limit = free
	}
	st, err := ExtractTree(ctx, dst, fr, ExtractOptions{MaxBytes: limit})
	if err != nil {
		return protocol.MigrationPartResult{}, err
	}
	// The tar end marker may leave padding; the trailer must follow.
	if _, err := io.Copy(io.Discard, fr); err != nil {
		return protocol.MigrationPartResult{}, err
	}
	if !fr.Done() {
		return protocol.MigrationPartResult{}, fmt.Errorf("%w: missing trailer", transfer.ErrFormat)
	}
	sum := fr.Summary()
	return protocol.MigrationPartResult{Bytes: sum.Bytes, SHA256: sum.SHA256, Chunks: sum.Chunks, Entries: st.Entries}, nil
}

// clearRoot removes everything inside a root (a volume the migration created
// itself, before a retried part).
func clearRoot(fsys FS) error {
	names, err := fsys.ReadDir(".")
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := fsys.RemoveAll(n); err != nil {
			return err
		}
	}
	return nil
}

// prepareVolume creates the destination volume, or accepts one this
// migration created before (a retried part); any other existing volume is
// a conflict.
func (s *Service) prepareVolume(ctx context.Context, eng engine.Engine, id string, spec protocol.MigrationVolumeSpec) (engine.Volume, error) {
	v, err := eng.InspectVolume(ctx, spec.Name)
	switch {
	case err == nil:
		if protocol.LabelValue(v.Labels, protocol.LabelMigration) != id {
			return v, fail(protocol.CodeAlreadyExists, "volume %s already exists on the destination", spec.Name)
		}
		return v, nil
	case !engine.IsCode(err, engine.CodeNotFound):
		return v, engineError(err)
	}
	labels := maps.Clone(spec.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[protocol.LabelMigration] = id
	if _, err := eng.CreateVolume(ctx, engine.VolumeSpec{Name: spec.Name, Driver: "local", Labels: labels}); err != nil {
		return v, engineError(err)
	}
	v, err = eng.InspectVolume(ctx, spec.Name)
	if err != nil {
		return v, engineError(err)
	}
	if protocol.LabelValue(v.Labels, protocol.LabelMigration) != id {
		return v, fail(protocol.CodeAlreadyExists, "volume %s appeared on the destination meanwhile", spec.Name)
	}
	return v, nil
}

// --- requests ---

// stop stops the project's containers in reverse dependency order.
func (s *Service) stop(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := decode[protocol.MigrationStopInput](raw)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateJobLinked(in.MigrationID); err != nil {
		return nil, invalid(err)
	}
	if err := in.Stack.Validate(); err != nil {
		return nil, invalid(err)
	}
	if in.TimeoutSeconds < 0 || in.TimeoutSeconds > 3600 {
		return nil, fail(protocol.CodeInvalidFrame, "timeoutSeconds must be between 0 and 3600")
	}
	var timeout *time.Duration
	if in.TimeoutSeconds > 0 {
		d := time.Duration(in.TimeoutSeconds) * time.Second
		timeout = &d
	}
	return s.lifecycleOp(ctx, in.Stack.ProjectName, func(g *lifecycle.Graph, rt lifecycle.Runtime, o lifecycle.Options) (lifecycle.Report, error) {
		o.StopTimeout = timeout
		return lifecycle.Stop(ctx, g, rt, nil, o)
	}, true)
}

// start starts exactly the services that ran before (dependencies first).
func (s *Service) start(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := decode[protocol.MigrationStartInput](raw)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateJobLinked(in.MigrationID); err != nil {
		return nil, invalid(err)
	}
	if err := in.Stack.Validate(); err != nil {
		return nil, invalid(err)
	}
	return s.lifecycleOp(ctx, in.Stack.ProjectName, func(g *lifecycle.Graph, rt lifecycle.Runtime, o lifecycle.Options) (lifecycle.Report, error) {
		return lifecycle.Resume(ctx, g, rt, in.Services, o)
	}, false)
}

func (s *Service) lifecycleOp(ctx context.Context, project string,
	op func(*lifecycle.Graph, lifecycle.Runtime, lifecycle.Options) (lifecycle.Report, error), stopping bool) (any, error) {
	eng, err := s.engine()
	if err != nil {
		return nil, err
	}
	if stopping {
		if err := s.opts.Guard.CheckProject(ctx, eng, project, protection.Stop); err != nil {
			return nil, fail(protocol.CodeConflict, "%s", err.Error())
		}
	}
	list, err := lifecycle.ProjectContainers(ctx, eng, project)
	if err != nil {
		return nil, engineError(err)
	}
	out := protocol.MigrationLifecycleOutput{Before: states(list), After: []protocol.ServiceState{}}
	if len(list) == 0 {
		return out, nil
	}
	g, err := lifecycle.GraphFromContainers(list)
	if err != nil {
		return nil, fail(protocol.CodeConflict, "%s", err.Error())
	}
	rt := lifecycle.EngineRuntime{Engine: eng, Project: project, Clock: s.opts.Clock}
	rep, opErr := op(g, rt, lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout})
	out.Warnings = rep.Warnings
	if after, err := lifecycle.ProjectContainers(ctx, eng, project); err == nil {
		out.After = states(after)
	}
	if opErr != nil {
		return nil, fail(protocol.CodeConflict, "%s: %s", lifecycle.CodeOf(opErr), opErr.Error())
	}
	return out, nil
}

func states(list []engine.Container) []protocol.ServiceState {
	by := map[string]*protocol.ServiceState{}
	for _, c := range list {
		svc := c.Labels[lifecycle.ComposeServiceLabel]
		st, ok := by[svc]
		if !ok {
			st = &protocol.ServiceState{Service: svc}
			by[svc] = st
		}
		st.Containers++
		if c.State == "running" {
			st.Running++
		}
	}
	out := make([]protocol.ServiceState, 0, len(by))
	for _, k := range slices.Sorted(maps.Keys(by)) {
		out = append(out, *by[k])
	}
	return out
}

const committedMarker = "committed"

// commit moves the staged project directory into place.
func (s *Service) commit(_ context.Context, raw json.RawMessage) (any, error) {
	in, err := decode[protocol.MigrationCommitInput](raw)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateJobLinked(in.MigrationID); err != nil {
		return nil, invalid(err)
	}
	if !protocol.ValidDirName(in.Dir) {
		return nil, fail(protocol.CodeInvalidFrame, "invalid project directory name")
	}
	sfs, stacksDir, err := s.openStacks()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sfs.Close() }()
	staging := stagingDir(in.MigrationID)
	out := protocol.MigrationCommitOutput{Path: path.Join(stacksDir, in.Dir)}
	if prev := readMarker(sfs, staging); prev == in.Dir {
		if _, err := sfs.Lstat(in.Dir); err == nil {
			if _, err := sfs.Lstat(staging + "/project"); errors.Is(err, fs.ErrNotExist) {
				return out, nil // already committed
			}
		}
	}
	if _, err := sfs.Lstat(staging + "/project"); err != nil {
		return nil, fail(protocol.CodeNotFound, "nothing was staged for this migration")
	}
	if _, err := sfs.Lstat(in.Dir); err == nil {
		return nil, fail(protocol.CodeAlreadyExists, "the directory %s already exists in the stacks volume", in.Dir)
	}
	if err := writeMarker(sfs, staging, in.Dir); err != nil {
		return nil, fail(protocol.CodeInternal, "record the commit: %s", err.Error())
	}
	if err := sfs.Rename(staging+"/project", in.Dir); err != nil {
		return nil, s.transferError(err)
	}
	s.log.Info("migrated project directory committed", "migration_id", in.MigrationID, "dir", in.Dir)
	return out, nil
}

func readMarker(fsys FS, staging string) string {
	f, err := fsys.Open(staging + "/" + committedMarker)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(io.LimitReader(f, 256))
	d := strings.TrimSpace(string(b))
	if !protocol.ValidDirName(d) {
		return ""
	}
	return d
}

func writeMarker(fsys FS, staging, dir string) error {
	_ = fsys.RemoveAll(staging + "/" + committedMarker)
	f, err := fsys.Create(staging + "/" + committedMarker)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, dir+"\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// cleanup removes what a migration created on the destination.
func (s *Service) cleanup(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := decode[protocol.MigrationCleanupInput](raw)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateJobLinked(in.MigrationID); err != nil {
		return nil, invalid(err)
	}
	if in.Project != "" && !protocol.ValidProjectName(in.Project) {
		return nil, fail(protocol.CodeInvalidFrame, "invalid project name")
	}
	out := protocol.MigrationCleanupOutput{Removed: []string{}}
	sfs, stacksDir, err := s.openStacks()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sfs.Close() }()
	staging := stagingDir(in.MigrationID)
	if !in.Finished {
		eng, err := s.engine()
		if err != nil {
			return nil, err
		}
		if dir := readMarker(sfs, staging); dir != "" {
			workDir := path.Join(stacksDir, dir)
			if in.Project != "" {
				removed, err := s.removeProjectAt(ctx, eng, in.Project, workDir)
				out.Removed = append(out.Removed, removed...)
				if err != nil {
					return out, err
				}
			}
			if err := sfs.RemoveAll(dir); err != nil {
				return out, s.transferError(err)
			}
			out.Removed = append(out.Removed, "directory "+dir)
		}
		for _, name := range in.Volumes {
			if !protocol.ValidDockerName(name) {
				continue
			}
			v, err := eng.InspectVolume(ctx, name)
			if err != nil {
				continue
			}
			if protocol.LabelValue(v.Labels, protocol.LabelMigration) != in.MigrationID {
				out.Kept = append(out.Kept, "volume "+name+" (not created by this migration)")
				continue
			}
			if err := eng.RemoveVolume(ctx, name, false); err != nil {
				out.Kept = append(out.Kept, "volume "+name+" ("+string(engine.CodeOf(err))+")")
				continue
			}
			out.Removed = append(out.Removed, "volume "+name)
		}
	}
	if err := sfs.RemoveAll(staging); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return out, s.transferError(err)
	}
	s.log.Info("migration cleaned up on the destination", "migration_id", in.MigrationID, "finished", in.Finished, "removed", len(out.Removed))
	return out, nil
}

// removeProjectAt removes a project's containers whose working directory is
// dir (the directory the migration committed) and the project's networks
// that are left without containers.
func (s *Service) removeProjectAt(ctx context.Context, eng engine.Engine, project, dir string) ([]string, error) {
	list, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true, Labels: []string{protocol.ComposeProjectLabel + "=" + project}})
	if err != nil {
		return nil, engineError(err)
	}
	var removed []string
	for _, c := range list {
		if path.Clean(c.Labels[protocol.ComposeWorkingDirLabel]) != path.Clean(dir) {
			continue
		}
		if err := eng.RemoveContainer(ctx, c.ID, engine.RemoveOptions{Force: true}); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			return removed, engineError(err)
		}
		removed = append(removed, "container "+strings.TrimPrefix(firstName(c), "/"))
	}
	nets, err := eng.ListNetworks(ctx, protocol.ComposeProjectLabel+"="+project)
	if err != nil {
		return removed, nil //nolint:nilerr // networks are best effort
	}
	for _, n := range nets {
		if err := eng.RemoveNetwork(ctx, n.ID); err == nil {
			removed = append(removed, "network "+n.Name)
		}
	}
	return removed, nil
}

func firstName(c engine.Container) string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return c.ID
}

// projectSpec loads a project's definition through fsys (the project
// directory's root) into a compose spec that loads from memory: the
// Compose, override and env files by their known names or the reference's
// explicit ones. Service env_files are not read (previews never need their
// values).
func projectSpec(fsys FS, ref protocol.ProjectRef, dir string) compose.ProjectSpec {
	spec := compose.ProjectSpec{Name: ref.ProjectName, Dir: dir, ConfigFiles: ref.ConfigFiles, EnvFiles: ref.EnvFiles,
		Profiles: ref.Profiles, Content: map[string][]byte{}, SkipEnvFiles: true}
	names := append(slices.Clone(compose.DefaultConfigFiles), "compose.override.yaml", "compose.override.yml",
		"docker-compose.override.yaml", "docker-compose.override.yml", ".env")
	names = append(append(names, ref.ConfigFiles...), ref.EnvFiles...)
	for _, n := range names {
		f, err := fsys.Open(n)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, protocol.MaxSourceFile+1))
		_ = f.Close()
		if err == nil && len(b) <= protocol.MaxSourceFile {
			spec.Content[n] = b
		}
	}
	return spec
}
