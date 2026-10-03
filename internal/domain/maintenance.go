package domain

import (
	"errors"
	"slices"
	"time"
)

// Docker maintenance (#14, #238): one instance-wide setup with one prune
// rule per resource category. Its enabled rules together are the "system
// cleanup": an explicit combination of rules, never a broad Docker system
// prune.

// Prune categories, in execution order.
const (
	PruneStoppedContainers = "stopped_containers"
	PruneDanglingImages    = "dangling_images"
	PruneUnusedImages      = "unused_images"
	PruneUnusedNetworks    = "unused_networks"
	PruneAnonymousVolumes  = "anonymous_volumes"
	PruneNamedVolumes      = "named_volumes"
	PruneBuildCache        = "build_cache"
)

var pruneCategories = []string{PruneStoppedContainers, PruneDanglingImages, PruneUnusedImages, PruneUnusedNetworks,
	PruneAnonymousVolumes, PruneNamedVolumes, PruneBuildCache}

// PruneCategories returns every category in execution order.
func PruneCategories() []string { return slices.Clone(pruneCategories) }

// IsVolumeCategory reports whether a category deletes volume data (its rule
// needs its own explicit opt-in).
func IsVolumeCategory(c string) bool { return c == PruneAnonymousVolumes || c == PruneNamedVolumes }

// SuggestedMinAge is the shipped age threshold of every rule.
const SuggestedMinAge = 30 * 24 * time.Hour

// MaintenanceRule is one category rule of maintenance (or of a one-off prune).
type MaintenanceRule struct {
	Category string
	// Enabled: the rule takes part in runs (every rule starts disabled).
	Enabled bool
	// MinAge: only objects older than this are removed (0: any age).
	MinAge time.Duration
	// IncludeLabels / ExcludeLabels are "key" or "key=value" filters;
	// Exclude lists IDs and names that are never removed.
	IncludeLabels []string
	ExcludeLabels []string
	Exclude       []string
	// ContainerStates (stopped containers only): exited, created, dead.
	ContainerStates []string
	// BuildCacheAll and KeepStorageBytes (build cache only).
	BuildCacheAll    bool
	KeepStorageBytes int64
	// VolumeOptIn is the explicit acknowledgement that the rule deletes
	// volume data; a volume rule cannot be enabled without it.
	VolumeOptIn bool
}

// SuggestedMaintenanceRules are Docker Manager's shipped suggestions: every rule
// disabled, a 30-day threshold, stopped (exited or dead) containers only,
// dangling build cache only. Suggestions are the setup's first rules; they
// never authorize a run.
func SuggestedMaintenanceRules() []MaintenanceRule {
	out := make([]MaintenanceRule, 0, len(pruneCategories))
	for _, c := range pruneCategories {
		r := MaintenanceRule{Category: c, MinAge: SuggestedMinAge}
		if c == PruneStoppedContainers {
			r.ContainerStates = []string{"exited", "dead"}
		}
		out = append(out, r)
	}
	return out
}

// CompleteRules returns one rule per category in category order: the
// given rules, and base for categories they lack.
func CompleteRules(rules, base []MaintenanceRule) []MaintenanceRule {
	out := make([]MaintenanceRule, 0, len(pruneCategories))
	for _, c := range pruneCategories {
		if i := slices.IndexFunc(rules, func(r MaintenanceRule) bool { return r.Category == c }); i >= 0 {
			out = append(out, rules[i])
			continue
		}
		if i := slices.IndexFunc(base, func(r MaintenanceRule) bool { return r.Category == c }); i >= 0 {
			out = append(out, base[i])
			continue
		}
		out = append(out, MaintenanceRule{Category: c, MinAge: SuggestedMinAge})
	}
	return out
}

// EnabledRules returns the enabled rules.
func EnabledRules(rules []MaintenanceRule) []MaintenanceRule {
	var out []MaintenanceRule
	for _, r := range rules {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out
}

// MaintenanceRunSummary is the outcome of the latest finished maintenance run.
type MaintenanceRunSummary struct {
	JobID          string
	State          JobState
	Origin         JobOrigin
	FinishedAt     time.Time
	Removed        int
	Skipped        int
	Failed         int
	Deferred       int
	BytesReclaimed int64
}

// MaintenanceSetup is the one maintenance setup of the instance (#238): it
// covers every environment except the ones left out.
type MaintenanceSetup struct {
	// ID is the policy ID its prune jobs and schedule carry.
	ID string
	// Enabled: scheduled runs (#13) on Cron in TimeZone; starts disabled.
	Enabled  bool
	Cron     string
	TimeZone string
	// Rules has one rule per category, in category order.
	Rules []MaintenanceRule
	// ExcludeEnvironments are the environments left out (IDs).
	ExcludeEnvironments []string
	LastRun             *MaintenanceRunSummary
	Revision            int64
	UpdatedAt           time.Time
}

// Excludes reports whether the setup leaves the environment out.
func (s MaintenanceSetup) Excludes(environmentID string) bool {
	return slices.Contains(s.ExcludeEnvironments, environmentID)
}

// MaintenanceSetupPatch changes the setup; each given rule replaces the
// rule of its category.
type MaintenanceSetupPatch struct {
	Enabled             *bool
	Cron                *string
	TimeZone            *string
	Rules               []MaintenanceRule
	ExcludeEnvironments *[]string
}

// Maintenance errors.
var (
	// ErrMaintenanceEmpty: a run needs at least one enabled rule.
	ErrMaintenanceEmpty = errors.New("maintenance has no enabled rule")
	// ErrMaintenanceNoEnvironments: every environment is left out.
	ErrMaintenanceNoEnvironments = errors.New("maintenance covers no environment")
)

// MaintenanceRunActiveError refuses a manual run while another run of
// maintenance is not finished.
type MaintenanceRunActiveError struct{ JobID string }

func (e *MaintenanceRunActiveError) Error() string {
	return "a maintenance run is still active (job " + e.JobID + ")"
}
