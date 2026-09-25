package enginefake

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
)

// Prune support (#14): timestamps and sizes tests control, and an
// in-memory BuildKit cache with the Engine's removal rules.

// SetContainerTimes sets a container's creation time and, when finished is
// not zero, the time it stopped.
func (e *Engine) SetContainerTimes(idOrName string, created, finished time.Time) {
	e.mu.Lock()
	defer e.unlock()
	c, ok := e.findContainer(idOrName)
	if !ok {
		panic("enginefake: no container " + idOrName)
	}
	c.Details.Created = created
	if !finished.IsZero() {
		c.Details.State.FinishedAt = finished
	}
}

// SetContainerState sets a container's state ("exited", "created", "dead",
// "running", "paused", "restarting").
func (e *Engine) SetContainerState(idOrName, status string) {
	e.mu.Lock()
	defer e.unlock()
	c, ok := e.findContainer(idOrName)
	if !ok {
		panic("enginefake: no container " + idOrName)
	}
	st := c.Details.State
	st.Status = status
	st.Running = status == "running" || status == "paused" || status == "restarting"
	st.Paused = status == "paused"
	st.Restarting = status == "restarting"
	st.Dead = status == "dead"
	c.Details.State = st
}

// SetContainerSize sets a container's writable layer size.
func (e *Engine) SetContainerSize(idOrName string, n int64) {
	e.mu.Lock()
	defer e.unlock()
	c, ok := e.findContainer(idOrName)
	if !ok {
		panic("enginefake: no container " + idOrName)
	}
	e.sizes[c.Details.ID] = n
}

// SetImageCreated sets an image's creation time.
func (e *Engine) SetImageCreated(ref string, t time.Time) {
	e.mu.Lock()
	defer e.unlock()
	im, ok := e.findImage(ref)
	if !ok {
		panic("enginefake: no image " + ref)
	}
	im.Created = t
}

// SetImageSize sets an image's size.
func (e *Engine) SetImageSize(ref string, n int64) {
	e.mu.Lock()
	defer e.unlock()
	im, ok := e.findImage(ref)
	if !ok {
		panic("enginefake: no image " + ref)
	}
	im.Size = n
}

// SetVolumeCreated sets a volume's creation time.
func (e *Engine) SetVolumeCreated(name string, t time.Time) {
	e.mu.Lock()
	defer e.unlock()
	v, ok := e.volumes[name]
	if !ok {
		panic("enginefake: no volume " + name)
	}
	v.CreatedAt = t
}

// SetVolumeSize sets the size VolumeUsage reports for a volume.
func (e *Engine) SetVolumeSize(name string, n int64) {
	e.mu.Lock()
	defer e.unlock()
	e.volumeSizes[name] = n
}

// SetNetworkCreated sets a network's creation time.
func (e *Engine) SetNetworkCreated(idOrName string, t time.Time) {
	e.mu.Lock()
	defer e.unlock()
	n, ok := e.findNetwork(idOrName)
	if !ok {
		panic("enginefake: no network " + idOrName)
	}
	n.Created = t
}

// AddBuildCache adds a build cache record.
func (e *Engine) AddBuildCache(r engine.BuildCacheRecord) {
	e.mu.Lock()
	defer e.unlock()
	c := r
	c.Parents = slices.Clone(r.Parents)
	e.buildCache[r.ID] = &c
}

// SetBuildCacheInUse marks a build cache record as used by a build.
func (e *Engine) SetBuildCacheInUse(id string, inUse bool) {
	e.mu.Lock()
	defer e.unlock()
	if r, ok := e.buildCache[id]; ok {
		r.InUse = inUse
	}
}

// BuildCacheIDs returns the IDs of the build cache records, sorted.
func (e *Engine) BuildCacheIDs() []string {
	e.mu.Lock()
	defer e.unlock()
	out := make([]string, 0, len(e.buildCache))
	for id := range e.buildCache {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// VolumeNames returns the volume names, sorted.
func (e *Engine) VolumeNames() []string {
	e.mu.Lock()
	defer e.unlock()
	out := make([]string, 0, len(e.volumes))
	for n := range e.volumes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// NetworkNames returns the network names, sorted.
func (e *Engine) NetworkNames() []string {
	e.mu.Lock()
	defer e.unlock()
	out := make([]string, 0, len(e.networks))
	for _, n := range e.networks {
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out
}

// ContainerNames returns the container names, sorted.
func (e *Engine) ContainerNames() []string {
	e.mu.Lock()
	defer e.unlock()
	out := make([]string, 0, len(e.containers))
	for _, c := range e.containers {
		out = append(out, c.Details.Name)
	}
	sort.Strings(out)
	return out
}

// VolumeUsage implements engine.Engine.
func (e *Engine) VolumeUsage(context.Context) (map[string]engine.VolumeUsage, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("volume.usage"); err != nil {
		return nil, err
	}
	out := map[string]engine.VolumeUsage{}
	for name := range e.volumes {
		refs := int64(0)
		for _, c := range e.containers {
			for _, m := range c.Details.Mounts {
				if m.Type == "volume" && m.Name == name {
					refs++
				}
			}
		}
		size, ok := e.volumeSizes[name]
		if !ok {
			size = -1
		}
		out[name] = engine.VolumeUsage{Size: size, RefCount: refs}
	}
	return out, nil
}

// ListBuildCache implements engine.Engine.
func (e *Engine) ListBuildCache(context.Context) ([]engine.BuildCacheRecord, error) {
	e.mu.Lock()
	defer e.unlock()
	if err := e.call("buildcache.list"); err != nil {
		return nil, err
	}
	out := make([]engine.BuildCacheRecord, 0, len(e.buildCache))
	for _, r := range e.buildCache {
		c := *r
		c.Parents = slices.Clone(r.Parents)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (e *Engine) hasChild(id string) bool {
	for _, r := range e.buildCache {
		if slices.Contains(r.Parents, id) {
			return true
		}
	}
	return false
}

// RemoveBuildCache implements engine.Engine with BuildKit's rules: records
// in use (by a build or a child record) are kept; without all, shared,
// internal and frontend records are kept too.
func (e *Engine) RemoveBuildCache(_ context.Context, id string, all bool) (engine.BuildCachePruneResult, error) {
	const op = "buildcache.remove"
	e.mu.Lock()
	defer e.unlock()
	if err := e.call(op); err != nil {
		return engine.BuildCachePruneResult{}, err
	}
	if id == "" {
		return engine.BuildCachePruneResult{}, Err(op, engine.CodeInvalidArgument, "a build cache record ID is required")
	}
	r, ok := e.buildCache[id]
	if !ok || r.InUse || e.hasChild(id) {
		return engine.BuildCachePruneResult{}, nil
	}
	if !all && (r.Shared || r.Type == "internal" || r.Type == "frontend") {
		return engine.BuildCachePruneResult{}, nil
	}
	delete(e.buildCache, id)
	res := engine.BuildCachePruneResult{Deleted: []string{id}}
	if !r.Shared && r.Size > 0 {
		res.SpaceReclaimed = uint64(r.Size)
	}
	return res, nil
}
