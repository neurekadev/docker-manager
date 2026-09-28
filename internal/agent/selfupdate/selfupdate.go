// Package selfupdate lets Docker Manager redeploy and update its own Compose
// project (#32). The agent cannot recreate the container it runs in:
// Compose would stop it in the middle of the job. The stack executors
// therefore converge every other service themselves and schedule the
// agent's own service (and services depending on it) here. Once the job
// has finished and its outcome is journaled, the Launcher starts a
// short-lived helper container from the agent's own image with the agent
// container's mounts (Docker socket, stacks, state volume). The helper
// (`docker-agent self-update <plan>`, Run) waits a few seconds so the job's
// result reaches the manager, then runs Compose up for those services from
// exactly the definition bytes the job used. The new agent starts with the
// same state volume, reconnects and logs the helper's result (Collect).
//
// Stopping, restarting and removing Docker Manager stay refused
// (internal/protection): nothing here takes Docker Manager down.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

const (
	// Subcommand is the docker-agent command the helper container runs.
	Subcommand = "self-update"
	// RoleHelper is the protocol.LabelRole value of helper containers.
	RoleHelper = protocol.RoleSelfUpdate
	// DefaultGrace is how long the helper waits before replacing the agent,
	// so the finished job's result reaches the manager first (the journal
	// replays it after the restart otherwise).
	DefaultGrace = 5 * time.Second
	// dirName is the directory below the agent state directory holding
	// plans (read and removed by the helper) and results (read and removed
	// by the next agent).
	dirName = "self-update"
)

// Plan is what a helper applies: the project as the job loaded it and the
// services to converge.
type Plan struct {
	JobID string `json:"jobId"`
	// AgentContainerID is the container being replaced; the helper starts
	// it again if Compose fails before a new agent runs.
	AgentContainerID string   `json:"agentContainerId"`
	ProjectName      string   `json:"projectName"`
	Dir              string   `json:"dir"`
	ConfigFiles      []string `json:"configFiles,omitempty"`
	EnvFiles         []string `json:"envFiles,omitempty"`
	Profiles         []string `json:"profiles,omitempty"`
	// Content holds the definition files the job loaded (Dir-relative).
	Content  map[string][]byte `json:"content"`
	Services []string          `json:"services"`
	// ForceRecreate recreates the services even when unchanged (a
	// redeploy with force recreate).
	ForceRecreate      bool `json:"forceRecreate,omitempty"`
	StopTimeoutSeconds int  `json:"stopTimeoutSeconds,omitempty"`
}

// Result is what a helper leaves for the next agent to log.
type Result struct {
	JobID      string    `json:"jobId"`
	Services   []string  `json:"services"`
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finishedAt"`
}

// Options configures a Launcher.
type Options struct {
	// StateDir is the agent state directory. The helper sees it at the same
	// path (it gets the agent container's mounts).
	StateDir string
	// SelfContainerID is the agent's own container ("" outside a container:
	// nothing is handed over).
	SelfContainerID string
	// Engine returns the connected Engine (nil while disconnected).
	Engine func() engine.Engine
	// DockerHost is the agent's DOCKER_HOST, passed on to the helper.
	DockerHost string
	Logger     *slog.Logger
}

// Launcher tells the stack executors which service is the agent's own and
// starts helper containers after their jobs.
type Launcher struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	pending map[string]Plan
}

// New returns a Launcher.
func New(opts Options) *Launcher {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Launcher{opts: opts, log: opts.Logger.With("component", "selfupdate"), pending: map[string]Plan{}}
}

// OwnService returns the Compose service of the agent's own container when
// the container belongs to project.
func (l *Launcher) OwnService(ctx context.Context, project string) (string, bool, error) {
	if l == nil || l.opts.SelfContainerID == "" || l.opts.Engine == nil {
		return "", false, nil
	}
	eng := l.opts.Engine()
	if eng == nil {
		return "", false, errors.New("the Docker Engine is not connected")
	}
	d, err := eng.InspectContainer(ctx, l.opts.SelfContainerID)
	if err != nil {
		return "", false, err
	}
	svc := d.Labels[lifecycle.ComposeServiceLabel]
	if d.Labels[lifecycle.ComposeProjectLabel] != project || svc == "" {
		return "", false, nil
	}
	return svc, true, nil
}

// Schedule records a plan; its helper starts when the job has finished
// successfully (JobFinished).
func (l *Launcher) Schedule(p Plan) {
	p.AgentContainerID = l.opts.SelfContainerID
	l.mu.Lock()
	l.pending[p.JobID] = p
	l.mu.Unlock()
}

// JobFinished starts the helper of a job that succeeded; any other outcome
// drops the plan (nothing of the agent's own service changes).
func (l *Launcher) JobFinished(ctx context.Context, jobID string, succeeded bool) {
	l.mu.Lock()
	p, ok := l.pending[jobID]
	delete(l.pending, jobID)
	l.mu.Unlock()
	if !ok {
		return
	}
	if !succeeded {
		l.log.Info("self-update dropped: the job did not succeed", "job_id", jobID)
		return
	}
	if err := l.launch(ctx, p); err != nil {
		l.log.Error("could not start the self-update helper; redeploy the stack to retry", "job_id", jobID, "error", err)
	}
}

func (l *Launcher) launch(ctx context.Context, p Plan) error {
	eng := l.opts.Engine()
	if eng == nil {
		return errors.New("the Docker Engine is not connected")
	}
	self, err := eng.InspectContainer(ctx, l.opts.SelfContainerID)
	if err != nil {
		return err
	}
	dir := filepath.Join(l.opts.StateDir, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, p.JobID+".json")
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := writeFile(path, b); err != nil {
		return err
	}
	var env []string
	if l.opts.DockerHost != "" {
		env = append(env, "DOCKER_HOST="+l.opts.DockerHost)
	}
	image := self.ImageID
	if image == "" {
		image = self.Image
	}
	spec := engine.ContainerSpec{
		Name:        HelperName(p.JobID),
		Image:       image,
		Cmd:         []string{Subcommand, path},
		Env:         env,
		Labels:      map[string]string{protocol.LabelRole: RoleHelper},
		Mounts:      Mounts(self.Mounts),
		NetworkMode: self.NetworkMode,
		// The image's health check watches the agent's health file.
		Healthcheck:   &engine.HealthcheckSpec{Test: []string{"NONE"}},
		RestartPolicy: "no",
		AutoRemove:    true,
	}
	id, _, err := eng.CreateContainer(ctx, spec)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := eng.StartContainer(ctx, id); err != nil {
		_ = os.Remove(path)
		_ = eng.RemoveContainer(ctx, id, engine.RemoveOptions{Force: true})
		return err
	}
	l.log.Info("started the self-update helper", "job_id", p.JobID, "helper_container_id", id, "services", p.Services)
	return nil
}

// HelperName is the helper container's name for a job.
func HelperName(jobID string) string {
	return "docker-agent-self-update-" + strings.ReplaceAll(jobID, "-", "")
}

// Mounts are the agent container's bind and volume mounts at the same
// destinations (Docker socket, stacks, state).
func Mounts(ms []engine.Mount) []engine.MountSpec {
	var out []engine.MountSpec
	for _, m := range ms {
		switch m.Type {
		case "bind":
			out = append(out, engine.MountSpec{Type: "bind", Source: m.Source, Target: m.Destination, ReadOnly: !m.ReadWrite})
		case "volume":
			if m.Name != "" {
				out = append(out, engine.MountSpec{Type: "volume", Source: m.Name, Target: m.Destination, ReadOnly: !m.ReadWrite})
			}
		}
	}
	return out
}

// Handoff splits the requested services (all when empty) of a project into
// those the job converges itself and those the helper converges: the
// agent's own service and every service depending on it (Compose would
// otherwise recreate the agent as a dependency).
func Handoff(p *compose.Project, own string, requested []string) (rest, handoff []string) {
	deps := map[string][]string{}
	for _, s := range p.Services {
		for _, d := range s.DependsOn {
			deps[s.Name] = append(deps[s.Name], d.Service)
		}
	}
	memo := map[string]bool{}
	var reaches func(svc string, seen map[string]bool) bool
	reaches = func(svc string, seen map[string]bool) bool {
		if svc == own {
			return true
		}
		if v, ok := memo[svc]; ok {
			return v
		}
		if seen[svc] {
			return false
		}
		seen[svc] = true
		v := slices.ContainsFunc(deps[svc], func(d string) bool { return reaches(d, seen) })
		memo[svc] = v
		return v
	}
	names := requested
	if len(names) == 0 {
		for _, s := range p.Services {
			names = append(names, s.Name)
		}
	}
	for _, n := range names {
		if reaches(n, map[string]bool{}) {
			handoff = append(handoff, n)
		} else {
			rest = append(rest, n)
		}
	}
	return rest, handoff
}

// HasService reports whether the project defines svc.
func HasService(p *compose.Project, svc string) bool {
	return slices.ContainsFunc(p.Services, func(s compose.ServiceInfo) bool { return s.Name == svc })
}

// RunOptions configures the helper.
type RunOptions struct {
	// PlanPath is the plan file the agent wrote.
	PlanPath string
	// DockerHost is the Engine endpoint (DOCKER_HOST).
	DockerHost string
	Grace      time.Duration
	Clock      clock.Clock
	Logger     *slog.Logger
}

// Run is the helper: it reads and removes the plan, waits the grace period,
// runs Compose up for the plan's services and records the result next to
// the plan for the next agent.
func Run(ctx context.Context, o RunOptions) error {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	b, err := os.ReadFile(o.PlanPath)
	if err != nil {
		return err
	}
	_ = os.Remove(o.PlanPath) // it holds the definition's env files
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("malformed self-update plan: %w", err)
	}
	res := Result{JobID: p.JobID, Services: p.Services}
	err = apply(ctx, o, p)
	if err != nil {
		res.Error = err.Error()
	}
	res.FinishedAt = o.Clock.Now().UTC()
	if rb, merr := json.Marshal(res); merr == nil {
		_ = writeFile(strings.TrimSuffix(o.PlanPath, ".json")+".result.json", rb)
	}
	return err
}

func apply(ctx context.Context, o RunOptions, p Plan) error {
	t := o.Clock.NewTimer(o.Grace)
	select {
	case <-ctx.Done():
		t.Stop()
		return ctx.Err()
	case <-t.C():
	}
	eng, err := engine.Connect(ctx, engine.Options{Host: o.DockerHost, Logger: o.Logger})
	if err != nil {
		return err
	}
	defer func() { _ = eng.Close() }()
	c, err := compose.New(ctx, compose.Options{Host: o.DockerHost, Engine: eng, Logger: o.Logger})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	proj, err := c.Load(ctx, compose.ProjectSpec{Name: p.ProjectName, Dir: p.Dir, ConfigFiles: p.ConfigFiles,
		EnvFiles: p.EnvFiles, Profiles: p.Profiles, Content: p.Content})
	if err != nil {
		return err
	}
	var timeout *time.Duration
	if p.StopTimeoutSeconds > 0 {
		d := time.Duration(p.StopTimeoutSeconds) * time.Second
		timeout = &d
	}
	o.Logger.Info("recreating the agent's own services", "job_id", p.JobID, "project", p.ProjectName, "services", p.Services)
	upErr := c.Up(ctx, proj, compose.UpOptions{Services: p.Services, ForceRecreate: p.ForceRecreate,
		KeepDependencies: true, StopTimeout: timeout})
	if upErr != nil {
		o.Logger.Error("self-update failed; restarting the previous agent", "job_id", p.JobID, "error", upErr)
		restore(ctx, eng, p)
	}
	return upErr
}

// restore starts the previous agent container again when Compose stopped
// it and no container of the agent's service runs.
func restore(ctx context.Context, eng engine.Engine, p Plan) {
	if p.AgentContainerID == "" {
		return
	}
	list, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true,
		Labels: []string{lifecycle.ComposeProjectLabel + "=" + p.ProjectName}})
	if err != nil {
		return
	}
	for _, c := range list {
		if c.State == "running" && slices.Contains(p.Services, c.Labels[lifecycle.ComposeServiceLabel]) && c.ID != p.AgentContainerID {
			return // a new agent runs
		}
	}
	_ = eng.StartContainer(ctx, p.AgentContainerID)
}

// Collect logs and removes the results helpers left behind (called by the
// agent at startup).
func Collect(stateDir string, log *slog.Logger) {
	dir := filepath.Join(stateDir, dirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if !strings.HasSuffix(e.Name(), ".result.json") {
			continue
		}
		b, err := os.ReadFile(path)
		_ = os.Remove(path)
		var r Result
		if err != nil || json.Unmarshal(b, &r) != nil {
			continue
		}
		if r.Error != "" {
			log.Error("the last self-update failed; the previous agent was started again", "job_id", r.JobID,
				"services", r.Services, "error", r.Error)
			continue
		}
		log.Info("self-update finished", "job_id", r.JobID, "services", r.Services, "finished_at", r.FinishedAt)
	}
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
