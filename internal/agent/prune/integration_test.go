//go:build integration

package prune_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/protect"
	"github.com/neurekadev/dockyard/internal/agent/prune"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

type journal struct{}

func (journal) Save(context.Context, *jobexec.State) error { return nil }

// TestEnginePruneEveryCategory runs a prune policy with every category
// against a real Docker Engine of the matrix (engine-matrix job): created
// candidates of each category are previewed and removed with targeted
// calls, while DockYard's own objects (a labeled agent container, the
// stacks volume), excluded objects and objects in use survive.
func TestEnginePruneEveryCategory(t *testing.T) {
	e := testharness.StartEngine(t, testharness.EngineOptions{})
	e.LoadWorkload(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)
	c, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Build cache and images: two builds of the same tag leave the first
	// image dangling; a second tag is unused; the builds create cache.
	dir := t.TempDir()
	for name, data := range map[string]string{"a.txt": "first", "b.txt": "second"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := func(file, tag string) string {
		res, err := c.Build(ctx, engine.BuildSpec{ContextDir: dir, DockerfileInline: "FROM scratch\nCOPY " + file + " /data\n", Tags: []string{tag}})
		if err != nil {
			t.Fatalf("build %s: %v", tag, err)
		}
		return res.ImageID
	}
	dangling := build("a.txt", "dockyard-test/prune:dangling")
	if re := build("b.txt", "dockyard-test/prune:dangling"); re == dangling {
		t.Fatal("the rebuild produced the same image")
	}
	unused := build("a.txt", "dockyard-test/prune:unused")
	kept := build("b.txt", "dockyard-test/prune:kept")
	if _, err := c.RemoveImage(ctx, "dockyard-test/prune:dangling", false, true); err != nil {
		t.Fatal(err)
	}

	// Networks and volumes.
	if _, err := c.CreateNetwork(ctx, engine.NetworkSpec{Name: "prune-net"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateNetwork(ctx, engine.NetworkSpec{Name: "prune-keep", Labels: map[string]string{"keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"prune-vol", "dockyard_stacks"} {
		if _, err := c.CreateVolume(ctx, engine.VolumeSpec{Name: v}); err != nil {
			t.Fatal(err)
		}
	}
	// A container with an anonymous volume; removing it without its volumes
	// leaves the anonymous volume behind.
	anonCtr, _, err := c.CreateContainer(ctx, engine.ContainerSpec{Name: "prune-anon", Image: testharness.WorkloadImage,
		Mounts: []engine.MountSpec{{Type: "volume", Target: "/scratch"}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.InspectContainer(ctx, anonCtr)
	if err != nil || len(d.Mounts) != 1 {
		t.Fatalf("anonymous mount: %+v %v", d.Mounts, err)
	}
	anon := d.Mounts[0].Name
	if err := c.RemoveContainer(ctx, anonCtr, engine.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	// Containers (never started: state "created"): a candidate, and a
	// DockYard agent container that is protected.
	if _, _, err := c.CreateContainer(ctx, engine.ContainerSpec{Name: "prune-ctr", Image: testharness.WorkloadImage}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.CreateContainer(ctx, engine.ContainerSpec{Name: "dockyard-agent-old", Image: testharness.WorkloadImage,
		Labels: map[string]string{protocol.LabelRole: "agent"}}); err != nil {
		t.Fatal(err)
	}

	guard := protect.New(protect.Options{StacksVolume: "dockyard_stacks", Logger: testutil.Logger(t)})
	svc := prune.New(prune.Options{Engine: func() engine.Engine { return c }, Guard: guard,
		Clock: clock.NewFake(time.Now().Add(time.Hour)), Logger: testutil.Logger(t)})
	var rules []protocol.PruneRule
	for _, cat := range protocol.PruneCategories() {
		r := protocol.PruneRule{Category: cat}
		switch cat {
		case protocol.PruneStoppedContainers:
			r.ContainerStates = []string{protocol.ContainerStateCreated}
		case protocol.PruneUnusedImages:
			r.Exclude = []string{"dockyard-test/prune:kept"}
		case protocol.PruneUnusedNetworks:
			r.ExcludeLabels = []string{"keep"}
		case protocol.PruneBuildCache:
			r.BuildCacheAll = true
		}
		rules = append(rules, r)
	}
	in := protocol.PruneInput{PolicyID: "pol-it", Rules: rules}

	// Preview.
	raw, _ := json.Marshal(in)
	res, err := svc.Requests()[protocol.ReqMaintenancePreview](ctx, raw)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	b, _ := json.Marshal(res)
	var p protocol.PrunePreviewOutput
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	decided := map[string]string{}
	cacheCandidates := 0
	for _, cat := range p.Categories {
		for _, it := range cat.Items {
			decided[cat.Category+" "+it.ID] = it.Decision
			if it.Name != "" {
				decided[cat.Category+" "+it.Name] = it.Decision
			}
			if cat.Category == protocol.PruneBuildCache && it.Decision == protocol.PruneRemove {
				cacheCandidates++
			}
		}
	}
	for key, want := range map[string]string{
		"stopped_containers prune-ctr":          protocol.PruneRemove,
		"stopped_containers dockyard-agent-old": protocol.PruneProtected,
		"dangling_images " + dangling:           protocol.PruneRemove,
		"unused_images " + unused:               protocol.PruneRemove,
		"unused_images " + kept:                 protocol.PruneExcluded,
		"unused_networks prune-net":             protocol.PruneRemove,
		"unused_networks prune-keep":            protocol.PruneExcluded,
		"unused_networks bridge":                protocol.PruneProtected,
		"anonymous_volumes " + anon:             protocol.PruneRemove,
		"named_volumes prune-vol":               protocol.PruneRemove,
		"named_volumes dockyard_stacks":         protocol.PruneProtected,
	} {
		if decided[key] != want {
			t.Errorf("preview %s: %q, want %q", key, decided[key], want)
		}
	}
	if cacheCandidates == 0 {
		t.Error("no build cache candidates after two builds")
	}

	// Run.
	raw, _ = json.Marshal(in)
	result, err := jobexec.Run(ctx, svc.Executor(), &jobexec.State{JobID: "j-prune", Attempt: 1, FencingToken: 1, Kind: jobspec.PruneRun, Input: raw},
		jobexec.Options{Journal: journal{}})
	if err != nil {
		t.Fatal(err)
	}
	var out protocol.PruneRunOutput
	if err := json.Unmarshal(result.Output, &out); err != nil || result.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("run: %+v (%v)", result, err)
	}
	for _, it := range out.Items {
		if it.Status != protocol.PruneItemRemoved {
			t.Errorf("item %+v", it)
		}
	}

	// The Engine afterwards.
	cs, _ := c.ListContainers(ctx, engine.ContainerFilter{All: true})
	names := []string{}
	for _, ct := range cs {
		names = append(names, strings.TrimPrefix(ct.Names[0], "/"))
	}
	if slices.Contains(names, "prune-ctr") || !slices.Contains(names, "dockyard-agent-old") {
		t.Errorf("containers: %v", names)
	}
	for _, id := range []string{dangling, unused} {
		if _, err := c.InspectImage(ctx, id); engine.CodeOf(err) != engine.CodeNotFound {
			t.Errorf("image %s still present (%v)", id, err)
		}
	}
	if _, err := c.InspectImage(ctx, kept); err != nil {
		t.Errorf("excluded image removed: %v", err)
	}
	if _, err := c.InspectNetwork(ctx, "prune-net"); engine.CodeOf(err) != engine.CodeNotFound {
		t.Errorf("prune-net: %v", err)
	}
	if _, err := c.InspectNetwork(ctx, "prune-keep"); err != nil {
		t.Errorf("prune-keep: %v", err)
	}
	for _, v := range []string{anon, "prune-vol"} {
		if _, err := c.InspectVolume(ctx, v); engine.CodeOf(err) != engine.CodeNotFound {
			t.Errorf("volume %s: %v", v, err)
		}
	}
	if _, err := c.InspectVolume(ctx, "dockyard_stacks"); err != nil {
		t.Errorf("stacks volume removed: %v", err)
	}
	// Parent records BuildKit kept while their children existed go in the
	// following runs.
	for range 5 {
		raw, _ = json.Marshal(protocol.PruneInput{PolicyID: "pol-it", Rules: []protocol.PruneRule{{Category: protocol.PruneBuildCache, BuildCacheAll: true}}})
		again, err := jobexec.Run(ctx, svc.Executor(), &jobexec.State{JobID: "j-cache", Attempt: 1, FencingToken: 1, Kind: jobspec.PruneRun, Input: raw},
			jobexec.Options{Journal: journal{}})
		if err != nil {
			t.Fatal(err)
		}
		var o protocol.PruneRunOutput
		if err := json.Unmarshal(again.Output, &o); err != nil || o.Removed == 0 {
			break
		}
	}
	recs, err := c.ListBuildCache(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if !r.InUse {
			t.Errorf("build cache record %s (%s) survived an all-unused prune", r.ID, r.Description)
		}
	}
}
