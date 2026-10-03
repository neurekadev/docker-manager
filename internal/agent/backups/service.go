// Package backups is the agent side of Docker Manager backups (#10): the
// backup.run, backup.retention and backup.verify executors, the
// backup.scope_preview / backup.snapshots / backup.contents requests and
// the backup.file stream. The agent runs restic (internal/restic) against
// its environment's physical repository below the destination the manager
// names; credentials arrive with each command or request and are never
// stored.
//
// Scope rules (#10): a stack item backs up its project directory (Compose
// files, .env, workspace and every relative bind source inside it) and its
// named volumes; anonymous volumes only when enabled; bind sources outside
// the project directory only when the policy opts in AND the path is in
// DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST. Symlinks never lead a source out of
// its root, Docker Manager's own volumes are never selected (#32), and a local
// repository may not lie inside any source.
package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/protect"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/agent/volumelabels"
	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/streammux"
)

// Loader loads Compose projects (*compose.Adapter implements it).
type Loader interface {
	Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error)
}

// Options configures the service.
type Options struct {
	// Engine, Loader and Storage return the live components (nil while the
	// Engine is not connected / storage not verified).
	Engine  func() engine.Engine
	Loader  func() Loader
	Storage func() *storage.Result
	// Guard identifies Docker Manager's own containers and volumes (#32).
	Guard *protect.Guard
	// Restic runs restic (default: the pinned binary).
	Restic restic.Opener
	// ExternalAllowlist lists host paths outside project directories that
	// policies may opt into (DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST).
	ExternalAllowlist []string
	// VolumeLabels are the Compose labels of stack volumes: a backup
	// exclude label declared there counts like one on the volume (nil: none).
	VolumeLabels *volumelabels.Store
	// StateDir keeps the agent's pending-prune marks (backup.PrunePending;
	// "": none).
	StateDir string
	Clock    clock.Clock
	Logger   *slog.Logger
	// WaitTimeout bounds each dependency wait when restarting containers.
	WaitTimeout time.Duration
	// EstimateBudget bounds the entries walked per item for size
	// estimates (default 200000).
	EstimateBudget int
}

// Service serves backups on the agent.
type Service struct {
	opts Options
	log  *slog.Logger
}

// New returns a Service.
func New(opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Restic == nil {
		opts.Restic = &restic.Runner{Logger: opts.Logger}
	}
	if opts.EstimateBudget <= 0 {
		opts.EstimateBudget = 200000
	}
	return &Service{opts: opts, log: opts.Logger}
}

// Requests returns the request handlers.
func (s *Service) Requests() map[string]session.RequestHandler {
	return map[string]session.RequestHandler{
		protocol.ReqBackupScopePreview: s.scopePreview,
		protocol.ReqBackupSnapshots:    s.snapshots,
		protocol.ReqBackupContents:     s.contents,
		protocol.ReqRestorePreview:     s.restorePreview,
	}
}

// Streams returns the stream handlers.
func (s *Service) Streams() map[string]session.StreamHandler {
	return map[string]session.StreamHandler{protocol.StreamBackupFile: s.file}
}

// Executors returns the job executors.
func (s *Service) Executors() []jobexec.Executor {
	return []jobexec.Executor{
		{Kind: jobspec.BackupRun, Steps: map[string]jobexec.StepFunc{
			"prepare":          s.stepPrepare,
			"stop_containers":  s.stepStopContainers,
			"snapshot":         s.stepSnapshot,
			"start_containers": s.stepStartContainers,
			"record":           s.stepRecord,
		}, Compensations: map[string]jobexec.CompensationFunc{jobspec.CompStartContainers: s.compStartContainers}},
		{Kind: jobspec.RestoreRun, Steps: map[string]jobexec.StepFunc{
			"prepare":          s.stepRestorePrepare,
			"stop_containers":  s.stepRestoreStop,
			"restore_data":     s.stepRestoreData,
			"start_containers": s.stepRestoreStart,
		}, Compensations: map[string]jobexec.CompensationFunc{jobspec.CompStartContainers: s.compStartContainers}},
		{Kind: jobspec.BackupRetention, Steps: map[string]jobexec.StepFunc{
			"forget":           s.stepForget,
			"prune_repository": s.stepPrune,
		}},
		{Kind: jobspec.BackupVerify, Steps: map[string]jobexec.StepFunc{
			"check": s.stepCheck,
		}},
	}
}

var errEngineUnavailable = &session.HandlerError{Code: protocol.CodeEngineUnavailable, Message: "the Docker Engine is not connected", Retryable: true}

func (s *Service) engine() (engine.Engine, error) {
	if s.opts.Engine == nil {
		return nil, errEngineUnavailable
	}
	if e := s.opts.Engine(); e != nil {
		return e, nil
	}
	return nil, errEngineUnavailable
}

func (s *Service) storage() *storage.Result {
	if s.opts.Storage == nil {
		return nil
	}
	return s.opts.Storage()
}

// --- repository access ---

// location validates a repository reference for this agent and returns
// the restic location with the credential.
func (s *Service) location(ref protocol.BackupRepositoryRef, cred *protocol.RepositoryCredential) (restic.Location, error) {
	if err := ref.Validate(); err != nil {
		return restic.Location{}, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: err.Error()}
	}
	if cred == nil || cred.RepositoryID != ref.RepositoryID || cred.Password == "" {
		return restic.Location{}, &session.HandlerError{Code: protocol.CodeUnauthorized, Message: "no credential for the backup repository"}
	}
	return ref.Destination.Location(ref.Scope, cred.S3()), nil
}

// open opens the location of a request (no initialization).
func (s *Service) open(ctx context.Context, ref protocol.BackupRepositoryRef, cred *protocol.RepositoryCredential) (backup.Opened, error) {
	loc, err := s.location(ref, cred)
	if err != nil {
		return backup.Opened{}, err
	}
	o, err := backup.OpenLocation(ctx, s.opts.Restic, loc, cred.Password, cred.PreviousPassword, false)
	if err != nil {
		return o, handlerError(err)
	}
	return o, nil
}

// handlerError maps restic errors to protocol codes (the manager maps them
// to job error classes and API errors).
func handlerError(err error) error {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return err
	}
	switch code := restic.CodeOf(err); code {
	case restic.CodeRepositoryNotFound, restic.CodeKeyRejected, restic.CodeLocked, restic.CodeRepositoryDamaged,
		restic.CodeAccessDenied, restic.CodeUnreachable, restic.CodeSnapshotNotFound:
		return &session.HandlerError{Code: code, Message: err.Error()}
	case restic.CodeUnavailable:
		return &session.HandlerError{Code: protocol.CodeResticUnavailable, Message: err.Error()}
	case restic.CodeCancelled:
		return &session.HandlerError{Code: protocol.CodeCancelled, Message: "cancelled"}
	case "":
		return &session.HandlerError{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return &session.HandlerError{Code: protocol.CodeResticFailed, Message: err.Error()}
}

func decode[T any](input json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(input, &v); err != nil {
		return v, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed input"}
	}
	return v, nil
}

// --- requests ---

func (s *Service) scopePreview(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.BackupScopePreviewInput](input)
	if err != nil {
		return nil, err
	}
	if len(in.Items) == 0 || len(in.Items) > protocol.MaxBackupItems {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: fmt.Sprintf("1 to %d items", protocol.MaxBackupItems)}
	}
	if in.Repository != nil {
		if err := in.Repository.Validate(); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: err.Error()}
		}
	}
	budget := in.EstimateBudget
	if budget <= 0 || budget > s.opts.EstimateBudget {
		budget = s.opts.EstimateBudget
	}
	out := protocol.BackupScopePreviewOutput{Items: []protocol.ScopePreviewItem{}}
	running := 0
	for _, it := range in.Items {
		p := s.plan(ctx, it, in.Shutdown)
		pi := p.preview()
		if p.err == nil {
			pi.Bytes, pi.Files, pi.Estimated = estimate(ctx, p.paths, p.excludes, budget)
		}
		for _, a := range pi.Affected {
			if a.StopOrder > 0 {
				running++
			}
		}
		out.Items = append(out.Items, pi)
	}
	if in.Shutdown && running > 0 {
		out.Downtime = fmt.Sprintf("%d running container(s) stop for the duration of each backup and start again afterwards "+
			"(only those that were running, in dependency order).", running)
	}
	return out, nil
}

// maxManifests bounds the manifests a listing decodes.
const maxManifests = 50

func (s *Service) snapshots(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.BackupSnapshotsInput](input)
	if err != nil {
		return nil, err
	}
	o, err := s.open(ctx, in.Repository, in.Credential)
	if err != nil {
		return nil, err
	}
	snaps, err := o.Repo.Snapshots(ctx, restic.SnapshotFilter{Tags: in.Tags})
	if err != nil {
		return nil, handlerError(err)
	}
	out := protocol.BackupSnapshotsOutput{ResticRepositoryID: o.ResticRepositoryID, Snapshots: snaps}
	if len(out.Snapshots) > backup.MaxListedSnapshots {
		out.Snapshots = out.Snapshots[len(out.Snapshots)-backup.MaxListedSnapshots:]
	}
	if in.Manifests {
		out.Manifests = readManifests(ctx, o.Repo, snaps, maxManifests)
	}
	return out, nil
}

// readManifests decodes the newest manifest snapshots (corrupt ones are
// skipped: they surface as missing members).
func readManifests(ctx context.Context, repo restic.Repo, snaps []restic.Snapshot, limit int) []backup.Manifest {
	var out []backup.Manifest
	for i := len(snaps) - 1; i >= 0 && len(out) < limit; i-- {
		if !snaps[i].HasTag(backup.TagManifest) {
			continue
		}
		var buf limitedBuffer
		buf.max = backup.MaxManifestSize + 1024
		if err := repo.Dump(ctx, snaps[i].ID, "/"+backup.ManifestFile, &buf); err != nil {
			continue
		}
		if m, err := backup.DecodeManifest(buf.b); err == nil {
			out = append(out, m)
		}
	}
	return out
}

type limitedBuffer struct {
	b   []byte
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(l.b)+len(p) > l.max {
		return 0, errors.New("too large")
	}
	l.b = append(l.b, p...)
	return len(p), nil
}

func (s *Service) contents(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.BackupContentsInput](input)
	if err != nil {
		return nil, err
	}
	if !protocol.ValidSnapshotID(in.SnapshotID) || (in.Path != "" && !protocol.ValidSnapshotPath(in.Path)) {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "invalid snapshot or path"}
	}
	o, err := s.open(ctx, in.Repository, in.Credential)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 || limit > restic.DefaultMaxNodes {
		limit = 1000
	}
	l, err := o.Repo.Ls(ctx, in.SnapshotID, in.Path, in.Recursive, limit)
	if err != nil {
		return nil, handlerError(err)
	}
	return protocol.BackupContentsOutput{Nodes: l.Nodes, Truncated: l.Truncated}, nil
}

func (s *Service) file(ctx context.Context, st *streammux.Stream) error {
	in, err := decode[protocol.BackupFileStreamInput](st.Input())
	if err != nil {
		return err
	}
	if !protocol.ValidSnapshotID(in.SnapshotID) || !protocol.ValidSnapshotPath(in.Path) || in.MaxBytes <= 0 {
		return &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "invalid snapshot, path or size"}
	}
	o, err := s.open(ctx, in.Repository, in.Credential)
	if err != nil {
		return err
	}
	l, err := o.Repo.Ls(ctx, in.SnapshotID, in.Path, false, 2)
	if err != nil {
		return handlerError(err)
	}
	for _, n := range l.Nodes {
		if n.Path != in.Path {
			continue
		}
		if n.Type != "file" {
			return &session.HandlerError{Code: protocol.CodeIsDirectory, Message: "only regular files can be downloaded"}
		}
		if n.Size > in.MaxBytes {
			return &session.HandlerError{Code: protocol.CodeTooLarge, Message: "the file exceeds the download limit"}
		}
		w := &capWriter{w: st, left: in.MaxBytes}
		if err := o.Repo.Dump(ctx, in.SnapshotID, in.Path, w); err != nil {
			return handlerError(err)
		}
		return st.CloseWrite()
	}
	return &session.HandlerError{Code: protocol.CodeSnapshotNotFound, Message: "no such file in the snapshot"}
}

type capWriter struct {
	w    io.Writer
	left int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, &session.HandlerError{Code: protocol.CodeTooLarge, Message: "the file grew beyond the download limit"}
	}
	n, err := c.w.Write(p)
	c.left -= int64(n)
	return n, err
}

// --- path helpers (OS paths; the Engine reports slash paths) ---

func osPath(p string) string { return filepath.Clean(filepath.FromSlash(p)) }

// inside reports whether child equals or lies below parent.
func inside(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// realPath resolves symlinks of an existing path (missing: the cleaned
// path and exists false).
func realPath(p string) (string, bool, error) {
	r, err := filepath.EvalSymlinks(p)
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return "", false, err
	}
	return r, true, nil
}

// projectRuntime drives a Compose project's containers.
func (s *Service) projectRuntime(eng engine.Engine, project string) lifecycle.EngineRuntime {
	return lifecycle.EngineRuntime{Engine: eng, Project: project, Clock: s.opts.Clock}
}
