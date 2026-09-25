package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/agent/session"
	agentstacks "github.com/neurekadev/dockyard/internal/agent/stacks"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/streammux"
)

// The Compose side of the homelab (#7, #22 track B2): what a real agent
// does with its stacks volume and the Compose SDK, which the fake Engine
// cannot. compose.validate/write work on the in-memory project files;
// compose.discover is the production handler over the fake Engine;
// deploys, downs, removals and builds are simulated steps that create,
// start and remove the fake Engine's containers with the Compose labels;
// start, stop and restart are the production executors (internal/agent/
// stacks over internal/agent/lifecycle). migration.preview answers both
// roles from the fake Engines so the #35 preflight can be reviewed. The
// migration's transfer itself is not simulated (it fails at stop_source,
// which shows the failure path).

// projectsMu serializes compose.write and the seed's edits of project
// files (requests and jobs run concurrently).
var projectsMu sync.Mutex

// stepDelay paces the simulated steps so progress is visible in the UI.
const stepDelay = 700 * time.Millisecond

// devStackDeps gives the production stack executors the fake Engine and a
// verified stacks root; they never reach the (absent) Compose SDK.
type devStackDeps struct{ h *homelabHost }

func (d devStackDeps) Composer() agentstacks.Composer { return nil }
func (d devStackDeps) Engine() engine.Engine          { return d.h.engine }
func (d devStackDeps) Storage() *storage.Result {
	return &storage.Result{StacksDir: d.h.stacksDir, Roots: []storage.Root{{Kind: storage.KindStacks, Path: d.h.stacksDir, OK: true}}}
}

// projectDir is a project directory's host path (slash-separated) and
// its local filesystem path.
func (s *stackSim) projectDir(dir string) (host, local string) {
	host = s.host.stacksDir + "/" + dir
	return host, filepath.FromSlash(host)
}

// stackSim serves a host's Compose requests and stack jobs.
type stackSim struct {
	host *homelabHost
	prod *agentstacks.Service
}

func newStackSim(h *homelabHost, prod *agentstacks.Service) *stackSim {
	return &stackSim{host: h, prod: prod}
}

func (s *stackSim) requests() map[string]session.RequestHandler {
	prod := s.prod.Requests()
	return map[string]session.RequestHandler{
		protocol.ReqComposeValidate:  s.validate,
		protocol.ReqComposeWrite:     s.write,
		protocol.ReqComposeDiscover:  prod[protocol.ReqComposeDiscover],
		protocol.ReqMigrationPreview: s.migrationPreview,
		protocol.ReqMigrationStop:    s.migrationStop,
		protocol.ReqMigrationStart:   s.migrationStart,
		protocol.ReqMigrationCommit:  s.migrationRefuse,
		protocol.ReqMigrationCleanup: s.migrationCleanup,
	}
}

// streams are the migration transfer streams: refused, so a started
// migration fails after stopping the source and the manager's
// compensation starts it again (the #35 failure path, reviewable here).
func (s *stackSim) streams() map[string]session.StreamHandler {
	refuse := func(context.Context, *streammux.Stream) error {
		return &session.HandlerError{Code: protocol.CodeUnsupportedStream,
			Message: "the devstack does not simulate migration transfers (run the integration suite)"}
	}
	return map[string]session.StreamHandler{protocol.StreamMigrationSend: refuse, protocol.StreamMigrationReceive: refuse}
}

// executors are the stack.* job kinds: production start/stop/restart,
// simulated deploy, down, remove and build.
func (s *stackSim) executors() []jobexec.Executor {
	var out []jobexec.Executor
	for _, x := range s.prod.Executors() {
		switch x.Kind {
		case jobspec.StackStart, jobspec.StackStop, jobspec.StackRestart:
			out = append(out, x)
		}
	}
	return append(out,
		jobexec.Executor{Kind: jobspec.StackDeploy, Steps: map[string]jobexec.StepFunc{
			"resolve_sources": s.resolveSources, "pull_images": s.pullImages, "build_images": s.noBuild, "apply": s.apply,
		}},
		jobexec.Executor{Kind: jobspec.StackDown, Steps: map[string]jobexec.StepFunc{"down": s.down}},
		jobexec.Executor{Kind: jobspec.StackRemove, Steps: map[string]jobexec.StepFunc{"down": s.down}},
		jobexec.Executor{Kind: jobspec.StackBuild, Steps: map[string]jobexec.StepFunc{
			"fetch_sources": s.resolveSources, "build_images": s.noBuild,
		}},
	)
}

// --- Compose model -------------------------------------------------------

type simService struct {
	name, image, restart string
	build                bool
	ports                []engine.PortBinding
	deps                 []protocol.ComposeDependency
	volumes              []engine.MountSpec
	binds                []protocol.ComposeBind
	healthcheck          bool
}

type simProject struct {
	services []simService
	volumes  []string
	version  bool
}

// parseCompose reads the parts of a Compose file the devstack simulates
// (services in file order, images, ports, depends_on, volumes, binds).
func parseCompose(dir, name, src string) (simProject, []protocol.ComposeIssue) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return simProject{}, []protocol.ComposeIssue{{Code: protocol.IssueInvalidProject, Message: "compose.yaml is not valid YAML: " + err.Error()}}
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return simProject{}, []protocol.ComposeIssue{{Code: protocol.IssueInvalidProject, Message: "compose.yaml must be a mapping with a services key"}}
	}
	var p simProject
	var issues []protocol.ComposeIssue
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, v := top.Content[i].Value, top.Content[i+1]
		switch k {
		case "version":
			p.version = true
		case "volumes":
			for j := 0; j+1 < len(v.Content); j += 2 {
				p.volumes = append(p.volumes, v.Content[j].Value)
			}
		case "services":
			for j := 0; j+1 < len(v.Content); j += 2 {
				svc, errs := parseService(dir, name, v.Content[j].Value, v.Content[j+1])
				issues = append(issues, errs...)
				p.services = append(p.services, svc)
			}
		case "secrets", "configs":
			issues = append(issues, protocol.ComposeIssue{Code: protocol.IssueUnsupportedFeature,
				Message: "top-level " + k + " are not simulated by the devstack"})
		}
	}
	if len(p.services) == 0 && len(issues) == 0 {
		issues = append(issues, protocol.ComposeIssue{Code: protocol.IssueInvalidProject, Message: "the project defines no services"})
	}
	return p, issues
}

func parseService(dir, project, name string, n *yaml.Node) (simService, []protocol.ComposeIssue) {
	s := simService{name: name}
	var issues []protocol.ComposeIssue
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		switch k {
		case "image":
			s.image = v.Value
		case "build":
			s.build = true
		case "restart":
			s.restart = v.Value
		case "healthcheck":
			s.healthcheck = true
		case "ports":
			for _, p := range v.Content {
				if b, ok := parsePort(p.Value); ok {
					s.ports = append(s.ports, b)
				} else {
					issues = append(issues, protocol.ComposeIssue{Code: protocol.IssueInvalidProject, Service: name,
						Message: fmt.Sprintf("port %q is not host:container", p.Value)})
				}
			}
		case "depends_on":
			if v.Kind == yaml.SequenceNode {
				for _, d := range v.Content {
					s.deps = append(s.deps, protocol.ComposeDependency{Service: d.Value, Condition: lifecycle.ConditionStarted, Required: true})
				}
			} else {
				for j := 0; j+1 < len(v.Content); j += 2 {
					d := protocol.ComposeDependency{Service: v.Content[j].Value, Condition: lifecycle.ConditionStarted, Required: true}
					for m := 0; m+1 < len(v.Content[j+1].Content); m += 2 {
						switch v.Content[j+1].Content[m].Value {
						case "condition":
							d.Condition = v.Content[j+1].Content[m+1].Value
						case "restart":
							d.Restart = v.Content[j+1].Content[m+1].Value == "true"
						}
					}
					s.deps = append(s.deps, d)
				}
			}
		case "volumes":
			for _, m := range v.Content {
				src, target, _ := strings.Cut(m.Value, ":")
				target, mode, _ := strings.Cut(target, ":")
				if src == "" || target == "" {
					continue
				}
				if strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") {
					abs := src
					if !strings.HasPrefix(src, "/") {
						abs = path.Join(dir, src)
					}
					b := protocol.ComposeBind{Service: name, Source: abs, Target: target, ReadOnly: mode == "ro"}
					if strings.HasPrefix(abs, dir+"/") {
						b.RelPath = strings.TrimPrefix(abs, dir+"/")
					} else {
						b.External = true
					}
					s.binds = append(s.binds, b)
				} else {
					s.volumes = append(s.volumes, engine.MountSpec{Type: "volume", Source: project + "_" + src, Target: target})
				}
			}
		}
	}
	if s.image == "" && !s.build {
		issues = append(issues, protocol.ComposeIssue{Code: protocol.IssueInvalidProject, Service: name, Message: "the service has neither image nor build"})
	}
	if s.image == "" {
		s.image = project + "-" + name
	}
	return s, issues
}

func parsePort(v string) (engine.PortBinding, bool) {
	spec, proto, _ := strings.Cut(v, "/")
	parts := strings.Split(spec, ":")
	if len(parts) < 2 {
		return engine.PortBinding{}, false
	}
	host, err1 := strconv.Atoi(parts[len(parts)-2])
	ctr, err2 := strconv.Atoi(parts[len(parts)-1])
	if err1 != nil || err2 != nil || host < 0 || host > 65535 || ctr < 1 || ctr > 65535 {
		return engine.PortBinding{}, false
	}
	b := engine.PortBinding{ContainerPort: uint16(ctr), HostPort: uint16(host), Protocol: proto}
	if len(parts) == 3 {
		b.HostIP = parts[0]
	}
	return b, true
}

func (p simProject) composeServices() []protocol.ComposeService {
	out := make([]protocol.ComposeService, 0, len(p.services))
	for _, s := range p.services {
		out = append(out, protocol.ComposeService{Name: s.name, Image: s.image, Build: s.build, DependsOn: s.deps})
	}
	return out
}

func (p simProject) binds() []protocol.ComposeBind {
	out := []protocol.ComposeBind{}
	for _, s := range p.services {
		out = append(out, s.binds...)
	}
	return out
}

func (p simProject) warnings() []protocol.ComposeIssue {
	out := []protocol.ComposeIssue{}
	if p.version {
		out = append(out, protocol.ComposeIssue{Code: protocol.IssueObsoleteVersion,
			Message: "the top-level version key is obsolete and ignored; remove it"})
	}
	for _, b := range p.binds() {
		if b.External {
			out = append(out, protocol.ComposeIssue{Code: protocol.IssueBindOutsideProject, Service: b.Service,
				Message: b.Source + " is outside the project directory: stack backups need an explicit opt-in to include it"})
		}
	}
	return out
}

func fileContent(files []protocol.SourceFile, name string) string {
	for _, f := range files {
		if f.Path == name {
			return string(f.Content)
		}
	}
	return ""
}

// --- Requests ---------------------------------------------------------------

func (s *stackSim) validate(_ context.Context, raw json.RawMessage) (any, error) {
	var in protocol.ComposeValidateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	files := in.Files
	if len(files) == 0 {
		_, local := s.projectDir(in.Stack.Dir)
		snap, ok := readProject(local)
		files = snap.Files
		if !ok {
			return nil, &session.HandlerError{Code: protocol.CodeNotFound, Message: "the project directory does not exist"}
		}
	}
	host, _ := s.projectDir(in.Stack.Dir)
	p, errs := parseCompose(host, in.Stack.ProjectName, fileContent(files, "compose.yaml"))
	out := protocol.ComposeValidateOutput{Valid: len(errs) == 0, ProjectName: in.Stack.ProjectName, Errors: errs,
		Warnings: p.warnings(), Services: p.composeServices(), Binds: p.binds()}
	if out.Errors == nil {
		out.Errors = []protocol.ComposeIssue{}
	}
	return out, nil
}

func (s *stackSim) write(_ context.Context, raw json.RawMessage) (any, error) {
	var in protocol.ComposeWriteInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	if err := in.Stack.Validate(); err != nil || in.Stack.Root != protocol.RootStacks {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: "the devstack writes stacks in the stacks volume only"}
	}
	projectsMu.Lock()
	defer projectsMu.Unlock()
	_, local := s.projectDir(in.Stack.Dir)
	_, statErr := os.Stat(local)
	exists := statErr == nil
	switch in.Mode {
	case protocol.WriteCreate:
		if exists {
			return nil, &session.HandlerError{Code: protocol.CodeConflict, Message: "the project directory already exists"}
		}
	case protocol.WriteReplace:
		if !exists {
			return nil, &session.HandlerError{Code: protocol.CodeNotFound, Message: "the project directory does not exist"}
		}
		if cur, _ := readProject(local); in.ExpectHash != "" && cur.Hash != in.ExpectHash {
			return nil, &session.HandlerError{Code: protocol.CodeConflict, Message: "the definition on disk changed since it was read"}
		}
		for _, r := range in.Remove {
			if !slices.Contains(definitionNames, r) {
				return nil, &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: r + " is not a definition file of the project"}
			}
			_ = os.Remove(filepath.Join(local, r))
		}
	default:
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: fmt.Sprintf("unknown write mode %q", in.Mode)}
	}
	for _, f := range in.Files {
		if !slices.Contains(definitionNames, f.Path) {
			return nil, &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: f.Path + " is not a definition file"}
		}
		if err := writeFile(filepath.Join(local, f.Path), string(f.Content)); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeInternal, Message: err.Error()}
		}
	}
	snap, _ := readProject(local)
	return protocol.ComposeWriteOutput{Snapshot: snap}, nil
}

// --- Jobs --------------------------------------------------------------------

func jobInput(sc *jobexec.StepContext) (protocol.StackJobInput, error) {
	var in protocol.StackJobInput
	err := json.Unmarshal(sc.Input, &in)
	return in, err
}

func updateOutput(ctx context.Context, sc *jobexec.StepContext, fn func(o *protocol.StackJobOutput)) error {
	var o protocol.StackJobOutput
	if b := sc.Output(); len(b) > 0 {
		_ = json.Unmarshal(b, &o)
	}
	fn(&o)
	if o.Before == nil {
		o.Before = []protocol.ServiceState{}
	}
	if o.After == nil {
		o.After = []protocol.ServiceState{}
	}
	return sc.SetOutput(ctx, o)
}

func pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(stepDelay):
		return nil
	}
}

func (s *stackSim) load(in protocol.StackJobInput) (simProject, protocol.SourceSnapshot, error) {
	host, local := s.projectDir(in.Stack.Dir)
	snap, ok := readProject(local)
	if !ok {
		return simProject{}, snap, errors.New("the project directory does not exist on this host")
	}
	sp, errs := parseCompose(host, in.Stack.ProjectName, fileContent(snap.Files, "compose.yaml"))
	if len(errs) > 0 {
		return sp, snap, fmt.Errorf("the definition is not valid: %s", errs[0].Message)
	}
	return sp, snap, nil
}

func (s *stackSim) states(ctx context.Context, project string) []protocol.ServiceState {
	list, _ := lifecycle.ProjectContainers(ctx, s.host.engine, project)
	by := map[string]*protocol.ServiceState{}
	var names []string
	for _, c := range list {
		svc := c.Labels[lifecycle.ComposeServiceLabel]
		st, ok := by[svc]
		if !ok {
			st = &protocol.ServiceState{Service: svc}
			by[svc] = st
			names = append(names, svc)
		}
		st.Containers++
		if c.State == "running" {
			st.Running++
		}
	}
	slices.Sort(names)
	out := []protocol.ServiceState{}
	for _, n := range names {
		out = append(out, *by[n])
	}
	return out
}

func (s *stackSim) resolveSources(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := jobInput(sc)
	if err != nil {
		return err
	}
	p, _, err := s.load(in)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 5, fmt.Sprintf("loaded %s: %d services", in.Stack.ProjectName, len(p.services)))
	before := s.states(ctx, in.Stack.ProjectName)
	if err := pause(ctx); err != nil {
		return err
	}
	return updateOutput(ctx, sc, func(o *protocol.StackJobOutput) {
		o.Before, o.Services, o.Binds, o.Warnings = before, p.composeServices(), p.binds(), p.warnings()
	})
}

func (s *stackSim) pullImages(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := jobInput(sc)
	if err != nil {
		return err
	}
	p, _, err := s.load(in)
	if err != nil {
		return err
	}
	for i, svc := range p.services {
		if svc.build {
			continue
		}
		if _, err := s.host.engine.InspectImage(ctx, svc.image); err != nil || in.Pull == "always" {
			sc.Progress(ctx, 10+30*(i+1)/len(p.services), "pulled "+svc.image)
			s.host.engine.AddImage(svc.image)
			if err := pause(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *stackSim) noBuild(context.Context, *jobexec.StepContext) error { return nil }

func dependsLabel(deps []protocol.ComposeDependency) string {
	parts := make([]string, 0, len(deps))
	for _, d := range deps {
		parts = append(parts, fmt.Sprintf("%s:%s:%t", d.Service, d.Condition, d.Restart))
	}
	return strings.Join(parts, ",")
}

func (s *stackSim) apply(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := jobInput(sc)
	if err != nil {
		return err
	}
	p, snap, err := s.load(in)
	if err != nil {
		return err
	}
	fe := s.host.engine
	project := in.Stack.ProjectName
	network := project + "_default"
	if _, err := fe.InspectNetwork(ctx, network); err != nil {
		fe.AddNetwork(network, map[string]string{protocol.ComposeProjectLabel: project, "com.docker.compose.network": "default"})
	}
	var images []protocol.AppliedImage
	for i, svc := range p.services {
		if len(in.Services) > 0 && !slices.Contains(in.Services, svc.name) {
			continue
		}
		sc.Progress(ctx, 50+45*i/max(1, len(p.services)), "starting "+svc.name)
		name := project + "-" + svc.name + "-1"
		if c, ok := fe.Container(name); ok && (in.ForceRecreate || c.Details.Image != svc.image) {
			_ = fe.RemoveContainer(ctx, c.Details.ID, engine.RemoveOptions{Force: true})
		}
		if _, ok := fe.Container(name); !ok {
			fe.AddImage(svc.image)
			labels := map[string]string{protocol.ComposeProjectLabel: project, protocol.ComposeServiceLabel: svc.name,
				protocol.ComposeWorkingDirLabel: s.host.stacksDir + "/" + in.Stack.Dir, "com.docker.compose.container-number": "1",
				"com.docker.compose.project.config_files": s.host.stacksDir + "/" + in.Stack.Dir + "/compose.yaml",
				lifecycle.DependsOnLabel:                  dependsLabel(svc.deps)}
			restart := svc.restart
			if restart == "" {
				restart = "no"
			}
			spec := engine.ContainerSpec{Name: name, Image: svc.image, NetworkMode: network, Labels: labels,
				RestartPolicy: restart, Ports: svc.ports, NetworkAliases: []string{svc.name}, Mounts: svc.volumes}
			for _, v := range svc.volumes {
				if _, err := fe.InspectVolume(ctx, v.Source); err != nil {
					fe.AddVolume(v.Source, map[string]string{protocol.ComposeProjectLabel: project})
				}
			}
			if svc.healthcheck {
				spec.Healthcheck = &engine.HealthcheckSpec{Test: []string{"CMD", "true"}}
				fe.SetStartHealth(svc.image, "healthy")
			}
			fe.AddContainer(spec, false)
		}
		c, _ := fe.Container(name)
		if !c.Details.State.Running {
			if err := fe.StartContainer(ctx, c.Details.ID); err != nil {
				return err
			}
		}
		img := protocol.AppliedImage{Service: svc.name, Image: svc.image, ImageID: c.Details.ImageID, Build: svc.build}
		if d := fe.ImageDigests(svc.image); len(d) > 0 && !svc.build {
			img.Digest = d[0]
		}
		images = append(images, img)
		if err := pause(ctx); err != nil {
			return err
		}
	}
	after := s.states(ctx, project)
	return updateOutput(ctx, sc, func(o *protocol.StackJobOutput) {
		src := snap
		o.Sources = &src
		o.Services, o.Images, o.Binds, o.Warnings, o.After = p.composeServices(), images, p.binds(), p.warnings(), after
	})
}

func (s *stackSim) down(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := jobInput(sc)
	if err != nil {
		return err
	}
	fe := s.host.engine
	project := in.Stack.ProjectName
	before := s.states(ctx, project)
	if err := updateOutput(ctx, sc, func(o *protocol.StackJobOutput) { o.Before = before }); err != nil {
		return err
	}
	list, _ := lifecycle.ProjectContainers(ctx, fe, project)
	for i, c := range list {
		sc.Progress(ctx, 100*(i+1)/max(1, len(list)), "removing "+strings.TrimPrefix(c.Names[0], "/"))
		if err := fe.RemoveContainer(ctx, c.ID, engine.RemoveOptions{Force: true}); err != nil {
			return err
		}
		if err := pause(ctx); err != nil {
			return err
		}
	}
	_ = fe.RemoveNetwork(ctx, project+"_default")
	after := s.states(ctx, project)
	return updateOutput(ctx, sc, func(o *protocol.StackJobOutput) { o.After = after })
}

// --- Migration preview (#35) ----------------------------------------------------

func (s *stackSim) migrationPreview(ctx context.Context, raw json.RawMessage) (any, error) {
	var in protocol.MigrationPreviewInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	if err := in.Validate(); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	id := s.host.engine.Identity()
	platform := id.OS + "/" + id.Arch
	if in.Role == protocol.RoleSource {
		if in.Source.Stack == nil {
			return nil, &session.HandlerError{Code: protocol.CodeUnsupportedRequest, Message: "the devstack simulates stack migrations only"}
		}
		f, err := s.sourceFacts(ctx, *in.Source.Stack)
		if err != nil {
			return nil, err
		}
		return protocol.MigrationPreviewOutput{Source: &protocol.MigrationSourceFacts{Platform: platform, Project: f}}, nil
	}
	return protocol.MigrationPreviewOutput{Destination: s.destinationFacts(ctx, *in.Destination, platform)}, nil
}

func (s *stackSim) migrationStop(ctx context.Context, raw json.RawMessage) (any, error) {
	var in protocol.MigrationStopInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	before := s.states(ctx, in.Stack.ProjectName)
	list, _ := lifecycle.ProjectContainers(ctx, s.host.engine, in.Stack.ProjectName)
	for _, c := range list {
		if c.State == "running" {
			_ = s.host.engine.StopContainer(ctx, c.ID, nil)
		}
	}
	return protocol.MigrationLifecycleOutput{Before: before, After: s.states(ctx, in.Stack.ProjectName)}, nil
}

func (s *stackSim) migrationStart(ctx context.Context, raw json.RawMessage) (any, error) {
	var in protocol.MigrationStartInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	before := s.states(ctx, in.Stack.ProjectName)
	list, _ := lifecycle.ProjectContainers(ctx, s.host.engine, in.Stack.ProjectName)
	for _, c := range list {
		if slices.Contains(in.Services, c.Labels[lifecycle.ComposeServiceLabel]) && c.State != "running" {
			_ = s.host.engine.StartContainer(ctx, c.ID)
		}
	}
	return protocol.MigrationLifecycleOutput{Before: before, After: s.states(ctx, in.Stack.ProjectName)}, nil
}

func (s *stackSim) migrationRefuse(context.Context, json.RawMessage) (any, error) {
	return nil, &session.HandlerError{Code: protocol.CodeUnsupportedRequest, Message: "the devstack does not simulate migration transfers"}
}

func (s *stackSim) migrationCleanup(context.Context, json.RawMessage) (any, error) {
	return protocol.MigrationCleanupOutput{Removed: []string{}}, nil
}

func (s *stackSim) sourceFacts(ctx context.Context, ref protocol.ProjectRef) (*protocol.MigrationProjectFacts, error) {
	host, local := s.projectDir(ref.Dir)
	snap, ok := readProject(local)
	if !ok {
		return nil, &session.HandlerError{Code: protocol.CodeNotFound, Message: "the project directory does not exist"}
	}
	sp, _ := parseCompose(host, ref.ProjectName, fileContent(snap.Files, "compose.yaml"))
	fe := s.host.engine
	var dirBytes int64
	for _, f := range snap.Files {
		dirBytes += f.Size
	}
	facts := &protocol.MigrationProjectFacts{Name: ref.ProjectName, Dir: host, Binds: sp.binds(),
		DirBytes: dirBytes + 48<<10, DirEntries: int64(len(snap.Files)) + 3, Warnings: sp.warnings(),
		Services: []protocol.MigrationServiceFacts{}, Volumes: []protocol.MigrationVolumeFacts{},
		Networks: []protocol.MigrationNetworkFacts{{Key: "default", Name: ref.ProjectName + "_default"}}}
	for _, svc := range sp.services {
		name := ref.ProjectName + "-" + svc.name + "-1"
		sf := protocol.MigrationServiceFacts{Name: svc.name, Image: svc.image, Build: svc.build, ContainerNames: []string{name}}
		if c, ok := fe.Container(name); ok {
			sf.Running = c.Details.State.Running
			sf.ImageID = c.Details.ImageID
		}
		if d, err := fe.InspectImage(ctx, svc.image); err == nil {
			sf.ImageID, sf.ImagePlatform, sf.ImageSize, sf.RepoDigests = d.ID, "linux/"+s.host.engine.Identity().Arch, 180<<20, d.RepoDigests
		}
		for _, b := range svc.ports {
			proto := b.Protocol
			if proto == "" {
				proto = "tcp"
			}
			sf.Ports = append(sf.Ports, protocol.MigrationPort{HostIP: b.HostIP, Published: b.HostPort, Target: b.ContainerPort, Protocol: proto})
		}
		facts.Services = append(facts.Services, sf)
		for _, v := range svc.volumes {
			var users []protocol.MigrationContainerUse
			if c, ok := fe.Container(name); ok {
				users = append(users, protocol.MigrationContainerUse{Name: name, Running: c.Details.State.Running, Project: ref.ProjectName})
			}
			facts.Volumes = append(facts.Volumes, protocol.MigrationVolumeFacts{Key: strings.TrimPrefix(v.Source, ref.ProjectName+"_"),
				Name: v.Source, Driver: "local", Exists: true, Supported: true, Bytes: 1536 << 20, Entries: 4210, UsedBy: users})
		}
	}
	return facts, nil
}

func (s *stackSim) destinationFacts(ctx context.Context, q protocol.MigrationDestinationQuery, platform string) *protocol.MigrationDestinationFacts {
	fe := s.host.engine
	free := s.host.diskTotal - s.host.diskUsed
	out := &protocol.MigrationDestinationFacts{Platform: platform, StacksOK: true, VolumesOK: true, StacksFree: free, VolumesFree: free}
	if q.Dir != "" {
		_, local := s.projectDir(q.Dir)
		_, err := os.Stat(local)
		out.DirExists = err == nil
	}
	cs, _ := fe.ListContainers(ctx, engine.ContainerFilter{All: true})
	for _, c := range cs {
		n := strings.TrimPrefix(c.Names[0], "/")
		if c.Labels[protocol.ComposeProjectLabel] == q.ProjectName && q.ProjectName != "" {
			out.ProjectContainers = append(out.ProjectContainers, n)
		}
		if slices.Contains(q.ContainerNames, n) {
			out.Containers = append(out.Containers, n)
		}
		if c.State != "running" {
			continue
		}
		for _, p := range c.Ports {
			for _, want := range q.Ports {
				if want.Published != 0 && p.PublicPort == want.Published {
					out.PortConflicts = append(out.PortConflicts, protocol.MigrationPortConflict{Port: want, Container: n})
				}
			}
		}
	}
	vols, _ := fe.ListVolumes(ctx)
	for _, v := range vols {
		if slices.Contains(q.Volumes, v.Name) {
			out.Volumes = append(out.Volumes, protocol.MigrationExistingVolume{Name: v.Name})
		}
	}
	for _, n := range q.Networks {
		if _, err := fe.InspectNetwork(ctx, n); err == nil {
			out.Networks = append(out.Networks, n)
		}
	}
	for _, n := range q.ExternalNetworks {
		if _, err := fe.InspectNetwork(ctx, n); err != nil {
			out.MissingNetworks = append(out.MissingNetworks, n)
		}
	}
	for _, n := range q.ExternalVolumes {
		if _, err := fe.InspectVolume(ctx, n); err != nil {
			out.MissingVolumes = append(out.MissingVolumes, n)
		}
	}
	for _, img := range q.Images {
		if _, err := fe.InspectImage(ctx, img); err == nil {
			out.ImagesPresent = append(out.ImagesPresent, img)
		}
	}
	return out
}
