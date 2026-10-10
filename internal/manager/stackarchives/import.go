package stackarchives

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

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

// Finding codes of imports (besides the migration codes they reuse).
const (
	FindingNamePinned    = "project_name_pinned"
	FindingImageMissing  = "image_missing"
	FindingVolumeMissing = "volume_not_included"
)

// ImportRequest is an import preview or start.
type ImportRequest struct {
	EnvironmentID string
	// Name is the new stack's (Compose project) name; DisplayName and
	// Description its display metadata (nil: the archive's; "" none).
	Name        string
	DisplayName *string
	Description *string
	// Deploy deploys the stack once its files and volumes are in place.
	Deploy bool
}

// ImportVolume is an included volume as the new stack gets it.
type ImportVolume struct {
	Key string
	// Source is its name in the archive; Name its name for the new stack.
	Source string
	Name   string
	PartStats
}

// ImportPlan is an import's preview, computed before anything is written.
type ImportPlan struct {
	ArchiveID     string
	EnvironmentID string
	Name          string
	Blockers      []migrations.Finding
	Warnings      []migrations.Finding
	Volumes       []ImportVolume
	NotIncluded   []Exclusion
	ProjectBytes  int64
	VolumeBytes   int64
	// StacksFree and VolumesFree are the destination's free bytes (-1
	// unknown).
	StacksFree  int64
	VolumesFree int64
}

// Allowed reports whether the import can start.
func (p ImportPlan) Allowed() bool { return len(p.Blockers) == 0 }

// VolumeNames are the names of the volumes the import creates.
func (p ImportPlan) VolumeNames() []string {
	out := make([]string, 0, len(p.Volumes))
	for _, v := range p.Volumes {
		out = append(out, v.Name)
	}
	return out
}

// Renaming: the names Compose derives from the project name follow the
// new stack's name; names the Compose files set themselves stay (the job
// confirms them with the destination after the commit).

func renamedVolume(v ManifestVolume, named map[string]string, name string) string {
	if n := named[v.Key]; n != "" {
		return n
	}
	if v.FollowsProject {
		return name + "_" + v.Key
	}
	return v.Name
}

func renamed(oldName, newName, sep, s string) string {
	if rest, ok := strings.CutPrefix(s, oldName+sep); ok {
		return newName + sep + rest
	}
	return s
}

// importFacts are what an import preview is evaluated from.
type importFacts struct {
	upload    Upload
	name      string
	online    bool
	supported bool
	// nameErr is the stack service's check of the name (taken, running
	// Compose project).
	nameErr error
	dest    *protocol.MigrationDestinationFacts
}

// destinationQuery lists what the new stack creates on the destination.
func destinationQuery(m Manifest, named map[string]string, name string) protocol.MigrationDestinationQuery {
	q := protocol.MigrationDestinationQuery{ProjectName: name, Dir: name}
	old := m.Stack.Name
	for _, sv := range m.Services {
		for _, c := range sv.ContainerNames {
			q.ContainerNames = append(q.ContainerNames, renamed(old, name, "-", c))
		}
		q.Ports = append(q.Ports, sv.Ports...)
		if sv.Image != "" {
			q.Images = append(q.Images, sv.Image)
		}
	}
	for _, v := range m.Volumes {
		q.Volumes = append(q.Volumes, renamedVolume(v, named, name))
	}
	for _, e := range m.NotIncluded {
		switch {
		case e.Kind != ExcludedVolume || e.Key == "":
		case named[e.Key] != "":
			q.Volumes = append(q.Volumes, named[e.Key])
		default:
			q.Volumes = append(q.Volumes, renamed(old, name, "_", e.Name))
		}
	}
	for _, n := range m.Networks {
		if n.External {
			q.ExternalNetworks = append(q.ExternalNetworks, n.Name)
		} else {
			q.Networks = append(q.Networks, renamed(old, name, "_", n.Name))
		}
	}
	q.ExternalVolumes = append(q.ExternalVolumes, m.ExternalVolumes...)
	slices.Sort(q.Images)
	q.Images = slices.Compact(q.Images)
	return q
}

// evaluateImport turns the facts into a plan (pure).
func evaluateImport(f importFacts) ImportPlan {
	m := f.upload.Manifest
	plan := ImportPlan{ArchiveID: f.upload.ID, Name: f.name, ProjectBytes: f.upload.Project.Bytes, StacksFree: -1, VolumesFree: -1,
		NotIncluded: slices.Clone(m.NotIncluded)}
	block := func(code, msg, res string) {
		plan.Blockers = append(plan.Blockers, migrations.Finding{Code: code, Message: msg, Resource: res})
	}
	warn := func(code, msg, res string) {
		plan.Warnings = append(plan.Warnings, migrations.Finding{Code: code, Message: msg, Resource: res})
	}
	for _, v := range m.Volumes {
		st := f.upload.Volumes[v.Key]
		plan.Volumes = append(plan.Volumes, ImportVolume{Key: v.Key, Source: v.Name, Name: renamedVolume(v, f.upload.NamedVolumes, f.name), PartStats: st})
		plan.VolumeBytes += st.Bytes
	}
	if f.upload.PinnedName != "" && f.upload.PinnedName != f.name {
		block(FindingNamePinned, fmt.Sprintf("the archive's Compose file sets the project name %s (top-level name:); create the stack as %s",
			f.upload.PinnedName, f.upload.PinnedName), f.upload.PinnedName)
	}
	var se *domain.StackError
	switch {
	case f.nameErr == nil:
	case errors.Is(f.nameErr, domain.ErrStackNameTaken):
		block(migrations.FindingStackNameConflict, "a stack named "+f.name+" already exists in this environment", f.name)
	case errors.As(f.nameErr, &se) && se.Code == domain.StackErrProjectExists:
		block(migrations.FindingProjectNameConflict, "a Compose project named "+f.name+" already runs in this environment", f.name)
	}
	switch {
	case !f.online:
		block(migrations.FindingEnvironmentOffline, "the environment is offline", "")
	case !f.supported:
		block(migrations.FindingAgentUnsupported, "the environment's agent cannot create stacks from archives; update it", "")
	}
	for _, e := range m.NotIncluded {
		switch e.Kind {
		case ExcludedVolume:
			warn(FindingVolumeMissing, "volume "+e.Name+" is not in the archive ("+e.Reason+"): Compose creates it empty", e.Name)
		case ExcludedAnonymous:
			warn(migrations.FindingAnonymousVolume, "anonymous volume "+e.Name+" is not in the archive", e.Name)
		case ExcludedBind:
			warn(migrations.FindingExternalBind, "the bind mount "+e.Name+" lay outside the project folder and is not in the archive", e.Name)
		}
	}
	d := f.dest
	if d == nil {
		return plan
	}
	plan.StacksFree, plan.VolumesFree = d.StacksFree, d.VolumesFree
	if !d.StacksOK || !d.VolumesOK {
		reason := d.Reason
		if reason == "" {
			reason = "the stacks volume or the volume directory did not pass the storage check"
		}
		block(migrations.FindingStorageUnavailable, reason, "")
	}
	if d.DirExists {
		block(migrations.FindingDirectoryConflict, "the folder "+f.name+" already exists in the stacks volume", f.name)
	}
	if len(d.ProjectContainers) > 0 && !slices.ContainsFunc(plan.Blockers, func(b migrations.Finding) bool {
		return b.Code == migrations.FindingProjectNameConflict
	}) {
		block(migrations.FindingProjectNameConflict, "containers of a Compose project named "+f.name+" already exist", f.name)
	}
	for _, c := range d.Containers {
		block(migrations.FindingContainerConflict, "a container named "+c+" already exists", c)
	}
	for _, v := range d.Volumes {
		block(migrations.FindingVolumeConflict, "a volume named "+v.Name+" already exists", v.Name)
	}
	for _, n := range d.Networks {
		block(migrations.FindingNetworkConflict, "a network named "+n+" already exists", n)
	}
	for _, n := range d.MissingNetworks {
		block(migrations.FindingExternalNetwork, "the external network "+n+" does not exist in this environment", n)
	}
	for _, v := range d.MissingVolumes {
		block(migrations.FindingExternalVolume, "the external volume "+v+" does not exist in this environment", v)
	}
	for _, pc := range d.PortConflicts {
		block(migrations.FindingPortConflict, fmt.Sprintf("host port %d/%s is used by %s", pc.Port.Published, pc.Port.Protocol, pc.Container), pc.Container)
	}
	if d.StacksFree >= 0 && plan.ProjectBytes > d.StacksFree {
		block(migrations.FindingInsufficientSpace, fmt.Sprintf("the project folder needs %s, the stacks volume has %s free",
			humanize.Bytes(plan.ProjectBytes), humanize.Bytes(d.StacksFree)), "")
	}
	if d.VolumesFree >= 0 && plan.VolumeBytes > d.VolumesFree {
		block(migrations.FindingInsufficientSpace, fmt.Sprintf("the volumes need %s, the Docker data root has %s free",
			humanize.Bytes(plan.VolumeBytes), humanize.Bytes(d.VolumesFree)), "")
	}
	for _, sv := range m.Services {
		if sv.Image != "" && !sv.Build && !sv.Registry && !slices.Contains(d.ImagesPresent, sv.Image) {
			warn(FindingImageMissing, "the image "+sv.Image+" of "+sv.Name+" was built on the source and is not in this environment: deploying fails until it is", sv.Image)
		}
	}
	return plan
}

// PreviewImport checks creating a stack from p's upload in the request's
// environment.
func (s *Service) PreviewImport(ctx context.Context, p authz.Principal, archiveID string, r ImportRequest) (ImportPlan, error) {
	u, err := s.Upload(p, archiveID)
	if err != nil {
		return ImportPlan{}, err
	}
	return s.previewImport(ctx, u, r, "")
}

func (s *Service) previewImport(ctx context.Context, u Upload, r ImportRequest, own string) (ImportPlan, error) {
	if !protocol.ValidProjectName(r.Name) {
		return ImportPlan{}, &domain.InputError{Field: "name", Message: "must be a Compose project name: 1-63 lower-case letters, digits, '-' and '_', starting with a letter or digit"}
	}
	env, err := s.opts.Environments.GetEnvironment(ctx, r.EnvironmentID)
	if err != nil {
		return ImportPlan{}, err
	}
	if env.Status == domain.EnvironmentArchived {
		return ImportPlan{}, domain.ErrEnvironmentArchived
	}
	f := importFacts{upload: u, name: r.Name, online: env.Online && s.opts.Agents.Online(r.EnvironmentID),
		supported: s.opts.Agents.EnvironmentHasFeature(r.EnvironmentID, protocol.FeatureMigrationComposeVolume)}
	if f.online && f.supported {
		f.nameErr = s.opts.Stacks.CheckArchiveName(ctx, r.EnvironmentID, r.Name, own)
		var se *domain.StackError
		if f.nameErr != nil && !errors.Is(f.nameErr, domain.ErrStackNameTaken) && (!errors.As(f.nameErr, &se) || se.Code != domain.StackErrProjectExists) {
			return ImportPlan{}, f.nameErr
		}
		q := destinationQuery(u.Manifest, u.NamedVolumes, r.Name)
		var out protocol.MigrationPreviewOutput
		if err := s.call(ctx, r.EnvironmentID, protocol.ReqMigrationPreview, protocol.MigrationPreviewInput{Role: protocol.RoleDestination,
			Destination: &q}, &out, s.opts.RequestTimeout); err != nil {
			return ImportPlan{}, agentErr(err)
		}
		f.dest = out.Destination
	}
	plan := evaluateImport(f)
	plan.EnvironmentID = r.EnvironmentID
	return plan, nil
}

// ImportChecks are what an import needs besides stack.create in the
// environment: creating its volumes and, to deploy it, stack.deploy.
func ImportChecks(env string, volumes bool, deploy bool) []Check {
	var out []Check
	if volumes {
		out = append(out, Check{Capability: "volume.create", Resource: authz.InEnvironment(catalog.TypeVolume, env)})
	}
	if deploy {
		out = append(out, Check{Capability: "stack.deploy", Resource: authz.InEnvironment(catalog.TypeStack, env)})
	}
	return out
}

// importVolume is a volume in the job input.
type importVolume struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// importInput is the stack.import_archive job input.
type importInput struct {
	ArchiveID string         `json:"archiveId"`
	StackID   string         `json:"stackId"`
	Name      string         `json:"name"`
	Volumes   []importVolume `json:"volumes,omitempty"`
	Deploy    bool           `json:"deploy,omitempty"`
}

// StartImport re-runs the preview and, without blockers, records the new
// stack and enqueues the stack.import_archive job.
func (s *Service) StartImport(ctx context.Context, p authz.Principal, archiveID string, r ImportRequest) (domain.Stack, domain.Job, error) {
	u, err := s.Upload(p, archiveID)
	if err != nil {
		return domain.Stack{}, domain.Job{}, err
	}
	// One stack per upload at a time: claimed before anything else, given
	// back unless a job takes it over.
	s.mu.Lock()
	_, used := s.inUse[archiveID]
	gone := s.uploads[archiveID] == nil
	if !used && !gone && !s.evicting[archiveID] {
		s.inUse[archiveID] = ""
	}
	busy := s.evicting[archiveID]
	s.mu.Unlock()
	switch {
	case gone:
		return domain.Stack{}, domain.Job{}, ErrUploadNotFound
	case used, busy:
		return domain.Stack{}, domain.Job{}, ErrUploadInUse
	}
	queued := false
	defer func() {
		if !queued {
			s.release(archiveID)
		}
	}()
	plan, err := s.previewImport(ctx, u, r, "")
	if err != nil {
		return domain.Stack{}, domain.Job{}, err
	}
	if !plan.Allowed() {
		return domain.Stack{}, domain.Job{}, &BlockedError{Blockers: plan.Blockers}
	}
	m := u.Manifest
	displayName, description := m.Stack.DisplayName, m.Stack.Description
	if r.DisplayName != nil {
		displayName = *r.DisplayName
	}
	if r.Description != nil {
		description = *r.Description
	}
	st, err := s.opts.Stacks.ReserveArchiveStack(ctx, domain.StackFromArchive{EnvironmentID: r.EnvironmentID, Name: r.Name,
		DisplayName: displayName, Meta: domain.DisplayMeta{Description: description}, Links: m.Stack.Links,
		ConfigFiles: m.Stack.ConfigFiles, EnvFiles: m.Stack.EnvFiles})
	if err != nil {
		return domain.Stack{}, domain.Job{}, err
	}
	in := importInput{ArchiveID: archiveID, StackID: st.ID, Name: r.Name, Deploy: r.Deploy}
	targets := []domain.JobTarget{{Type: domain.TargetStack, ID: st.ID}}
	for _, v := range plan.Volumes {
		in.Volumes = append(in.Volumes, importVolume{Key: v.Key, Name: v.Name})
		targets = append(targets, domain.JobTarget{Type: domain.TargetVolume, ID: v.Name})
	}
	s.mu.Lock()
	s.inUse[archiveID] = st.ID
	s.mu.Unlock()
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.StackImportArchive, Principal: p, EnvironmentID: r.EnvironmentID,
		Targets: targets, Input: in})
	if err != nil {
		if derr := s.opts.Stacks.DropArchiveStack(ctx, st.ID); derr != nil {
			s.log.Error("stack archive import: forget the stack after a refused job", "stack_id", st.ID, "error", derr)
		}
		return domain.Stack{}, domain.Job{}, err
	}
	queued = true
	if cur, err := s.opts.Stacks.AttachArchiveJob(ctx, st.ID, j); err == nil {
		st = cur
	} else if !errors.Is(err, domain.ErrStackNotFound) {
		return st, j, err
	}
	s.log.Info("stack creation from an archive queued", "job_id", j.ID, "stack_id", st.ID, "environment_id", r.EnvironmentID,
		"archive_id", archiveID, "volumes", len(in.Volumes))
	return st, j, nil
}

func (s *Service) release(archiveID string) {
	s.mu.Lock()
	delete(s.inUse, archiveID)
	s.mu.Unlock()
}

// importOutput is the stack.import_archive job's journaled output.
type importOutput struct {
	// Parts are the parts the destination received and verified.
	Parts     []string `json:"parts,omitempty"`
	Committed bool     `json:"committed,omitempty"`
	Recorded  bool     `json:"recorded,omitempty"`
	// VolumeNames are the names Compose gives the volumes of the committed
	// project, by key (they decide where the data goes).
	VolumeNames map[string]string `json:"volumeNames,omitempty"`
	// Kept: the stack keeps its files and volumes (nothing is undone, a
	// failed deploy included).
	Kept        bool   `json:"kept,omitempty"`
	DeployJobID string `json:"deployJobId,omitempty"`
}

func readImportOutput(raw []byte) importOutput {
	var o importOutput
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o
}

func writeImportOutput(ctx context.Context, sc *jobexec.StepContext, fn func(o *importOutput)) error {
	o := readImportOutput(sc.Output())
	fn(&o)
	return sc.SetOutput(ctx, o)
}

// removeArgs are the remove_archive_import compensation's arguments.
type removeArgs struct {
	JobID       string   `json:"jobId"`
	Environment string   `json:"environment"`
	Project     string   `json:"project"`
	Volumes     []string `json:"volumes,omitempty"`
}

func (s *Service) importExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.StackImportArchive, Steps: map[string]jobexec.StepFunc{
		"prepare":          s.importPrepare,
		"transfer_project": s.importProject,
		"commit":           s.importCommit,
		"check_definition": s.importCheck,
		"transfer_volumes": s.importVolumes,
		"deploy":           s.importDeploy,
		"finalize":         s.importFinalize,
	}, Compensations: map[string]jobexec.CompensationFunc{jobspec.CompRemoveArchiveImport: s.removeImport}}
}

func importJobInput(sc *jobexec.StepContext) (importInput, error) {
	var in importInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, fmt.Errorf("malformed stack.import_archive input: %w", err)
	}
	if in.ArchiveID == "" || in.StackID == "" || !protocol.ValidProjectName(in.Name) || !protocol.ValidDirName(in.Name) {
		return in, errors.New("incomplete stack.import_archive input")
	}
	for _, v := range in.Volumes {
		if !volumeKeyRE.MatchString(v.Key) || !protocol.ValidDockerName(v.Name) {
			return in, errors.New("invalid volume in the stack.import_archive input")
		}
	}
	return in, nil
}

func (in importInput) volumeNames() []string {
	out := make([]string, 0, len(in.Volumes))
	for _, v := range in.Volumes {
		out = append(out, v.Name)
	}
	return out
}

const (
	recoveryNothingKept = "Nothing was kept: what the job wrote was removed and the stack forgotten. "
	recoveryImport      = recoveryNothingKept + "Fix the cause, then create the stack from the archive again."
)

// importUpload returns the job's upload (whoever uploaded it, the job's
// initiator started it).
func (s *Service) importUpload(id string) (Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return Upload{}, &classed{class: ClassArchiveGone, err: ErrUploadNotFound,
			recovery: "The uploaded archive is no longer on the manager (it is kept for a day). Upload it again."}
	}
	return *u, nil
}

func (s *Service) importPrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importJobInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	u, err := s.importUpload(in.ArchiveID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.inUse[in.ArchiveID] = st.ID
	s.mu.Unlock()
	if err := s.authorize(ctx, sc.JobID, ImportChecks(st.EnvironmentID, len(in.Volumes) > 0, in.Deploy)); err != nil {
		return err
	}
	sc.Progress(ctx, 2, "checking the environment")
	plan, err := s.previewImport(ctx, u, ImportRequest{EnvironmentID: st.EnvironmentID, Name: in.Name}, st.ID)
	if err != nil {
		return agentFailure(err, ClassBlocked, recoveryImport)
	}
	if !plan.Allowed() {
		b := plan.Blockers[0]
		return &classed{class: ClassBlocked, err: fmt.Errorf("%s: %s", b.Code, b.Message), recovery: recoveryImport}
	}
	if !slices.Equal(plan.VolumeNames(), in.volumeNames()) {
		return &classed{class: ClassBlocked, err: errors.New("the archive's volumes changed"), recovery: recoveryImport}
	}
	return nil
}

// receivePart streams one part of the upload to the agent
// (migration.receive), retrying it from the start after a lost session.
func (s *Service) receivePart(ctx context.Context, sc *jobexec.StepContext, env string, c *archiveCursor, p Part, in protocol.MigrationReceiveInput) error {
	for attempt := 1; ; attempt++ {
		st, err := s.receiveOnce(ctx, sc.JobID, env, c, p, in)
		if err == nil {
			sc.Item(ctx, p.Name(), domain.ItemSucceeded, fmt.Sprintf("%d entries, %s", st.Entries, humanize.Bytes(st.Bytes)))
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ce *classed
		switch {
		case errors.As(err, &ce):
			return err
		case lostSession(err) && attempt < s.opts.PartAttempts:
			s.log.Warn("stack archive import lost the agent; waiting for it", "job_id", sc.JobID, "part", p.Name(), "attempt", attempt)
			sc.Progress(ctx, -1, "the agent disconnected during "+p.Name()+"; waiting for it to reconnect")
			if !s.waitOnline(ctx, env) {
				return s.interrupted(env, p.Name(), err, recoveryImport)
			}
			continue
		case lostSession(err):
			return s.interrupted(env, p.Name(), err, recoveryImport)
		case errors.Is(err, errDigest) && attempt < s.opts.PartAttempts:
			continue
		case errors.Is(err, ErrInvalid):
			return &classed{class: ClassTransferFailed, err: err, recovery: recoveryNothingKept + "The archive is damaged; export the stack again."}
		}
		return &classed{class: ClassTransferFailed, err: fmt.Errorf("%s: %w", p.Name(), err), recovery: recoveryImport}
	}
}

// writeFunc adapts a function to io.Writer.
type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(b []byte) (int, error) { return f(b) }

// archiveCursor keeps an upload's archive open between the parts a step
// sends in archive order: each part is decompressed once, not again for
// every later part. A part already passed, or any failure, opens the
// archive anew.
type archiveCursor struct {
	s      *Service
	id     string
	rd     *Reader
	close  func()
	passed map[Part]bool
}

func (s *Service) cursor(archiveID string) *archiveCursor { return &archiveCursor{s: s, id: archiveID} }

// seek positions the archive at part p.
func (c *archiveCursor) seek(p Part) (*Reader, error) {
	if c.rd != nil && c.passed[p] {
		c.reset()
	}
	if c.rd == nil {
		rd, closeFile, err := c.s.openUpload(c.id)
		if err != nil {
			return nil, err
		}
		c.rd, c.close, c.passed = rd, closeFile, map[Part]bool{}
	}
	for {
		cur, err := c.rd.Next()
		if err != nil {
			c.reset()
			if errors.Is(err, io.EOF) {
				err = fmt.Errorf("%w: %s is missing", ErrInvalid, p.Name())
			}
			return nil, err
		}
		c.passed[cur] = true
		if cur == p {
			return c.rd, nil
		}
		if _, err := c.rd.WriteTo(cur, io.Discard); err != nil {
			c.reset()
			return nil, err
		}
	}
}

// reset closes the archive.
func (c *archiveCursor) reset() {
	if c.close != nil {
		c.close()
	}
	c.rd, c.close, c.passed = nil, nil, nil
}

func (s *Service) receiveOnce(ctx context.Context, jobID, env string, c *archiveCursor, p Part, in protocol.MigrationReceiveInput) (ps PartStats, err error) {
	rd, err := c.seek(p)
	if err != nil {
		return PartStats{}, err
	}
	defer func() {
		if err != nil {
			c.reset() // the reader stopped inside the part
		}
	}()
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := s.opts.Agents.OpenStream(pctx, env, protocol.StreamMigrationReceive, in, streammux.OpenOptions{JobID: jobID})
	if err != nil {
		return PartStats{}, err
	}
	fw := transfer.NewWriter(writeFunc(func(b []byte) (int, error) {
		if err := s.opts.Limiter.Wait(pctx, len(b)); err != nil {
			return 0, err
		}
		return st.Write(b)
	}), 0)
	ps, err = rd.WriteTo(p, fw)
	if err == nil {
		err = fw.Close()
	}
	if err != nil {
		st.Abort(protocol.CloseReasonError, protocol.CodeCancelled, "the import stopped")
		return ps, receiveErr(st, err)
	}
	sum := fw.Summary()
	if err := st.CloseWrite(); err != nil {
		return ps, receiveErr(st, err)
	}
	raw, err := st.Result(ctx)
	if err != nil {
		return ps, receiveErr(st, err)
	}
	var got protocol.MigrationPartResult
	if err := json.Unmarshal(raw, &got); err != nil || got.Bytes != sum.Bytes || got.SHA256 != sum.SHA256 {
		return ps, fmt.Errorf("%w: the host received different bytes than were sent", errDigest)
	}
	return ps, nil
}

// receiveErr maps a failed receive: the agent's refusal (an unsafe
// archive, a full disk, an existing volume) or a lost session.
func receiveErr(st *streammux.Stream, err error) error {
	var ce *streammux.CloseError
	if e := st.Err(); e != nil {
		if errors.As(e, &ce) && !ce.Local {
			if ce.Code == protocol.CodeDigestMismatch {
				return fmt.Errorf("%w: %s", errDigest, ce.Message)
			}
			return fmt.Errorf("the host refused the data (%s): %s", ce.Code, ce.Message)
		}
		if lostSession(e) {
			return e
		}
	}
	return err
}

func (s *Service) importProject(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importJobInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	if slices.Contains(readImportOutput(sc.Output()).Parts, protocol.PartProject) {
		return nil
	}
	// Registered before anything is written.
	if err := sc.AddCompensation(ctx, jobspec.CompRemoveArchiveImport, removeArgs{JobID: sc.JobID, Environment: st.EnvironmentID,
		Project: in.Name, Volumes: in.volumeNames()}); err != nil {
		return err
	}
	sc.Progress(ctx, 10, "copying the project folder")
	c := s.cursor(in.ArchiveID)
	defer c.reset()
	if err := s.receivePart(ctx, sc, st.EnvironmentID, c, Part{},
		protocol.MigrationReceiveInput{MigrationID: sc.JobID, Part: protocol.PartProject}); err != nil {
		return err
	}
	return writeImportOutput(ctx, sc, func(o *importOutput) { o.Parts = append(o.Parts, protocol.PartProject) })
}

func (s *Service) importCommit(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importJobInput(sc)
	if err != nil {
		return err
	}
	if readImportOutput(sc.Output()).Committed {
		return nil
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 30, "creating the project folder "+in.Name)
	var co protocol.MigrationCommitOutput
	if err := s.call(ctx, st.EnvironmentID, protocol.ReqMigrationCommit, protocol.MigrationCommitInput{MigrationID: sc.JobID, Dir: in.Name},
		&co, s.opts.RequestTimeout); err != nil {
		var ce protocol.CodedError
		if errors.As(err, &ce) && ce.ProtocolCode() == protocol.CodeAlreadyExists {
			return &classed{class: ClassBlocked, err: errors.New("the folder " + in.Name + " appeared in the stacks volume meanwhile"),
				recovery: recoveryImport}
		}
		return agentFailure(err, ClassTransferFailed, recoveryImport)
	}
	return writeImportOutput(ctx, sc, func(o *importOutput) { o.Committed = true })
}

func (s *Service) importCheck(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importJobInput(sc)
	if err != nil {
		return err
	}
	if readImportOutput(sc.Output()).Recorded {
		return nil
	}
	j, err := s.opts.Jobs.Get(ctx, sc.JobID)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 35, "reading the Compose definition back")
	st, err := s.opts.Stacks.RecordArchiveStack(ctx, in.StackID, principalOf(j))
	if err != nil {
		var se *domain.StackError
		switch {
		case errors.As(err, &se) && se.Code == domain.StackErrInvalidDefinition:
			msg := se.Message
			for _, i := range se.Issues {
				msg += "; " + i.Message
			}
			return &classed{class: ClassInvalid, err: errors.New(msg), recovery: recoveryImport}
		case errors.As(err, &se) && (se.Code == domain.StackErrOffline || se.Code == domain.StackErrAgentTimeout):
			return &classed{class: domain.ErrorAgentOffline, err: fmt.Errorf("%s (%w)", se.Message, jobexec.ErrStepInterrupted),
				recovery: recoveryImport}
		}
		return &classed{class: ClassTransferFailed, err: err, recovery: recoveryImport}
	}
	names, err := s.composeVolumeNames(ctx, st, in)
	if err != nil {
		return err
	}
	return writeImportOutput(ctx, sc, func(o *importOutput) { o.Recorded, o.VolumeNames = true, names })
}

// composeVolumeNames asks the destination which names Compose gives the
// volumes of the committed project under the new stack's name: the check
// derived them from the archive (a volume named after the old project
// follows the new one), but only the definition decides (an explicit
// name: equal to <old project>_<key> stays). A volume whose name differs
// from the checked one must not exist yet; the cleanup covers it too.
func (s *Service) composeVolumeNames(ctx context.Context, st domain.Stack, in importInput) (map[string]string, error) {
	out := map[string]string{}
	if len(in.Volumes) == 0 {
		return out, nil
	}
	facts, err := s.sourceFacts(ctx, st, false)
	if err != nil {
		return nil, agentFailure(err, ClassTransferFailed, recoveryImport)
	}
	byKey := map[string]protocol.MigrationVolumeFacts{}
	for _, v := range facts.Volumes {
		if v.Key != "" {
			byKey[v.Key] = v
		}
	}
	for _, v := range in.Volumes {
		f, ok := byKey[v.Key]
		switch {
		case !ok || f.External || f.Anonymous:
			return nil, &classed{class: ClassInvalid, err: fmt.Errorf("the archive's volume %s is not a volume of the Compose definition", v.Key),
				recovery: recoveryNothingKept + "The archive's files and volumes disagree; export the stack again."}
		case f.Name != v.Name && f.Exists:
			return nil, &classed{class: ClassBlocked, err: fmt.Errorf("the Compose file names the volume %s %s, which already exists here", v.Key, f.Name),
				recovery: recoveryImport}
		}
		out[v.Key] = f.Name
	}
	return out, nil
}

func (s *Service) importVolumes(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importJobInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	u, err := s.importUpload(in.ArchiveID)
	if err != nil {
		return err
	}
	names := readImportOutput(sc.Output()).VolumeNames
	// Registered again with the names Compose gives the volumes (the
	// cleanup only removes volumes this job created).
	all := in.volumeNames()
	for _, n := range names {
		if !slices.Contains(all, n) {
			all = append(all, n)
		}
	}
	if err := sc.AddCompensation(ctx, jobspec.CompRemoveArchiveImport, removeArgs{JobID: sc.JobID, Environment: st.EnvironmentID,
		Project: in.Name, Volumes: all}); err != nil {
		return err
	}
	ref := stackRef(st)
	c := s.cursor(in.ArchiveID)
	defer c.reset()
	for i, v := range in.Volumes {
		if n := names[v.Key]; n != "" {
			v.Name = n
		}
		p := Part{Volume: v.Key}
		if slices.Contains(readImportOutput(sc.Output()).Parts, p.Name()) {
			continue
		}
		if sc.CancelRequested() {
			return fmt.Errorf("before %s: %w", p.Name(), jobexec.ErrStepCancelled)
		}
		mv, ok := u.Manifest.Volume(v.Key)
		if !ok {
			return &classed{class: ClassTransferFailed, err: fmt.Errorf("volume %s is not in the archive", v.Key), recovery: recoveryImport}
		}
		sc.Progress(ctx, 40+50*i/len(in.Volumes), "copying volume "+v.Name)
		spec := protocol.MigrationVolumeSpec{Name: v.Name, Labels: mv.Labels,
			Compose: &protocol.MigrationComposeVolume{Stack: ref, Key: v.Key}}
		if err := s.receivePart(ctx, sc, st.EnvironmentID, c, p,
			protocol.MigrationReceiveInput{MigrationID: sc.JobID, Part: protocol.PartVolume, Volume: &spec}); err != nil {
			return err
		}
		if err := writeImportOutput(ctx, sc, func(o *importOutput) { o.Parts = append(o.Parts, p.Name()) }); err != nil {
			return err
		}
	}
	return nil
}

// keep marks the stack's files as kept: from now on nothing is undone.
// The compensation is released first: a crash in between leaves files
// behind for a forgotten stack (removed by hand, or the next import of the
// name fails on the folder), never a kept stack without its files.
func (s *Service) keep(ctx context.Context, sc *jobexec.StepContext, env string) error {
	if readImportOutput(sc.Output()).Kept {
		return nil
	}
	if err := sc.ReleaseCompensation(ctx, jobspec.CompRemoveArchiveImport); err != nil {
		return err
	}
	if err := writeImportOutput(ctx, sc, func(o *importOutput) { o.Kept = true }); err != nil {
		return err
	}
	var co protocol.MigrationCleanupOutput
	if err := s.call(ctx, env, protocol.ReqMigrationCleanup, protocol.MigrationCleanupInput{MigrationID: sc.JobID, Finished: true},
		&co, s.opts.RequestTimeout); err != nil {
		s.log.Warn("could not remove the import's staging directory", "job_id", sc.JobID, "error", err)
	}
	return nil
}

func (s *Service) importDeploy(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importJobInput(sc)
	if err != nil {
		return err
	}
	st, err := s.opts.Stacks.Get(ctx, in.StackID)
	if err != nil {
		return err
	}
	if err := s.keep(ctx, sc, st.EnvironmentID); err != nil {
		return err
	}
	if !in.Deploy || readImportOutput(sc.Output()).DeployJobID != "" {
		return nil
	}
	// The deploy is its own job, queued and not awaited: it needs this
	// stack's lock, which this job holds until it ends.
	j, err := s.opts.Jobs.Get(ctx, sc.JobID)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 95, "queuing the deploy of "+st.Name)
	dj, err := s.opts.Stacks.Deploy(ctx, principalOf(j), st, domain.StackJobRequest{IdempotencyKey: "archive-" + sc.JobID},
		domain.StackDeployOptions{Pull: "missing"})
	if err != nil {
		return &classed{class: ClassDeployFailed, err: fmt.Errorf("start the deploy: %w", err),
			recovery: "The stack and its volumes were created; deploy it from its page."}
	}
	sc.Item(ctx, "deploy", domain.ItemSucceeded, "deploy queued as job "+dj.ID)
	return writeImportOutput(ctx, sc, func(o *importOutput) { o.DeployJobID = dj.ID })
}

func (s *Service) importFinalize(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importJobInput(sc)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 100, "created "+in.Name+" from the archive")
	s.log.Info("stack created from an archive", "job_id", sc.JobID, "stack_id", in.StackID, "volumes", len(in.Volumes))
	return nil
}

// removeImport is the remove_archive_import compensation: the project
// directory (once committed) and the volumes this job created are removed.
func (s *Service) removeImport(ctx context.Context, raw json.RawMessage) error {
	var a removeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("malformed compensation arguments: %w", err)
	}
	var out protocol.MigrationCleanupOutput
	if err := s.call(ctx, a.Environment, protocol.ReqMigrationCleanup, protocol.MigrationCleanupInput{MigrationID: a.JobID,
		Project: a.Project, Volumes: a.Volumes}, &out, s.opts.StopTimeout); err != nil {
		if errors.Is(err, jobs.ErrAgentOffline) {
			return errors.New("the environment's agent is offline: remove the folder " + a.Project + " from its stacks volume and the volumes " +
				strings.Join(a.Volumes, ", ") + " once it reconnects")
		}
		return fmt.Errorf("remove what the import wrote: %w", err)
	}
	s.log.Info("stack archive import undone", "job_id", a.JobID, "removed", len(out.Removed))
	return nil
}

// onImportFinished forgets the stack of an import that did not keep its
// files and removes the upload once a stack kept it.
func (s *Service) onImportFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	var in importInput
	if err := json.Unmarshal(j.Input, &in); err != nil || in.StackID == "" {
		return nil //nolint:nilerr // malformed input: nothing to follow
	}
	out := readImportOutput(j.ResultOutput)
	if j.State == domain.JobSucceeded || out.Kept {
		// Removed before the claim ends: no other stack starts from it.
		s.removeUpload(in.ArchiveID)
		s.release(in.ArchiveID)
		return nil
	}
	s.release(in.ArchiveID)
	return s.opts.Stacks.ForgetArchiveStack(ctx, db, in.StackID, j)
}
