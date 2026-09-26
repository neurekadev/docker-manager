package protocol

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Docker maintenance: prune policies (#14). The manager sends a policy's
// enabled rules plus the objects it knows must survive (Docker Manager stacks,
// saved container specifications, backup destinations) to the agent: in
// the maintenance.preview request and as the input of prune.run jobs. The
// agent lists the Engine's objects, applies the rules, Docker Manager's
// self-protection (#32) and the manager's protections, and removes each
// candidate with a targeted call after revalidating it. It never calls a
// broad Engine prune endpoint.

// Prune categories, in execution order: containers first (removing them
// frees images, networks and volumes), build cache last.
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

// ValidPruneCategory reports whether c is a prune category.
func ValidPruneCategory(c string) bool { return slices.Contains(pruneCategories, c) }

// AnonymousVolumeLabel marks volumes the Engine created for anonymous
// mounts (Docker 23+); volumes without it are named volumes, as for
// docker volume prune.
const AnonymousVolumeLabel = "com.docker.volume.anonymous"

// Container states a stopped-container rule may select. Running, paused,
// restarting and removing containers are never candidates.
const (
	ContainerStateExited  = "exited"
	ContainerStateCreated = "created"
	ContainerStateDead    = "dead"
)

// PruneContainerStates are the selectable states.
func PruneContainerStates() []string {
	return []string{ContainerStateExited, ContainerStateCreated, ContainerStateDead}
}

// DefaultPruneContainerStates are selected when a rule names none: never
// started ("created") containers are left alone unless selected.
func DefaultPruneContainerStates() []string {
	return []string{ContainerStateExited, ContainerStateDead}
}

// Limits of prune inputs and outputs.
const (
	MaxPruneLabels       = 32
	MaxPruneExclusions   = 256
	MaxPruneProtections  = 4096
	MaxPruneValueLen     = 512
	MaxPruneMinAge       = 10 * 365 * 24 * time.Hour
	MaxPrunePolicyIDLen  = 64
	MaxPruneReasonLen    = 160
	PrunePreviewItemsMax = 200
	// PruneRunItemsMax bounds the candidates one prune.run removes (its
	// output is at most MaxResultOutput); the rest wait for the next run.
	PruneRunItemsMax = 300
)

// PruneRule is one enabled category rule of a policy.
type PruneRule struct {
	Category string `json:"category"`
	// MinAgeSeconds: only objects older than this are candidates (0: any
	// age). Age is measured from when a container stopped (its creation
	// when it never ran), an image's, network's or volume's creation, and
	// a build cache record's last use.
	MinAgeSeconds int64 `json:"minAgeSeconds,omitempty"`
	// IncludeLabels: candidates must carry every one ("key" or
	// "key=value"). ExcludeLabels: a candidate carrying any is excluded.
	IncludeLabels []string `json:"includeLabels,omitempty"`
	ExcludeLabels []string `json:"excludeLabels,omitempty"`
	// Exclude lists IDs (full or a prefix of at least 12 characters) and
	// names that are never removed.
	Exclude []string `json:"exclude,omitempty"`
	// ContainerStates (stopped_containers only; default exited and dead).
	ContainerStates []string `json:"containerStates,omitempty"`
	// BuildCacheAll (build_cache only): all unused records, including
	// shared, internal and frontend ones; default: dangling only.
	BuildCacheAll bool `json:"buildCacheAll,omitempty"`
	// KeepStorageBytes (build_cache only): keep the most recently used
	// cache up to this total size (0: no cap).
	KeepStorageBytes int64 `json:"keepStorageBytes,omitempty"`
}

// MinAge returns the rule's age threshold.
func (r PruneRule) MinAge() time.Duration { return time.Duration(r.MinAgeSeconds) * time.Second }

// States returns the selected container states (defaults applied).
func (r PruneRule) States() []string {
	if len(r.ContainerStates) == 0 {
		return DefaultPruneContainerStates()
	}
	return r.ContainerStates
}

// ProtectedRef is an object the manager protects, with the reason shown in
// previews and results.
type ProtectedRef struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// PruneProtection are the objects the manager knows must survive.
type PruneProtection struct {
	// Projects are the Compose projects of Docker Manager stacks: their
	// containers, networks and volumes are never candidates.
	Projects []ProtectedRef `json:"projects,omitempty"`
	// Images are image references (normalized by the agent) or IDs used
	// by stack definitions and saved container specifications.
	Images []ProtectedRef `json:"images,omitempty"`
	// Volumes and Networks are names from saved container specifications
	// and backup destinations (#10).
	Volumes  []ProtectedRef `json:"volumes,omitempty"`
	Networks []ProtectedRef `json:"networks,omitempty"`
}

// PruneInput is the maintenance.preview request and the prune.run job
// input.
type PruneInput struct {
	PolicyID string          `json:"policyId"`
	Rules    []PruneRule     `json:"rules"`
	Protect  PruneProtection `json:"protect"`
}

// Rule returns the rule of a category.
func (in PruneInput) Rule(category string) (PruneRule, bool) {
	for _, r := range in.Rules {
		if r.Category == category {
			return r, true
		}
	}
	return PruneRule{}, false
}

// Validate checks the input.
func (in PruneInput) Validate() error {
	if in.PolicyID == "" || len(in.PolicyID) > MaxPrunePolicyIDLen {
		return fieldErr("policyId", "must be 1 to %d characters", MaxPrunePolicyIDLen)
	}
	if len(in.Rules) > len(pruneCategories) {
		return fieldErr("rules", "at most one rule per category")
	}
	seen := map[string]bool{}
	for i, r := range in.Rules {
		if seen[r.Category] {
			return fieldErr("rules", "category %s appears twice", r.Category)
		}
		seen[r.Category] = true
		if err := r.Validate(); err != nil {
			var fe *FieldError
			if errors.As(err, &fe) {
				return fieldErr("rules["+strconv.Itoa(i)+"]."+fe.Field, "%s", fe.Message)
			}
			return err
		}
	}
	p := in.Protect
	for field, refs := range map[string][]ProtectedRef{"protect.projects": p.Projects, "protect.images": p.Images,
		"protect.volumes": p.Volumes, "protect.networks": p.Networks} {
		if len(refs) > MaxPruneProtections {
			return fieldErr(field, "at most %d entries", MaxPruneProtections)
		}
		for _, r := range refs {
			if !validPruneValue(r.Ref) || len(r.Reason) > MaxPruneValueLen {
				return fieldErr(field, "invalid entry")
			}
		}
	}
	return nil
}

// Validate checks one rule.
func (r PruneRule) Validate() error {
	if !ValidPruneCategory(r.Category) {
		return fieldErr("category", "unknown category %q", r.Category)
	}
	if r.MinAgeSeconds < 0 || r.MinAge() > MaxPruneMinAge {
		return fieldErr("minAgeSeconds", "must be between 0 and 10 years")
	}
	if err := validLabelFilters("includeLabels", r.IncludeLabels); err != nil {
		return err
	}
	if err := validLabelFilters("excludeLabels", r.ExcludeLabels); err != nil {
		return err
	}
	if len(r.Exclude) > MaxPruneExclusions {
		return fieldErr("exclude", "at most %d entries", MaxPruneExclusions)
	}
	for _, e := range r.Exclude {
		if !validPruneValue(e) {
			return fieldErr("exclude", "entries must be 1 to %d characters without control characters", MaxPruneValueLen)
		}
	}
	if r.Category == PruneStoppedContainers {
		for _, s := range r.ContainerStates {
			if !slices.Contains(PruneContainerStates(), s) {
				return fieldErr("containerStates", "unknown state %q (exited, created or dead)", s)
			}
		}
	} else if len(r.ContainerStates) > 0 {
		return fieldErr("containerStates", "only stopped-container rules select states")
	}
	if r.Category == PruneBuildCache {
		if len(r.IncludeLabels) > 0 || len(r.ExcludeLabels) > 0 {
			return fieldErr("includeLabels", "build cache records have no labels; exclude records by ID instead")
		}
		if r.KeepStorageBytes < 0 {
			return fieldErr("keepStorageBytes", "must not be negative")
		}
	} else if r.BuildCacheAll || r.KeepStorageBytes != 0 {
		return fieldErr("buildCacheAll", "only build cache rules have these options")
	}
	return nil
}

func validLabelFilters(field string, fs []string) error {
	if len(fs) > MaxPruneLabels {
		return fieldErr(field, "at most %d labels", MaxPruneLabels)
	}
	for _, f := range fs {
		k, _, _ := strings.Cut(f, "=")
		if k == "" || !validPruneValue(f) || strings.TrimSpace(k) != k {
			return fieldErr(field, "%q must be key or key=value", f)
		}
	}
	return nil
}

func validPruneValue(s string) bool {
	if s == "" || len(s) > MaxPruneValueLen {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// MatchLabel reports whether labels carry a "key" or "key=value" filter.
func MatchLabel(labels map[string]string, filter string) bool {
	k, v, hasV := strings.Cut(filter, "=")
	got, ok := labels[k]
	return ok && (!hasV || got == v)
}

// Decisions of a preview item.
const (
	// PruneRemove: a candidate the run removes (after revalidation).
	PruneRemove = "remove"
	// PruneProtected: Docker Manager's own, part of a Docker Manager stack or saved
	// container specification, a backup destination or a predefined
	// Docker network; never removed.
	PruneProtected = "protected"
	// PruneExcluded: excluded by the rule (IDs, names, labels).
	PruneExcluded = "excluded"
	// PruneRetained: matches the category but is newer than the age
	// threshold or within the build cache keep-storage cap.
	PruneRetained = "retained"
)

// PruneItem is one object of a preview.
type PruneItem struct {
	Category string `json:"category"`
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	// Bytes is the approximate space the removal frees (-1: unknown).
	Bytes int64 `json:"bytes"`
	// Since is the time the age threshold is measured from.
	Since time.Time `json:"since,omitzero"`
}

// PruneCategoryPlan is one category of a preview.
type PruneCategoryPlan struct {
	Category  string `json:"category"`
	Remove    int    `json:"remove"`
	Protected int    `json:"protected"`
	Excluded  int    `json:"excluded"`
	Retained  int    `json:"retained"`
	// Bytes sums the known sizes of the candidates; UnknownSizes counts
	// candidates whose size the Engine does not report.
	Bytes        int64 `json:"bytes"`
	UnknownSizes int   `json:"unknownSizes"`
	// Items lists candidates first, then protected, excluded and retained
	// objects (at most PrunePreviewItemsMax; Truncated when more).
	Items     []PruneItem `json:"items"`
	Truncated bool        `json:"truncated"`
}

// PrunePreviewOutput is the maintenance.preview answer.
type PrunePreviewOutput struct {
	At         time.Time           `json:"at"`
	Categories []PruneCategoryPlan `json:"categories"`
}

// Item states of a prune run.
const (
	PruneItemPending = "pending"
	PruneItemRemoved = "removed"
	PruneItemSkipped = "skipped"
	PruneItemFailed  = "failed"
)

// PruneRunItem is one candidate of a run and its result.
type PruneRunItem struct {
	Category string `json:"category"`
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	// Bytes: planned (approximate) size while pending, reclaimed once
	// removed (-1: unknown).
	Bytes int64 `json:"bytes"`
}

// PruneRunOutput is the result output of a prune.run job (journaled after
// every item).
type PruneRunOutput struct {
	Items []PruneRunItem `json:"items"`
	// Counts of the collected plan.
	Protected int `json:"protected"`
	Excluded  int `json:"excluded"`
	Retained  int `json:"retained"`
	// Deferred counts candidates beyond PruneRunItemsMax, left for the next run.
	Deferred int `json:"deferred"`
	// Totals of the deletion step.
	Removed        int   `json:"removed"`
	Skipped        int   `json:"skipped"`
	Failed         int   `json:"failed"`
	BytesReclaimed int64 `json:"bytesReclaimed"`
}
