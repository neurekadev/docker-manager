package domain

import (
	"errors"
	"slices"
	"time"
)

// Docker maintenance policies (#14): per-environment prune policies with
// one rule per resource category. A policy that enables several rules is
// the "system cleanup": an explicit combination of rules, never a broad
// Docker system prune.

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

// MaintenanceRule is one category rule of a policy.
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

// SuggestedMaintenanceRules are DockYard's shipped suggestions: every rule
// disabled, a 30-day threshold, stopped (exited or dead) containers only,
// dangling build cache only. Suggestions prefill new policies; they never
// authorize a run.
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

// MaintenanceRunSummary is the outcome of a policy's latest finished run.
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

// MaintenancePolicy is a prune policy of one environment.
type MaintenancePolicy struct {
	ID            string
	EnvironmentID string
	Name          string
	Description   string
	// Cron, TimeZone and ScheduleEnabled are the policy's own schedule
	// (#13); automatic runs start disabled.
	Cron            string
	TimeZone        string
	ScheduleEnabled bool
	// Rules has one rule per category, in category order.
	Rules     []MaintenanceRule
	LastRun   *MaintenanceRunSummary
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MaintenancePolicyCreate creates a policy; empty schedule fields take the
// instance defaults, missing rules the maintenance defaults.
type MaintenancePolicyCreate struct {
	EnvironmentID   string
	Name            string
	Description     string
	Cron            string
	TimeZone        string
	ScheduleEnabled bool
	Rules           []MaintenanceRule
}

// MaintenancePolicyPatch changes a policy; each given rule replaces the
// rule of its category.
type MaintenancePolicyPatch struct {
	Name            *string
	Description     *string
	Cron            *string
	TimeZone        *string
	ScheduleEnabled *bool
	Rules           []MaintenanceRule
}

// MaintenanceDefaults are the instance's suggested rules for new policies.
type MaintenanceDefaults struct {
	Rules     []MaintenanceRule
	Revision  int64
	UpdatedAt time.Time
}

// Maintenance errors.
var (
	ErrMaintenancePolicyNotFound  = errors.New("maintenance policy not found")
	ErrMaintenancePolicyNameTaken = errors.New("maintenance policy name taken")
	// ErrMaintenancePolicyEmpty: a run needs at least one enabled rule.
	ErrMaintenancePolicyEmpty = errors.New("the maintenance policy has no enabled rule")
)

// MaintenanceRunActiveError refuses a manual run while another run of the
// same policy is not finished.
type MaintenanceRunActiveError struct{ JobID string }

func (e *MaintenanceRunActiveError) Error() string {
	return "a run of this policy is still active (job " + e.JobID + ")"
}
