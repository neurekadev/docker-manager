package resources

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/agent/protect"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

const stacksRoot = "/var/lib/docker/volumes/docker-manager_stacks/_data"

// fixture: a fake Engine with a Docker Manager-managed stack ("shop": web + db
// in the stacks root, with its volume and network), an unmanaged Compose
// project ("legacy") and a standalone container.
func fixture(t *testing.T) (*Service, *enginefake.Engine) {
	t.Helper()
	fe := enginefake.New("ENGINE-A")
	fe.AddImage("nginx:1.27")
	fe.AddImage("postgres:17")
	fe.AddVolume("shop_data", map[string]string{protocol.ComposeProjectLabel: "shop"})
	fe.AddNetwork("shop_default", map[string]string{protocol.ComposeProjectLabel: "shop"})
	compose := func(project, service, dir string) map[string]string {
		return map[string]string{protocol.ComposeProjectLabel: project, protocol.ComposeServiceLabel: service, protocol.ComposeWorkingDirLabel: dir}
	}
	fe.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27", NetworkMode: "shop_default",
		Labels: compose("shop", "web", stacksRoot+"/shop")}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "shop-db-1", Image: "postgres:17", NetworkMode: "shop_default",
		Labels: compose("shop", "db", stacksRoot+"/shop"), Mounts: []engine.MountSpec{{Type: "volume", Source: "shop_data", Target: "/var/lib/postgresql/data"}}}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "legacy-app-1", Image: "nginx:1.27", Labels: compose("legacy", "app", "/srv/legacy")}, false)
	fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27", Ports: []engine.PortBinding{{ContainerPort: 80, HostPort: 8080}},
		Mounts: []engine.MountSpec{{Type: "bind", Source: "/srv/www", Target: "/usr/share/nginx/html", ReadOnly: true}}}, true)
	s := New(Options{Engine: func() engine.Engine { return fe }, Logger: testutil.Logger(t),
		ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacksRoot+"/") }})
	return s, fe
}

func call[O any](t *testing.T, s *Service, name string, input any) (O, error) {
	t.Helper()
	var out O
	raw, _ := json.Marshal(input)
	res, err := s.Requests()[name](testutil.Context(t), raw)
	if err != nil {
		return out, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func must[O any](t *testing.T, s *Service, name string, input any) O {
	t.Helper()
	out, err := call[O](t, s, name, input)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func handlerCode(err error) string {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he.Code
	}
	return "not a handler error: " + err.Error()
}

// TestRequestsReportInventory: the read requests report containers with
// their Compose stack (managed when in a verified stack root), images with
// their users, volumes and networks with users, stack and builtin flags;
// bind sources are shown, volume host paths never are.
func TestRequestsReportInventory(t *testing.T) {
	s, fe := fixture(t)
	list := must[protocol.ContainerListOutput](t, s, protocol.ReqContainerList, protocol.ContainerListInput{})
	byName := map[string]protocol.ContainerSummary{}
	for _, c := range list.Containers {
		byName[c.Name] = c
	}
	if len(byName) != 4 || byName["shop-web-1"].Stack == nil || !byName["shop-web-1"].Stack.Managed || byName["shop-web-1"].Stack.Service != "web" ||
		byName["legacy-app-1"].Stack == nil || byName["legacy-app-1"].Stack.Managed || byName["web"].Stack != nil || byName["web"].State != "running" {
		t.Fatalf("containers %+v", list.Containers)
	}
	// Running containers carry their start time and addresses; stopped
	// ones keep their networks without addresses and have no start time.
	if db := byName["shop-db-1"]; db.StartedAt == nil || len(db.NetworkList) != 1 || db.NetworkList[0].Name != "shop_default" ||
		db.NetworkList[0].IPAddress == "" || !slices.Equal(db.Networks, []string{"shop_default"}) {
		t.Fatalf("running container %+v", db)
	}
	if legacy := byName["legacy-app-1"]; legacy.StartedAt != nil || len(legacy.NetworkList) != 1 || legacy.NetworkList[0].IPAddress != "" {
		t.Fatalf("stopped container %+v", legacy)
	}
	if m := byName["web"].Mounts; len(m) != 1 || m[0].Source != "/srv/www" || !m[0].ReadOnly {
		t.Fatalf("bind mount %+v", m)
	}
	if m := byName["shop-db-1"].Mounts; len(m) != 1 || m[0].Name != "shop_data" || m[0].Source != "" {
		t.Fatalf("volume mount must not expose the host path: %+v", m)
	}

	d := must[protocol.ContainerDetails](t, s, protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: "shop-db-1"})
	if d.Name != "shop-db-1" || !d.Running || len(d.NetworkList) != 1 || d.NetworkList[0].Name != "shop_default" || d.Stack == nil || !d.Stack.Managed {
		t.Fatalf("details %+v", d)
	}
	// By ID prefix too.
	if d2 := must[protocol.ContainerDetails](t, s, protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: d.ID[:12]}); d2.ID != d.ID {
		t.Fatalf("by prefix: %+v", d2)
	}
	// The restart policy with its retry count (none for other policies).
	if d.RestartPolicy != "no" || d.RestartMaxRetries != 0 {
		t.Fatalf("restart policy %q max %d", d.RestartPolicy, d.RestartMaxRetries)
	}
	fe.SetRestartPolicy("shop-db-1", "on-failure", 5)
	if d := must[protocol.ContainerDetails](t, s, protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: "shop-db-1"}); d.RestartPolicy != "on-failure" ||
		d.RestartMaxRetries != 5 {
		t.Fatalf("on-failure restart policy %q max %d", d.RestartPolicy, d.RestartMaxRetries)
	}

	ims := must[protocol.ImageListOutput](t, s, protocol.ReqImageList, protocol.ImageListInput{})
	users := map[string]int{}
	for _, im := range ims.Images {
		users[im.RepoTags[0]] = len(im.UsedBy)
	}
	if users["nginx:1.27"] != 3 || users["postgres:17"] != 1 {
		t.Fatalf("image users %v", users)
	}

	vols := must[protocol.VolumeListOutput](t, s, protocol.ReqVolumeList, protocol.VolumeListInput{})
	if len(vols.Volumes) != 1 || vols.Volumes[0].Stack == nil || !vols.Volumes[0].Stack.Managed || len(vols.Volumes[0].UsedBy) != 1 {
		t.Fatalf("volumes %+v", vols)
	}
	fe.SetVolumeSize("shop_data", 4096)
	fe.AddVolume("empty", nil)
	usage := must[protocol.VolumeUsageOutput](t, s, protocol.ReqVolumeUsage, protocol.VolumeUsageInput{})
	if len(usage.Volumes) != 2 || usage.Volumes[0] != (protocol.VolumeUsage{Name: "empty", Size: -1, RefCount: 0}) ||
		usage.Volumes[1] != (protocol.VolumeUsage{Name: "shop_data", Size: 4096, RefCount: 1}) {
		t.Fatalf("volume usage %+v", usage)
	}
	nets := must[protocol.NetworkListOutput](t, s, protocol.ReqNetworkList, protocol.NetworkListInput{})
	builtin := 0
	for _, n := range nets.Networks {
		if n.Builtin {
			builtin++
		}
	}
	if len(nets.Networks) != 4 || builtin != 3 {
		t.Fatalf("networks %+v", nets)
	}
	n := must[protocol.NetworkInfo](t, s, protocol.ReqNetworkInspect, protocol.NetworkInspectInput{Network: "shop_default"})
	if len(n.Containers) != 2 || n.Stack == nil || !n.Stack.Managed {
		t.Fatalf("network %+v", n)
	}
	// Attached containers carry their names and their addresses on it.
	for _, c := range n.Containers {
		if c.Name == "" || c.IPAddress != "172.17.0.2" || c.IPv6Address != "" {
			t.Fatalf("attached container %+v", c)
		}
	}

	// image.tag: the new tag appears; an invalid target is refused.
	id := fe.AddImage("app:1")
	im := must[protocol.ImageDetails](t, s, protocol.ReqImageTag, protocol.ImageTagInput{Image: id, Target: "registry.example.com/app:stable"})
	if !slices.Contains(im.RepoTags, "registry.example.com/app:stable") {
		t.Fatalf("tag %+v", im)
	}
	if _, err := call[protocol.ImageDetails](t, s, protocol.ReqImageTag, protocol.ImageTagInput{Image: id, Target: "Bad Tag"}); handlerCode(err) != protocol.CodeInvalidArgument {
		t.Fatalf("invalid tag: %v", err)
	}
}

// TestRequestErrorsUseProtocolCodes: Engine failures become the protocol
// codes the manager maps to stable API errors.
func TestRequestErrorsUseProtocolCodes(t *testing.T) {
	s, fe := fixture(t)
	ctx := testutil.Context(t)
	if _, err := call[protocol.ContainerDetails](t, s, protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: "nope"}); handlerCode(err) != protocol.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	for code, want := range map[engine.Code]string{
		engine.CodeUnsupportedAPIVersion: protocol.CodeUnsupportedAPIVersion,
		engine.CodeEngineUnavailable:     protocol.CodeEngineUnavailable,
		engine.CodeTimeout:               protocol.CodeDeadlineExceeded,
		engine.CodeEngineError:           protocol.CodeEngineError,
	} {
		fe.Fail("container.list", enginefake.Err("container.list", code, "boom"))
		if _, err := call[protocol.ContainerListOutput](t, s, protocol.ReqContainerList, nil); handlerCode(err) != want {
			t.Errorf("%s: %v, want %s", code, err, want)
		}
	}
	h := s.Requests()[protocol.ReqContainerInspect]
	if _, err := h(ctx, json.RawMessage(`{"container":"web","extra":1}`)); handlerCode(err) != protocol.CodeInvalidFrame {
		t.Fatalf("unknown field: %v", err)
	}
	offline := New(Options{Engine: func() engine.Engine { return nil }})
	if _, err := offline.Requests()[protocol.ReqImageList](ctx, nil); handlerCode(err) != protocol.CodeEngineUnavailable {
		t.Fatalf("no engine: %v", err)
	}
}

type memJournal struct{}

func (memJournal) Save(context.Context, *jobexec.State) error { return nil }

type progress struct{ reports []protocol.ProgressPayload }

func (p *progress) Progress(_ context.Context, _ *jobexec.State, pp protocol.ProgressPayload) {
	p.reports = append(p.reports, pp)
}

// run executes one job of kind with input through jobexec, like the
// agent's runner does.
func run(t *testing.T, s *Service, kind domain.JobKind, input any) (protocol.ResultPayload, *progress) {
	t.Helper()
	return runWith(t, s, kind, input, nil)
}

// runWith is run with the attempt's credentials (#19).
func runWith(t *testing.T, s *Service, kind domain.JobKind, input any, secrets *protocol.CommandSecrets) (protocol.ResultPayload, *progress) {
	t.Helper()
	var exec jobexec.Executor
	for _, x := range s.Executors() {
		if x.Kind == kind {
			exec = x
		}
	}
	raw, _ := json.Marshal(input)
	p := &progress{}
	res, err := jobexec.Run(testutil.Context(t), exec, &jobexec.State{JobID: "j", Attempt: 1, FencingToken: 1, Kind: kind, Input: raw, Secrets: secrets},
		jobexec.Options{Journal: memJournal{}, Reporter: p})
	if err != nil {
		t.Fatal(err)
	}
	return res, p
}

func ok(t *testing.T, what string, res protocol.ResultPayload) {
	t.Helper()
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("%s: %+v", what, res)
	}
}

func failed(t *testing.T, what string, res protocol.ResultPayload, class string) {
	t.Helper()
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != class || res.Recovery == "" {
		t.Fatalf("%s: %+v, want class %s", what, res, class)
	}
}

// TestExecutorsCoverTheirKinds: every Docker resource kind of the catalog
// has a complete executor.
func TestExecutorsCoverTheirKinds(t *testing.T) {
	s, _ := fixture(t)
	var kinds []domain.JobKind
	for _, x := range s.Executors() {
		if err := x.Validate(domain.ExecutorAgent); err != nil {
			t.Error(err)
		}
		kinds = append(kinds, x.Kind)
	}
	for _, k := range []domain.JobKind{jobspec.ContainerCreate, jobspec.ContainerStart, jobspec.ContainerStop, jobspec.ContainerRestart,
		jobspec.ContainerPause, jobspec.ContainerUnpause, jobspec.ContainerRemove, jobspec.ContainerUpdate, jobspec.ImagePull,
		jobspec.ImageRemove, jobspec.VolumeCreate, jobspec.VolumeRemove, jobspec.NetworkCreate, jobspec.NetworkRemove} {
		if !slices.Contains(kinds, k) {
			t.Errorf("no executor for %s", k)
		}
	}
}

// TestContainerJobs: create (with a second network and start), the
// lifecycle actions (idempotent when already in the target state), update,
// remove (running needs force) and the ID check against recreation.
func TestContainerJobs(t *testing.T) {
	s, fe := fixture(t)
	fe.AddNetwork("backend", nil)
	create := protocol.ContainerCreateInput{Start: true, Ownership: map[string]string{protocol.LabelManaged: protocol.ManagedStandalone, protocol.LabelSpec: "spec-1"},
		Spec: protocol.ContainerSpec{Name: "api", Image: "nginx:1.27", Env: []string{"TOKEN=s3cret"}, Labels: map[string]string{"team": "ops"},
			Networks: []protocol.NetworkAttachment{{Name: "bridge"}, {Name: "backend", Aliases: []string{"api"}}}, RestartPolicy: "unless-stopped",
			Ports: []protocol.PortSpec{{ContainerPort: 80, HostPort: 8081}}}}
	res, _ := run(t, s, jobspec.ContainerCreate, create)
	ok(t, "create", res)
	c, found := fe.Container("api")
	if !found || !c.Details.State.Running || len(c.Details.Networks) != 2 || c.Details.Labels[protocol.LabelSpec] != "spec-1" ||
		c.Details.Labels["team"] != "ops" || c.Details.RestartPolicy != "unless-stopped" || !slices.Equal(c.Env, []string{"TOKEN=s3cret"}) {
		t.Fatalf("created %+v", c)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "api" || strings.Contains(res.Items[0].Message, "s3cret") {
		t.Fatalf("items %+v", res.Items)
	}
	// Same name again: the name is taken.
	res, _ = run(t, s, jobspec.ContainerCreate, create)
	failed(t, "duplicate", res, ClassNameTaken)
	// Reserved labels are refused by the agent too.
	bad := create
	bad.Spec.Name, bad.Spec.Labels = "api2", map[string]string{protocol.LabelRole: "manager"}
	res, _ = run(t, s, jobspec.ContainerCreate, bad)
	failed(t, "reserved label", res, ClassInvalidInput)
	// A missing image: not_found with guidance to pull first.
	bad = create
	bad.Spec.Name, bad.Spec.Image = "api3", "ghcr.io/org/missing:1"
	res, _ = run(t, s, jobspec.ContainerCreate, bad)
	failed(t, "missing image", res, "not_found")

	act := func(kind domain.JobKind, extra func(*protocol.ContainerActionInput)) protocol.ResultPayload {
		in := protocol.ContainerActionInput{Name: "api", ID: c.Details.ID}
		if extra != nil {
			extra(&in)
		}
		res, _ := run(t, s, kind, in)
		return res
	}
	state := func() engine.ContainerState { c, _ := fe.Container("api"); return c.Details.State }
	ok(t, "stop", act(jobspec.ContainerStop, func(in *protocol.ContainerActionInput) { ten := 10; in.TimeoutSeconds = &ten }))
	if state().Running {
		t.Fatal("still running")
	}
	ok(t, "stop again", act(jobspec.ContainerStop, nil))
	ok(t, "start", act(jobspec.ContainerStart, nil))
	ok(t, "start again", act(jobspec.ContainerStart, nil))
	ok(t, "pause", act(jobspec.ContainerPause, nil))
	if !state().Paused {
		t.Fatal("not paused")
	}
	ok(t, "unpause", act(jobspec.ContainerUnpause, nil))
	ok(t, "restart", act(jobspec.ContainerRestart, nil))

	pids := int64(100)
	res, _ = run(t, s, jobspec.ContainerUpdate, protocol.ContainerUpdateInput{Name: "api", ID: c.Details.ID, RestartPolicy: "always",
		Resources: &protocol.ResourcesSpec{Memory: 64 << 20, PidsLimit: &pids}})
	ok(t, "update", res)
	if c, _ := fe.Container("api"); c.Details.Resources.Memory != 64<<20 || c.Details.RestartPolicy != "always" {
		t.Fatalf("updated %+v", c.Details)
	}

	failed(t, "remove running", act(jobspec.ContainerRemove, nil), "conflict")
	ok(t, "remove forced", act(jobspec.ContainerRemove, func(in *protocol.ContainerActionInput) { in.Force = true }))
	if _, found := fe.Container("api"); found {
		t.Fatal("not removed")
	}
	// Idempotent: the container is already gone.
	ok(t, "remove again", act(jobspec.ContainerRemove, func(in *protocol.ContainerActionInput) { in.Force = true }))

	// Another container now carries the name: the job's container (by ID)
	// is gone, so start fails and nothing touches the new one.
	fe.AddContainer(engine.ContainerSpec{Name: "api", Image: "nginx:1.27"}, false)
	failed(t, "recreated", act(jobspec.ContainerStart, nil), "not_found")
	other, _ := fe.Container("web")
	res, _ = run(t, s, jobspec.ContainerStart, protocol.ContainerActionInput{Name: "api", ID: other.Details.ID})
	failed(t, "name mismatch", res, ClassRecreated)
}

// TestStackManagedAndInUseRefusals: the agent itself refuses direct edits
// of a Docker Manager-managed stack's containers, volumes and networks and the
// removal of images, volumes and networks still in use, whatever the
// manager decided.
func TestStackManagedAndInUseRefusals(t *testing.T) {
	s, fe := fixture(t)
	web, _ := fe.Container("shop-web-1")
	res, _ := run(t, s, jobspec.ContainerRemove, protocol.ContainerActionInput{Name: "shop-web-1", ID: web.Details.ID, Force: true})
	failed(t, "remove stack container", res, ClassStackManaged)
	res, _ = run(t, s, jobspec.ContainerUpdate, protocol.ContainerUpdateInput{Name: "shop-web-1", ID: web.Details.ID, RestartPolicy: "always"})
	failed(t, "update stack container", res, ClassStackManaged)
	// Runtime actions are fine.
	res, _ = run(t, s, jobspec.ContainerRestart, protocol.ContainerActionInput{Name: "shop-web-1", ID: web.Details.ID})
	ok(t, "restart stack container", res)
	// An unmanaged Compose project's container may be removed.
	legacy, _ := fe.Container("legacy-app-1")
	res, _ = run(t, s, jobspec.ContainerRemove, protocol.ContainerActionInput{Name: "legacy-app-1", ID: legacy.Details.ID})
	ok(t, "remove unmanaged compose container", res)

	res, _ = run(t, s, jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: "shop_data"})
	failed(t, "stack volume", res, ClassStackManaged)
	fe.AddVolume("scratch", nil)
	fe.AddContainer(engine.ContainerSpec{Name: "user", Image: "nginx:1.27", Mounts: []engine.MountSpec{{Type: "volume", Source: "scratch", Target: "/d"}}}, false)
	res, _ = run(t, s, jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: "scratch"})
	failed(t, "volume in use", res, ClassVolumeInUse)
	if !strings.Contains(res.Message, "user") {
		t.Fatalf("in-use message names the user: %+v", res)
	}

	nginx := ""
	for id, tags := range fe.Images() {
		if slices.Contains(tags, "nginx:1.27") {
			nginx = id
		}
	}
	res, _ = run(t, s, jobspec.ImageRemove, protocol.ImageRemoveInput{Image: nginx})
	failed(t, "image in use", res, ClassImageInUse)

	shopNet, _ := fe.InspectNetwork(testutil.Context(t), "shop_default")
	res, _ = run(t, s, jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: "shop_default", ID: shopNet.ID})
	failed(t, "stack network", res, ClassStackManaged)
	bridge, _ := fe.InspectNetwork(testutil.Context(t), "bridge")
	res, _ = run(t, s, jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: "bridge", ID: bridge.ID})
	failed(t, "builtin", res, ClassNetworkBuiltin)
	used := fe.AddNetwork("used", nil)
	if err := fe.ConnectNetwork(testutil.Context(t), "used", "web"); err != nil {
		t.Fatal(err)
	}
	res, _ = run(t, s, jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: "used", ID: used})
	failed(t, "network in use", res, ClassNetworkInUse)
}

// TestImageVolumeNetworkJobs: pulls report progress and the digest;
// registry failures keep their class (auth, rate limit) with guidance;
// removals, creations and their idempotent repeats.
func TestImageVolumeNetworkJobs(t *testing.T) {
	s, fe := fixture(t)
	res, p := run(t, s, jobspec.ImagePull, protocol.ImagePullInput{Reference: "ghcr.io/org/app:2"})
	ok(t, "pull", res)
	if len(p.reports) == 0 || !strings.Contains(res.Items[0].Message, "sha256:") {
		t.Fatalf("pull progress/digest: %+v %+v", p.reports, res)
	}
	for code, class := range map[engine.Code]string{
		engine.CodeUnauthorized: "unauthorized", engine.CodeRateLimited: "rate_limited", engine.CodeRegistryUnavailable: "registry_unavailable",
		engine.CodeNotFound: "not_found",
	} {
		fe.Fail("image.pull", enginefake.Err("image.pull", code, "registry said no"))
		res, _ = run(t, s, jobspec.ImagePull, protocol.ImagePullInput{Reference: "ghcr.io/org/private:1"})
		failed(t, string(code), res, class)
	}
	res, _ = run(t, s, jobspec.ImagePull, protocol.ImagePullInput{Reference: "UPPER/case"})
	failed(t, "invalid reference", res, ClassInvalidInput)
	// Registry credentials (#19) come with the attempt (never in the
	// input); a named connection without one never pulls anonymously.
	secrets := &protocol.CommandSecrets{Registries: []protocol.RegistryCredential{{ConnectionID: "reg-1", Host: "ghcr.io",
		ServerAddress: "ghcr.io", Username: "robot", Secret: "t0ken"}}}
	named := protocol.ImagePullInput{Reference: "ghcr.io/org/private:1", RegistryConnections: []string{"reg-1"}}
	res, _ = runWith(t, s, jobspec.ImagePull, named, secrets)
	ok(t, "authenticated pull", res)
	if a := fe.PullAuths(); len(a) == 0 || a[len(a)-1] == nil || a[len(a)-1].Username != "robot" || string(a[len(a)-1].Password) != "t0ken" {
		t.Fatalf("pull auth %+v", a)
	}
	res, _ = runWith(t, s, jobspec.ImagePull, named, nil)
	failed(t, "named connection without its credential", res, domain.ErrorCredentialUnavailable)
	res, _ = run(t, s, jobspec.ImagePull, protocol.ImagePullInput{Reference: "docker.io/library/alpine:3.22"})
	ok(t, "anonymous pull", res)
	if a := fe.PullAuths(); a[len(a)-1] != nil {
		t.Fatalf("anonymous pull sent credentials %+v", a[len(a)-1])
	}

	var appID string
	for id, tags := range fe.Images() {
		if slices.Contains(tags, "ghcr.io/org/app:2") {
			appID = id
		}
	}
	res, _ = run(t, s, jobspec.ImageRemove, protocol.ImageRemoveInput{Image: appID})
	ok(t, "remove image", res)
	res, _ = run(t, s, jobspec.ImageRemove, protocol.ImageRemoveInput{Image: appID})
	ok(t, "remove image again", res)

	ok(t, "create volume", func() protocol.ResultPayload {
		r, _ := run(t, s, jobspec.VolumeCreate, protocol.VolumeCreateInput{Name: "cache", Labels: map[string]string{"k": "v"}})
		return r
	}())
	ok(t, "remove volume", func() protocol.ResultPayload {
		r, _ := run(t, s, jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: "cache"})
		return r
	}())
	ok(t, "remove volume again", func() protocol.ResultPayload {
		r, _ := run(t, s, jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: "cache"})
		return r
	}())
	res, _ = run(t, s, jobspec.NetworkCreate, protocol.NetworkCreateInput{Name: "front", Internal: true})
	ok(t, "create network", res)
	res, _ = run(t, s, jobspec.NetworkCreate, protocol.NetworkCreateInput{Name: "front"})
	failed(t, "network name taken", res, ClassNameTaken)
	front, _ := fe.InspectNetwork(testutil.Context(t), "front")
	ok(t, "remove network", func() protocol.ResultPayload {
		r, _ := run(t, s, jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: "front", ID: front.ID})
		return r
	}())
	// Engine unavailable during a job: its own class and guidance.
	fe.Fail("volume.create", enginefake.Err("volume.create", engine.CodeEngineUnavailable, "cannot connect"))
	res, _ = run(t, s, jobspec.VolumeCreate, protocol.VolumeCreateInput{Name: "x"})
	failed(t, "engine down", res, "engine_unavailable")
}

// deployed is an agent service on a host running Docker Manager (manager, agent,
// proxy) with the manager identity received.
func deployed(t *testing.T) (*Service, *enginefake.Engine, enginefake.Deployment) {
	t.Helper()
	fe := enginefake.New("ENGINE-A")
	d := fe.Deploy(true)
	fe.AddImage("nginx:1.27")
	g := protect.New(protect.Options{SelfContainerID: d.AgentID, StacksVolume: d.Stacks, Logger: testutil.Logger(t)})
	g.SetManager("inst-1", d.ManagerID)
	s := New(Options{Engine: func() engine.Engine { return fe }, Guard: g, Logger: testutil.Logger(t)})
	return s, fe, d
}

// TestSelfProtectionInExecutors (#32): the agent refuses, whatever the
// manager sent, to stop, pause, restart, update or remove itself, to stop
// or remove the manager and its proxy (a restart only with confirmation),
// to remove Docker Manager's volumes, images and networks, and to mount them (or
// the Docker data root) into new containers. Start stays allowed.
func TestSelfProtectionInExecutors(t *testing.T) {
	s, fe, d := deployed(t)
	agent := func(kind domain.JobKind, confirm bool) protocol.ResultPayload {
		r, _ := run(t, s, kind, protocol.ContainerActionInput{Name: "docker-manager-docker-agent-1", ID: d.AgentID, Force: true, Confirmed: confirm})
		return r
	}
	manager := func(kind domain.JobKind, confirm bool) protocol.ResultPayload {
		r, _ := run(t, s, kind, protocol.ContainerActionInput{Name: "docker-manager-docker-manager-1", ID: d.ManagerID, Force: true, Confirmed: confirm})
		return r
	}
	for _, k := range []domain.JobKind{jobspec.ContainerStop, jobspec.ContainerPause, jobspec.ContainerRemove} {
		failed(t, "agent "+string(k), agent(k, true), protection.CodeProtected)
		failed(t, "manager "+string(k), manager(k, true), protection.CodeProtected)
	}
	failed(t, "agent restart", agent(jobspec.ContainerRestart, true), protection.CodeProtected)
	failed(t, "manager restart", manager(jobspec.ContainerRestart, false), protection.CodeConfirmationRequired)
	ok(t, "manager restart confirmed", manager(jobspec.ContainerRestart, true))
	ok(t, "agent start", agent(jobspec.ContainerStart, false))
	res, _ := run(t, s, jobspec.ContainerUpdate, protocol.ContainerUpdateInput{Name: "docker-manager-caddy-1", ID: d.ProxyID, RestartPolicy: "always"})
	failed(t, "proxy update", res, protection.CodeProtected)
	if c, _ := fe.Container(d.AgentID); !c.Details.State.Running {
		t.Fatal("the agent was stopped")
	}

	for _, v := range []string{d.ManagerData, d.AgentState, d.Stacks, d.ProxyData} {
		res, _ = run(t, s, jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: v})
		failed(t, "remove volume "+v, res, protection.CodeProtected)
	}
	res, _ = run(t, s, jobspec.ImageRemove, protocol.ImageRemoveInput{Image: d.ManagerImage, Force: true})
	failed(t, "remove manager image", res, protection.CodeProtected)
	n, _ := fe.InspectNetwork(testutil.Context(t), d.Network)
	res, _ = run(t, s, jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: d.Network, ID: n.ID})
	failed(t, "remove Docker Manager network", res, protection.CodeProtected)

	for _, m := range []protocol.MountSpec{
		{Type: "volume", Source: d.ManagerData, Target: "/steal"},
		{Type: "volume", Source: d.Stacks, Target: "/steal"},
		{Type: "bind", Source: "/var/lib/docker/volumes", Target: "/steal"},
		{Type: "bind", Source: "/var/lib", Target: "/steal"},
	} {
		res, _ = run(t, s, jobspec.ContainerCreate, protocol.ContainerCreateInput{Spec: protocol.ContainerSpec{Name: "thief", Image: "nginx:1.27",
			Mounts: []protocol.MountSpec{m}}})
		failed(t, "mount "+m.Source, res, protection.CodeProtected)
	}
	if _, found := fe.Container("thief"); found {
		t.Fatal("a container mounting Docker Manager's data was created")
	}
	res, _ = run(t, s, jobspec.ContainerCreate, protocol.ContainerCreateInput{Spec: protocol.ContainerSpec{Name: "fine", Image: "nginx:1.27",
		Mounts: []protocol.MountSpec{{Type: "bind", Source: "/srv/www", Target: "/www"}, {Type: "volume", Source: "fresh", Target: "/data"}}}})
	ok(t, "unprotected mounts", res)

	// The inventory shows the protections.
	list := must[protocol.ContainerListOutput](t, s, protocol.ReqContainerList, protocol.ContainerListInput{})
	protected := map[string]string{}
	for _, c := range list.Containers {
		if c.Protection != nil {
			protected[c.Name] = c.Protection.Role
		}
	}
	if len(protected) != 3 || protected["docker-manager-docker-agent-1"] != protection.RoleAgent || protected["docker-manager-caddy-1"] != protection.RoleProject {
		t.Fatalf("protected containers %v", protected)
	}
	vols := must[protocol.VolumeListOutput](t, s, protocol.ReqVolumeList, protocol.VolumeListInput{})
	n2 := 0
	for _, v := range vols.Volumes {
		if v.Protection != nil {
			n2++
		}
	}
	if n2 != 4 {
		t.Fatalf("protected volumes %d: %+v", n2, vols.Volumes)
	}
	im := must[protocol.ImageDetails](t, s, protocol.ReqImageInspect, protocol.ImageInspectInput{Image: d.AgentImage})
	nw := must[protocol.NetworkInfo](t, s, protocol.ReqNetworkInspect, protocol.NetworkInspectInput{Network: d.Network})
	d2 := must[protocol.ContainerDetails](t, s, protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: d.ManagerID})
	if im.Protection == nil || nw.Protection == nil || d2.Protection == nil || !d2.Protection.Self {
		t.Fatalf("inspect protections %+v %+v %+v", im.Protection, nw.Protection, d2.Protection)
	}
}

// TestContainerListOfAProject (#307): container.list with a project lists
// only the containers of that Compose project, with their stack and
// protection; noStartTimes inspects no container.
func TestContainerListOfAProject(t *testing.T) {
	s, fe := fixture(t)
	d := fe.Deploy(true)
	before := len(fe.Calls())
	list := must[protocol.ContainerListOutput](t, s, protocol.ReqContainerList, protocol.ContainerListInput{Project: "shop", NoStartTimes: true})
	var names []string
	for _, c := range list.Containers {
		names = append(names, c.Name)
		if c.Stack == nil || c.Stack.Project != "shop" || !c.Stack.Managed || c.StartedAt != nil || c.Protection != nil {
			t.Errorf("container %+v", c)
		}
	}
	if slices.Sort(names); !slices.Equal(names, []string{"shop-db-1", "shop-web-1"}) {
		t.Fatalf("containers %v", names)
	}
	if calls := fe.Calls()[before:]; slices.Contains(calls, "container.inspect") {
		t.Errorf("noStartTimes inspected containers: %v", calls)
	}
	// Without noStartTimes the running containers carry their start time.
	list = must[protocol.ContainerListOutput](t, s, protocol.ReqContainerList, protocol.ContainerListInput{Project: "shop"})
	for _, c := range list.Containers {
		if c.StartedAt == nil {
			t.Errorf("running container without start time: %+v", c)
		}
	}
	// Docker Manager's own project keeps its protection in its own list.
	list = must[protocol.ContainerListOutput](t, s, protocol.ReqContainerList, protocol.ContainerListInput{Project: "docker-manager", NoStartTimes: true})
	found := false
	for _, c := range list.Containers {
		if c.Stack == nil || c.Stack.Project != "docker-manager" {
			t.Errorf("container of another project: %+v", c)
		}
		if c.ID == d.ProxyID {
			found = true
			if c.Protection == nil || c.Protection.Role != protection.RoleProject {
				t.Errorf("proxy protection %+v", c.Protection)
			}
		}
	}
	if !found {
		t.Fatalf("the proxy is missing from %+v", list.Containers)
	}
}

// slowInspect is an Engine whose inspection of one container blocks until
// the caller gives up, like the Engine's while it removes that container
// on a loaded host; the other inspections are reported on inspected.
type slowInspect struct {
	*enginefake.Engine
	id        string
	blocked   chan struct{}
	inspected chan string
}

func (e *slowInspect) InspectContainer(ctx context.Context, id string) (engine.ContainerDetails, error) {
	if id != e.id {
		d, err := e.Engine.InspectContainer(ctx, id)
		e.inspected <- id
		return d, err
	}
	close(e.blocked)
	<-ctx.Done()
	return engine.ContainerDetails{}, ctx.Err()
}

// TestContainerListDoesNotWaitForASlowInspection (#307): a container the
// Engine does not inspect within the list's budget is listed without a
// start time; the list still answers, with the other start times.
func TestContainerListDoesNotWaitForASlowInspection(t *testing.T) {
	ctx := testutil.Context(t)
	_, fe := fixture(t)
	slow := fe.AddContainer(engine.ContainerSpec{Name: "ci-job", Image: "nginx:1.27"}, true)
	eng := &slowInspect{Engine: fe, id: slow, blocked: make(chan struct{}), inspected: make(chan string, 16)}
	fc := testutil.FakeClock()
	s := New(Options{Engine: func() engine.Engine { return eng }, Clock: fc, Logger: testutil.Logger(t),
		ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacksRoot+"/") }})
	type result struct {
		out protocol.ContainerListOutput
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := call[protocol.ContainerListOutput](t, s, protocol.ReqContainerList, protocol.ContainerListInput{})
		done <- result{out, err}
	}()
	// The other running containers (shop-web-1, shop-db-1, web) are
	// inspected while ci-job's inspection blocks.
	<-eng.blocked
	for range 3 {
		select {
		case <-eng.inspected:
		case <-ctx.Done():
			t.Fatal("the other containers were not inspected")
		}
	}
	if err := fc.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	fc.Advance(startTimesBudget)
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		t.Fatal("the list waited for the blocked inspection")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	byName := map[string]protocol.ContainerSummary{}
	for _, c := range r.out.Containers {
		byName[c.Name] = c
	}
	if c, ok := byName["ci-job"]; !ok || c.StartedAt != nil || c.State != "running" {
		t.Errorf("slow container %+v", c)
	}
	for _, n := range []string{"shop-web-1", "shop-db-1", "web"} {
		if byName[n].StartedAt == nil {
			t.Errorf("%s has no start time", n)
		}
	}
}
