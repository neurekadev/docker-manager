//go:build integration

package resources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/resources"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestEngineResourceOperations runs the agent's #6 requests and job
// executors against a real Docker Engine of the matrix (engine-matrix job):
// create with the common options, the lifecycle, in-place update, removal
// rules, authenticated and failing pulls with their error classes, tags,
// volumes and networks with their in-use and stack-managed refusals.
func TestEngineResourceOperations(t *testing.T) {
	nw := testharness.NewNetwork(t)
	reg := testharness.StartRegistry(t, testharness.RegistryOptions{Network: nw})
	e := testharness.StartEngine(t, reg.EngineOptions())
	e.LoadWorkload(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)
	c, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	const stacks = "/var/lib/docker/volumes/dockyard_stacks/_data"
	svc := resources.New(resources.Options{Engine: func() engine.Engine { return c }, Logger: testutil.Logger(t),
		ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacks+"/") }})
	execs := map[domain.JobKind]jobexec.Executor{}
	for _, x := range svc.Executors() {
		execs[x.Kind] = x
	}
	// The registry fixture's credential as the manager sends it with a
	// pull attempt (#19).
	secrets := &protocol.CommandSecrets{Registries: []protocol.RegistryCredential{{ConnectionID: "reg-it", Host: reg.EngineAddress,
		ServerAddress: reg.EngineAddress, Username: reg.User, Secret: reg.Password}}}
	runWith := func(kind domain.JobKind, input any, s *protocol.CommandSecrets) protocol.ResultPayload {
		t.Helper()
		raw, _ := json.Marshal(input)
		res, err := jobexec.Run(ctx, execs[kind], &jobexec.State{JobID: "j-" + string(kind), Attempt: 1, FencingToken: 1, Kind: kind, Input: raw,
			Secrets: s}, jobexec.Options{Journal: journal{}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	run := func(kind domain.JobKind, input any) protocol.ResultPayload {
		t.Helper()
		return runWith(kind, input, nil)
	}
	request := func(name string, input, out any) error {
		t.Helper()
		raw, _ := json.Marshal(input)
		res, err := svc.Requests()[name](ctx, raw)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(res)
		return json.Unmarshal(b, out)
	}
	ok := func(what string, res protocol.ResultPayload) {
		t.Helper()
		if res.Outcome != jobexec.OutcomeSucceeded {
			t.Fatalf("%s: %+v", what, res)
		}
	}
	failed := func(what string, res protocol.ResultPayload, class string) {
		t.Helper()
		if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != class {
			t.Errorf("%s: %+v, want class %s", what, res, class)
		}
	}

	t.Run("container.create_and_lifecycle", func(t *testing.T) {
		ok("network", run(jobspec.NetworkCreate, protocol.NetworkCreateInput{Name: "res-back"}))
		pids := int64(200)
		ok("create", run(jobspec.ContainerCreate, protocol.ContainerCreateInput{Start: true,
			Ownership: map[string]string{protocol.LabelManaged: protocol.ManagedStandalone, protocol.LabelSpec: "spec-it"},
			Spec: protocol.ContainerSpec{Name: "res-web", Image: testharness.WorkloadImage, Command: []string{"serve", "up"},
				Env: []string{"A=1"}, Labels: map[string]string{"team": "ops"}, RestartPolicy: "unless-stopped",
				Ports:       []protocol.PortSpec{{ContainerPort: 8080, HostIP: "127.0.0.1"}},
				Mounts:      []protocol.MountSpec{{Type: "volume", Source: "res-data", Target: "/data"}, {Type: "tmpfs", Target: "/scratch"}},
				Networks:    []protocol.NetworkAttachment{{Name: "bridge"}, {Name: "res-back", Aliases: []string{"web"}}},
				Resources:   protocol.ResourcesSpec{Memory: 64 << 20, PidsLimit: &pids},
				Healthcheck: &protocol.HealthcheckSpec{Test: []string{"CMD", testharness.WorkloadBinary, "check", "/etc/hostname"}, Interval: time.Second, Retries: 3}}}))
		t.Cleanup(func() {
			_ = c.RemoveContainer(context.Background(), "res-web", engine.RemoveOptions{Force: true, Volumes: true})
			_ = c.RemoveVolume(context.Background(), "res-data", true)
			_ = c.RemoveNetwork(context.Background(), "res-back")
		})
		var d protocol.ContainerDetails
		if err := request(protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: "res-web"}, &d); err != nil {
			t.Fatal(err)
		}
		if !d.Running || len(d.NetworkList) != 2 || d.Labels[protocol.LabelSpec] != "spec-it" || d.RestartPolicy != "unless-stopped" ||
			d.Resources.Memory != 64<<20 || d.Healthcheck == nil {
			t.Fatalf("created %+v", d)
		}
		act := func(kind domain.JobKind, force bool) protocol.ResultPayload {
			return run(kind, protocol.ContainerActionInput{Name: "res-web", ID: d.ID, Force: force})
		}
		ok("stop", act(jobspec.ContainerStop, false))
		ok("stop again", act(jobspec.ContainerStop, false))
		ok("start", act(jobspec.ContainerStart, false))
		ok("pause", act(jobspec.ContainerPause, false))
		ok("unpause", act(jobspec.ContainerUnpause, false))
		ok("restart", act(jobspec.ContainerRestart, false))
		ok("update", run(jobspec.ContainerUpdate, protocol.ContainerUpdateInput{Name: "res-web", ID: d.ID, RestartPolicy: "always",
			Resources: &protocol.ResourcesSpec{Memory: 128 << 20, MemorySwap: 256 << 20}}))
		if err := request(protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: d.ID}, &d); err != nil || d.Resources.Memory != 128<<20 ||
			d.RestartPolicy != "always" {
			t.Fatalf("updated %+v %v", d, err)
		}
		// The volume and network are in use; the image too.
		failed("volume in use", run(jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: "res-data"}), resources.ClassVolumeInUse)
		var n protocol.NetworkInfo
		if err := request(protocol.ReqNetworkInspect, protocol.NetworkInspectInput{Network: "res-back"}, &n); err != nil || len(n.Containers) != 1 {
			t.Fatalf("network %+v %v", n, err)
		}
		failed("network in use", run(jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: "res-back", ID: n.ID}), resources.ClassNetworkInUse)
		var im protocol.ImageDetails
		if err := request(protocol.ReqImageInspect, protocol.ImageInspectInput{Image: testharness.WorkloadImage}, &im); err != nil || len(im.UsedBy) == 0 {
			t.Fatalf("image %+v %v", im, err)
		}
		failed("image in use", run(jobspec.ImageRemove, protocol.ImageRemoveInput{Image: im.ID}), resources.ClassImageInUse)
		failed("remove running", act(jobspec.ContainerRemove, false), string(engine.CodeConflict))
		ok("remove", act(jobspec.ContainerRemove, true))
		ok("remove again", act(jobspec.ContainerRemove, true))
		ok("volume", run(jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: "res-data"}))
		ok("network", run(jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: "res-back", ID: n.ID}))
	})

	t.Run("stack_managed", func(t *testing.T) {
		labels := map[string]string{protocol.ComposeProjectLabel: "shop", protocol.ComposeServiceLabel: "web",
			protocol.ComposeWorkingDirLabel: stacks + "/shop"}
		id, _, err := c.CreateContainer(ctx, engine.ContainerSpec{Name: "shop-web-1", Image: testharness.WorkloadImage, Cmd: []string{"serve", "up"}, Labels: labels})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.RemoveContainer(context.Background(), id, engine.RemoveOptions{Force: true}) })
		failed("remove", run(jobspec.ContainerRemove, protocol.ContainerActionInput{Name: "shop-web-1", ID: id, Force: true}), resources.ClassStackManaged)
		failed("update", run(jobspec.ContainerUpdate, protocol.ContainerUpdateInput{Name: "shop-web-1", ID: id, RestartPolicy: "always"}), resources.ClassStackManaged)
		ok("start", run(jobspec.ContainerStart, protocol.ContainerActionInput{Name: "shop-web-1", ID: id}))
	})

	t.Run("image.pull_tag_remove", func(t *testing.T) {
		img, err := testharness.NewTestImage(runtime.GOARCH, "resources pull fixture")
		if err != nil {
			t.Fatal(err)
		}
		if err := testharness.PushOCIImage(ctx, http.DefaultClient, reg.Direct, reg.User, reg.Password, "dockyard/res", "v1", img); err != nil {
			t.Fatal(err)
		}
		ref := reg.EngineAddress + "/dockyard/res:v1"
		failed("anonymous", run(jobspec.ImagePull, protocol.ImagePullInput{Reference: ref}), string(engine.CodeUnauthorized))
		named := protocol.ImagePullInput{Reference: ref, RegistryConnections: []string{"reg-it"}}
		failed("named connection without its credential", runWith(jobspec.ImagePull, named, nil), domain.ErrorCredentialUnavailable)
		reg.Proxy.Reset()
		remove := reg.Proxy.Inject(testharness.FaultRule{Path: testharness.RegistryManifestPath, Status: http.StatusTooManyRequests, RetryAfter: "7"})
		failed("429", runWith(jobspec.ImagePull, named, secrets), string(engine.CodeRateLimited))
		remove()
		ok("authenticated", runWith(jobspec.ImagePull, named, secrets))
		var im protocol.ImageDetails
		if err := request(protocol.ReqImageTag, protocol.ImageTagInput{Image: mustImageID(t, request, ref), Target: "dockyard/res:copy"}, &im); err != nil {
			t.Fatal(err)
		}
		if len(im.RepoTags) != 2 {
			t.Fatalf("tags %v", im.RepoTags)
		}
		failed("several tags without force", run(jobspec.ImageRemove, protocol.ImageRemoveInput{Image: im.ID}), string(engine.CodeConflict))
		ok("remove forced", run(jobspec.ImageRemove, protocol.ImageRemoveInput{Image: im.ID, Force: true}))
		ok("remove again", run(jobspec.ImageRemove, protocol.ImageRemoveInput{Image: im.ID, Force: true}))
		failed("missing", runWith(jobspec.ImagePull, protocol.ImagePullInput{Reference: reg.EngineAddress + "/dockyard/missing:v1",
			RegistryConnections: []string{"reg-it"}}, secrets), string(engine.CodeNotFound))
	})

	t.Run("network.builtin_and_names", func(t *testing.T) {
		var n protocol.NetworkInfo
		if err := request(protocol.ReqNetworkInspect, protocol.NetworkInspectInput{Network: "bridge"}, &n); err != nil || !n.Builtin {
			t.Fatalf("bridge %+v %v", n, err)
		}
		failed("builtin", run(jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: "bridge", ID: n.ID}), resources.ClassNetworkBuiltin)
		ok("create", run(jobspec.NetworkCreate, protocol.NetworkCreateInput{Name: "res-dup"}))
		t.Cleanup(func() { _ = c.RemoveNetwork(context.Background(), "res-dup") })
		failed("duplicate", run(jobspec.NetworkCreate, protocol.NetworkCreateInput{Name: "res-dup"}), resources.ClassNameTaken)
	})
}

func mustImageID(t *testing.T, request func(string, any, any) error, ref string) string {
	t.Helper()
	var im protocol.ImageDetails
	if err := request(protocol.ReqImageInspect, protocol.ImageInspectInput{Image: ref}, &im); err != nil {
		t.Fatal(err)
	}
	return im.ID
}

type journal struct{}

func (journal) Save(context.Context, *jobexec.State) error { return nil }
