package stackarchives

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/transfer"
)

// Finding codes of archives (besides the migration codes they reuse).
const (
	FindingArchiveTooLarge    = "archive_too_large"
	FindingManagerSpace       = "manager_space"
	FindingVolumeNotPermitted = "volume_not_permitted"
	FindingNotExportable      = "not_exportable"
)

// assumedRate estimates the copy speed for the downtime (bytes/s).
const assumedRate = 50 << 20

// ExportRequest is an export preview or start.
type ExportRequest struct {
	// ExcludeVolumes are Compose volume keys whose data is left out.
	ExcludeVolumes []string
	// TimeoutSeconds is the stop grace period of the stack's containers.
	TimeoutSeconds int
	IdempotencyKey string
	// MayDownload reports whether the caller may download a volume's files
	// (nil: every volume); a volume they may not must be left out.
	MayDownload func(volume string) bool
}

// ExportVolume is a named volume of the stack and what an export does
// with it.
type ExportVolume struct {
	Key  string
	Name string
	// Included: its data goes into the archive. Otherwise Excluded (the
	// request left it out) or Reason says why it cannot be.
	Included bool
	Excluded bool
	Reason   string
	// NotPermitted: the caller may not download the volume's files, so it
	// must be left out.
	NotPermitted bool
	Bytes        int64
	Entries      int64
	// Truncated: the size is a lower bound.
	Truncated      bool
	FollowsProject bool
	Labels         map[string]string
}

// ExportFile is an archive ready for download.
type ExportFile struct {
	JobID     string    `json:"jobId"`
	StackID   string    `json:"stackId"`
	FileName  string    `json:"fileName"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	// Volumes are the included volume keys; VolumeNames their names (a
	// download needs volume.files.download on each).
	Volumes     []string `json:"volumes"`
	VolumeNames []string `json:"volumeNames"`
}

// ExportPlan is an export's preview, computed before anything stops.
type ExportPlan struct {
	StackID     string
	ProjectName string
	Blockers    []migrations.Finding
	Warnings    []migrations.Finding
	Volumes     []ExportVolume
	// NotIncluded are the anonymous volumes and the binds outside the
	// project directory (the volumes are in Volumes).
	NotIncluded  []Exclusion
	ProjectBytes int64
	VolumeBytes  int64
	TotalBytes   int64
	Truncated    bool
	// ManagerFree is the free space for archives (-1 unknown); MaxBytes
	// the archive limit.
	ManagerFree int64
	MaxBytes    int64
	// Running are the services that run (stopped while the archive is
	// written, then started again).
	Running         []string
	DowntimeSeconds int64
	// Latest is the newest archive of the stack still available.
	Latest *ExportFile
}

// Allowed reports whether the export can start.
func (p ExportPlan) Allowed() bool { return len(p.Blockers) == 0 }

// BlockedError refuses an export or import with its blockers.
type BlockedError struct{ Blockers []migrations.Finding }

func (e *BlockedError) Error() string {
	codes := make([]string, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		codes = append(codes, b.Code)
	}
	return "blocked by its check: " + strings.Join(codes, ", ")
}

// ErrExportNotFound: no such archive (expired, removed or never written).
var ErrExportNotFound = errors.New("stack archive not found")

func stackRef(st domain.Stack) protocol.ProjectRef {
	return protocol.ProjectRef{Root: st.Root, RootPath: st.RootPath, Dir: st.Dir, ProjectName: st.Name,
		ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles}
}

// sourceFacts asks the stack's agent about its project.
func (s *Service) sourceFacts(ctx context.Context, st domain.Stack, measure bool) (*protocol.MigrationProjectFacts, error) {
	ref := stackRef(st)
	var out protocol.MigrationPreviewOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{Role: protocol.RoleSource,
		Source: &protocol.MigrationSourceQuery{Stack: &ref, Measure: measure}}, &out, s.opts.RequestTimeout); err != nil {
		return nil, err
	}
	if out.Source == nil || out.Source.Project == nil {
		return nil, errors.New("the agent returned no project facts")
	}
	return out.Source.Project, nil
}

// AgentError is a failed request to the stack's or the destination's agent
// during a preview.
type AgentError struct {
	Err              error
	Offline, Timeout bool
}

func (e *AgentError) Error() string { return "the agent: " + e.Err.Error() }

// Unwrap returns the transport error.
func (e *AgentError) Unwrap() error { return e.Err }

func agentErr(err error) error {
	return &AgentError{Err: err, Offline: errors.Is(err, jobs.ErrAgentOffline), Timeout: errors.Is(err, protocol.ErrRequestTimeout)}
}

// PreviewExport computes what exporting st does.
func (s *Service) PreviewExport(ctx context.Context, st domain.Stack, r ExportRequest) (ExportPlan, error) {
	plan, _, err := s.previewExport(ctx, st, r, true)
	if err != nil {
		return plan, err
	}
	plan.Latest = s.LatestExport(ctx, st.ID)
	return plan, nil
}

func (s *Service) previewExport(ctx context.Context, st domain.Stack, r ExportRequest, measure bool) (ExportPlan, *protocol.MigrationProjectFacts, error) {
	plan := ExportPlan{StackID: st.ID, ProjectName: st.Name, MaxBytes: s.opts.MaxSize, ManagerFree: s.free(s.exportsDir())}
	env, err := s.opts.Environments.GetEnvironment(ctx, st.EnvironmentID)
	if err != nil {
		return plan, nil, err
	}
	block := func(code, msg string) {
		plan.Blockers = append(plan.Blockers, migrations.Finding{Code: code, Message: msg})
	}
	switch {
	case env.Status == domain.EnvironmentArchived:
		return plan, nil, domain.ErrEnvironmentArchived
	case !env.Online || !s.opts.Agents.Online(st.EnvironmentID):
		block(migrations.FindingEnvironmentOffline, "the stack's environment is offline")
		return plan, nil, nil
	case !s.sourceSupported(st.EnvironmentID):
		block(migrations.FindingAgentUnsupported, "the environment's agent cannot export stacks; update it")
		return plan, nil, nil
	}
	if pr, err := s.opts.Stacks.Protection(ctx, st); err != nil {
		return plan, nil, err
	} else if pr != nil {
		block(migrations.FindingDockerManagerResource, "Docker Manager's own stack cannot be exported")
	}
	facts, err := s.sourceFacts(ctx, st, measure)
	if err != nil {
		return plan, nil, agentErr(err)
	}
	evaluateExport(&plan, facts, r)
	// An archive an upload would refuse is refused here, before the stack
	// stops (more volumes or labels than an archive holds, ...).
	if err := s.manifest(st, facts, exportInputOf(st, plan, 0), exportSizes{}).Validate(); err != nil {
		plan.Blockers = append(plan.Blockers, migrations.Finding{Code: FindingNotExportable,
			Message: "the stack cannot be written as an archive: " + strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")})
	}
	return plan, facts, nil
}

// exportInputOf is the job input of exporting plan's included volumes.
func exportInputOf(st domain.Stack, plan ExportPlan, timeout int) exportInput {
	in := exportInput{StackID: st.ID, TimeoutSeconds: timeout}
	for _, v := range plan.Volumes {
		switch {
		case v.Included:
			in.Volumes = append(in.Volumes, exportVolume{Key: v.Key, Name: v.Name, FollowsProject: v.FollowsProject, Labels: v.Labels})
		case v.Excluded:
			in.Excluded = append(in.Excluded, v.Key)
		}
	}
	return in
}

// evaluateExport fills a plan from the project's facts (pure).
func evaluateExport(plan *ExportPlan, f *protocol.MigrationProjectFacts, r ExportRequest) {
	warn := func(code, msg, res string) {
		plan.Warnings = append(plan.Warnings, migrations.Finding{Code: code, Message: msg, Resource: res})
	}
	if f.Protected {
		if !slices.ContainsFunc(plan.Blockers, func(b migrations.Finding) bool { return b.Code == migrations.FindingDockerManagerResource }) {
			plan.Blockers = append(plan.Blockers, migrations.Finding{Code: migrations.FindingDockerManagerResource,
				Message: "Docker Manager's own stack cannot be exported"})
		}
	}
	plan.ProjectBytes, plan.Truncated = f.DirBytes, f.DirTruncated
	for _, v := range f.Volumes {
		if v.Anonymous {
			plan.NotIncluded = append(plan.NotIncluded, Exclusion{Kind: ExcludedAnonymous, Name: v.Name, Reason: "anonymous volume"})
			warn(migrations.FindingAnonymousVolume, "anonymous volume "+v.Name+" is not included", v.Name)
			continue
		}
		ev := ExportVolume{Key: v.Key, Name: v.Name, Bytes: v.Bytes, Entries: v.Entries, Truncated: v.Truncated,
			FollowsProject: v.Name == f.Name+"_"+v.Key, Labels: userLabels(v.Labels)}
		switch {
		case v.External:
			ev.Reason = "external volume: it must exist where the stack is created"
		case v.Protected:
			ev.Reason = "Docker Manager's own volume"
		case !v.Exists:
			ev.Reason = "not created yet: Compose creates it empty"
		case !v.Supported:
			ev.Reason = "only plain local volumes can be exported"
			if v.Reason != "" {
				ev.Reason += " (" + v.Reason + ")"
			}
			warn(migrations.FindingVolumeDefinitionOnly, "the data of volume "+v.Name+" is not included: "+ev.Reason, v.Name)
		case slices.Contains(r.ExcludeVolumes, v.Key):
			ev.Excluded = true
			ev.NotPermitted = r.MayDownload != nil && !r.MayDownload(v.Name)
		case r.MayDownload != nil && !r.MayDownload(v.Name):
			ev.NotPermitted = true
			ev.Reason = "you may not download this volume's files"
			plan.Blockers = append(plan.Blockers, migrations.Finding{Code: FindingVolumeNotPermitted,
				Message: "you may not download the files of volume " + v.Name + "; leave it out", Resource: v.Name})
		case !volumeKeyRE.MatchString(v.Key):
			ev.Reason = "its Compose key cannot name a folder of the archive"
		default:
			ev.Included = true
			plan.VolumeBytes += v.Bytes
			plan.Truncated = plan.Truncated || v.Truncated
		}
		plan.Volumes = append(plan.Volumes, ev)
	}
	for _, b := range f.Binds {
		if b.External {
			plan.NotIncluded = append(plan.NotIncluded, Exclusion{Kind: ExcludedBind, Name: b.Source, Reason: "outside the project folder"})
			warn(migrations.FindingExternalBind, "the bind mount "+b.Source+" of "+b.Service+" lies outside the project folder and is not included", b.Source)
		}
	}
	for _, sv := range f.Services {
		if sv.Running {
			plan.Running = append(plan.Running, sv.Name)
		}
	}
	plan.TotalBytes = plan.ProjectBytes + plan.VolumeBytes
	if plan.Truncated {
		warn(migrations.FindingSizeEstimated, "the data was too large to measure completely; the sizes are lower bounds", "")
	}
	if plan.TotalBytes > plan.MaxBytes {
		plan.Blockers = append(plan.Blockers, migrations.Finding{Code: FindingArchiveTooLarge,
			Message: fmt.Sprintf("the data (%s) exceeds the archive limit of %s", humanize.Bytes(plan.TotalBytes), humanize.Bytes(plan.MaxBytes))})
	}
	if plan.ManagerFree >= 0 && plan.TotalBytes+spaceMargin > plan.ManagerFree {
		plan.Blockers = append(plan.Blockers, migrations.Finding{Code: FindingManagerSpace,
			Message: fmt.Sprintf("the manager has %s free for the archive of up to %s", humanize.Bytes(plan.ManagerFree), humanize.Bytes(plan.TotalBytes))})
	}
	if len(plan.Running) > 0 {
		plan.DowntimeSeconds = 10 + plan.TotalBytes/assumedRate
	}
}

// userLabels are a volume's labels other than Compose's and Docker
// Manager's own.
func userLabels(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if !strings.HasPrefix(k, "com.docker.compose.") && !protocol.OwnLabel(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ExportChecks are what an export needs besides stack.export on the stack:
// reading the project directory and its Compose definition, and
// downloading each included volume's files.
func ExportChecks(st domain.Stack, volumes []string) []Check {
	stack := authz.Resource{Type: catalog.TypeStack, ID: st.ID, EnvironmentID: st.EnvironmentID, Parents: []authz.ResourceRef{}}
	out := []Check{{Capability: "stack.files.download", Resource: stack}, {Capability: "stack.definition.read", Resource: stack}}
	for _, v := range volumes {
		out = append(out, Check{Capability: "volume.files.download", Resource: authz.Resource{Type: catalog.TypeVolume, ID: v,
			EnvironmentID: st.EnvironmentID, Parents: []authz.ResourceRef{{Type: catalog.TypeStack, ID: st.ID}}}})
	}
	return out
}

// IncludedVolumes are the plan's included volume names.
func (p ExportPlan) IncludedVolumes() []string {
	var out []string
	for _, v := range p.Volumes {
		if v.Included {
			out = append(out, v.Name)
		}
	}
	return out
}

// exportVolume is an included volume in the job input.
type exportVolume struct {
	Key            string            `json:"key"`
	Name           string            `json:"name"`
	FollowsProject bool              `json:"followsProject,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

// exportInput is the stack.export job input. It holds no sizes: they
// change while a stack runs, and a retried request with the same
// Idempotency-Key must find the same job.
type exportInput struct {
	StackID        string         `json:"stackId"`
	Volumes        []exportVolume `json:"volumes,omitempty"`
	Excluded       []string       `json:"excluded,omitempty"`
	TimeoutSeconds int            `json:"timeoutSeconds,omitempty"`
}

// exportSizes are the sizes the prepare step measured.
type exportSizes struct {
	Project PartStats            `json:"project"`
	Volumes map[string]PartStats `json:"volumes,omitempty"`
	Total   int64                `json:"total"`
}

// StartExport re-runs the preview and, without blockers, enqueues the
// stack.export job.
func (s *Service) StartExport(ctx context.Context, p authz.Principal, st domain.Stack, r ExportRequest) (domain.Job, error) {
	if r.TimeoutSeconds < 0 || r.TimeoutSeconds > 3600 {
		return domain.Job{}, &domain.InputError{Field: "timeoutSeconds", Message: "must be between 0 and 3600"}
	}
	plan, _, err := s.previewExport(ctx, st, r, true)
	if err != nil {
		return domain.Job{}, err
	}
	if !plan.Allowed() {
		return domain.Job{}, &BlockedError{Blockers: plan.Blockers}
	}
	in := exportInputOf(st, plan, r.TimeoutSeconds)
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.StackExport, Principal: p, EnvironmentID: st.EnvironmentID,
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: st.ID}}, Input: in, IdempotencyKey: r.IdempotencyKey})
	if err != nil {
		return domain.Job{}, err
	}
	s.log.Info("stack export queued", "job_id", j.ID, "stack_id", st.ID, "volumes", len(in.Volumes))
	return j, nil
}

// exportOutput is the stack.export job's journaled output.
type exportOutput struct {
	// Stopped: the stop step ran; Running the services it stopped.
	Stopped bool     `json:"stopped,omitempty"`
	Running []string `json:"running,omitempty"`
	Started bool     `json:"started,omitempty"`
	// Sizes are the data measured by the prepare step.
	Sizes *exportSizes `json:"sizes,omitempty"`
	// File is the written archive.
	File *ExportFile `json:"file,omitempty"`
}

func readExportOutput(raw []byte) exportOutput {
	var o exportOutput
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o
}

func writeExportOutput(ctx context.Context, sc *jobexec.StepContext, fn func(o *exportOutput)) error {
	o := readExportOutput(sc.Output())
	fn(&o)
	return sc.SetOutput(ctx, o)
}

// startArgs are the start_stack compensation's arguments.
type startArgs struct {
	JobID       string              `json:"jobId"`
	Environment string              `json:"environment"`
	Stack       protocol.ProjectRef `json:"stack"`
	Services    []string            `json:"services"`
}

func (s *Service) exportExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.StackExport, Steps: map[string]jobexec.StepFunc{
		"prepare":       s.exportPrepare,
		"stop":          s.exportStop,
		"write_archive": s.exportWrite,
		"start":         s.exportStart,
		"finalize":      s.exportFinalize,
	}, Compensations: map[string]jobexec.CompensationFunc{jobspec.CompStartStack: s.startStack}}
}

func exportJobInput(sc *jobexec.StepContext) (exportInput, error) {
	var in exportInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, fmt.Errorf("malformed stack.export input: %w", err)
	}
	if in.StackID == "" {
		return in, errors.New("incomplete stack.export input")
	}
	for _, v := range in.Volumes {
		if !volumeKeyRE.MatchString(v.Key) || !protocol.ValidDockerName(v.Name) {
			return in, errors.New("invalid volume in the stack.export input")
		}
	}
	return in, nil
}

const recoveryExport = "The stack was started again (unless the job reports that this failed). Fix the cause, then export the stack again."

func (s *Service) exportPrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := exportJobInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(in.Volumes))
	for _, v := range in.Volumes {
		names = append(names, v.Name)
	}
	if err := s.authorize(ctx, sc.JobID, ExportChecks(st, names)); err != nil {
		return err
	}
	sc.Progress(ctx, 2, "checking and measuring the stack")
	plan, _, err := s.previewExport(ctx, st, ExportRequest{ExcludeVolumes: in.Excluded}, true)
	if err != nil {
		return agentFailure(err, ClassBlocked, "Nothing was changed. Export the stack again.")
	}
	if !plan.Allowed() {
		b := plan.Blockers[0]
		return &classed{class: ClassBlocked, err: fmt.Errorf("%s: %s", b.Code, b.Message),
			recovery: "Nothing was changed. Check the export again and resolve what blocks it."}
	}
	for _, v := range in.Volumes {
		if !slices.ContainsFunc(plan.Volumes, func(p ExportVolume) bool { return p.Key == v.Key && p.Name == v.Name && p.Included }) {
			return &classed{class: ClassBlocked, err: fmt.Errorf("volume %s can no longer be exported", v.Name),
				recovery: "Nothing was changed. Check the export again."}
		}
	}
	sizes := exportSizes{Project: PartStats{Bytes: plan.ProjectBytes}, Volumes: map[string]PartStats{}, Total: plan.TotalBytes}
	for _, v := range plan.Volumes {
		if v.Included {
			sizes.Volumes[v.Key] = PartStats{Bytes: v.Bytes, Entries: v.Entries}
		}
	}
	return writeExportOutput(ctx, sc, func(o *exportOutput) { o.Sizes = &sizes })
}

func (s *Service) exportStop(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := exportJobInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	out := readExportOutput(sc.Output())
	running := out.Running
	if !out.Stopped {
		facts, err := s.sourceFacts(ctx, st, false)
		if err != nil {
			return agentFailure(err, ClassStopFailed, "Nothing was changed. Export the stack again.")
		}
		running = nil
		for _, sv := range facts.Services {
			if sv.Running {
				running = append(running, sv.Name)
			}
		}
		if err := writeExportOutput(ctx, sc, func(o *exportOutput) { o.Stopped, o.Running = true, running }); err != nil {
			return err
		}
	}
	if len(running) == 0 {
		return nil
	}
	// Registered before anything stops: whatever ends this job before the
	// start step starts the stack again.
	if err := sc.AddCompensation(ctx, jobspec.CompStartStack, startArgs{JobID: sc.JobID, Environment: st.EnvironmentID,
		Stack: stackRef(st), Services: running}); err != nil {
		return err
	}
	sc.Progress(ctx, 5, fmt.Sprintf("stopping %d services", len(running)))
	var res protocol.MigrationLifecycleOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqMigrationStop, protocol.MigrationStopInput{MigrationID: sc.JobID,
		Stack: stackRef(st), TimeoutSeconds: in.TimeoutSeconds}, &res, s.opts.StopTimeout); err != nil {
		return agentFailure(err, ClassStopFailed, recoveryExport)
	}
	return nil
}

func (s *Service) exportFileName(st domain.Stack, at time.Time) string {
	return st.Name + "-" + at.UTC().Format("2006-01-02") + FileExtension
}

func (s *Service) exportPath(jobID string) string {
	return filepath.Join(s.exportsDir(), jobID+FileExtension)
}

func (s *Service) exportWrite(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := exportJobInput(sc)
	if err != nil {
		return err
	}
	if readExportOutput(sc.Output()).File != nil {
		return nil
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	facts, err := s.sourceFacts(ctx, st, false)
	if err != nil {
		return agentFailure(err, ClassTransferFailed, recoveryExport)
	}
	sizes := readExportOutput(sc.Output()).Sizes
	if sizes == nil {
		sizes = &exportSizes{}
	}
	m := s.manifest(st, facts, in, *sizes)
	for attempt := 1; ; attempt++ {
		f, err := s.writeArchive(ctx, sc, st, in, m, sizes.Total)
		if err == nil {
			return writeExportOutput(ctx, sc, func(o *exportOutput) { o.File = f })
		}
		_ = os.Remove(s.exportPath(sc.JobID) + partSuffix)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ce *classed
		switch {
		case errors.As(err, &ce):
			return err
		case lostSession(err) && attempt < s.opts.PartAttempts:
			s.log.Warn("stack export lost the agent; waiting for it", "job_id", sc.JobID, "attempt", attempt)
			sc.Progress(ctx, -1, "the agent disconnected; waiting for it to reconnect")
			if !s.waitOnline(ctx, st.EnvironmentID) {
				return s.interrupted(st.EnvironmentID, "the export", err, recoveryExport)
			}
			continue
		case lostSession(err):
			return s.interrupted(st.EnvironmentID, "the export", err, recoveryExport)
		case errors.Is(err, errDigest) && attempt < s.opts.PartAttempts:
			s.log.Warn("stack export part failed verification; retrying", "job_id", sc.JobID, "attempt", attempt)
			continue
		}
		return &classed{class: ClassTransferFailed, err: err, recovery: recoveryExport}
	}
}

// manifest describes the archive an export writes.
func (s *Service) manifest(st domain.Stack, f *protocol.MigrationProjectFacts, in exportInput, sizes exportSizes) Manifest {
	m := Manifest{Format: FormatName, Version: FormatVersion, ExportedAt: s.now(), ManagerVersion: s.opts.ManagerVersion,
		Stack: ManifestStack{Name: st.Name, DisplayName: st.DisplayName, Description: st.Meta.Description, Links: st.Links,
			ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles},
		Project: sizes.Project, Volumes: []ManifestVolume{}, NotIncluded: []Exclusion{},
		Services: []ManifestService{}, Networks: []ManifestNetwork{}, ExternalVolumes: []string{}}
	for _, v := range in.Volumes {
		m.Volumes = append(m.Volumes, ManifestVolume{Key: v.Key, Name: v.Name, FollowsProject: v.FollowsProject, Labels: maps.Clone(v.Labels),
			PartStats: sizes.Volumes[v.Key]})
	}
	for _, v := range f.Volumes {
		switch {
		case v.External:
			m.ExternalVolumes = append(m.ExternalVolumes, v.Name)
		case v.Anonymous:
			m.NotIncluded = append(m.NotIncluded, Exclusion{Kind: ExcludedAnonymous, Name: v.Name, Reason: "anonymous volume"})
		case !slices.ContainsFunc(in.Volumes, func(e exportVolume) bool { return e.Key == v.Key }):
			reason := "left out on export"
			if !v.Supported {
				reason = "not a plain local volume"
			}
			m.NotIncluded = append(m.NotIncluded, Exclusion{Kind: ExcludedVolume, Key: v.Key, Name: v.Name, Reason: reason})
		}
	}
	for _, b := range f.Binds {
		if b.External {
			m.NotIncluded = append(m.NotIncluded, Exclusion{Kind: ExcludedBind, Name: b.Source, Reason: "outside the project folder"})
		}
	}
	for _, sv := range f.Services {
		m.Services = append(m.Services, ManifestService{Name: sv.Name, Image: sv.Image, Build: sv.Build, Registry: len(sv.RepoDigests) > 0,
			ContainerNames: sv.ContainerNames, Ports: sv.Ports})
	}
	for _, n := range f.Networks {
		m.Networks = append(m.Networks, ManifestNetwork{Key: n.Key, Name: n.Name, External: n.External})
	}
	return m
}

// errDigest: a part's checksums disagree (the part is retried).
var errDigest = errors.New("the archive part failed verification")

// countingWriter fails once more than limit bytes were written.
type countingWriter struct {
	n, limit int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.n > c.limit {
		return 0, &classed{class: ClassTooLarge, err: fmt.Errorf("the archive exceeds %s", humanize.Bytes(c.limit)),
			recovery: recoveryExport + " Leave volumes out or raise DOCKER_MANAGER_STACK_ARCHIVE_MAX_MB."}
	}
	return len(p), nil
}

// progressReader reports the bytes read through it.
type progressReader struct {
	r    io.Reader
	n    int64
	tick func(n int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	p.tick(p.n)
	return n, err
}

// writeArchive writes the whole archive once (a retry starts over).
func (s *Service) writeArchive(ctx context.Context, sc *jobexec.StepContext, st domain.Stack, in exportInput, m Manifest, measured int64) (*ExportFile, error) {
	// The archive is at most the measured data (compressed); claim that
	// much of the manager's free space while it is written, against other
	// exports and uploads.
	release, err := s.reserve(s.exportsDir(), measured)
	if err != nil {
		return nil, &classed{class: FindingManagerSpace, err: err,
			recovery: recoveryExport + " Free space in the manager's data directory first."}
	}
	defer release()
	part := s.exportPath(sc.JobID) + partSuffix
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	cw := &countingWriter{limit: s.opts.MaxSize}
	bw := bufio.NewWriterSize(io.MultiWriter(f, h, cw), 1<<20)
	w, err := NewWriter(bw, m)
	if err != nil {
		return nil, err
	}
	parts := []Part{{}}
	for _, v := range in.Volumes {
		parts = append(parts, Part{Volume: v.Key})
	}
	total := max(measured, 1)
	var done int64
	last := s.clk.Now()
	for _, p := range parts {
		if sc.CancelRequested() {
			return nil, fmt.Errorf("before %s: %w", p.Name(), jobexec.ErrStepCancelled)
		}
		send := protocol.MigrationSendInput{MigrationID: sc.JobID, Part: protocol.PartProject}
		if p.Volume == "" {
			ref := stackRef(st)
			send.Stack = &ref
		} else {
			send.Part = protocol.PartVolume
			for _, v := range in.Volumes {
				if v.Key == p.Volume {
					send.Volume = v.Name
				}
			}
		}
		base := done
		tick := func(n int64) {
			if now := s.clk.Now(); now.Sub(last) >= 2*time.Second {
				last = now
				sc.Progress(ctx, 10+int(min(80*(base+n)/total, 80)), fmt.Sprintf("writing %s (%s)", p.Name(), humanize.Bytes(base+n)))
			}
		}
		ps, n, err := s.sendPart(ctx, sc.JobID, st.EnvironmentID, send, func(r io.Reader) (PartStats, error) {
			return w.AddPart(p, &progressReader{r: r, tick: tick})
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name(), err)
		}
		done += n
		sc.Item(ctx, p.Name(), domain.ItemSucceeded, fmt.Sprintf("%d entries, %s", ps.Entries, humanize.Bytes(ps.Bytes)))
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	final := s.exportPath(sc.JobID)
	if err := os.Rename(part, final); err != nil {
		return nil, err
	}
	now := s.now()
	// The sweep expires archives by their modification time on the
	// service's clock.
	if err := os.Chtimes(final, now, now); err != nil {
		return nil, err
	}
	keys, names := make([]string, 0, len(in.Volumes)), make([]string, 0, len(in.Volumes))
	for _, v := range in.Volumes {
		keys, names = append(keys, v.Key), append(names, v.Name)
	}
	return &ExportFile{JobID: sc.JobID, StackID: st.ID, FileName: s.exportFileName(st, now), Size: cw.n,
		SHA256: hex.EncodeToString(h.Sum(nil)), CreatedAt: now, ExpiresAt: now.Add(s.opts.Retention), Volumes: keys, VolumeNames: names}, nil
}

// sendPart reads one part from the agent (migration.send) through use and
// checks the agent's checksums against the manager's. It returns the
// part's stats and the tar bytes read.
func (s *Service) sendPart(ctx context.Context, jobID, env string, in protocol.MigrationSendInput, use func(io.Reader) (PartStats, error)) (PartStats, int64, error) {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := s.opts.Agents.OpenStream(pctx, env, protocol.StreamMigrationSend, in, streammux.OpenOptions{JobID: jobID})
	if err != nil {
		return PartStats{}, 0, err
	}
	fr := transfer.NewReader(&limited{ctx: pctx, r: st, l: s.opts.Limiter})
	ps, err := use(fr)
	if err == nil {
		// The tar end marker may leave padding; the trailer must follow.
		_, err = io.Copy(io.Discard, fr)
	}
	if err == nil && !fr.Done() {
		err = fmt.Errorf("%w: the part ended without its trailer", errDigest)
	}
	if err != nil {
		st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "the export stopped")
		if errors.Is(err, transfer.ErrChecksum) || errors.Is(err, transfer.ErrFormat) || errors.Is(err, streammux.ErrVerification) {
			err = fmt.Errorf("%w: %w", errDigest, err)
		}
		if e := st.Err(); e != nil && lostSession(e) {
			err = e
		}
		return ps, 0, err
	}
	_ = st.CloseWrite()
	sum := fr.Summary()
	var res protocol.MigrationPartResult
	if rc := st.RemoteClose(); rc != nil && len(rc.Result) > 0 {
		_ = json.Unmarshal(rc.Result, &res)
	}
	if res.SHA256 != sum.SHA256 || res.Bytes != sum.Bytes || res.Chunks != sum.Chunks {
		return ps, 0, fmt.Errorf("%w: the agent and the manager disagree on the part's checksum", errDigest)
	}
	return ps, sum.Bytes, nil
}

func (s *Service) exportStart(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := exportJobInput(sc)
	if err != nil {
		return err
	}
	out := readExportOutput(sc.Output())
	if out.Started || len(out.Running) == 0 {
		return sc.ReleaseCompensation(ctx, jobspec.CompStartStack)
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 92, fmt.Sprintf("starting %d services again", len(out.Running)))
	var res protocol.MigrationLifecycleOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqMigrationStart, protocol.MigrationStartInput{MigrationID: sc.JobID,
		Stack: stackRef(st), Services: out.Running}, &res, s.opts.StopTimeout); err != nil {
		return agentFailure(err, ClassStartFailed, "The archive was written and can be downloaded. Start the stack from its page.")
	}
	if err := writeExportOutput(ctx, sc, func(o *exportOutput) { o.Started = true }); err != nil {
		return err
	}
	return sc.ReleaseCompensation(ctx, jobspec.CompStartStack)
}

func (s *Service) exportFinalize(ctx context.Context, sc *jobexec.StepContext) error {
	out := readExportOutput(sc.Output())
	if out.File == nil {
		return errors.New("the archive was not written")
	}
	sc.Progress(ctx, 100, fmt.Sprintf("%s ready for download (%s)", out.File.FileName, humanize.Bytes(out.File.Size)))
	s.log.Info("stack exported", "job_id", sc.JobID, "stack_id", out.File.StackID, "bytes", out.File.Size, "volumes", len(out.File.Volumes))
	return nil
}

// startStack is the start_stack compensation: start the services the
// export stopped.
func (s *Service) startStack(ctx context.Context, raw json.RawMessage) error {
	var a startArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("malformed compensation arguments: %w", err)
	}
	var res protocol.MigrationLifecycleOutput
	if err := s.call(ctx, a.Environment, protocol.ReqMigrationStart, protocol.MigrationStartInput{MigrationID: a.JobID, Stack: a.Stack,
		Services: a.Services}, &res, s.opts.StopTimeout); err != nil {
		if errors.Is(err, jobs.ErrAgentOffline) {
			return errors.New("the environment's agent is offline: start the stack once it reconnects")
		}
		return fmt.Errorf("start the stack's services: %w", err)
	}
	s.log.Info("stack export stopped early: services started again", "job_id", a.JobID, "services", len(a.Services))
	return nil
}

// onExportFinished removes the file of an export that did not write its
// archive (a written archive stays downloadable even when starting the
// stack again failed).
func (s *Service) onExportFinished(_ context.Context, _ bun.IDB, j domain.Job) error {
	if readExportOutput(j.ResultOutput).File != nil {
		return nil
	}
	_ = os.Remove(s.exportPath(j.ID) + partSuffix)
	_ = os.Remove(s.exportPath(j.ID))
	return nil
}

// Export returns a stack's archive for download.
func (s *Service) Export(ctx context.Context, stackID, jobID string) (ExportFile, error) {
	j, err := s.opts.Jobs.Get(ctx, jobID)
	if err != nil {
		if errors.Is(err, domain.ErrJobNotFound) {
			return ExportFile{}, ErrExportNotFound
		}
		return ExportFile{}, err
	}
	f := readExportOutput(j.ResultOutput).File
	if j.Kind != jobspec.StackExport || f == nil || f.StackID != stackID || !j.State.Terminal() || !s.clk.Now().Before(f.ExpiresAt) {
		return ExportFile{}, ErrExportNotFound
	}
	if _, err := os.Stat(s.exportPath(j.ID)); err != nil {
		return ExportFile{}, ErrExportNotFound
	}
	return *f, nil
}

// OpenExport opens an archive's file.
func (s *Service) OpenExport(f ExportFile) (io.ReadSeekCloser, error) {
	file, err := os.Open(s.exportPath(f.JobID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrExportNotFound
	}
	return file, err
}

// LatestExport returns the stack's newest archive still available (nil
// when none).
func (s *Service) LatestExport(ctx context.Context, stackID string) *ExportFile {
	list, err := s.opts.Jobs.List(ctx, domain.JobFilter{Kinds: []domain.JobKind{jobspec.StackExport},
		Target: &domain.JobTarget{Type: domain.TargetStack, ID: stackID}, Limit: 20})
	if err != nil {
		return nil
	}
	for _, j := range list {
		if !j.State.Terminal() {
			continue
		}
		if f, err := s.Export(ctx, stackID, j.ID); err == nil {
			return &f
		}
	}
	return nil
}
