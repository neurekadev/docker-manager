package domain

// Environment removal (#34). Removing an environment archives it: the host
// is hidden from operations, its agent's credential is revoked and every
// record is kept (history, backups, stacks and policies, which resume when
// the Engine is enrolled again with a reattach:<environmentId>
// enrollment), except the permission rules scoped to it, which are removed
// with an audit record. Nothing on the host is touched.

// Dependent record kinds of a removal preview.
const (
	DependentStack              = "stack"
	DependentManagedContainer   = "managed_container"
	DependentUpdatePolicy       = "update_policy"
	DependentBackupPolicy       = "backup_policy"
	DependentMaintenancePolicy  = "maintenance_policy"
	DependentBackupRepository   = "backup_repository"
	DependentBackupSet          = "backup_set"
	DependentRegistryConnection = "registry_connection"
	DependentBuildDefinition    = "build_definition"
	DependentPermissionRule     = "permission_rule"
	DependentSchedule           = "schedule"
	DependentJob                = "job"
)

// DependentKinds lists every kind in preview order.
func DependentKinds() []string {
	return []string{DependentStack, DependentManagedContainer, DependentUpdatePolicy, DependentBackupPolicy,
		DependentMaintenancePolicy, DependentBackupRepository, DependentBackupSet, DependentRegistryConnection,
		DependentBuildDefinition, DependentPermissionRule, DependentSchedule, DependentJob}
}

// What archiving does to a dependent record.
const (
	// OnArchiveKept: kept unchanged and hidden with the environment; it
	// works again after a re-attach.
	OnArchiveKept = "kept"
	// OnArchivePaused: kept; scheduled runs are refused while the
	// environment is archived and resume after a re-attach.
	OnArchivePaused = "paused"
	// OnArchiveRemoved: deleted (permission rules), with an audit record.
	OnArchiveRemoved = "removed"
	// OnArchiveInterrupted: the agent is disconnected; unfinished jobs end
	// by the job engine's offline rules.
	OnArchiveInterrupted = "interrupted"
)

// EnvironmentDependent is one record that depends on an environment.
type EnvironmentDependent struct {
	Kind string
	ID   string
	Name string
	// Detail is a short plain-language note (e.g. "bound to stack shop",
	// "3 snapshots of this host").
	Detail string
	// OnArchive is what archiving does to it (OnArchive*).
	OnArchive string
	// Resource is the authorization resource type and ID the reader needs
	// the type's read capability on to see the item (empty: owner only).
	ResourceType string
	ResourceID   string
	// Job is the unfinished job (DependentJob; visibility follows its
	// targets).
	Job *Job
}

// EnvironmentRemovalPreview lists everything that depends on an
// environment before it is archived.
type EnvironmentRemovalPreview struct {
	Environment Environment
	Dependents  []EnvironmentDependent
	// BackupSnapshots counts this host's snapshot index entries (kept).
	BackupSnapshots int
}

// RemovedPermissionRule is a permission rule removed because its scope
// was an archived environment.
type RemovedPermissionRule struct {
	// SubjectKind is "group" or "user".
	SubjectKind string
	SubjectID   string
	Rule        PermissionRule
}
