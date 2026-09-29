package domain

import (
	"errors"
	"time"
)

// Backups (#10, #24). Repositories, the Recovery Key, policies, sets and
// snapshots belong to the manager instance, never to the user who created
// them; the initiator of an action is audit metadata only.

// Backup repository states.
const (
	// BackupRepositoryAwaitingConfirmation: created, but the owner has not
	// confirmed (re-entered) the Recovery Key yet; nothing is initialized
	// and no policy using it can be enabled.
	BackupRepositoryAwaitingConfirmation = "awaiting_confirmation"
	// BackupRepositoryReady: confirmed; jobs may initialize and use it.
	BackupRepositoryReady = "ready"
)

// BackupExecutorManager is the executor of local repositories on the manager
// (other local repositories name their environment).
const BackupExecutorManager = "manager"

// Backup repository compression modes (restic's --compression; the
// values match restic.Compression*).
const (
	BackupCompressionAuto = "auto"
	BackupCompressionMax  = "max"
	BackupCompressionOff  = "off"
)

// BackupRepository is a destination for restic repositories: a local
// directory on one executor (the manager or one environment's agent) or an
// S3 bucket/prefix. S3 credentials are sealed; only their fingerprint is
// readable.
type BackupRepository struct {
	ID   string
	Name string
	// Kind is "local" or "s3".
	Kind string
	// Executor of a local repository: BackupExecutorManager or an
	// environment ID ("" for S3).
	Executor  string
	Path      string
	Endpoint  string
	Bucket    string
	Prefix    string
	Region    string
	PathStyle bool
	// Compression is the compression mode of the data written to the
	// repository: BackupCompressionAuto (default), Max or Off. A change
	// affects only data written afterwards (backups, and data prune
	// repacks).
	Compression string
	// CredentialFingerprint identifies the stored S3 key pair ("" none).
	CredentialFingerprint string
	State                 string
	ConfirmedAt           *time.Time
	// Verification schedule (#13) and depth.
	VerifyCron     string
	VerifyTimeZone string
	VerifyEnabled  bool
	// VerifyReadData is restic's --read-data-subset for scheduled checks
	// ("" = structure only).
	VerifyReadData string
	// Last connection test.
	LastTestAt     *time.Time
	LastTestResult string
	LastTest       *BackupConnectionTest
	Revision       int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BackupRepositoryInput creates a repository.
type BackupRepositoryInput struct {
	Name      string
	Kind      string
	Executor  string
	Path      string
	Endpoint  string
	Bucket    string
	Prefix    string
	Region    string
	PathStyle bool
	// Compression is BackupCompressionAuto ("" means the same), Max or Off.
	Compression     string
	AccessKeyID     string
	SecretAccessKey string
	VerifyCron      string
	VerifyTimeZone  string
	VerifyReadData  string
}

// BackupRepositoryPatch edits a repository (nil = unchanged). The
// destination itself cannot move; credentials are write-only.
type BackupRepositoryPatch struct {
	Name            *string
	Region          *string
	PathStyle       *bool
	Compression     *string
	AccessKeyID     *string
	SecretAccessKey *string
	VerifyCron      *string
	VerifyTimeZone  *string
	VerifyEnabled   *bool
	VerifyReadData  *string
}

// BackupConnectionTest is the outcome of a connection test.
type BackupConnectionTest struct {
	At time.Time
	OK bool
	// Result is "ok" or an error class (restic.Code*, s3 probe classes).
	Result  string
	Message string
	// Capabilities probed on S3 (nil for local repositories).
	CanRead   *bool
	CanWrite  *bool
	CanDelete *bool
	// ObjectLock is true when the bucket enforces Object Lock (retention
	// pruning may be refused).
	ObjectLock *bool
	// Scopes found at the destination (restic repositories that already
	// exist) and whether the Recovery Key opens them.
	Scopes   []BackupScopeProbe
	Warnings []string
}

// BackupScopeProbe is one restic repository found by a connection test.
type BackupScopeProbe struct {
	Scope       string
	Exists      bool
	KeyAccepted bool
	// PreviousKey: only the previous Recovery Key opens it (a rotation
	// that has not reached it yet).
	PreviousKey        bool
	ResticRepositoryID string
	ErrorClass         string
}

// BackupLocation is one physical restic repository (a repository's scope).
type BackupLocation struct {
	RepositoryID       string
	Scope              string
	ResticRepositoryID string
	// KeyGeneration is the Recovery Key generation the location is known
	// to use.
	KeyGeneration    int
	InitializedAt    *time.Time
	LastBackupAt     *time.Time
	LastVerifiedAt   *time.Time
	LastVerifyResult string
	LastVerifyJobID  string
	// SizeBytes is what the location stores (compressed, deduplicated);
	// the fields below come with it from the last measurement (#10).
	SizeBytes int64
	// UncompressedBytes is the same data before compression.
	UncompressedBytes int64
	// CompressionRatio is restic's uncompressed/stored ratio.
	CompressionRatio float64
	// CompressionProgress is the percentage of data stored compressed.
	CompressionProgress float64
	// StatsSnapshots counts the restic snapshots of the location.
	StatsSnapshots int64
	// StatsAt is when the size was measured (nil: never).
	StatsAt   *time.Time
	UpdatedAt time.Time
}

// LocationStats is a location's measured size (#10).
type LocationStats struct {
	SizeBytes           int64
	UncompressedBytes   int64
	CompressionRatio    float64
	CompressionProgress float64
	Snapshots           int64
}

// BackupStorageSample is one measurement of a location's size in the
// storage history (#10). A zero sample marks a removed repository.
type BackupStorageSample struct {
	RepositoryID      string
	Scope             string
	At                time.Time
	SizeBytes         int64
	UncompressedBytes int64
}

// BackupStorageQuery selects a storage history: the time range, the width
// of its buckets and which locations count.
type BackupStorageQuery struct {
	From, To time.Time
	Step     time.Duration
	// Scope keeps only the locations of one scope ("" = all).
	Scope string
	// Repository reports whether a repository's samples count (nil = all).
	Repository func(repositoryID string) bool
}

// BackupStoragePoint is the storage at one time: the sum over locations of
// each one's latest sample at or before At. Known is false while no
// location had a sample yet.
type BackupStoragePoint struct {
	At                time.Time
	Known             bool
	SizeBytes         int64
	UncompressedBytes int64
}

// BackupKeyState is the instance's Recovery Key state (never the key).
type BackupKeyState struct {
	// Generation counts confirmed keys (0: none yet).
	Generation  int
	Fingerprint string
	CreatedAt   *time.Time
	ConfirmedAt *time.Time
	// Pending is a generated key awaiting its confirmation.
	PendingFingerprint string
	PendingCreatedAt   *time.Time
	// PreviousFingerprint is set while a rotation still has locations on
	// the previous key.
	PreviousFingerprint string
	RotationStartedAt   *time.Time
	Revision            int64
}

// RotationInProgress reports whether locations may still use the previous
// key.
func (k BackupKeyState) RotationInProgress() bool { return k.PreviousFingerprint != "" }

// BackupStackSelection selects one stack of a policy.
type BackupStackSelection struct {
	StackID          string
	VolumeInclude    []string
	VolumeExclude    []string
	AnonymousVolumes bool
	PathExcludes     []string
	ExternalPaths    []string
}

// BackupVolumeSelection selects one standalone named volume.
type BackupVolumeSelection struct {
	EnvironmentID string
	Volume        string
	PathExcludes  []string
}

// BackupRetention configures retention (backup.RetentionRules fields).
type BackupRetention struct {
	Last       int
	Hourly     int
	Daily      int
	Weekly     int
	Monthly    int
	Yearly     int
	WithinDays int
	// ExpireDeletedDays removes every backup of a deleted stack or volume
	// once its newest backup is this many days old (0 = off, the default).
	ExpireDeletedDays int
	// AfterBackup applies retention automatically after every finished
	// backup run of the policy.
	AfterBackup bool
}

// MaxExpireDeletedDays bounds BackupRetention.ExpireDeletedDays.
const MaxExpireDeletedDays = 3650

// BackupPolicy is a backup policy (instance resource).
type BackupPolicy struct {
	ID   string
	Name string
	// EnvironmentID is empty for All Environments. Every managed stack and
	// standalone volume in scope is selected at preview/run time.
	EnvironmentID string
	ExcludeStacks []string
	// ExcludeVolumes contains names for a single environment and
	// environmentID/volumeName for All Environments. It applies to
	// standalone volumes and to the volumes of every selected stack.
	ExcludeVolumes []string
	// AnonymousVolumes also backs up anonymous volumes (default off): those
	// of the selected stacks' containers and standalone ones.
	AnonymousVolumes bool
	// BuildxVolumes also backs up buildx builder volumes (default off:
	// rebuildable build cache, protocol.IsBuildxVolume).
	BuildxVolumes bool
	// ExternalBinds also backs up the selected stacks' bind sources outside
	// their project directories (default off); each agent still backs up
	// only those below its DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST.
	ExternalBinds bool
	// RepositoryID is the destination of every scope; EnvironmentRepos
	// overrides it per environment (local repositories live on each
	// environment's own agent).
	RepositoryID     string
	EnvironmentRepos map[string]string
	IncludeManager   bool
	IncludeMetrics   bool
	Stacks           []BackupStackSelection
	Volumes          []BackupVolumeSelection
	// Shutdown stops the affected containers during backups (default off).
	Shutdown  bool
	Cron      string
	TimeZone  string
	Enabled   bool
	Retention BackupRetention
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RepositoryFor returns the repository of a scope environment ("" for the
// manager scope).
func (p BackupPolicy) RepositoryFor(environmentID string) string {
	if environmentID != "" {
		if r, ok := p.EnvironmentRepos[environmentID]; ok && r != "" {
			return r
		}
	}
	return p.RepositoryID
}

// BackupSetMember is one planned snapshot of a set.
type BackupSetMember struct {
	Item          string
	Kind          string
	Scope         string
	RepositoryID  string
	EnvironmentID string
	StackID       string
	StackName     string
	Volume        string
	State         string
	ErrorClass    string
	SnapshotID    string
	SnapshotTime  *time.Time
	JobID         string
}

// BackupSet is one logical backup run.
type BackupSet struct {
	ID         string
	PolicyID   string
	PolicyName string
	Origin     JobOrigin
	State      string
	StartedAt  time.Time
	FinishedAt *time.Time
	Members    []BackupSetMember
	// ManifestSnapshotID is the set manifest's snapshot in the manager
	// scope ("" before it is written).
	ManifestSnapshotID string
	// FollowUp is "retention" while the policy's retention still has to be
	// queued after the set finished, "done" afterwards.
	FollowUp  string
	UpdatedAt time.Time
}

// BackupSnapshot is one restic snapshot Docker Manager knows (a "backup").
type BackupSnapshot struct {
	ID               string
	SetID            string
	PolicyID         string
	RepositoryID     string
	Scope            string
	EnvironmentID    string
	Kind             string
	Item             string
	StackID          string
	StackName        string
	Volume           string
	ResticSnapshotID string
	SnapshotTime     time.Time
	Paths            []string
	Volumes          []string
	// ProjectPath / VolumePaths locate the stack's project directory and
	// each volume's data inside the snapshot ("" / nil when unknown).
	ProjectPath string
	VolumePaths map[string]string
	Consistency string
	State       string
	ErrorClass  string
	BytesAdded  int64
	BytesTotal  int64
	Files       int64
	JobID       string
	VerifiedAt  *time.Time
	ForgottenAt *time.Time
	CreatedAt   time.Time
}

// BackupSnapshotFilter filters snapshot lists.
type BackupSnapshotFilter struct {
	AfterID       string
	Limit         int
	RepositoryID  string
	PolicyID      string
	SetID         string
	EnvironmentID string
	StackID       string
	Kind          string
	// Volume: snapshots of this volume, and stack snapshots that hold it.
	Volume string
	// IncludeForgotten also lists snapshots removed by retention.
	IncludeForgotten bool
}

// Backup errors.
var (
	ErrBackupRepositoryNotFound = errors.New("backup repository not found")
	ErrBackupRepositoryNameUsed = errors.New("backup repository name is taken")
	ErrBackupRepositoryInUse    = errors.New("backup repository is used by a policy")
	ErrBackupPolicyNotFound     = errors.New("backup policy not found")
	ErrBackupPolicyNameUsed     = errors.New("backup policy name is taken")
	ErrBackupScopeOverlap       = errors.New("a backup policy already covers this environment")
	ErrBackupNotFound           = errors.New("backup not found")
	ErrBackupSetNotFound        = errors.New("backup set not found")
	// ErrRecoveryKeyMismatch: the re-entered Recovery Key does not match.
	ErrRecoveryKeyMismatch = errors.New("the Recovery Key does not match")
	// ErrRecoveryKeyMalformed: the input is not a well-formed Recovery Key
	// (a typo: its checksum does not match).
	ErrRecoveryKeyMalformed = errors.New("the Recovery Key is malformed (check for typos)")
	// ErrRecoveryKeyNotConfirmed: the repository (or the instance key) is
	// not confirmed yet.
	ErrRecoveryKeyNotConfirmed = errors.New("the Recovery Key has not been confirmed")
	// ErrKeyRotationInProgress: a rotation still moves locations.
	ErrKeyRotationInProgress = errors.New("a Recovery Key rotation is still in progress")
)

// BackupRunActiveError refuses a manual run while a run of the same policy
// (manual or scheduled) is still queued or running: a second run would only
// queue behind it and back up the same data again right after. Retrying a
// set's failed members is still allowed.
type BackupRunActiveError struct{ JobID string }

func (e *BackupRunActiveError) Error() string {
	return "a backup of this policy is still queued or running (job " + e.JobID + ")"
}
