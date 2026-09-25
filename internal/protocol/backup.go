package protocol

import (
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/restic"
)

// Backups (#10): wire types of the backup.* job commands, the
// backup.scope_preview / backup.snapshots / backup.contents requests and
// the backup.file stream. The agent runs restic against the physical
// repository of its environment scope below the named destination; the
// credentials (Recovery Key, S3 key pair) travel only in the command's
// secrets (CommandSecrets.Repositories) or, for requests and streams, in
// the input's Credential field, and are never journaled or logged.

// BackupRepositoryRef names the physical repository a job or request uses.
type BackupRepositoryRef struct {
	RepositoryID string             `json:"repositoryId"`
	Destination  backup.Destination `json:"destination"`
	// Scope is the environment scope (backup.EnvironmentScope).
	Scope string `json:"scope"`
	// KeyGeneration is the manager's current Recovery Key generation; a
	// location opened with the previous key is moved to the current one.
	KeyGeneration int `json:"keyGeneration"`
	// KeyFingerprint identifies the current key (manifests).
	KeyFingerprint string `json:"keyFingerprint,omitempty"`
}

// Validate checks the reference's shape.
func (r BackupRepositoryRef) Validate() error {
	if r.RepositoryID == "" || len(r.RepositoryID) > 64 {
		return errors.New("backup repository: invalid repository ID")
	}
	if err := r.Destination.Validate(); err != nil {
		return fmt.Errorf("backup repository: %w", err)
	}
	if _, ok := backup.ScopeEnvironment(r.Scope); !ok || !backup.ValidScope(r.Scope) {
		return errors.New("backup repository: invalid environment scope")
	}
	return nil
}

// RepositoryCredential opens one backup repository for one attempt,
// request or stream. It is never journaled, stored or logged.
type RepositoryCredential struct {
	RepositoryID string `json:"repositoryId"`
	// Password is the current Recovery Key; PreviousPassword the key it
	// replaces while a rotation is in progress (else empty).
	Password         string `json:"password"`
	PreviousPassword string `json:"previousPassword,omitempty"`
	AccessKeyID      string `json:"accessKeyId,omitempty"`
	SecretAccessKey  string `json:"secretAccessKey,omitempty"`
}

// String hides the secret values.
func (c *RepositoryCredential) String() string {
	if c == nil {
		return "<nil>"
	}
	return "RepositoryCredential{" + c.RepositoryID + ", redacted}"
}

// GoString hides the secret values (%#v).
func (c *RepositoryCredential) GoString() string { return c.String() }

// LogValue hides the secret values in slog.
func (c *RepositoryCredential) LogValue() slog.Value { return slog.StringValue(c.String()) }

// S3 returns the S3 credentials.
func (c *RepositoryCredential) S3() backup.S3Credentials {
	if c == nil {
		return backup.S3Credentials{}
	}
	return backup.S3Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey}
}

// RepositoryCredentialFor returns the credential of a repository in s.
func (s *CommandSecrets) RepositoryCredentialFor(repositoryID string) *RepositoryCredential {
	if s == nil {
		return nil
	}
	for i := range s.Repositories {
		if s.Repositories[i].RepositoryID == repositoryID {
			return &s.Repositories[i]
		}
	}
	return nil
}

// BackupItem is one stack or standalone volume of a backup.
type BackupItem struct {
	// Kind is backup.MemberStack or backup.MemberVolume.
	Kind      string      `json:"kind"`
	StackID   string      `json:"stackId,omitempty"`
	StackName string      `json:"stackName,omitempty"`
	Project   *ProjectRef `json:"project,omitempty"`
	// Volume is a standalone volume's Docker name.
	Volume string      `json:"volume,omitempty"`
	Rules  BackupRules `json:"rules"`
}

// Key is the item key (backup.StackItem / backup.VolumeItem).
func (i BackupItem) Key() string {
	if i.Kind == backup.MemberStack {
		return backup.StackItem(i.StackID)
	}
	return backup.VolumeItem(i.Volume)
}

// BackupRules are the policy's per-item filters.
type BackupRules struct {
	// VolumeExclude lists named volumes (Compose keys or Docker names) of a
	// stack that are not backed up; VolumeInclude, when not empty, limits
	// the stack's named volumes to these.
	VolumeInclude []string `json:"volumeInclude,omitempty"`
	VolumeExclude []string `json:"volumeExclude,omitempty"`
	// AnonymousVolumes includes the stack's anonymous volumes (default off).
	AnonymousVolumes bool `json:"anonymousVolumes,omitempty"`
	// PathExcludes are paths relative to the project directory (stacks) or
	// the volume root (volumes) that are not backed up; a trailing "/**"
	// is implied for directories.
	PathExcludes []string `json:"pathExcludes,omitempty"`
	// ExternalPaths are absolute bind sources outside the project directory
	// the policy explicitly opts into; each must also be allowed by the
	// agent's DOCKYARD_BACKUP_EXTERNAL_ALLOWLIST.
	ExternalPaths []string `json:"externalPaths,omitempty"`
}

// MaxBackupItems bounds the items of one backup job.
const MaxBackupItems = 64

// Validate checks the rules' shape.
func (r BackupRules) Validate() error {
	for _, v := range append(append([]string{}, r.VolumeInclude...), r.VolumeExclude...) {
		if !ValidVolumeName(v) {
			return fmt.Errorf("invalid volume name %q", v)
		}
	}
	for _, p := range r.PathExcludes {
		if !ValidRelativePath(p) || p == "." {
			return fmt.Errorf("path exclude %q must be a relative path inside the project or volume", p)
		}
	}
	for _, p := range r.ExternalPaths {
		if !backup.AbsPath(p) {
			return fmt.Errorf("external path %q must be a clean absolute path other than /", p)
		}
	}
	if len(r.PathExcludes) > 256 || len(r.ExternalPaths) > 64 || len(r.VolumeInclude)+len(r.VolumeExclude) > 256 {
		return errors.New("too many rules")
	}
	return nil
}

// Validate checks an item's shape.
func (i BackupItem) Validate() error {
	switch i.Kind {
	case backup.MemberStack:
		if i.StackID == "" || i.Project == nil {
			return errors.New("stack item: stack ID and project are required")
		}
		if err := i.Project.Validate(); err != nil {
			return err
		}
	case backup.MemberVolume:
		if !ValidVolumeName(i.Volume) {
			return fmt.Errorf("volume item: invalid volume name %q", i.Volume)
		}
		if len(i.Rules.ExternalPaths) > 0 || len(i.Rules.VolumeInclude)+len(i.Rules.VolumeExclude) > 0 || i.Rules.AnonymousVolumes {
			return errors.New("volume item: only path excludes apply")
		}
	default:
		return fmt.Errorf("unknown item kind %q", i.Kind)
	}
	return i.Rules.Validate()
}

// BackupRunInput is the input of backup.run.
type BackupRunInput struct {
	SetID      string              `json:"setId"`
	PolicyID   string              `json:"policyId,omitempty"`
	PolicyName string              `json:"policyName,omitempty"`
	InstanceID string              `json:"instanceId"`
	Repository BackupRepositoryRef `json:"repository"`
	// Shutdown stops the affected containers during the snapshot and
	// restarts the previously running ones afterwards (default off).
	Shutdown bool         `json:"shutdown,omitempty"`
	Items    []BackupItem `json:"items"`
	// StartedAt is when the set started (manifests).
	StartedAt time.Time `json:"startedAt"`
	// EnvironmentName is recorded in the host manifest.
	EnvironmentName string `json:"environmentName,omitempty"`
}

// Validate checks the input.
func (in BackupRunInput) Validate() error {
	if in.SetID == "" || in.InstanceID == "" {
		return errors.New("backup: set and instance are required")
	}
	if err := in.Repository.Validate(); err != nil {
		return err
	}
	if len(in.Items) == 0 || len(in.Items) > MaxBackupItems {
		return fmt.Errorf("backup: 1 to %d items", MaxBackupItems)
	}
	seen := map[string]bool{}
	for _, it := range in.Items {
		if err := it.Validate(); err != nil {
			return err
		}
		if seen[it.Key()] {
			return fmt.Errorf("backup: duplicate item %s", it.Key())
		}
		seen[it.Key()] = true
	}
	return nil
}

// ShutdownServiceState is a Compose service's state before a backup or
// restore stopped it (the recovery record).
type ShutdownServiceState struct {
	Project string `json:"project"`
	Service string `json:"service"`
	Running bool   `json:"running"`
}

// ShutdownReport describes backup-time or restore-time container shutdown.
type ShutdownReport struct {
	// PreState is every affected service's state before anything stopped.
	PreState []ShutdownServiceState `json:"preState"`
	// Stopped lists "project/service" in stop order; Restarted in start
	// order.
	Stopped   []string `json:"stopped,omitempty"`
	Restarted []string `json:"restarted,omitempty"`
	// Conflicts are shared-volume or dependency conflicts that were
	// surfaced, not forced.
	Conflicts []string `json:"conflicts,omitempty"`
	// RestartError is set when restarting failed (manual recovery needed).
	RestartError string `json:"restartError,omitempty"`
}

// BackupRunOutput is the result output of backup.run.
type BackupRunOutput struct {
	ResticRepositoryID string          `json:"resticRepositoryId,omitempty"`
	KeyGeneration      int             `json:"keyGeneration,omitempty"`
	Members            []backup.Member `json:"members"`
	ManifestSnapshotID string          `json:"manifestSnapshotId,omitempty"`
	Shutdown           *ShutdownReport `json:"shutdown,omitempty"`
	Warnings           []string        `json:"warnings,omitempty"`
}

// BackupRetentionInput is the input of backup.retention: forget the
// policy's snapshots of this scope that the rules do not keep, then prune.
type BackupRetentionInput struct {
	Repository BackupRepositoryRef   `json:"repository"`
	PolicyID   string                `json:"policyId"`
	Rules      backup.RetentionRules `json:"rules"`
	// TimeZone evaluates the rules' periods (IANA name).
	TimeZone string `json:"timeZone"`
	// Expected lists the snapshot IDs a preview showed as removable; when
	// set, nothing outside it is removed (the preview is binding).
	Expected []string `json:"expected,omitempty"`
}

// RetentionOutput is the result output of the retention kinds.
type RetentionOutput struct {
	ResticRepositoryID string   `json:"resticRepositoryId,omitempty"`
	KeyGeneration      int      `json:"keyGeneration,omitempty"`
	Forgotten          []string `json:"forgotten"`
	Kept               int      `json:"kept"`
	// ReclaimedBytes is the raw repository size difference (may be 0).
	ReclaimedBytes int64  `json:"reclaimedBytes"`
	PruneError     string `json:"pruneError,omitempty"`
}

// BackupVerifyInput is the input of backup.verify.
type BackupVerifyInput struct {
	Repository BackupRepositoryRef `json:"repository"`
	// ReadDataSubset reads part of the pack data ("5%"); empty checks the
	// structure only.
	ReadDataSubset string `json:"readDataSubset,omitempty"`
}

var subsetRE = regexp.MustCompile(`^([1-9][0-9]?(\.[0-9]+)?%|100%|[1-9][0-9]{0,3}/[1-9][0-9]{0,3}|[1-9][0-9]{0,5}[KMG])$`)

// ValidReadDataSubset reports whether s is an accepted --read-data-subset.
func ValidReadDataSubset(s string) bool { return s == "" || subsetRE.MatchString(s) }

// VerifyOutput is the result output of the verification kinds. The job
// fails with restic.CodeRepositoryDamaged when the check finds damage.
type VerifyOutput struct {
	ResticRepositoryID string            `json:"resticRepositoryId,omitempty"`
	KeyGeneration      int               `json:"keyGeneration,omitempty"`
	ReadData           bool              `json:"readData"`
	Snapshots          []restic.Snapshot `json:"snapshots,omitempty"`
	// Manifests are the host manifests found (index refresh, #24).
	Manifests []backup.Manifest `json:"manifests,omitempty"`
	Damaged   bool              `json:"damaged,omitempty"`
}

// BackupScopePreviewInput is the input of backup.scope_preview.
type BackupScopePreviewInput struct {
	Items    []BackupItem `json:"items"`
	Shutdown bool         `json:"shutdown,omitempty"`
	// Repository is the destination (a local destination on this agent
	// must not be inside a backup source).
	Repository *BackupRepositoryRef `json:"repository,omitempty"`
	// EstimateBudget bounds the size estimate walk per item (entries).
	EstimateBudget int `json:"estimateBudget,omitempty"`
}

// Scope source states.
const (
	SourceIncluded    = "included"
	SourceExcluded    = "excluded"
	SourceRequiresOpt = "requires_opt_in"
	SourceBlocked     = "blocked"
)

// Scope source kinds.
const (
	SourceProject   = "project"
	SourceBind      = "bind"
	SourceVolume    = "volume"
	SourceAnonymous = "anonymous_volume"
	SourceExternal  = "external_path"
)

// ScopeSource is one candidate source of an item.
type ScopeSource struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Name    string `json:"name,omitempty"`
	Service string `json:"service,omitempty"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
}

// AffectedContainer is a container a shutdown would stop.
type AffectedContainer struct {
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
	Service string `json:"service,omitempty"`
	Running bool   `json:"running"`
	// StopOrder is the position in the stop sequence (1 first), 0 when
	// not stopped.
	StopOrder int `json:"stopOrder,omitempty"`
	// Protected explains why a DockYard container is left alone (#32).
	Protected string `json:"protected,omitempty"`
}

// ScopePreviewItem is the preview of one item.
type ScopePreviewItem struct {
	Item       string              `json:"item"`
	Kind       string              `json:"kind"`
	StackID    string              `json:"stackId,omitempty"`
	Volume     string              `json:"volume,omitempty"`
	Sources    []ScopeSource       `json:"sources"`
	Excludes   []string            `json:"excludes,omitempty"`
	Paths      []string            `json:"paths,omitempty"`
	Volumes    []string            `json:"volumes,omitempty"`
	Bytes      int64               `json:"estimatedBytes"`
	Files      int64               `json:"estimatedFiles"`
	Estimated  bool                `json:"estimateComplete"`
	Affected   []AffectedContainer `json:"affectedContainers,omitempty"`
	Conflicts  []string            `json:"conflicts,omitempty"`
	Warnings   []string            `json:"warnings,omitempty"`
	Error      string              `json:"error,omitempty"`
	ErrorClass string              `json:"errorClass,omitempty"`
}

// BackupScopePreviewOutput is the output of backup.scope_preview.
type BackupScopePreviewOutput struct {
	Items []ScopePreviewItem `json:"items"`
	// Downtime is set when the shutdown toggle would stop running
	// containers.
	Downtime string `json:"downtime,omitempty"`
}

// BackupSnapshotsInput is the input of backup.snapshots.
type BackupSnapshotsInput struct {
	Repository BackupRepositoryRef   `json:"repository"`
	Credential *RepositoryCredential `json:"credential"`
	Tags       []string              `json:"tags,omitempty"`
	// Manifests also returns the decoded host manifests.
	Manifests bool `json:"manifests,omitempty"`
}

// BackupSnapshotsOutput is the output of backup.snapshots.
type BackupSnapshotsOutput struct {
	ResticRepositoryID string            `json:"resticRepositoryId"`
	Snapshots          []restic.Snapshot `json:"snapshots"`
	Manifests          []backup.Manifest `json:"manifests,omitempty"`
}

// BackupContentsInput is the input of backup.contents.
type BackupContentsInput struct {
	Repository BackupRepositoryRef   `json:"repository"`
	Credential *RepositoryCredential `json:"credential"`
	SnapshotID string                `json:"snapshotId"`
	Path       string                `json:"path"`
	Recursive  bool                  `json:"recursive,omitempty"`
	Limit      int                   `json:"limit,omitempty"`
}

// BackupContentsOutput is the output of backup.contents.
type BackupContentsOutput struct {
	Nodes     []restic.Node `json:"nodes"`
	Truncated bool          `json:"truncated"`
}

// BackupFileStreamInput opens a backup.file stream: one regular file of a
// snapshot, at most MaxBytes.
type BackupFileStreamInput struct {
	Repository BackupRepositoryRef   `json:"repository"`
	Credential *RepositoryCredential `json:"credential"`
	SnapshotID string                `json:"snapshotId"`
	Path       string                `json:"path"`
	MaxBytes   int64                 `json:"maxBytes"`
}

var snapshotIDRE = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// ValidSnapshotID reports whether id is a restic snapshot ID (short or
// full hex).
func ValidSnapshotID(id string) bool { return snapshotIDRE.MatchString(id) }

// ValidSnapshotPath reports whether p is a clean absolute path inside a
// snapshot.
func ValidSnapshotPath(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && len(p) <= 4096 && !strings.ContainsAny(p, "\x00\n")
}
