package app

import (
	"net/http"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestDockerOperationsReachTheEventBus (#6 Done-when 2, with the #5
// relay): every Docker operation Docker Manager runs through the API — container
// lifecycle, creation and removal, image pull, tag and removal, volume and
// network creation and removal — produces the Engine's events, which the
// agent's event relay sends over its session and the manager republishes
// on its bus as docker.event of that environment only; the inventory
// routes show the result. The Engine is the in-memory fake (its events are
// what a real Engine emits for these calls; TestEngineTwoAgentsEnrollAndServeJobs
// checks the real Engine).
func TestDockerOperationsReachTheEventBus(t *testing.T) {
	e := newEnv(t)
	nasEngine, cloudEngine := enginefake.New("ENGINE-NAS"), enginefake.New("ENGINE-CLOUD")
	for _, fe := range []*enginefake.Engine{nasEngine, cloudEngine} {
		fe.AddImage("nginx:1.27")
		fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27"}, true)
	}
	sub := e.m.Events().Subscribe(1024, func(ev events.Event) bool { return ev.Type == events.DockerEvent })
	defer sub.Close()
	nas, cloud := e.connectObservedAgent("NAS", nasEngine), e.connectObservedAgent("Cloud", cloudEngine)
	owner, _ := e.setupOwner()
	base := "/api/v1/environments/" + nas.env

	var seen []events.Event
	// expect runs the job of an accepted request and waits for the Docker
	// events it must produce, in order, on the manager's bus.
	var waitEvents func(want ...[3]string)
	expect := func(r response, want ...[3]string) {
		t.Helper()
		if j := e.runJob(jobOf(t, r)); j.State != domain.JobSucceeded {
			t.Fatalf("job %+v", j)
		}
		waitEvents(want...)
	}
	waitEvents = func(want ...[3]string) {
		t.Helper()
		ctx := testutil.Context(t)
		for _, w := range want {
			for found := false; !found; {
				select {
				case ev := <-sub.C():
					seen = append(seen, ev)
					found = ev.ResourceType == w[0] && ev.ResourceID == w[1] && ev.Attributes["action"] == w[2]
				case <-ctx.Done():
					t.Fatalf("no %s %s %s event on the bus; seen %+v", w[0], w[1], w[2], seen)
				}
			}
		}
	}
	ev := func(typ, id, action string) [3]string { return [3]string{typ, id, action} }

	expect(owner.must(http.StatusAccepted, http.MethodPost, base+"/containers/web/restart", nil), ev("container", "web", "restart"))
	expect(owner.must(http.StatusAccepted, http.MethodPost, base+"/containers/web/stop", map[string]any{"timeoutSeconds": 3}),
		ev("container", "web", "die"), ev("container", "web", "stop"))
	expect(owner.must(http.StatusAccepted, http.MethodPost, base+"/containers/web/start", nil), ev("container", "web", "start"))
	expect(owner.must(http.StatusAccepted, http.MethodPost, base+"/containers", map[string]any{"name": "api", "image": "nginx:1.27"}),
		ev("container", "api", "create"), ev("container", "api", "start"))
	var list struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, base+"/containers", nil).json(t, &list)
	if !slices.ContainsFunc(list.Items, func(c struct {
		Name string `json:"name"`
	}) bool {
		return c.Name == "api"
	}) {
		t.Fatalf("created container not listed: %+v", list)
	}
	expect(owner.must(http.StatusAccepted, http.MethodDelete, base+"/containers/api?force=true", nil), ev("container", "api", "destroy"))
	expect(owner.must(http.StatusAccepted, http.MethodPost, base+"/images/pulls", map[string]any{"reference": "alpine:3.22"}),
		ev("image", "alpine:3.22", "pull"))
	alpine := ""
	for id, tags := range nasEngine.Images() {
		if slices.Contains(tags, "alpine:3.22") {
			alpine = id
		}
	}
	owner.must(http.StatusOK, http.MethodPost, base+"/images/"+alpine+"/tags", map[string]any{"repository": "mirror/alpine", "tag": "3.22"})
	waitEvents(ev("image", "mirror/alpine:3.22", "tag"))
	expect(owner.must(http.StatusAccepted, http.MethodDelete, base+"/images/"+alpine+"?force=true", nil),
		ev("image", "alpine:3.22", "untag"), ev("image", "mirror/alpine:3.22", "untag"))
	expect(owner.must(http.StatusAccepted, http.MethodPost, base+"/volumes", map[string]any{"name": "fresh"}), ev("volume", "fresh", "create"))
	expect(owner.must(http.StatusAccepted, http.MethodDelete, base+"/volumes/fresh", nil), ev("volume", "fresh", "destroy"))
	expect(owner.must(http.StatusAccepted, http.MethodPost, base+"/networks", map[string]any{"name": "backend"}), ev("network", "backend", "create"))
	expect(owner.must(http.StatusAccepted, http.MethodDelete, base+"/networks/backend", nil), ev("network", "backend", "destroy"))

	// Every event belongs to NAS: Cloud's Engine did nothing.
	for _, s := range seen {
		if s.EnvironmentID != nas.env {
			t.Errorf("event of %s (want %s only; Cloud is %s): %+v", s.EnvironmentID, nas.env, cloud.env, s)
		}
	}
	if len(cloudEngine.EmittedEvents()) != 0 {
		t.Errorf("Cloud's Engine emitted %+v", cloudEngine.EmittedEvents())
	}
}
