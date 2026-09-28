package prune

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

const stacksRoot = "/var/lib/docker/volumes/docker-manager_stacks/_data"

var (
	now    = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	old    = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)   // 75 days before now
	recent = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) // 5 days before now
)

const thirtyDays = 30 * 24 * 3600

// world is a fake Engine with objects of every prune category: for each,
// old candidates, recent ones, used ones, protected and excluded ones.
type world struct {
	fe  *enginefake.Engine
	ids map[string]string // label -> image ID / network ID
}

func compose(project, service, dir string) map[string]string {
	return map[string]string{protocol.ComposeProjectLabel: project, protocol.ComposeServiceLabel: service, protocol.ComposeWorkingDirLabel: dir}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	fe := enginefake.New("ENGINE-A")
	w := &world{fe: fe, ids: map[string]string{}}

	// Images.
	w.ids["nginx"] = fe.AddImage("nginx:1.27")    // used by a running container
	w.ids["app-old"] = fe.AddImage("app:old")     // unused, old: unused_images candidate
	w.ids["app-new"] = fe.AddImage("app:new")     // unused, recent: retained
	w.ids["dangling"] = fe.AddImage()             // dangling, old: dangling_images candidate
	w.ids["stackimg"] = fe.AddImage("shop/api:2") // referenced by a Docker Manager stack (manager protection)
	w.ids["keep"] = fe.AddLabeledImage(map[string]string{"keep": "true"}, "keep:1")
	w.ids["exited-img"] = fe.AddImage("tool:1") // used only by a stopped candidate container
	for _, k := range []string{"nginx", "app-old", "dangling", "stackimg", "keep", "exited-img"} {
		fe.SetImageCreated(w.ids[k], old)
	}
	fe.SetImageCreated(w.ids["app-new"], recent)
	fe.SetImageSize(w.ids["app-old"], 100)
	fe.SetImageSize(w.ids["dangling"], 10)
	fe.SetImageSize(w.ids["exited-img"], 7)

	// Networks.
	w.ids["unused-net"] = fe.AddNetwork("unused-net", nil)
	w.ids["used-net"] = fe.AddNetwork("used-net", nil)
	w.ids["shop-net"] = fe.AddNetwork("shop_backend", map[string]string{protocol.ComposeProjectLabel: "shop"})
	w.ids["spec-net"] = fe.AddNetwork("spec-net", nil)
	for _, n := range fe.NetworkNames() {
		fe.SetNetworkCreated(n, old)
	}

	// Volumes.
	fe.AddVolume("anon1", map[string]string{protocol.AnonymousVolumeLabel: ""})
	fe.AddVolume("olddata", nil)
	fe.AddVolume("newdata", nil)
	fe.AddVolume("backup_repo", nil)
	fe.AddVolume("shop_data", map[string]string{protocol.ComposeProjectLabel: "shop"})
	fe.AddVolume("docker-manager_stacks", nil)
	fe.AddVolume("used_data", nil)
	for _, v := range fe.VolumeNames() {
		fe.SetVolumeCreated(v, old)
	}
	fe.SetVolumeCreated("newdata", recent)
	fe.SetVolumeSize("anon1", 1000)
	fe.SetVolumeSize("olddata", 2000)

	// Containers.
	stopped := func(name, image string, labels map[string]string, finished time.Time, extra ...func(*engine.ContainerSpec)) {
		spec := engine.ContainerSpec{Name: name, Image: image, Labels: labels}
		for _, fn := range extra {
			fn(&spec)
		}
		fe.AddContainer(spec, false)
		fe.SetContainerState(name, "exited")
		fe.SetContainerTimes(name, old, finished)
	}
	fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27", NetworkMode: "used-net",
		Mounts: []engine.MountSpec{{Type: "volume", Source: "used_data", Target: "/data"}}}, true)
	stopped("old-exited", "tool:1", nil, old)
	fe.SetContainerSize("old-exited", 50)
	stopped("new-exited", "nginx:1.27", nil, recent)
	stopped("excluded-by-name", "nginx:1.27", nil, old)
	stopped("shop-db-1", "nginx:1.27", compose("shop", "db", stacksRoot+"/shop"), old)
	stopped("spec-ctr", "nginx:1.27", map[string]string{protocol.LabelManaged: protocol.ManagedStandalone, protocol.LabelSpec: "spec-1"}, old)
	fe.AddContainer(engine.ContainerSpec{Name: "never-started", Image: "nginx:1.27"}, false)
	fe.SetContainerTimes("never-started", old, time.Time{})
	// Docker Manager's own manager, stopped, with its data volume: protected (#32).
	stopped("docker-manager", "nginx:1.27", map[string]string{protocol.LabelRole: "manager"}, old, func(s *engine.ContainerSpec) {
		s.Mounts = []engine.MountSpec{{Type: "volume", Source: "docker-manager_data", Target: protect.ManagerDataDir}}
	})

	// Build cache.
	cache := func(id string, lastUsed time.Time, size int64, mod func(*engine.BuildCacheRecord)) {
		r := engine.BuildCacheRecord{ID: id, Type: "regular", Description: "RUN step " + id, Size: size, CreatedAt: old, LastUsedAt: lastUsed}
		if mod != nil {
			mod(&r)
		}
		fe.AddBuildCache(r)
	}
	cache("bc-root", old.Add(-time.Hour), 300, nil)
	cache("bc-leaf", old, 200, func(r *engine.BuildCacheRecord) { r.Parents = []string{"bc-root"} })
	cache("bc-shared", old, 400, func(r *engine.BuildCacheRecord) { r.Shared = true })
	cache("bc-inuse", old, 500, func(r *engine.BuildCacheRecord) { r.InUse = true })
	cache("bc-new", recent, 600, nil)
	return w
}

func (w *world) service(t *testing.T) *Service {
	guard := protect.New(protect.Options{StacksVolume: "docker-manager_stacks", Logger: testutil.Logger(t)})
	return New(Options{Engine: func() engine.Engine { return w.fe }, Guard: guard, Clock: clock.NewFake(now), Logger: testutil.Logger(t),
		ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacksRoot+"/") }})
}

// allRules enables every category with a 30-day threshold.
func allRules() []protocol.PruneRule {
	var out []protocol.PruneRule
	for _, c := range protocol.PruneCategories() {
		out = append(out, protocol.PruneRule{Category: c, MinAgeSeconds: thirtyDays})
	}
	return out
}

func withRule(rules []protocol.PruneRule, category string, fn func(*protocol.PruneRule)) []protocol.PruneRule {
	out := slices.Clone(rules)
	for i := range out {
		if out[i].Category == category {
			fn(&out[i])
		}
	}
	return out
}

// managerProtection is what the manager sends for this world: the
// Docker Manager stack "shop", its image and a backup destination volume.
func managerProtection() protocol.PruneProtection {
	return protocol.PruneProtection{
		Projects: []protocol.ProtectedRef{{Ref: "shop", Reason: "part of Docker Manager stack shop"}},
		Images:   []protocol.ProtectedRef{{Ref: "docker.io/shop/api:2", Reason: "used by the definition of stack shop"}},
		Volumes:  []protocol.ProtectedRef{{Ref: "backup_repo", Reason: "backup destination (#10)"}},
		Networks: []protocol.ProtectedRef{{Ref: "spec-net", Reason: "used by the saved specification of container api"}},
	}
}

func input(rules []protocol.PruneRule) protocol.PruneInput {
	return protocol.PruneInput{PolicyID: "pol-1", Rules: rules, Protect: managerProtection()}
}

func preview(t *testing.T, s *Service, in protocol.PruneInput) protocol.PrunePreviewOutput {
	t.Helper()
	raw, _ := json.Marshal(in)
	res, err := s.Requests()[protocol.ReqMaintenancePreview](testutil.Context(t), raw)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	b, _ := json.Marshal(res)
	var out protocol.PrunePreviewOutput
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// decisions maps "<decision> <name>" per category.
func decisions(p protocol.PrunePreviewOutput) map[string][]string {
	out := map[string][]string{}
	for _, c := range p.Categories {
		out[c.Category] = []string{}
		for _, it := range c.Items {
			out[c.Category] = append(out[c.Category], it.Decision+" "+it.Name)
		}
		sort.Strings(out[c.Category])
	}
	return out
}

func category(p protocol.PrunePreviewOutput, name string) protocol.PruneCategoryPlan {
	for _, c := range p.Categories {
		if c.Category == name {
			return c
		}
	}
	return protocol.PruneCategoryPlan{}
}

// TestPreviewEveryCategory: an accurate preview of every category with
// the reasons for candidates, protected, excluded and retained objects.
func TestPreviewEveryCategory(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	rules := withRule(allRules(), protocol.PruneStoppedContainers, func(r *protocol.PruneRule) { r.Exclude = []string{"excluded-by-name"} })
	rules = withRule(rules, protocol.PruneUnusedImages, func(r *protocol.PruneRule) { r.ExcludeLabels = []string{"keep=true"} })
	p := preview(t, s, input(rules))
	got := decisions(p)
	want := map[string][]string{
		protocol.PruneStoppedContainers: {"excluded excluded-by-name", "protected docker-manager", "protected shop-db-1", "protected spec-ctr",
			"remove old-exited", "retained new-exited"},
		protocol.PruneDanglingImages: {"remove " + shortID(w.ids["dangling"])},
		// tool:1 is only used by old-exited, a candidate removed first.
		protocol.PruneUnusedImages: {"excluded keep:1", "protected shop/api:2", "remove app:old", "remove tool:1", "retained app:new"},
		protocol.PruneUnusedNetworks: {"protected bridge", "protected host", "protected none", "protected shop_backend", "protected spec-net",
			"remove unused-net"},
		protocol.PruneAnonymousVolumes: {"remove anon1"},
		protocol.PruneNamedVolumes: {"protected backup_repo", "protected docker-manager_stacks", "protected shop_data", "remove olddata",
			"retained newdata"},
		protocol.PruneBuildCache: {"remove RUN step bc-leaf", "remove RUN step bc-root", "retained RUN step bc-new"},
	}
	for _, c := range protocol.PruneCategories() {
		if !slices.Equal(got[c], want[c]) {
			t.Errorf("%s:\n got %v\nwant %v", c, got[c], want[c])
		}
	}
	// Reasons and approximate sizes.
	containers := category(p, protocol.PruneStoppedContainers)
	if containers.Remove != 1 || containers.Protected != 3 || containers.Excluded != 1 || containers.Retained != 1 || containers.Bytes != 50 {
		t.Fatalf("container counts: %+v", containers)
	}
	for _, it := range containers.Items {
		switch it.Name {
		case "docker-manager":
			if !strings.Contains(it.Reason, "Docker Manager") {
				t.Errorf("manager reason: %q", it.Reason)
			}
		case "shop-db-1":
			if !strings.Contains(it.Reason, "Docker Manager stack") {
				t.Errorf("stack reason: %q", it.Reason)
			}
		case "old-exited":
			if !it.Since.Equal(old) || !strings.Contains(it.Reason, "exited since") {
				t.Errorf("candidate: %+v", it)
			}
		}
	}
	images := category(p, protocol.PruneUnusedImages)
	if images.Bytes != 107 {
		t.Errorf("image bytes = %d, want 107", images.Bytes)
	}
	vols := category(p, protocol.PruneNamedVolumes)
	if vols.Bytes != 2000 || vols.Remove != 1 {
		t.Errorf("named volumes: %+v", vols)
	}
	for _, it := range vols.Items {
		if it.Name == "backup_repo" && it.Reason != "backup destination (#10)" {
			t.Errorf("backup reason: %q", it.Reason)
		}
	}
	// Candidates come first, in execution order (children before parents).
	bc := category(p, protocol.PruneBuildCache)
	if bc.Items[0].ID != "bc-leaf" || bc.Items[1].ID != "bc-root" || bc.Bytes != 500 {
		t.Errorf("build cache order: %+v", bc)
	}
	// A preview changes nothing.
	for _, c := range w.fe.Calls() {
		if strings.HasSuffix(c, ".remove") {
			t.Fatalf("preview called %s", c)
		}
	}
}

// TestNoRulesNothingPruned: a policy without enabled rules (every rule
// starts disabled) has no candidates and its run removes nothing.
func TestNoRulesNothingPruned(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	p := preview(t, s, input(nil))
	if len(p.Categories) != 0 {
		t.Fatalf("categories: %+v", p.Categories)
	}
	before := snapshot(w.fe)
	res, out := runJob(t, s, input(nil), nil)
	if res.Outcome != jobexec.OutcomeSucceeded || len(out.Items) != 0 {
		t.Fatalf("run: %+v %+v", res, out)
	}
	if after := snapshot(w.fe); !maps.EqualFunc(before, after, slices.Equal) {
		t.Fatalf("objects changed: %v -> %v", before, after)
	}
}

func snapshot(fe *enginefake.Engine) map[string][]string {
	var images []string
	for id := range fe.Images() {
		images = append(images, id)
	}
	sort.Strings(images)
	return map[string][]string{"containers": fe.ContainerNames(), "images": images, "volumes": fe.VolumeNames(),
		"networks": fe.NetworkNames(), "cache": fe.BuildCacheIDs()}
}

type memJournal struct {
	mu    sync.Mutex
	saves int
	last  jobexec.State
}

func (j *memJournal) Save(_ context.Context, st *jobexec.State) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.saves++
	j.last = st.Clone()
	return nil
}

// reporter records progress and runs hooks when a step starts or an item
// finished.
type reporter struct {
	onStep func(step string)
	onItem func(it protocol.ItemPayload)
	items  []protocol.ItemPayload
}

func (r *reporter) Progress(_ context.Context, _ *jobexec.State, p protocol.ProgressPayload) {
	if p.Item != nil {
		r.items = append(r.items, *p.Item)
		if r.onItem != nil {
			r.onItem(*p.Item)
		}
		return
	}
	if p.Message == "step "+p.Step+" started" && r.onStep != nil {
		r.onStep(p.Step)
	}
}

type runOpts struct {
	rep    *reporter
	cancel func() bool
	state  *jobexec.State
}

func runJob(t *testing.T, s *Service, in protocol.PruneInput, o *runOpts) (protocol.ResultPayload, protocol.PruneRunOutput) {
	t.Helper()
	if o == nil {
		o = &runOpts{}
	}
	if o.rep == nil {
		o.rep = &reporter{}
	}
	raw, _ := json.Marshal(in)
	st := o.state
	if st == nil {
		st = &jobexec.State{JobID: "j", Attempt: 1, FencingToken: 1, Kind: jobspec.PruneRun, Input: raw}
	}
	res, err := jobexec.Run(testutil.Context(t), s.Executor(), st, jobexec.Options{Journal: &memJournal{}, Reporter: o.rep, CancelRequested: o.cancel})
	if err != nil {
		t.Fatal(err)
	}
	var out protocol.PruneRunOutput
	if len(res.Output) > 0 {
		if err := json.Unmarshal(res.Output, &out); err != nil {
			t.Fatal(err)
		}
	}
	return res, out
}

func statuses(out protocol.PruneRunOutput) map[string]string {
	m := map[string]string{}
	for _, it := range out.Items {
		m[it.Name] = it.Status
	}
	return m
}

// TestRunRemovesCandidatesOnly: the run removes exactly the previewed
// candidates; Docker Manager's own objects, stack members, saved specifications,
// backup destinations, excluded and recent objects all survive.
func TestRunRemovesCandidatesOnly(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	rules := withRule(allRules(), protocol.PruneStoppedContainers, func(r *protocol.PruneRule) { r.Exclude = []string{"excluded-by-name"} })
	rules = withRule(rules, protocol.PruneUnusedImages, func(r *protocol.PruneRule) { r.ExcludeLabels = []string{"keep=true"} })
	res, out := runJob(t, s, input(rules), nil)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("run: %+v", res)
	}
	if out.Removed != 9 || out.Skipped != 0 || out.Failed != 0 || out.Protected != 12 || out.Excluded != 2 || out.Retained != 4 {
		t.Fatalf("totals: %+v", out)
	}
	// 50 (container) + 10 + 100 + 7 (images) + 1000 + 2000 (volumes) + 500 (cache)
	if out.BytesReclaimed != 3667 {
		t.Fatalf("bytes reclaimed = %d", out.BytesReclaimed)
	}
	if got := w.fe.ContainerNames(); !slices.Equal(got, []string{"docker-manager", "excluded-by-name", "never-started", "new-exited", "shop-db-1",
		"spec-ctr", "web"}) {
		t.Errorf("containers left: %v", got)
	}
	imgs := w.fe.Images()
	for _, k := range []string{"app-old", "dangling", "exited-img"} {
		if _, ok := imgs[w.ids[k]]; ok {
			t.Errorf("image %s survived", k)
		}
	}
	for _, k := range []string{"nginx", "app-new", "stackimg", "keep"} {
		if _, ok := imgs[w.ids[k]]; !ok {
			t.Errorf("image %s was removed", k)
		}
	}
	if got := w.fe.VolumeNames(); !slices.Equal(got, []string{"backup_repo", "docker-manager_data", "docker-manager_stacks", "newdata", "shop_data", "used_data"}) {
		t.Errorf("volumes left: %v", got)
	}
	if got := w.fe.NetworkNames(); !slices.Equal(got, []string{"bridge", "host", "none", "shop_backend", "spec-net", "used-net"}) {
		t.Errorf("networks left: %v", got)
	}
	if got := w.fe.BuildCacheIDs(); !slices.Equal(got, []string{"bc-inuse", "bc-new", "bc-shared"}) {
		t.Errorf("cache left: %v", got)
	}
	// Per-item job items (audited with the job) name every removal.
	if len(res.Items) != 9 {
		t.Fatalf("job items: %+v", res.Items)
	}
	for _, it := range res.Items {
		if it.Status != domain.ItemSucceeded || !strings.HasPrefix(it.Message, "removed") {
			t.Errorf("item %+v", it)
		}
	}
	// Only targeted removals: one call per candidate, never a broad prune.
	removals := 0
	for _, c := range w.fe.Calls() {
		if strings.HasSuffix(c, ".remove") {
			removals++
		}
	}
	if removals != 9 {
		t.Errorf("remove calls = %d: %v", removals, w.fe.Calls())
	}
}

// TestVolumeCategoriesAreSeparate: the anonymous and named volume rules
// select only their own volumes; without a volume rule no volume is
// touched.
func TestVolumeCategoriesAreSeparate(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	_, out := runJob(t, s, input([]protocol.PruneRule{{Category: protocol.PruneAnonymousVolumes}}), nil)
	if st := statuses(out); len(st) != 1 || st["anon1"] != protocol.PruneItemRemoved {
		t.Fatalf("anonymous run: %+v", out)
	}
	if slices.Contains(w.fe.VolumeNames(), "anon1") || !slices.Contains(w.fe.VolumeNames(), "olddata") {
		t.Fatalf("volumes: %v", w.fe.VolumeNames())
	}
	w2 := newWorld(t)
	_, out = runJob(t, w2.service(t), input(withRule(allRules()[:4], protocol.PruneStoppedContainers, func(*protocol.PruneRule) {})), nil)
	for _, it := range out.Items {
		if strings.HasSuffix(it.Category, "_volumes") {
			t.Fatalf("volume touched without a volume rule: %+v", it)
		}
	}
	if !slices.Contains(w2.fe.VolumeNames(), "anon1") || !slices.Contains(w2.fe.VolumeNames(), "olddata") {
		t.Fatalf("volumes: %v", w2.fe.VolumeNames())
	}
}

// TestRevalidationRace: objects that became used, protected or excluded
// between the plan and their deletion are skipped with the reason.
func TestRevalidationRace(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	rep := &reporter{onStep: func(step string) {
		if step != "delete_candidates" {
			return
		}
		// Between collect_candidates and delete_candidates:
		w.fe.SetContainerState("old-exited", "running")                                                   // restarted
		w.fe.AddContainer(engine.ContainerSpec{Name: "late", Image: "app:old", NetworkMode: "unused-net", // starts using
			Mounts: []engine.MountSpec{{Type: "volume", Source: "olddata", Target: "/d"}}}, true) // image, network, volume
		w.fe.SetBuildCacheInUse("bc-leaf", true) // a build started
	}}
	res, out := runJob(t, s, input(allRules()), &runOpts{rep: rep})
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("run: %+v", res)
	}
	want := map[string]string{"old-exited": "now running", "app:old": "now used by container late", "unused-net": "now used by container late",
		"olddata": "now mounted by container late", "RUN step bc-leaf": "now in use by a build"}
	for _, it := range out.Items {
		if reason, ok := want[it.Name]; ok {
			if it.Status != protocol.PruneItemSkipped || it.Reason != reason {
				t.Errorf("%s: %s %q, want skipped %q", it.Name, it.Status, it.Reason, reason)
			}
			delete(want, it.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("items not planned: %v (%+v)", want, out.Items)
	}
	if !slices.Contains(w.fe.ContainerNames(), "old-exited") || !slices.Contains(w.fe.VolumeNames(), "olddata") ||
		!slices.Contains(w.fe.NetworkNames(), "unused-net") || !slices.Contains(w.fe.BuildCacheIDs(), "bc-leaf") {
		t.Fatal("a revalidated candidate was removed")
	}
	if _, ok := w.fe.Images()[w.ids["app-old"]]; !ok {
		t.Fatal("image in use was removed")
	}
	// tool:1 is still used by old-exited (restarted): skipped.
	if st := statuses(out)["tool:1"]; st != protocol.PruneItemSkipped {
		t.Fatalf("tool:1: %s", st)
	}
	// bc-root stays: its child could not be removed (the Engine keeps it).
	if st := statuses(out)["RUN step bc-root"]; st != protocol.PruneItemSkipped {
		t.Fatalf("bc-root: %+v", out.Items)
	}
}

// TestRaceBecameProtected: a candidate that became one of Docker Manager's own
// objects before its deletion (here: the manager announced that the
// stopped container is its own container) survives with the reason.
func TestRaceBecameProtected(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	c, _ := w.fe.Container("old-exited")
	rep := &reporter{onStep: func(step string) {
		if step == "delete_candidates" {
			s.guard.SetManager("instance-1", c.Details.ID)
		}
	}}
	_, out := runJob(t, s, input(allRules()), &runOpts{rep: rep})
	for _, it := range out.Items {
		if it.Name == "old-exited" && (it.Status != protocol.PruneItemSkipped || !strings.HasPrefix(it.Reason, "protected: the Docker Manager")) {
			t.Fatalf("old-exited: %+v", it)
		}
	}
	if !slices.Contains(w.fe.ContainerNames(), "old-exited") {
		t.Fatal("the Docker Manager container was removed")
	}
	// Its image is Docker Manager's image now: skipped as in use / protected.
	if _, ok := w.fe.Images()[w.ids["exited-img"]]; !ok {
		t.Fatal("the Docker Manager image was removed")
	}
}

// TestCancellationAtItemBoundary: a cancellation stops the run between
// items; completed removals stay reported, the rest stays pending.
func TestCancellationAtItemBoundary(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	cancelled := false
	rep := &reporter{onItem: func(protocol.ItemPayload) { cancelled = true }}
	res, out := runJob(t, s, input(allRules()), &runOpts{rep: rep, cancel: func() bool { return cancelled }})
	if res.Outcome != jobexec.OutcomeCancelled {
		t.Fatalf("outcome %+v", res)
	}
	if out.Removed != 1 || out.Items[0].Status != protocol.PruneItemRemoved || out.Items[0].Name != "excluded-by-name" {
		t.Fatalf("output: %+v", out)
	}
	if !slices.Contains(w.fe.ContainerNames(), "old-exited") {
		t.Fatal("removed after the cancellation")
	}
	pending := 0
	for _, it := range out.Items {
		if it.Status == protocol.PruneItemPending {
			pending++
		}
	}
	if pending != len(out.Items)-1 {
		t.Fatalf("pending = %d of %d", pending, len(out.Items))
	}
	if len(res.Items) != 1 {
		t.Fatalf("job items: %+v", res.Items)
	}
}

// failingEngine makes the Engine unreachable after n removals.
type failingEngine struct {
	*enginefake.Engine
	mu      sync.Mutex
	removes int
	after   int
}

func (f *failingEngine) InspectImage(ctx context.Context, ref string) (engine.ImageDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removes >= f.after {
		return engine.ImageDetails{}, &engine.Error{Op: "image.inspect", Code: engine.CodeEngineUnavailable, Message: "connection refused"}
	}
	return f.Engine.InspectImage(ctx, ref)
}

func (f *failingEngine) RemoveContainer(ctx context.Context, id string, o engine.RemoveOptions) error {
	f.mu.Lock()
	f.removes++
	f.mu.Unlock()
	return f.Engine.RemoveContainer(ctx, id, o)
}

// TestEngineLossAndRetry: the Engine going away mid-run fails the job
// with the items done so far; a resumed attempt (the job engine's retry
// after an interrupted attempt) continues with the pending items only.
func TestEngineLossAndRetry(t *testing.T) {
	w := newWorld(t)
	fe := &failingEngine{Engine: w.fe, after: 2}
	guard := protect.New(protect.Options{StacksVolume: "docker-manager_stacks", Logger: testutil.Logger(t)})
	s := New(Options{Engine: func() engine.Engine { return fe }, Guard: guard, Clock: clock.NewFake(now), Logger: testutil.Logger(t),
		ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacksRoot+"/") }})
	in := input(allRules())
	res, out := runJob(t, s, in, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != string(engine.CodeEngineUnavailable) {
		t.Fatalf("first attempt: %+v", res)
	}
	if st := statuses(out); st["old-exited"] != protocol.PruneItemRemoved || out.Removed != 2 || st["app:old"] != protocol.PruneItemPending {
		t.Fatalf("first output: %+v", out)
	}
	// Resume: collect_candidates is complete, the output carries the
	// statuses; the Engine is back.
	fe.after = 1 << 30
	raw, _ := json.Marshal(in)
	st := &jobexec.State{JobID: "j", Attempt: 2, FencingToken: 2, Kind: jobspec.PruneRun, Input: raw,
		Completed: []string{"collect_candidates"}, Output: res.Output}
	res2, out2 := runJob(t, s, in, &runOpts{state: st})
	if res2.Outcome != jobexec.OutcomeSucceeded || out2.Removed != 11 || out2.Failed != 0 || len(res2.Items) != 9 {
		t.Fatalf("second attempt: %+v %+v", res2, out2)
	}
	// The container removed by attempt 1 is not processed again.
	for _, it := range res2.Items {
		if strings.Contains(it.Name, "old-exited") || strings.Contains(it.Name, "excluded-by-name") {
			t.Fatalf("re-processed: %+v", it)
		}
	}
}

// TestBuildCacheModesAndKeepStorage: dangling vs all, and the keep-storage
// cap keeping the most recently used records.
func TestBuildCacheModesAndKeepStorage(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	all := preview(t, s, input([]protocol.PruneRule{{Category: protocol.PruneBuildCache, BuildCacheAll: true}}))
	if got := decisions(all)[protocol.PruneBuildCache]; !slices.Equal(got, []string{"remove RUN step bc-leaf", "remove RUN step bc-new",
		"remove RUN step bc-root", "remove RUN step bc-shared"}) {
		t.Fatalf("all: %v", got)
	}
	// Unshared cache: 300 + 200 + 500 (in use) + 600 = 1600 bytes. With an
	// 1100-byte cap the oldest records go first: bc-root (1600 -> 1300),
	// bc-leaf (-> 1100); bc-new, the most recently used, stays.
	capped := preview(t, s, input([]protocol.PruneRule{{Category: protocol.PruneBuildCache, KeepStorageBytes: 1100}}))
	if got := decisions(capped)[protocol.PruneBuildCache]; !slices.Equal(got, []string{"remove RUN step bc-leaf", "remove RUN step bc-root",
		"retained RUN step bc-new"}) {
		t.Fatalf("capped: %v", got)
	}
	_, out := runJob(t, s, input([]protocol.PruneRule{{Category: protocol.PruneBuildCache, KeepStorageBytes: 1100}}), nil)
	if out.Removed != 2 || out.BytesReclaimed != 500 {
		t.Fatalf("run: %+v", out)
	}
	if got := w.fe.BuildCacheIDs(); !slices.Equal(got, []string{"bc-inuse", "bc-new", "bc-shared"}) {
		t.Fatalf("left: %v", got)
	}
}

// TestInputValidation: malformed inputs and unsupported configurations
// are refused before anything is listed.
func TestInputValidation(t *testing.T) {
	w := newWorld(t)
	s := w.service(t)
	bad := []protocol.PruneInput{
		{PolicyID: "", Rules: nil},
		{PolicyID: "p", Rules: []protocol.PruneRule{{Category: "system"}}},
		{PolicyID: "p", Rules: []protocol.PruneRule{{Category: protocol.PruneBuildCache, IncludeLabels: []string{"a"}}}},
		{PolicyID: "p", Rules: []protocol.PruneRule{{Category: protocol.PruneNamedVolumes, ContainerStates: []string{"exited"}}}},
		{PolicyID: "p", Rules: []protocol.PruneRule{{Category: protocol.PruneStoppedContainers, ContainerStates: []string{"running"}}}},
		{PolicyID: "p", Rules: []protocol.PruneRule{{Category: protocol.PruneUnusedImages}, {Category: protocol.PruneUnusedImages}}},
		{PolicyID: "p", Rules: []protocol.PruneRule{{Category: protocol.PruneUnusedImages, MinAgeSeconds: -1}}},
	}
	for _, in := range bad {
		raw, _ := json.Marshal(in)
		if _, err := s.Requests()[protocol.ReqMaintenancePreview](testutil.Context(t), raw); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	calls := 0
	for _, c := range w.fe.Calls() {
		if strings.HasSuffix(c, ".list") {
			calls++
		}
	}
	if calls != 0 {
		t.Fatalf("listed before validating: %v", w.fe.Calls())
	}
	var he interface{ ProtocolCode() string }
	raw, _ := json.Marshal(protocol.PruneInput{PolicyID: "p", Rules: []protocol.PruneRule{{Category: "bogus"}}})
	_, err := s.Requests()[protocol.ReqMaintenancePreview](testutil.Context(t), raw)
	if !errors.As(err, &he) || he.ProtocolCode() != protocol.CodeInvalidArgument {
		t.Fatalf("error: %v", err)
	}
}

// TestRunLimitDefersTheRest: a run removes at most PruneRunItemsMax
// candidates; the rest is counted as deferred for the next run.
func TestRunLimitDefersTheRest(t *testing.T) {
	fe := enginefake.New("E")
	for i := range protocol.PruneRunItemsMax + 5 {
		n := "v" + strings.Repeat("x", 3) + string(rune('a'+i%26)) + "-" + time.Duration(i).String()
		fe.AddVolume(n, nil)
		fe.SetVolumeCreated(n, old)
	}
	s := New(Options{Engine: func() engine.Engine { return fe }, Clock: clock.NewFake(now), Logger: testutil.Logger(t)})
	_, out := runJob(t, s, protocol.PruneInput{PolicyID: "p", Rules: []protocol.PruneRule{{Category: protocol.PruneNamedVolumes}}}, nil)
	if out.Removed != protocol.PruneRunItemsMax || out.Deferred != 5 || len(fe.VolumeNames()) != 5 {
		t.Fatalf("removed %d deferred %d left %d", out.Removed, out.Deferred, len(fe.VolumeNames()))
	}
}

// TestMaintenanceExcludeLabel: an object carrying
// docker-manager.maintenance.exclude=true is never removed, whatever the
// rule and however old it is; any other value leaves it to the rule.
func TestMaintenanceExcludeLabel(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	r := protocol.PruneRule{Category: protocol.PruneNamedVolumes}
	labelled := map[string]string{protocol.LabelMaintenanceExclude: "true"}
	if d, why := ruleDecision(r, "data", []string{"data"}, labelled, now.Add(-365*24*time.Hour), now); d != protocol.PruneExcluded ||
		!strings.Contains(why, protocol.LabelMaintenanceExclude) {
		t.Errorf("labelled object: %s %q", d, why)
	}
	if d, _ := ruleDecision(r, "data", []string{"data"}, map[string]string{protocol.LabelMaintenanceExclude: "false"}, now.Add(-time.Hour), now); d != protocol.PruneRemove {
		t.Errorf("label set to false: %s", d)
	}
}
