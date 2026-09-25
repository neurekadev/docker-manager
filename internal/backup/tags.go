package backup

import (
	"strings"
)

// Snapshot tags. Every snapshot DockYard writes carries TagDockYard, its
// set and its item, so repositories stay self-describing without the
// manager database (#24).
const (
	// TagDockYard marks data snapshots written by DockYard.
	TagDockYard = "dockyard"
	// TagManifest marks manifest snapshots (one small file).
	TagManifest = "dockyard-manifest"
	// TagManagerState marks manager-state snapshots.
	TagManagerState = "dockyard-manager-state"

	tagSet    = "set:"
	tagPolicy = "policy:"
	tagItem   = "item:"
)

// SetTag tags every snapshot of a backup set.
func SetTag(setID string) string { return tagSet + setID }

// PolicyTag tags snapshots of a policy's runs (retention selects by it).
func PolicyTag(policyID string) string { return tagPolicy + policyID }

// ItemTag tags a snapshot with its item key (retention groups by it).
func ItemTag(item string) string { return tagItem + item }

// Item keys: what one snapshot contains.
const (
	ItemManagerState = "manager"
)

// StackItem is the item key of a stack's snapshot.
func StackItem(stackID string) string { return "stack/" + stackID }

// VolumeItem is the item key of a standalone volume's snapshot.
func VolumeItem(volume string) string { return "volume/" + volume }

// TagValue returns the value of the first tag with prefix (set:, policy:,
// item:) in tags.
func TagValue(tags []string, prefix string) string {
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, prefix); ok {
			return v
		}
	}
	return ""
}

// SetOf returns a snapshot's set ID.
func SetOf(tags []string) string { return TagValue(tags, tagSet) }

// PolicyOf returns a snapshot's policy ID.
func PolicyOf(tags []string) string { return TagValue(tags, tagPolicy) }

// ItemOf returns a snapshot's item key.
func ItemOf(tags []string) string { return TagValue(tags, tagItem) }

// ManifestFile is the file name of manifest snapshots.
const ManifestFile = "dockyard-manifest.json"
