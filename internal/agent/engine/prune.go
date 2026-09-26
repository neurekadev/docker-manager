package engine

import (
	"context"
	"slices"
	"time"

	"github.com/moby/moby/client"
)

// Build cache and disk usage for prune policies (#14). Docker Manager never calls
// the Engine's broad prune endpoints (container/image/network/volume/system
// prune): it lists, filters and removes each candidate itself, so item-level
// exclusions and protections always hold. The one exception is the build
// cache, which has no per-record delete endpoint: RemoveBuildCache calls the
// builder prune endpoint restricted to exactly one record ID.

// diskUsageTimeout bounds /system/df: computing volume sizes walks every
// local volume on the host.
const diskUsageTimeout = 5 * time.Minute

// BuildCacheRecord is one record of the Engine's BuildKit build cache.
type BuildCacheRecord struct {
	ID      string
	Parents []string
	// Type is the BuildKit record type ("regular", "internal", "frontend",
	// "source.local", "source.git.checkout", "exec.cachemount", ...).
	Type        string
	Description string
	// InUse: a running build or a child record uses it. Shared: its layers
	// are shared with images (removing it frees less than Size).
	InUse      bool
	Shared     bool
	Size       int64
	CreatedAt  time.Time
	LastUsedAt time.Time
	UsageCount int
}

// BuildCachePruneResult reports a build cache removal.
type BuildCachePruneResult struct {
	Deleted        []string
	SpaceReclaimed uint64
}

// VolumeUsage is a volume's size and reference count from the Engine's disk
// usage report; -1 means unknown (non-local drivers).
type VolumeUsage struct {
	Size     int64
	RefCount int64
}

// ListBuildCache lists the build cache records.
func (c *Client) ListBuildCache(ctx context.Context) ([]BuildCacheRecord, error) {
	ctx, cancel := c.boundExtra(ctx, diskUsageTimeout)
	defer cancel()
	res, err := c.api.DiskUsage(ctx, client.DiskUsageOptions{BuildCache: true, Verbose: true})
	if err != nil {
		return nil, wrap("buildcache.list", err)
	}
	out := make([]BuildCacheRecord, 0, len(res.BuildCache.Items))
	for _, r := range res.BuildCache.Items {
		rec := BuildCacheRecord{ID: r.ID, Parents: slices.Clone(r.Parents), Type: r.Type, Description: r.Description, InUse: r.InUse,
			Shared: r.Shared, Size: r.Size, CreatedAt: r.CreatedAt.UTC(), UsageCount: r.UsageCount}
		if r.LastUsedAt != nil {
			rec.LastUsedAt = r.LastUsedAt.UTC()
		}
		out = append(out, rec)
	}
	return out, nil
}

// RemoveBuildCache removes one build cache record by ID. all selects the
// Engine's "all unused" semantics; without it BuildKit keeps shared,
// internal and frontend records (the "dangling only" default of docker
// builder prune). The Engine never removes a record in use: the result's
// Deleted list tells whether the record went away. An empty id is refused:
// the adapter never prunes the whole cache.
func (c *Client) RemoveBuildCache(ctx context.Context, id string, all bool) (BuildCachePruneResult, error) {
	const op = "buildcache.remove"
	if id == "" {
		return BuildCachePruneResult{}, newError(op, CodeInvalidArgument, "a build cache record ID is required")
	}
	ctx, cancel := c.boundExtra(ctx, diskUsageTimeout)
	defer cancel()
	res, err := c.api.BuildCachePrune(ctx, client.BuildCachePruneOptions{All: all, Filters: client.Filters{}.Add("id", id)})
	if err != nil {
		return BuildCachePruneResult{}, wrap(op, err)
	}
	// The Engine matches the id filter as a pattern; only the requested
	// record is reported as ours (record IDs have a fixed length, so no
	// other ID contains it).
	return BuildCachePruneResult{Deleted: slices.Clone(res.Report.CachesDeleted), SpaceReclaimed: res.Report.SpaceReclaimed}, nil
}

// VolumeUsage returns the size and reference count of every volume.
func (c *Client) VolumeUsage(ctx context.Context) (map[string]VolumeUsage, error) {
	ctx, cancel := c.boundExtra(ctx, diskUsageTimeout)
	defer cancel()
	res, err := c.api.DiskUsage(ctx, client.DiskUsageOptions{Volumes: true, Verbose: true})
	if err != nil {
		return nil, wrap("volume.usage", err)
	}
	out := make(map[string]VolumeUsage, len(res.Volumes.Items))
	for _, v := range res.Volumes.Items {
		u := VolumeUsage{Size: -1, RefCount: -1}
		if v.UsageData != nil {
			u = VolumeUsage{Size: v.UsageData.Size, RefCount: v.UsageData.RefCount}
		}
		out[v.Name] = u
	}
	return out, nil
}
