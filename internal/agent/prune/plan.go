package prune

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/imageref"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// facts is what every decision needs besides the object itself: the
// moment, the containers (usage), Docker Manager's own objects and every
// protection the manager sent or the agent recognizes.
type facts struct {
	now        time.Time
	containers []engine.Container
	set        *protect.Set
	// projects are Compose projects of Docker Manager stacks (manager-sent, or
	// with a container whose working directory is in a stack root).
	projects map[string]string
	images   map[string]string // normalized reference or image ID -> reason
	volumes  map[string]string
	networks map[string]string
	// volumeSizes are read once per plan (the Engine walks the volumes).
	volumeSizes map[string]engine.VolumeUsage
	sizesRead   bool
}

// gather reads the containers (with sizes when asked) and computes the
// protected sets.
func (s *Service) gather(ctx context.Context, eng engine.Engine, in protocol.PruneInput, sizes bool) (*facts, error) {
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true, Size: sizes})
	if err != nil {
		return nil, err
	}
	f := &facts{now: s.clk.Now().UTC(), containers: cs, set: s.guard.Identify(ctx, eng, cs), projects: map[string]string{},
		images: map[string]string{}, volumes: map[string]string{}, networks: map[string]string{}}
	for _, p := range in.Protect.Projects {
		f.projects[p.Ref] = p.Reason
	}
	for _, c := range cs {
		project := c.Labels[protocol.ComposeProjectLabel]
		if project == "" || f.projects[project] != "" {
			continue
		}
		if dir := c.Labels[protocol.ComposeWorkingDirLabel]; dir != "" && path.IsAbs(dir) && s.opts.ManagedStackDir != nil &&
			s.opts.ManagedStackDir(path.Clean(dir)) {
			f.projects[project] = fmt.Sprintf("part of Docker Manager stack %q", project)
		}
	}
	for _, r := range in.Protect.Images {
		for _, k := range imageKeys(r.Ref) {
			if f.images[k] == "" {
				f.images[k] = r.Reason
			}
		}
	}
	for _, r := range in.Protect.Volumes {
		f.volumes[r.Ref] = r.Reason
	}
	for _, r := range in.Protect.Networks {
		f.networks[r.Ref] = r.Reason
	}
	return f, nil
}

// imageKeys normalizes a reference (or ID) for matching: image IDs as is,
// references as host/repository:tag and host/repository@digest.
func imageKeys(ref string) []string {
	if strings.HasPrefix(ref, "sha256:") {
		return []string{ref}
	}
	r, err := imageref.Parse(ref)
	if err != nil {
		return []string{ref}
	}
	var out []string
	if r.Tag != "" {
		out = append(out, r.Name()+":"+r.Tag)
	}
	if r.Digest != "" {
		out = append(out, r.Name()+"@"+r.Digest)
	}
	return out
}

// Protection reasons of objects the manager or the agent know besides
// Docker Manager's own (#32).
func (f *facts) containerProtection(c engine.Container) string {
	if p := f.set.Container(c.ID); p != nil {
		return p.Reason
	}
	if project := c.Labels[protocol.ComposeProjectLabel]; project != "" && f.projects[project] != "" {
		return f.projects[project]
	}
	if c.Labels[protocol.LabelManaged] == protocol.ManagedStandalone && c.Labels[protocol.LabelSpec] != "" {
		return "created through Docker Manager with a saved recreate specification"
	}
	return ""
}

func (f *facts) imageProtection(id string, tags, digests []string) string {
	if p := f.set.Image(id); p != nil {
		return p.Reason
	}
	if r := f.images[id]; r != "" {
		return r
	}
	for _, t := range append(slices.Clone(tags), digests...) {
		for _, k := range imageKeys(t) {
			if r := f.images[k]; r != "" {
				return r
			}
		}
	}
	return ""
}

func (f *facts) volumeProtection(v engine.Volume) string {
	if p := f.set.Volume(v.Name, v.Labels); p != nil {
		return p.Reason
	}
	if r := f.volumes[v.Name]; r != "" {
		return r
	}
	if project := v.Labels[protocol.ComposeProjectLabel]; project != "" && f.projects[project] != "" {
		return "volume of " + strings.TrimPrefix(f.projects[project], "part of ")
	}
	return ""
}

func (f *facts) networkProtection(n engine.Network) string {
	if isSystemNetwork(n) {
		return "predefined or swarm-scoped Docker network"
	}
	if p := f.set.Network(n.ID, n.Name, n.Labels); p != nil {
		return p.Reason
	}
	if r := f.networks[n.Name]; r != "" {
		return r
	}
	if r := f.networks[n.ID]; r != "" {
		return r
	}
	if project := n.Labels[protocol.ComposeProjectLabel]; project != "" && f.projects[project] != "" {
		return "network of " + strings.TrimPrefix(f.projects[project], "part of ")
	}
	return ""
}

func isSystemNetwork(n engine.Network) bool {
	return slices.Contains(protocol.BuiltinNetworks, n.Name) || n.Name == "ingress" || n.Name == "docker_gwbridge" || n.Scope == "swarm"
}

// usage returns which images, volumes and networks the containers use,
// leaving out the containers in skip (candidates removed earlier in the
// same run).
func usage(cs []engine.Container, skip map[string]bool) (images, volumes, networks map[string][]string) {
	images, volumes, networks = map[string][]string{}, map[string][]string{}, map[string][]string{}
	for _, c := range cs {
		if skip[c.ID] {
			continue
		}
		n := containerName(c)
		images[c.ImageID] = append(images[c.ImageID], n)
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				volumes[m.Name] = append(volumes[m.Name], n)
			}
		}
		for _, net := range c.Networks {
			networks[net] = append(networks[net], n)
		}
	}
	return images, volumes, networks
}

func containerName(c engine.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return shortID(c.ID)
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// ruleDecision applies the maintenance exclude label, a rule's exclusions
// and its age threshold to an object that is in the rule's category and
// not protected (the removal re-check uses it too).
func ruleDecision(r protocol.PruneRule, id string, names []string, labels map[string]string, since, now time.Time) (string, string) {
	if protocol.MaintenanceExcluded(labels) {
		return protocol.PruneExcluded, "carries the label " + protocol.LabelMaintenanceExclude + "=true"
	}
	for _, e := range r.Exclude {
		e = strings.TrimPrefix(e, "/")
		bare := strings.TrimPrefix(e, "sha256:")
		if e == id || (len(bare) >= 12 && strings.HasPrefix(strings.TrimPrefix(id, "sha256:"), bare)) || slices.Contains(names, e) {
			return protocol.PruneExcluded, "excluded by the rule (" + e + ")"
		}
	}
	for _, l := range r.ExcludeLabels {
		if protocol.MatchLabel(labels, l) {
			return protocol.PruneExcluded, "carries the excluded label " + l
		}
	}
	for _, l := range r.IncludeLabels {
		if !protocol.MatchLabel(labels, l) {
			return protocol.PruneExcluded, "does not carry the label " + l + " the rule requires"
		}
	}
	if minAge := r.MinAge(); minAge > 0 {
		if since.IsZero() {
			return protocol.PruneRetained, "age unknown; kept"
		}
		if now.Sub(since) < minAge {
			return protocol.PruneRetained, "newer than " + fmtAge(minAge) + " (since " + since.UTC().Format(time.RFC3339) + ")"
		}
	}
	return protocol.PruneRemove, ""
}

func fmtAge(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		days := int(d / (24 * time.Hour))
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	return d.String()
}

// plan is the evaluation of every enabled rule.
type plan struct {
	at   time.Time
	cats []categoryPlan
}

type categoryPlan struct {
	category string
	items    []protocol.PruneItem
}

// candidates returns the remove decisions in execution order.
func (p *plan) candidates() []protocol.PruneItem {
	var out []protocol.PruneItem
	for _, c := range p.cats {
		for _, it := range c.items {
			if it.Decision == protocol.PruneRemove {
				out = append(out, it)
			}
		}
	}
	return out
}

func (p *plan) count(decision string) int {
	n := 0
	for _, c := range p.cats {
		for _, it := range c.items {
			if it.Decision == decision {
				n++
			}
		}
	}
	return n
}

var decisionOrder = map[string]int{protocol.PruneRemove: 0, protocol.PruneProtected: 1, protocol.PruneExcluded: 2, protocol.PruneRetained: 3}

// preview renders the plan for the maintenance.preview answer.
func (p *plan) preview() protocol.PrunePreviewOutput {
	out := protocol.PrunePreviewOutput{At: p.at, Categories: []protocol.PruneCategoryPlan{}}
	for _, c := range p.cats {
		cp := protocol.PruneCategoryPlan{Category: c.category, Items: []protocol.PruneItem{}}
		items := slices.Clone(c.items)
		slices.SortStableFunc(items, func(a, b protocol.PruneItem) int {
			return cmp.Compare(decisionOrder[a.Decision], decisionOrder[b.Decision])
		})
		for _, it := range items {
			switch it.Decision {
			case protocol.PruneRemove:
				cp.Remove++
				if it.Bytes < 0 {
					cp.UnknownSizes++
				} else {
					cp.Bytes += it.Bytes
				}
			case protocol.PruneProtected:
				cp.Protected++
			case protocol.PruneExcluded:
				cp.Excluded++
			case protocol.PruneRetained:
				cp.Retained++
			}
			if len(cp.Items) < protocol.PrunePreviewItemsMax {
				cp.Items = append(cp.Items, it)
			} else {
				cp.Truncated = true
			}
		}
		out.Categories = append(out.Categories, cp)
	}
	return out
}

// plan evaluates the input's rules against the Engine now.
func (s *Service) plan(ctx context.Context, eng engine.Engine, in protocol.PruneInput) (*plan, error) {
	_, withContainers := in.Rule(protocol.PruneStoppedContainers)
	f, err := s.gather(ctx, eng, in, withContainers)
	if err != nil {
		return nil, err
	}
	p := &plan{at: f.now}
	removed := map[string]bool{}
	for _, cat := range protocol.PruneCategories() {
		r, ok := in.Rule(cat)
		if !ok {
			continue
		}
		var items []protocol.PruneItem
		switch cat {
		case protocol.PruneStoppedContainers:
			items, err = s.planContainers(ctx, eng, f, r)
			for _, it := range items {
				if it.Decision == protocol.PruneRemove {
					removed[it.ID] = true
				}
			}
		case protocol.PruneDanglingImages, protocol.PruneUnusedImages:
			items, err = s.planImages(ctx, eng, f, r, removed, p)
		case protocol.PruneUnusedNetworks:
			items, err = s.planNetworks(ctx, eng, f, r, removed)
		case protocol.PruneAnonymousVolumes, protocol.PruneNamedVolumes:
			items, err = s.planVolumes(ctx, eng, f, r, removed)
		case protocol.PruneBuildCache:
			items, err = s.planBuildCache(ctx, eng, f, r)
		}
		if err != nil {
			return nil, err
		}
		p.cats = append(p.cats, categoryPlan{category: cat, items: items})
	}
	return p, nil
}

// containerSince is when a container stopped (its creation when it never
// ran).
func containerSince(c engine.Container, d engine.ContainerDetails) time.Time {
	if c.State != protocol.ContainerStateCreated && !d.State.FinishedAt.IsZero() && d.State.FinishedAt.Year() > 1 {
		return d.State.FinishedAt.UTC()
	}
	return c.Created.UTC()
}

// containerItem evaluates a stopped container; ok is false when it is not
// in the rule's category (running, paused, another state).
func (f *facts) containerItem(r protocol.PruneRule, c engine.Container, d engine.ContainerDetails, withSize bool) (protocol.PruneItem, bool) {
	if !slices.Contains(r.States(), c.State) {
		return protocol.PruneItem{}, false
	}
	it := protocol.PruneItem{Category: r.Category, ID: c.ID, Name: containerName(c), Bytes: -1, Since: containerSince(c, d)}
	if withSize {
		it.Bytes = c.SizeRw
	}
	if reason := f.containerProtection(c); reason != "" {
		it.Decision, it.Reason = protocol.PruneProtected, reason
		return it, true
	}
	names := []string{containerName(c)}
	it.Decision, it.Reason = ruleDecision(r, c.ID, names, c.Labels, it.Since, f.now)
	if it.Decision == protocol.PruneRemove {
		it.Reason = c.State + " since " + it.Since.Format(time.RFC3339)
	}
	return it, true
}

func (s *Service) planContainers(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule) ([]protocol.PruneItem, error) {
	var out []protocol.PruneItem
	for _, c := range f.containers {
		if !slices.Contains(r.States(), c.State) {
			continue
		}
		d, err := eng.InspectContainer(ctx, c.ID)
		if engine.CodeOf(err) == engine.CodeNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if it, ok := f.containerItem(r, c, d, true); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// realTags drops the "<none>:<none>" placeholder of old Engines.
func realTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if t != "<none>:<none>" && t != "" {
			out = append(out, t)
		}
	}
	return out
}

func realDigests(ds []string) []string {
	var out []string
	for _, d := range ds {
		if d != "<none>@<none>" && d != "" {
			out = append(out, d)
		}
	}
	return out
}

// imageItem evaluates an unused image; ok is false when it is not in the
// rule's category (tagged images for the dangling rule).
func (f *facts) imageItem(r protocol.PruneRule, id string, tags, digests []string, labels map[string]string, created time.Time, size int64) (protocol.PruneItem, bool) {
	tags, digests = realTags(tags), realDigests(digests)
	if r.Category == protocol.PruneDanglingImages && len(tags) > 0 {
		return protocol.PruneItem{}, false
	}
	name := shortID(id)
	if len(tags) > 0 {
		name = tags[0]
	}
	it := protocol.PruneItem{Category: r.Category, ID: id, Name: name, Bytes: size, Since: created.UTC()}
	if reason := f.imageProtection(id, tags, digests); reason != "" {
		it.Decision, it.Reason = protocol.PruneProtected, reason
		return it, true
	}
	names := slices.Clone(tags)
	for _, e := range r.Exclude {
		// "nginx" excludes docker.io/library/nginx:latest like the Engine.
		for _, k := range imageKeys(e) {
			for _, t := range tags {
				if slices.Contains(imageKeys(t), k) {
					names = append(names, e)
				}
			}
		}
	}
	it.Decision, it.Reason = ruleDecision(r, id, names, labels, it.Since, f.now)
	if it.Decision == protocol.PruneRemove {
		if len(tags) == 0 {
			it.Reason = "dangling (untagged) and unused"
		} else {
			it.Reason = "not used by any container"
		}
	}
	return it, true
}

func (s *Service) planImages(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule, removed map[string]bool, p *plan) ([]protocol.PruneItem, error) {
	ims, err := eng.ListImages(ctx, false)
	if err != nil {
		return nil, err
	}
	users, _, _ := usage(f.containers, removed)
	// An image the dangling rule already covers is not listed again by
	// the unused rule.
	seen := map[string]bool{}
	for _, c := range p.cats {
		if c.category == protocol.PruneDanglingImages {
			for _, it := range c.items {
				seen[it.ID] = true
			}
		}
	}
	var out []protocol.PruneItem
	for _, im := range ims {
		if len(users[im.ID]) > 0 || seen[im.ID] {
			continue
		}
		if it, ok := f.imageItem(r, im.ID, im.RepoTags, im.RepoDigests, im.Labels, im.Created, im.Size); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// networkItem evaluates an unused network.
func (f *facts) networkItem(r protocol.PruneRule, n engine.Network) protocol.PruneItem {
	it := protocol.PruneItem{Category: r.Category, ID: n.ID, Name: n.Name, Bytes: 0, Since: n.Created.UTC()}
	if reason := f.networkProtection(n); reason != "" {
		it.Decision, it.Reason = protocol.PruneProtected, reason
		return it
	}
	it.Decision, it.Reason = ruleDecision(r, n.ID, []string{n.Name}, n.Labels, it.Since, f.now)
	if it.Decision == protocol.PruneRemove {
		it.Reason = "no container uses it"
	}
	return it
}

func (s *Service) planNetworks(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule, removed map[string]bool) ([]protocol.PruneItem, error) {
	nets, err := eng.ListNetworks(ctx)
	if err != nil {
		return nil, err
	}
	_, _, used := usage(f.containers, removed)
	var out []protocol.PruneItem
	for _, n := range nets {
		if isSystemNetwork(n) {
			out = append(out, f.networkItem(r, n))
			continue
		}
		if len(used[n.Name]) > 0 || len(used[n.ID]) > 0 {
			continue
		}
		d, err := eng.InspectNetwork(ctx, n.ID)
		if engine.CodeOf(err) == engine.CodeNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if attached(d, removed) {
			continue
		}
		out = append(out, f.networkItem(r, d))
	}
	return out, nil
}

// attached reports whether a network has endpoints of containers other
// than those in skip.
func attached(n engine.Network, skip map[string]bool) bool {
	for id := range n.Containers {
		if !skip[id] {
			return true
		}
	}
	return false
}

// volumeCategory is the category of a volume: anonymous volumes carry the
// Engine's anonymous label.
func volumeCategory(v engine.Volume) string {
	if _, ok := v.Labels[protocol.AnonymousVolumeLabel]; ok {
		return protocol.PruneAnonymousVolumes
	}
	return protocol.PruneNamedVolumes
}

// volumeItem evaluates an unused volume; ok is false for the other volume
// category.
func (f *facts) volumeItem(r protocol.PruneRule, v engine.Volume, size int64) (protocol.PruneItem, bool) {
	if volumeCategory(v) != r.Category {
		return protocol.PruneItem{}, false
	}
	it := protocol.PruneItem{Category: r.Category, ID: v.Name, Name: v.Name, Bytes: size, Since: v.CreatedAt.UTC()}
	if reason := f.volumeProtection(v); reason != "" {
		it.Decision, it.Reason = protocol.PruneProtected, reason
		return it, true
	}
	it.Decision, it.Reason = ruleDecision(r, v.Name, []string{v.Name}, v.Labels, it.Since, f.now)
	if it.Decision == protocol.PruneRemove {
		it.Reason = "no container mounts it; its data is deleted"
	}
	return it, true
}

func (s *Service) planVolumes(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule, removed map[string]bool) ([]protocol.PruneItem, error) {
	vols, err := eng.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	if !f.sizesRead {
		f.sizesRead = true
		if f.volumeSizes, err = eng.VolumeUsage(ctx); err != nil {
			// Sizes are informational: the plan stays valid without them.
			s.log.Warn("could not read volume sizes", "error", err)
			f.volumeSizes = nil
		}
	}
	sizes := f.volumeSizes
	_, used, _ := usage(f.containers, removed)
	var out []protocol.PruneItem
	for _, v := range vols {
		if len(used[v.Name]) > 0 {
			continue
		}
		size := int64(-1)
		if u, ok := sizes[v.Name]; ok {
			size = u.Size
		}
		if it, ok := f.volumeItem(r, v, size); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// buildCacheSince is a record's last use (its creation when never used).
func buildCacheSince(rec engine.BuildCacheRecord) time.Time {
	if !rec.LastUsedAt.IsZero() {
		return rec.LastUsedAt.UTC()
	}
	return rec.CreatedAt.UTC()
}

// buildCacheEligible reports whether a record is in the rule's category:
// unused, and for the dangling default neither shared with images nor an
// internal/frontend record.
func buildCacheEligible(r protocol.PruneRule, rec engine.BuildCacheRecord) bool {
	if rec.InUse {
		return false
	}
	return r.BuildCacheAll || (!rec.Shared && rec.Type != "internal" && rec.Type != "frontend")
}

// cacheTotal is the build cache's disk use (shared records count with the
// images that share them).
func cacheTotal(recs []engine.BuildCacheRecord) int64 {
	var n int64
	for _, r := range recs {
		if !r.Shared && r.Size > 0 {
			n += r.Size
		}
	}
	return n
}

func (s *Service) planBuildCache(ctx context.Context, eng engine.Engine, f *facts, r protocol.PruneRule) ([]protocol.PruneItem, error) {
	recs, err := eng.ListBuildCache(ctx)
	if err != nil {
		return nil, err
	}
	var out []protocol.PruneItem
	for _, rec := range recs {
		if !buildCacheEligible(r, rec) {
			continue
		}
		it := protocol.PruneItem{Category: r.Category, ID: rec.ID, Name: cacheName(rec), Bytes: rec.Size, Since: buildCacheSince(rec)}
		it.Decision, it.Reason = ruleDecision(r, rec.ID, nil, nil, it.Since, f.now)
		if it.Decision == protocol.PruneRemove {
			it.Reason = "unused build cache (last used " + it.Since.Format(time.RFC3339) + ")"
			if rec.Shared {
				it.Reason += "; shared with images, frees less than its size"
			}
		}
		out = append(out, it)
	}
	// Oldest first: the keep-storage cap keeps the most recently used.
	slices.SortStableFunc(out, func(a, b protocol.PruneItem) int { return a.Since.Compare(b.Since) })
	if limit := r.KeepStorageBytes; limit > 0 {
		total := cacheTotal(recs)
		shared := map[string]bool{}
		for _, rec := range recs {
			shared[rec.ID] = rec.Shared
		}
		for i := range out {
			if out[i].Decision != protocol.PruneRemove {
				continue
			}
			if total <= limit {
				out[i].Decision, out[i].Reason = protocol.PruneRetained, fmt.Sprintf("kept within the keep-storage cap (%d bytes)", limit)
				continue
			}
			if !shared[out[i].ID] && out[i].Bytes > 0 {
				total -= out[i].Bytes
			}
		}
	}
	return orderChildrenFirst(out, recs), nil
}

func cacheName(rec engine.BuildCacheRecord) string {
	d := rec.Description
	if len(d) > 80 {
		d = d[:80]
	}
	if d == "" {
		return rec.Type
	}
	return d
}

// orderChildrenFirst keeps the order of items but moves a record after
// every candidate that names it as parent: BuildKit keeps a parent while a
// child exists.
func orderChildrenFirst(items []protocol.PruneItem, recs []engine.BuildCacheRecord) []protocol.PruneItem {
	parents := map[string][]string{}
	for _, r := range recs {
		parents[r.ID] = r.Parents
	}
	pending := map[string]int{} // candidate -> candidate children not yet placed
	isCandidate := map[string]bool{}
	for _, it := range items {
		if it.Decision == protocol.PruneRemove {
			isCandidate[it.ID] = true
		}
	}
	for id := range isCandidate {
		for _, p := range parents[id] {
			if isCandidate[p] {
				pending[p]++
			}
		}
	}
	out := make([]protocol.PruneItem, 0, len(items))
	placed := map[string]bool{}
	for len(out) < len(items) {
		progressed := false
		for _, it := range items {
			if placed[it.ID+"\x00"+it.Decision] || pending[it.ID] > 0 {
				continue
			}
			placed[it.ID+"\x00"+it.Decision] = true
			out = append(out, it)
			progressed = true
			if isCandidate[it.ID] && it.Decision == protocol.PruneRemove {
				for _, p := range parents[it.ID] {
					if isCandidate[p] {
						pending[p]--
					}
				}
			}
			break
		}
		if !progressed { // a cycle cannot happen; keep the rest as is
			for _, it := range items {
				if !placed[it.ID+"\x00"+it.Decision] {
					placed[it.ID+"\x00"+it.Decision] = true
					out = append(out, it)
				}
			}
		}
	}
	return out
}
