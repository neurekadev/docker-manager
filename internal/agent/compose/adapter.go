// Package compose is the agent's adapter over the official Docker Compose
// Go SDK (github.com/docker/compose/v5, #2, #7, #21). Together with
// internal/agent/engine it is the only code that talks to Docker Engine.
//
// Every operation builds a fresh Compose service over an in-memory
// docker/cli (cli.go) that holds only that operation's registry
// credentials: nothing is read from or written to a Docker config, no
// credential helper, Docker CLI or CLI plugin is executed (#19).
//
// Images for services with a build section are built by Docker Manager through
// the Engine's BuildKit (engine.Client.Build) before the SDK runs, because
// the SDK can only use BuildKit through the buildx CLI plugin and otherwise
// falls back to the deprecated legacy builder (#33).
//
// Project environments come from the project's env files only; the agent's
// own environment (which holds its enrollment token) is never used for
// interpolation.
package compose

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	composesdk "github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/client"
	"github.com/sirupsen/logrus"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
)

// Options configures an Adapter.
type Options struct {
	// Host is the Engine endpoint (DOCKER_HOST) the SDK's Moby client uses.
	Host string
	// Engine builds images for build sections and reports the Engine platform.
	Engine engine.Engine
	Logger *slog.Logger
	// MaxConcurrency limits parallel Engine calls per operation (0 = SDK default).
	MaxConcurrency int
	// Clock paces polling (default clock.Real()).
	Clock clock.Clock
	// Guard, when set, must accept a project directory before it is loaded
	// or deployed: the agent passes the #28 storage check, so no deploy runs
	// from a directory the Engine would see at a different path.
	Guard func(dir string) error
}

// Adapter runs Compose operations.
type Adapter struct {
	opts  Options
	api   *client.Client
	log   *slog.Logger
	clock clock.Clock
}

var logrusOnce sync.Once

// New creates an adapter with its own negotiated Moby client for the SDK.
func New(ctx context.Context, opts Options) (*Adapter, error) {
	const op = "compose.connect"
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Engine == nil {
		return nil, engine.Errorf(op, engine.CodeInvalidArgument, "the Compose adapter needs the Engine adapter")
	}
	logrusOnce.Do(func() { routeLogrus(opts.Logger) })
	// The docker/cli config store would read credentials from this
	// variable; Docker Manager passes credentials per operation only.
	if os.Getenv("DOCKER_AUTH_CONFIG") != "" {
		opts.Logger.Warn("ignoring DOCKER_AUTH_CONFIG: registry credentials come from the manager per operation")
		_ = os.Unsetenv("DOCKER_AUTH_CONFIG")
	}
	api, err := client.New(client.WithHost(opts.Host), client.WithUserAgent("docker-agent/"+buildinfo.Get().Version+" compose-sdk"))
	if err != nil {
		return nil, engine.Errorf(op, engine.CodeInvalidArgument, "invalid Engine host: %v", err)
	}
	if _, err := api.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		_ = api.Close()
		return nil, engine.Wrap(op, err)
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	return &Adapter{opts: opts, api: api, log: opts.Logger, clock: opts.Clock}, nil
}

// Close releases the SDK client's connections.
func (a *Adapter) Close() error { return a.api.Close() }

// Event is a Compose progress event (a container, network, volume or image
// being created, started, pulled, ...).
type Event struct {
	// Resource is e.g. "Container app-web-1", "Network app_default".
	Resource string
	// Status is "working", "done", "warning" or "error".
	Status  string
	Text    string
	Details string
}

// RunOptions are common to all operations.
type RunOptions struct {
	// Auth holds the registry credentials of this operation (#19).
	Auth []engine.RegistryAuth
	// Events receives progress (may be nil); calls are serialized.
	Events func(Event)
	// Output receives the SDK's textual output (may be nil).
	Output io.Writer
}

// service builds the per-operation Compose service.
func (a *Adapter) service(o RunOptions) (api.Compose, error) {
	out := o.Output
	if out == nil {
		out = io.Discard
	}
	cli := newMemoryCLI(a.api, a.opts.Host, a.opts.Engine.Identity().OS, o.Auth, out)
	sdkOpts := []composesdk.Option{
		composesdk.WithStreams(out, out, strings.NewReader("")),
		// Never confirm destructive prompts (e.g. recreating a volume whose
		// configuration changed): data is never removed implicitly.
		composesdk.WithPrompt(func(string, bool) (bool, error) { return false, nil }),
		composesdk.WithEventProcessor(&eventSink{fn: o.Events}),
		composesdk.WithProxyConfig(map[string]string{}),
	}
	if a.opts.MaxConcurrency > 0 {
		sdkOpts = append(sdkOpts, composesdk.WithMaxConcurrency(a.opts.MaxConcurrency))
	}
	return composesdk.NewComposeService(cli, sdkOpts...)
}

// UpOptions configures Up.
type UpOptions struct {
	RunOptions
	// Services narrows the operation to these services and their
	// dependencies (empty = all).
	Services []string
	// Build rebuilds every service with a build section; otherwise only
	// images that are missing locally are built.
	Build   bool
	NoCache bool
	// PullBase pulls newer base images when building.
	PullBase bool
	// ForceRecreate recreates containers even when unchanged.
	ForceRecreate bool
	// RenewAnonymousVolumes gives recreated containers new, empty anonymous
	// volumes (docker compose up --renew-anon-volumes). By default a
	// recreated container inherits its predecessor's anonymous volumes,
	// like the Compose CLI; the SDK's zero value would drop them.
	RenewAnonymousVolumes bool
	RemoveOrphans         bool
	// Wait waits until services are running/healthy (bounded by WaitTimeout).
	Wait        bool
	WaitTimeout time.Duration
	// StopTimeout overrides the stop grace period when recreating.
	StopTimeout *time.Duration
}

// Up creates and starts the project, honoring depends_on order and
// conditions (service_started, service_healthy,
// service_completed_successfully, required: false) and restart: true
// propagation when a dependency is recreated.
func (a *Adapter) Up(ctx context.Context, p *Project, o UpOptions) error {
	const op = "compose.up"
	if err := a.guard(op, p); err != nil {
		return err
	}
	model, err := selected(p, o.Services)
	if err != nil {
		return engine.WrapCode(op, engine.CodeInvalidArgument, err)
	}
	if err := a.buildImages(ctx, model, buildRequest{all: o.Build, noCache: o.NoCache, pull: o.PullBase, auth: o.Auth, events: o.Events}); err != nil {
		return err
	}
	svc, err := a.service(o.RunOptions)
	if err != nil {
		return engine.Wrap(op, err)
	}
	recreate := api.RecreateDiverged
	if o.ForceRecreate {
		recreate = api.RecreateForce
	}
	err = svc.Up(ctx, model, api.UpOptions{
		Create: api.CreateOptions{
			Services:             o.Services,
			RemoveOrphans:        o.RemoveOrphans,
			Recreate:             recreate,
			RecreateDependencies: api.RecreateDiverged,
			// The CLI's default (Inherit: !renewAnonVolumes); the SDK's
			// zero value would give every recreated container empty
			// anonymous volumes and lose their data.
			Inherit:   !o.RenewAnonymousVolumes,
			Timeout:   o.StopTimeout,
			QuietPull: true,
		},
		Start: api.StartOptions{Project: model, Services: o.Services, Wait: o.Wait, WaitTimeout: o.WaitTimeout},
	})
	return composeError(op, err)
}

// CreateOptions configures Create.
type CreateOptions struct {
	RunOptions
	// Services are the services to converge; their dependencies are part
	// of the model but never recreated.
	Services []string
	// StopTimeout overrides the stop grace period of replaced containers.
	StopTimeout *time.Duration
}

// Create converges the services' containers with the loaded definition
// without starting anything: a container whose configuration or image
// changed (the image ID label differs after its tag moved, #20) is
// replaced by a new, created container; anonymous volumes are inherited
// like Compose's up does. Dependencies are never recreated and nothing is
// built. Callers start the services afterwards (internal/agent/lifecycle).
func (a *Adapter) Create(ctx context.Context, p *Project, o CreateOptions) error {
	const op = "compose.create"
	if err := a.guard(op, p); err != nil {
		return err
	}
	if len(o.Services) == 0 {
		return engine.Errorf(op, engine.CodeInvalidArgument, "no services to create")
	}
	model, err := selected(p, o.Services)
	if err != nil {
		return engine.WrapCode(op, engine.CodeInvalidArgument, err)
	}
	svc, err := a.service(o.RunOptions)
	if err != nil {
		return engine.Wrap(op, err)
	}
	return composeError(op, svc.Create(ctx, model, api.CreateOptions{
		Services:             o.Services,
		Recreate:             api.RecreateDiverged,
		RecreateDependencies: api.RecreateNever,
		Inherit:              true,
		Timeout:              o.StopTimeout,
		QuietPull:            true,
	}))
}

// DownOptions configures Down.
type DownOptions struct {
	RunOptions
	// Volumes also removes named volumes declared by the project and
	// anonymous volumes.
	Volumes       bool
	RemoveOrphans bool
	Timeout       *time.Duration
}

// Down stops and removes the project's containers and networks (in reverse
// dependency order). p may be nil to act on the project name alone.
func (a *Adapter) Down(ctx context.Context, name string, p *Project, o DownOptions) error {
	svc, err := a.service(o.RunOptions)
	if err != nil {
		return engine.Wrap("compose.down", err)
	}
	return composeError("compose.down", svc.Down(ctx, name, api.DownOptions{
		Project: modelOf(p), Volumes: o.Volumes, RemoveOrphans: o.RemoveOrphans, Timeout: o.Timeout,
	}))
}

// Start starts the project's existing containers in dependency order.
func (a *Adapter) Start(ctx context.Context, p *Project, o RunOptions) error {
	if err := a.guard("compose.start", p); err != nil {
		return err
	}
	svc, err := a.service(o)
	if err != nil {
		return engine.Wrap("compose.start", err)
	}
	return composeError("compose.start", svc.Start(ctx, p.Name, api.StartOptions{Project: modelOf(p)}))
}

// Stop stops the project's containers in reverse dependency order.
func (a *Adapter) Stop(ctx context.Context, p *Project, services []string, timeout *time.Duration, o RunOptions) error {
	svc, err := a.service(o)
	if err != nil {
		return engine.Wrap("compose.stop", err)
	}
	model, err := selected(p, services)
	if err != nil {
		return engine.WrapCode("compose.stop", engine.CodeInvalidArgument, err)
	}
	if err := svc.Stop(ctx, p.Name, api.StopOptions{Project: model, Services: services, Timeout: timeout}); err != nil {
		return composeError("compose.stop", err)
	}
	return a.awaitStopped(ctx, p.Name, services)
}

// Container-list settling after a stop (see awaitStopped).
const (
	listSettleTimeout = 30 * time.Second
	listSettlePoll    = 100 * time.Millisecond
)

// awaitStopped returns once the Engine's container list no longer reports
// the stopped services as running. Engines before 26 can return from a
// container stop while their container list still shows the container as
// running; the SDK's next start would then skip it and fail waiting for its
// dependencies (docs/support-matrix.md). Waiting here keeps stop followed by
// start (e.g. backup shutdown and resume, #10) correct on every Engine.
func (a *Adapter) awaitStopped(ctx context.Context, project string, services []string) error {
	const op = "compose.stop"
	deadline := a.clock.NewTimer(listSettleTimeout)
	defer deadline.Stop()
	for {
		list, err := a.opts.Engine.ListContainers(ctx, engine.ContainerFilter{All: true, Labels: []string{api.ProjectLabel + "=" + project}})
		if err != nil {
			return err
		}
		running := false
		for _, c := range list {
			if c.State == "running" && c.Labels[api.OneoffLabel] != "True" &&
				(len(services) == 0 || slices.Contains(services, c.Labels[api.ServiceLabel])) {
				running = true
			}
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return engine.Wrap(op, ctx.Err())
		case <-deadline.C():
			return engine.Errorf(op, engine.CodeTimeout, "containers of %s still listed as running %s after stop", project, listSettleTimeout)
		case <-a.clock.After(listSettlePoll):
		}
	}
}

// Restart restarts services (all when empty); dependents declared with
// restart: true are restarted too.
func (a *Adapter) Restart(ctx context.Context, p *Project, services []string, timeout *time.Duration, o RunOptions) error {
	if err := a.guard("compose.restart", p); err != nil {
		return err
	}
	svc, err := a.service(o)
	if err != nil {
		return engine.Wrap("compose.restart", err)
	}
	return composeError("compose.restart", svc.Restart(ctx, p.Name, api.RestartOptions{Project: modelOf(p), Services: services, Timeout: timeout}))
}

// Pull pulls the images of the project's services (services with a build
// section are skipped), with the operation's registry credentials.
func (a *Adapter) Pull(ctx context.Context, p *Project, o RunOptions) error {
	svc, err := a.service(o)
	if err != nil {
		return engine.Wrap("compose.pull", err)
	}
	return composeError("compose.pull", svc.Pull(ctx, modelOf(p), api.PullOptions{Quiet: true, IgnoreBuildable: true}))
}

// ContainerStatus is a container of a project.
type ContainerStatus struct {
	ID       string
	Name     string
	Service  string
	Image    string
	State    string
	Health   string
	ExitCode int
}

// Ps lists the project's containers (all states).
func (a *Adapter) Ps(ctx context.Context, name string) ([]ContainerStatus, error) {
	svc, err := a.service(RunOptions{})
	if err != nil {
		return nil, engine.Wrap("compose.ps", err)
	}
	list, err := svc.Ps(ctx, name, api.PsOptions{All: true})
	if err != nil {
		return nil, composeError("compose.ps", err)
	}
	out := make([]ContainerStatus, 0, len(list))
	for _, c := range list {
		out = append(out, ContainerStatus{
			ID: c.ID, Name: c.Name, Service: c.Service, Image: c.Image,
			State: string(c.State), Health: string(c.Health), ExitCode: c.ExitCode,
		})
	}
	return out, nil
}

func modelOf(p *Project) *types.Project {
	if p == nil {
		return nil
	}
	return p.model
}

// CodeStorageUnverified is used when Guard refuses without a specific code.
const CodeStorageUnverified engine.Code = "storage_unverified"

// guard applies Options.Guard to a loaded project.
func (a *Adapter) guard(op string, p *Project) error {
	if p == nil {
		return engine.WrapCode(op, engine.CodeInvalidArgument, errNotLoaded)
	}
	return a.guardDir(op, p.Dir)
}

func (a *Adapter) guardDir(op, dir string) error {
	if a.opts.Guard == nil {
		return nil
	}
	err := a.opts.Guard(dir)
	if err == nil {
		return nil
	}
	code := CodeStorageUnverified
	var c interface{ DiagnosticCode() string }
	if errors.As(err, &c) {
		code = engine.Code(c.DiagnosticCode())
	}
	return engine.WrapCode(op, code, err)
}

// selected narrows the project to services and their dependencies.
func selected(p *Project, services []string) (*types.Project, error) {
	if p == nil || p.model == nil {
		return nil, errNotLoaded
	}
	// Operations adjust services (e.g. the pull policy of images built by
	// the adapter); they work on a deep copy so the loaded project stays as
	// the user wrote it.
	model, err := p.model.WithServicesTransform(func(_ string, s types.ServiceConfig) (types.ServiceConfig, error) { return s, nil })
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return model, nil
	}
	return model.WithSelectedServices(services)
}

// composeError maps SDK errors: dependency failures (unhealthy dependency,
// failed one-shot) get CodeDependencyFailed; the rest go through the Engine
// error model.
func composeError(op string, err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "dependency failed to start"),
		strings.Contains(msg, "didn't complete successfully"),
		strings.Contains(msg, "is unhealthy"):
		return engine.WrapCode(op, engine.CodeDependencyFailed, err)
	}
	return engine.Wrap(op, err)
}

// eventSink forwards SDK progress events.
type eventSink struct {
	mu sync.Mutex // the SDK reports from concurrent goroutines
	fn func(Event)
}

func (e *eventSink) Start(context.Context, string) {}
func (e *eventSink) Done(string, bool)             {}
func (e *eventSink) On(events ...api.Resource) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fn == nil {
		return
	}
	for _, r := range events {
		status := "working"
		switch r.Status {
		case api.Done:
			status = "done"
		case api.Warning:
			status = "warning"
		case api.Error:
			status = "error"
		}
		e.fn(Event{Resource: r.ID, Status: status, Text: r.Text, Details: r.Details})
	}
}

// routeLogrus sends the SDK's logrus output to slog at debug level (the
// SDK logs warnings such as unset variables through the logrus global).
func routeLogrus(log *slog.Logger) {
	logrus.SetOutput(io.Discard)
	logrus.AddHook(&logrusHook{log: log})
}

type logrusHook struct{ log *slog.Logger }

func (h *logrusHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *logrusHook) Fire(e *logrus.Entry) error {
	h.log.Debug("compose sdk", "level", e.Level.String(), "msg", e.Message)
	return nil
}
