package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/downvolumes"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/regauth"
	"github.com/neurekadev/docker-manager/internal/agent/volumelabels"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// sourceRetries bounds how often apply re-reads a definition that changed
// while it was being loaded.
const sourceRetries = 3

// Executors returns the stack.* job executors (runtime.Options.Executors).
func (s *Service) Executors() []jobexec.Executor {
	return []jobexec.Executor{
		{Kind: jobspec.StackDeploy, Steps: map[string]jobexec.StepFunc{
			"resolve_sources": classified(s.resolveSources),
			"pull_images":     classified(s.pullImages),
			"build_images":    classified(s.buildImages),
			"apply":           classified(s.apply),
		}},
		{Kind: jobspec.StackStart, Steps: map[string]jobexec.StepFunc{"start": classified(s.lifecycleStep(opStart))}},
		{Kind: jobspec.StackStop, Steps: map[string]jobexec.StepFunc{"stop": classified(s.lifecycleStep(opStop))}},
		{Kind: jobspec.StackRestart, Steps: map[string]jobexec.StepFunc{"restart": classified(s.lifecycleStep(opRestart))}},
		{Kind: jobspec.StackDown, Steps: map[string]jobexec.StepFunc{"down": classified(s.down)}},
		{Kind: jobspec.StackRemove, Steps: map[string]jobexec.StepFunc{"down": classified(s.down)}},
		{Kind: jobspec.StackBuild, Steps: map[string]jobexec.StepFunc{
			"fetch_sources": classified(s.fetchSources),
			"build_images":  classified(s.stackBuild),
		}},
		s.updateExecutor(),
		s.importExecutor(),
		s.renameExecutor(),
		{Kind: jobspec.StackPull, Steps: map[string]jobexec.StepFunc{"pull_images": classified(s.pullOnly)}},
	}
}

func input(sc *jobexec.StepContext) (protocol.StackJobInput, error) {
	var in protocol.StackJobInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, fmt.Errorf("malformed stack job input: %w", err)
	}
	if err := in.Stack.Validate(); err != nil {
		return in, err
	}
	switch in.Pull {
	case "", "missing", "always":
	default:
		return in, fmt.Errorf("unknown pull mode %q", in.Pull)
	}
	if in.BuildTimeoutSeconds < 0 || time.Duration(in.BuildTimeoutSeconds)*time.Second > jobspec.MaxBuildTimeout {
		return in, fmt.Errorf("build timeout must be between 0 and %s", jobspec.MaxBuildTimeout)
	}
	return in, nil
}

// auth returns the registry credentials of the command (#19): every
// credential it carries, for pulls, builds (BuildKit asks per host) and the
// SDK's own pulls. When the input named connections, their credentials must
// be there: the job fails rather than pulling anonymously.
func auth(sc *jobexec.StepContext, in protocol.StackJobInput) ([]engine.RegistryAuth, error) {
	a := regauth.All(sc.Secrets)
	if len(in.RegistryConnections) > 0 && len(a) == 0 {
		return nil, regauth.ErrMissing
	}
	return a, nil
}

// update reads the output so far, applies fn and journals it.
func update(ctx context.Context, sc *jobexec.StepContext, fn func(o *protocol.StackJobOutput)) error {
	var o protocol.StackJobOutput
	if b := sc.Output(); len(b) > 0 {
		if err := json.Unmarshal(b, &o); err != nil {
			return err
		}
	}
	fn(&o)
	err := sc.SetOutput(ctx, o)
	if errors.Is(err, jobexec.ErrOutputTooLarge) && o.Sources != nil && !o.Sources.ContentOmitted {
		// Report the definition by hash only rather than losing the output.
		src := *o.Sources
		src.Files = slices.Clone(src.Files)
		for i := range src.Files {
			src.Files[i].Content = nil
		}
		src.ContentOmitted = true
		o.Sources = &src
		return sc.SetOutput(ctx, o)
	}
	return err
}

// project resolves and loads the stack's project (with the storage guard).
func (s *Service) project(ctx context.Context, in protocol.StackJobInput) (*compose.Project, string, error) {
	dir, err := s.resolve(in.Stack)
	if err != nil {
		return nil, "", err
	}
	c, err := s.composer()
	if err != nil {
		return nil, "", err
	}
	p, err := c.Load(ctx, specOf(in.Stack, dir))
	if err != nil {
		return nil, "", err
	}
	return p, dir, nil
}

func (s *Service) progress(ctx context.Context, sc *jobexec.StepContext) func(compose.Event) {
	return func(e compose.Event) {
		if e.Status == "working" && e.Text == "" {
			return
		}
		msg := strings.TrimSpace(e.Resource + " " + e.Text)
		if e.Details != "" {
			msg += " (" + e.Details + ")"
		}
		sc.Progress(ctx, -1, msg)
	}
}

// resolveSources validates the on-disk project and captures the state of
// its containers before anything changes (journaled for recovery).
func (s *Service) resolveSources(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := input(sc)
	if err != nil {
		return err
	}
	p, dir, err := s.project(ctx, in)
	if err != nil {
		return err
	}
	if err := checkDeclaredName(ctx, in.Stack, dir); err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	before, err := serviceStates(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	bs := binds(p, dir)
	sc.Progress(ctx, 5, fmt.Sprintf("loaded %s: %d services", p.Name, len(p.Services)))
	return update(ctx, sc, func(o *protocol.StackJobOutput) {
		o.Before = before
		o.Services = serviceInfos(p)
		o.Binds = bs
		o.Warnings = warnings(p, bs)
	})
}

// pullImages pulls every image with pull "always"; otherwise the apply step
// pulls only images missing on the host (a redeploy never moves tags
// implicitly, #20).
func (s *Service) pullImages(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := input(sc)
	if err != nil {
		return err
	}
	if in.Pull != "always" {
		return nil
	}
	creds, err := auth(sc, in)
	if err != nil {
		return err
	}
	p, _, err := s.project(ctx, in)
	if err != nil {
		return err
	}
	c, _ := s.composer()
	sc.Progress(ctx, 15, "pulling images")
	return c.Pull(ctx, p, compose.RunOptions{Events: s.progress(ctx, sc), Auth: creds})
}

// pullOnly (stack.pull) pulls every image of the stack and changes no
// container: it reports the services whose reference now names another
// image (a deploy runs it).
func (s *Service) pullOnly(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := input(sc)
	if err != nil {
		return err
	}
	creds, err := auth(sc, in)
	if err != nil {
		return err
	}
	p, _, err := s.project(ctx, in)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	imageOf := func() map[string]string {
		out := map[string]string{}
		for _, svc := range p.Services {
			if svc.Build || svc.Image == "" {
				continue
			}
			if img, err := eng.InspectImage(ctx, svc.Image); err == nil {
				out[svc.Name] = img.ID
			}
		}
		return out
	}
	before := imageOf()
	c, _ := s.composer()
	sc.Progress(ctx, 10, "pulling images")
	if err := c.Pull(ctx, p, compose.RunOptions{Events: s.progress(ctx, sc), Auth: creds}); err != nil {
		return err
	}
	after := imageOf()
	var pulled []string
	for _, svc := range p.Services {
		id, ok := after[svc.Name]
		if !ok {
			continue
		}
		if before[svc.Name] != id {
			pulled = append(pulled, svc.Name)
			sc.Item(ctx, svc.Name, domain.ItemSucceeded, "newer image pulled: deploy to run it")
		} else {
			sc.Item(ctx, svc.Name, domain.ItemSkipped, "image up to date")
		}
	}
	images := appliedImages(ctx, eng, p)
	return update(ctx, sc, func(o *protocol.StackJobOutput) {
		o.Pulled = pulled
		o.Images = images
	})
}

// buildImages builds the deploy's images from build sections (#33): every
// build section when requested, otherwise only the images missing on the
// host, through the same path as stack.build (streamed, scrubbed progress,
// cancellation, timeout). Nothing is deployed yet, so a cancelled build
// leaves the stack as it was.
func (s *Service) buildImages(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := input(sc)
	if err != nil {
		return err
	}
	p, _, err := s.project(ctx, in)
	if err != nil {
		return err
	}
	return s.runBuild(ctx, sc, in, p, !in.Build)
}

// apply deploys exactly the bytes it reports: it snapshots the definition,
// loads the project from that snapshot and checks the files did not change
// meanwhile. The result carries the sources (the applied revision), the
// applied images and the state after the deploy, also when Up fails.
func (s *Service) apply(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := input(sc)
	if err != nil {
		return err
	}
	creds, err := auth(sc, in)
	if err != nil {
		return err
	}
	dir, err := s.resolve(in.Stack)
	if err != nil {
		return err
	}
	c, err := s.composer()
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	var p *compose.Project
	var snap protocol.SourceSnapshot
	for attempt := 0; ; attempt++ {
		p, snap, err = s.loadSnapshot(ctx, c, in, dir)
		if err == nil {
			break
		}
		if !errors.Is(err, errSourcesChanged) || attempt+1 >= sourceRetries {
			return err
		}
	}
	sc.Progress(ctx, 50, "applying "+short(snap.Hash))
	var timeout *time.Duration
	if in.TimeoutSeconds > 0 {
		d := time.Duration(in.TimeoutSeconds) * time.Second
		timeout = &d
	}
	// Docker Manager's own project (#32): the agent's own service is
	// converged by a helper container after the job.
	services, handoff, err := s.selfHandoff(ctx, p, in.Stack.ProjectName, in.Services, in.RemoveOrphans)
	if err != nil {
		return err
	}
	startsBefore, serr := containerStarts(ctx, eng, in.Stack.ProjectName)
	if serr != nil {
		return serr
	}
	var upErr error
	if len(handoff) == 0 || len(services) > 0 {
		upErr = c.Up(ctx, p, compose.UpOptions{RunOptions: compose.RunOptions{Events: s.progress(ctx, sc), Auth: creds},
			Services: services, ForceRecreate: in.ForceRecreate, RemoveOrphans: in.RemoveOrphans, StopTimeout: timeout})
	}
	if upErr != nil && ctx.Err() != nil {
		return upErr // shutdown: the attempt is recovered from the journal
	}
	after, aerr := serviceStates(ctx, eng, in.Stack.ProjectName)
	startsAfter, serr := containerStarts(ctx, eng, in.Stack.ProjectName)
	unchanged := serr == nil && upErr == nil && len(handoff) == 0 && !startedAny(startsBefore, startsAfter)
	images := appliedImages(ctx, eng, p, handoff...)
	bs := binds(p, dir)
	if err := update(ctx, sc, func(o *protocol.StackJobOutput) {
		o.Unchanged = unchanged
		src := snap
		if inlineSize(src) > protocol.MaxInlineSources {
			for i := range src.Files {
				src.Files[i].Content = nil
			}
			src.ContentOmitted = true
		}
		o.Sources = &src
		o.Services = serviceInfos(p)
		o.Images = images
		o.Binds = bs
		o.Warnings = warnings(p, bs)
		if len(handoff) > 0 && upErr == nil {
			o.Warnings = append(o.Warnings, protocol.ComposeIssue{Code: protocol.IssueAgentSelfUpdate, Message: selfUpdateMessage(handoff)})
		}
		o.After = after
	}); err != nil {
		return errors.Join(upErr, err)
	}
	if upErr != nil {
		return upErr
	}
	s.recordVolumeLabels(ctx, eng, in.Stack.ProjectName, p)
	// The deployed containers have anonymous volumes of their own now: a
	// record of an earlier down no longer describes the stack (#276).
	if err := s.opts.DownVolumes.Forget(in.Stack.ProjectName); err != nil {
		s.log.Warn("could not forget the anonymous volumes of the last down", "project", in.Stack.ProjectName, "error", err)
	}
	if len(handoff) > 0 {
		s.scheduleSelf(sc, in.Stack, dir, snap, handoff, in.ForceRecreate, in.TimeoutSeconds)
	}
	return aerr
}

// recordVolumeLabels records the Docker Manager labels the deployed
// definition declares on its volumes with a value the volumes lack: Docker
// keeps the labels a volume was created with, so backups and maintenance
// honor these instead (volumelabels). Volumes not created (yet) have
// nothing to honor. Best effort: a failure keeps the previous record.
func (s *Service) recordVolumeLabels(ctx context.Context, eng engine.Engine, project string, p *compose.Project) {
	if s.opts.VolumeLabels == nil {
		return
	}
	var vols []volumelabels.Volume
	for _, v := range p.Resources().Volumes {
		if v.External || v.Name == "" {
			continue
		}
		ev, err := eng.InspectVolume(ctx, v.Name)
		if engine.IsCode(err, engine.CodeNotFound) {
			continue
		}
		if err != nil {
			s.log.Warn("could not record the volume labels of the definition", "project", project, "error", err)
			return
		}
		vols = append(vols, volumelabels.Volume{Name: ev.Name, Created: ev.CreatedAt, Actual: ev.Labels, Declared: v.Labels})
	}
	if err := s.opts.VolumeLabels.Record(project, vols); err != nil {
		s.log.Warn("could not record the volume labels of the definition", "project", project, "error", err)
	}
}

// containerStarts maps each container of the project to when it last
// started (zero: never).
func containerStarts(ctx context.Context, eng engine.Engine, project string) (map[string]time.Time, error) {
	list, err := lifecycle.ProjectContainers(ctx, eng, project)
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(list))
	for _, c := range list {
		d, err := eng.InspectContainer(ctx, c.ID)
		if engine.IsCode(err, engine.CodeNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[c.ID] = d.State.StartedAt
	}
	return out, nil
}

// startedAny reports whether a container started between the two
// snapshots: a new one that runs, or one that started again (its uptime
// reset).
func startedAny(before, after map[string]time.Time) bool {
	for id, t := range after {
		if t.IsZero() {
			continue
		}
		if b, ok := before[id]; !ok || !t.Equal(b) {
			return true
		}
	}
	return false
}

var errSourcesChanged = errors.New("the definition files changed while the deploy read them")

// loadSnapshot reads the definition, loads the project from those bytes
// and re-reads the files to make sure they did not change in between.
func (s *Service) loadSnapshot(ctx context.Context, c Composer, in protocol.StackJobInput, dir string) (*compose.Project, protocol.SourceSnapshot, error) {
	files, _ := definitionFiles(ctx, in.Stack, dir)
	snap, err := readSources(dir, files)
	if err != nil {
		return nil, snap, err
	}
	spec := specOf(in.Stack, dir)
	spec.Content = map[string][]byte{}
	for _, f := range snap.Files {
		spec.Content[f.Path] = f.Content
	}
	p, err := c.Load(ctx, spec)
	if err != nil {
		return nil, snap, err
	}
	again, err := readSources(dir, files)
	if err != nil {
		return nil, snap, err
	}
	if again.Hash != snap.Hash {
		return nil, snap, errSourcesChanged
	}
	return p, snap, nil
}

func inlineSize(s protocol.SourceSnapshot) int {
	n := 0
	for _, f := range s.Files {
		n += len(f.Content)
	}
	return n
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// appliedImages resolves each service's running image: its ID, the
// repository digest matching the reference (the #20 baseline) and the
// platform. The handed-off services (#32: the agent's own service, which
// a helper recreates after the job) still run their old container when the
// job reports, so they get the image their reference names now (the one
// the deploy pulled and the helper runs); otherwise the stack would show
// image drift for the agent after every self-update.
func appliedImages(ctx context.Context, eng engine.Engine, p *compose.Project, handoff ...string) []protocol.AppliedImage {
	list, _ := lifecycle.ProjectContainers(ctx, eng, p.Name)
	out := make([]protocol.AppliedImage, 0, len(p.Services))
	for _, svc := range p.Services {
		ai := protocol.AppliedImage{Service: svc.Name, Image: svc.Image, Build: svc.Build}
		if slices.Contains(handoff, svc.Name) && svc.Image != "" {
			if img, err := eng.InspectImage(ctx, svc.Image); err == nil {
				ai.ImageID = img.ID
			}
		}
		for _, c := range list {
			if ai.ImageID == "" && c.Labels[lifecycle.ComposeServiceLabel] == svc.Name && c.ImageID != "" {
				ai.ImageID = c.ImageID
				break
			}
		}
		if ai.ImageID != "" {
			if img, err := eng.InspectImage(ctx, ai.ImageID); err == nil {
				ai.Digest = DigestFor(svc.Image, img.RepoDigests)
				ai.Platform = img.OS + "/" + img.Architecture
				if img.Variant != "" {
					ai.Platform += "/" + img.Variant
				}
			}
		}
		out = append(out, ai)
	}
	return out
}

// DigestFor picks the repository digest of ref from an image's
// RepoDigests ("repo@sha256:..."): the digest pinned in ref itself, or the
// entry whose repository matches ref's. Docker Hub names match in their
// short and fully qualified forms.
func DigestFor(ref string, repoDigests []string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[i+1:]
	}
	repo := normalizeRepo(repository(ref))
	for _, rd := range repoDigests {
		i := strings.Index(rd, "@")
		if i < 0 {
			continue
		}
		if normalizeRepo(rd[:i]) == repo {
			return rd[i+1:]
		}
	}
	return ""
}

// repository strips the tag from a reference ("host:5000/app:1" -> "host:5000/app").
func repository(ref string) string {
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon]
	}
	return ref
}

func normalizeRepo(r string) string {
	first, _, found := strings.Cut(r, "/")
	if !found || (!strings.ContainsAny(first, ".:") && first != "localhost") {
		r = "docker.io/" + r
	}
	if strings.HasPrefix(r, "docker.io/") && strings.Count(r, "/") == 1 {
		r = "docker.io/library/" + strings.TrimPrefix(r, "docker.io/")
	}
	return r
}

// Lifecycle operations.
type lifecycleOp int

const (
	opStart lifecycleOp = iota
	opStop
	opRestart
)

// lifecycleStep runs start/stop/restart on the deployed containers through
// the shared dependency-aware lifecycle (graph from the containers'
// labels: the deployed stack, not undeployed edits on disk).
func (s *Service) lifecycleStep(op lifecycleOp) jobexec.StepFunc {
	return func(ctx context.Context, sc *jobexec.StepContext) error {
		in, err := input(sc)
		if err != nil {
			return err
		}
		eng, err := s.engine()
		if err != nil {
			return err
		}
		list, err := lifecycle.ProjectContainers(ctx, eng, in.Stack.ProjectName)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			// Taken down (#280): a start brings it up again from its files
			// when they are still the last deployed ones.
			if op == opStart && in.AppliedHash != "" {
				return s.upFromDefinition(ctx, sc, in)
			}
			return fmt.Errorf("project %s has no containers on this Engine; deploy the stack first", in.Stack.ProjectName)
		}
		before, err := serviceStates(ctx, eng, in.Stack.ProjectName)
		if err != nil {
			return err
		}
		if err := update(ctx, sc, func(o *protocol.StackJobOutput) { o.Before = before }); err != nil {
			return err
		}
		g, err := lifecycle.GraphFromContainers(list)
		if err != nil {
			return err
		}
		rt := lifecycle.EngineRuntime{Engine: eng, Project: in.Stack.ProjectName, Clock: s.opts.Clock}
		o := lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout,
			Progress: func(service, msg string) { sc.Progress(ctx, -1, service+": "+msg) }}
		if in.TimeoutSeconds > 0 {
			d := time.Duration(in.TimeoutSeconds) * time.Second
			o.StopTimeout = &d
		}
		var rep lifecycle.Report
		switch op {
		case opStart:
			rep, err = lifecycle.Start(ctx, g, rt, in.Services, o)
		case opStop:
			rep, err = lifecycle.Stop(ctx, g, rt, in.Services, o)
		case opRestart:
			rep, err = lifecycle.Restart(ctx, g, rt, in.Services, o)
		}
		after, aerr := serviceStates(ctx, eng, in.Stack.ProjectName)
		for _, w := range rep.Warnings {
			sc.Item(ctx, w, domain.ItemSkipped, "")
		}
		if uerr := update(ctx, sc, func(o *protocol.StackJobOutput) {
			o.After = after
			for _, w := range rep.Warnings {
				o.Warnings = append(o.Warnings, protocol.ComposeIssue{Code: "optional_dependency", Message: w})
			}
		}); uerr != nil && err == nil {
			err = uerr
		}
		if err != nil {
			return err
		}
		return aerr
	}
}

// upFromDefinition is a stack.start of a project without containers (taken
// down, #280): Compose up from the definition on disk, only when it still
// hashes to the last applied revision (in.AppliedHash), so a start never
// applies undeployed changes. Nothing is built; images missing on the host
// are pulled like Compose up does, without registry connections.
func (s *Service) upFromDefinition(ctx context.Context, sc *jobexec.StepContext, in protocol.StackJobInput) error {
	dir, err := s.resolve(in.Stack)
	if err != nil {
		return err
	}
	c, err := s.composer()
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	var p *compose.Project
	var snap protocol.SourceSnapshot
	for attempt := 0; ; attempt++ {
		p, snap, err = s.loadSnapshot(ctx, c, in, dir)
		if err == nil {
			break
		}
		if !errors.Is(err, errSourcesChanged) || attempt+1 >= sourceRetries {
			return err
		}
	}
	if snap.Hash != in.AppliedHash {
		return &stepError{class: protocol.StackClassDefinitionChanged,
			err: fmt.Errorf("the files of %s changed since its last deploy", in.Stack.ProjectName),
			recovery: "Nothing was started. Deploy the stack to start it with the changed files, or restore the deployed " +
				"revision to disk and start it again."}
	}
	if err := update(ctx, sc, func(o *protocol.StackJobOutput) { o.Before = []protocol.ServiceState{} }); err != nil {
		return err
	}
	sc.Progress(ctx, 50, "starting "+short(snap.Hash))
	var timeout *time.Duration
	if in.TimeoutSeconds > 0 {
		d := time.Duration(in.TimeoutSeconds) * time.Second
		timeout = &d
	}
	upErr := c.Up(ctx, p, compose.UpOptions{RunOptions: compose.RunOptions{Events: s.progress(ctx, sc)},
		Services: in.Services, StopTimeout: timeout})
	if upErr != nil && ctx.Err() != nil {
		return upErr // shutdown: the attempt is recovered from the journal
	}
	after, aerr := serviceStates(ctx, eng, in.Stack.ProjectName)
	if err := update(ctx, sc, func(o *protocol.StackJobOutput) { o.After = after }); err != nil {
		return errors.Join(upErr, err)
	}
	if upErr != nil {
		return upErr
	}
	s.recordVolumeLabels(ctx, eng, in.Stack.ProjectName, p)
	// The new containers have anonymous volumes of their own: the record
	// of the down no longer describes the stack (#276).
	if err := s.opts.DownVolumes.Forget(in.Stack.ProjectName); err != nil {
		s.log.Warn("could not forget the anonymous volumes of the last down", "project", in.Stack.ProjectName, "error", err)
	}
	return aerr
}

// down stops and removes the project's containers and networks through the
// Compose SDK (volumes are kept). The project is found by its name and
// labels, so a stack whose files are gone can still be taken down. A
// stack.remove with RemoveVolumes plans the stack's own volumes first and
// removes them afterwards (volumes.go).
func (s *Service) down(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := input(sc)
	if err != nil {
		return err
	}
	c, err := s.composer()
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	withVolumes := sc.Kind == jobspec.StackRemove && in.RemoveVolumes
	if withVolumes {
		if err := s.planVolumes(ctx, sc, in, eng); err != nil {
			return err
		}
	}
	before, err := serviceStates(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	if err := update(ctx, sc, func(o *protocol.StackJobOutput) { o.Before = before }); err != nil {
		return err
	}
	var timeout *time.Duration
	if in.TimeoutSeconds > 0 {
		d := time.Duration(in.TimeoutSeconds) * time.Second
		timeout = &d
	}
	// The anonymous volumes of the containers a down removes: nothing ties
	// them to the project afterwards (#276). Recorded before the down with
	// the containers they come from, so a down after one that removed some
	// of them keeps those volumes, while a service whose container came back
	// replaces its own; backups only use the record while the project has
	// no containers.
	if len(before) > 0 && sc.Kind == jobspec.StackDown {
		containers, anonymous, err := anonymousVolumes(ctx, eng, in.Stack.ProjectName)
		if err != nil {
			return err
		}
		if err := s.opts.DownVolumes.Record(in.Stack.ProjectName, containers, anonymous); err != nil {
			s.log.Warn("could not record the anonymous volumes the down leaves behind", "project", in.Stack.ProjectName, "error", err)
		}
	}
	if len(before) > 0 {
		if err := c.Down(ctx, in.Stack.ProjectName, nil, compose.DownOptions{RunOptions: compose.RunOptions{Events: s.progress(ctx, sc)},
			RemoveOrphans: true, Timeout: timeout}); err != nil {
			return err
		}
	}
	after, err := serviceStates(ctx, eng, in.Stack.ProjectName)
	if uerr := update(ctx, sc, func(o *protocol.StackJobOutput) { o.After = after }); uerr != nil && err == nil {
		err = uerr
	}
	if err == nil && withVolumes {
		err = s.removeVolumes(ctx, sc, in, eng)
	}
	if err == nil && sc.Kind == jobspec.StackRemove {
		// No definition declares labels on its volumes any more.
		if ferr := s.opts.VolumeLabels.Forget(in.Stack.ProjectName); ferr != nil {
			s.log.Warn("could not forget the volume labels of the removed stack", "project", in.Stack.ProjectName, "error", ferr)
		}
		if ferr := s.opts.DownVolumes.Forget(in.Stack.ProjectName); ferr != nil {
			s.log.Warn("could not forget the anonymous volumes of the removed stack", "project", in.Stack.ProjectName, "error", ferr)
		}
	}
	return err
}

// anonymousVolumes lists the project's containers and the anonymous
// volumes they mount, by name (the Engine gives them a random 64-digit hex
// name). Temporary containers of Docker Manager or Compose are left out
// (protocol.IsHelperContainer), like backups do.
func anonymousVolumes(ctx context.Context, eng engine.Engine, project string) ([]downvolumes.Container, []downvolumes.Volume, error) {
	list, err := lifecycle.ProjectContainers(ctx, eng, project)
	if err != nil {
		return nil, nil, err
	}
	var ids []downvolumes.Container
	var out []downvolumes.Volume
	seen := map[string]bool{}
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		if protocol.IsHelperContainer(name, c.Labels) {
			continue
		}
		ids = append(ids, downvolumes.Container{ID: c.ID, Service: c.Labels[lifecycle.ComposeServiceLabel]})
		for _, m := range c.Mounts {
			if m.Type != "volume" || !protocol.AnonymousVolumeName(m.Name) || seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			out = append(out, downvolumes.Volume{Name: m.Name, Service: c.Labels[lifecycle.ComposeServiceLabel], Destination: m.Destination})
		}
	}
	slices.SortFunc(out, func(a, b downvolumes.Volume) int { return strings.Compare(a.Name, b.Name) })
	return ids, out, nil
}
