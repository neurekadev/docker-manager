package enginefake

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
)

// TestPersistOutlivesTheProcess: an Engine backed by a file keeps every
// operation's effect for the next Engine opened on it (the #26 crash
// harness restarts the agent process over the same Engine).
func TestPersistOutlivesTheProcess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "engine.json")
	a := New("E")
	if err := a.Persist(path); err != nil {
		t.Fatal(err)
	}
	id := a.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27", Labels: map[string]string{"k": "v"}}, false)
	if err := a.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	a.AddVolume("data", nil)
	if _, err := a.PullImage(ctx, "busybox:1.37", engine.PullOptions{}); err != nil {
		t.Fatal(err)
	}

	b := New("E")
	if err := b.Persist(path); err != nil {
		t.Fatal(err)
	}
	c, ok := b.Container("web")
	if !ok || c.Details.ID != id || !c.Details.State.Running || c.Details.Labels["k"] != "v" {
		t.Fatalf("container after reopening: %+v %v", c.Details, ok)
	}
	if !slices.Contains(b.VolumeNames(), "data") {
		t.Fatalf("volumes %v", b.VolumeNames())
	}
	if _, err := b.InspectImage(ctx, "busybox:1.37"); err != nil {
		t.Fatalf("pulled image lost: %v", err)
	}
	if calls := b.Calls(); !slices.Contains(calls, "container.start") || !slices.Contains(calls, "image.pull") {
		t.Fatalf("calls %v", calls)
	}
	// New objects get new IDs after the reload (the sequence persisted).
	id2 := b.AddContainer(engine.ContainerSpec{Name: "worker", Image: "nginx:1.27"}, false)
	if id2 == id {
		t.Fatal("an ID was reused after reopening")
	}
	if err := b.StopContainer(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	c2 := New("E")
	if err := c2.Persist(path); err != nil {
		t.Fatal(err)
	}
	if c, _ := c2.Container("web"); c.Details.State.Running {
		t.Fatal("the stop was not persisted")
	}
}
