package domain

import (
	"errors"
	"time"
)

// Compose stacks (#7). The on-disk definition (compose files, override
// files, .env and service env_files in the project directory) is the
// source of truth (#25 Q1): the manager records immutable revisions of it
// at every deploy and whenever it observes a change, and stores the
// deployment intent (last applied revision, applied images) next to the
// last observed Engine state, so the three are distinguishable.

// StackDeploymentStatus is the manager's record of what DockYard last did
// to a stack (not the live Engine state, see StackEngineState).
type StackDeploymentStatus string

// Deployment statuses.
const (
	// StackUndeployed: DockYard has never deployed it (created, not yet
	// deployed).
	StackUndeployed StackDeploymentStatus = "undeployed"
	// StackDeployed: the last deploy (or start/restart) succeeded, or an
	// adopted project was running when imported.
	StackDeployed StackDeploymentStatus = "deployed"
	// StackStopped: stopped by DockYard (containers kept).
	StackStopped StackDeploymentStatus = "stopped"
	// StackDown: taken down (containers and networks removed).
	StackDown StackDeploymentStatus = "down"
	// StackFailed: the last deploy failed while applying; the last applied
	// revision and the previous images are kept for recovery.
	StackFailed StackDeploymentStatus = "failed"
)

// StackEngineState summarizes the live Engine state last observed.
type StackEngineState string

// Engine states.
const (
	EngineStateUnknown StackEngineState = "unknown"
	EngineStateRunning StackEngineState = "running"
	EngineStatePartial StackEngineState = "partial"
	EngineStateStopped StackEngineState = "stopped"
	EngineStateMissing StackEngineState = "missing"
)

// RevisionSource says how a revision was observed.
type RevisionSource string

// Revision sources.
const (
	RevisionDeploy      RevisionSource = "deploy"
	RevisionEditor      RevisionSource = "editor"
	RevisionFileManager RevisionSource = "file_manager"
	RevisionExternal    RevisionSource = "external"
	RevisionRestore     RevisionSource = "restore"
)

// Valid reports whether s is known.
func (s RevisionSource) Valid() bool {
	switch s {
	case RevisionDeploy, RevisionEditor, RevisionFileManager, RevisionExternal, RevisionRestore:
		return true
	}
	return false
}

// Stack origins.
const (
	StackOriginCreated  = "created"
	StackOriginImported = "imported"
)

// Stack roots (where the project directory lives, #28).
const (
	StackRootStacks = "stacks"
	StackRootBind   = "bind"
)

// DisplayMeta is DockYard display metadata of a stack or service. It is
// stored in the manager and never written to Compose files (#22).
type DisplayMeta struct {
	Description string
	// Icon is a Lucide icon name override.
	Icon string
}

// StackDependency is a depends_on entry of a service.
type StackDependency struct {
	Service   string
	Condition string
	Required  bool
	Restart   bool
}

// StackServiceDef is a service of the definition (from the last deploy, or
// from validation before the first).
type StackServiceDef struct {
	Name      string
	Image     string
	Build     bool
	DependsOn []StackDependency
	// PullPolicy is the service's Compose pull_policy ("" = missing).
	PullPolicy string
}

// StackImage is the image a service runs after a deploy (#20 baseline).
type StackImage struct {
	Service  string
	Image    string
	ImageID  string
	Digest   string
	Platform string
	Build    bool
}

// StackBind is a resolved bind-mount source (#10).
type StackBind struct {
	Service  string
	Source   string
	Target   string
	RelPath  string
	External bool
	ReadOnly bool
}

// StackServiceState counts a service's containers at one point in time.
type StackServiceState struct {
	Service    string
	Containers int
	Running    int
	ImageIDs   []string
}

// Stack is a managed Compose project.
type Stack struct {
	ID            string
	EnvironmentID string
	// Name is the Compose project name (also the project directory name for
	// stacks created by DockYard).
	Name string
	// DisplayName, Description and Icon are display metadata.
	DisplayName string
	Meta        DisplayMeta
	// ServiceMeta is per-service display metadata.
	ServiceMeta map[string]DisplayMeta
	// Root, RootPath and Dir locate the project directory: Dir relative to
	// the stacks volume (Root "stacks") or to the registered root RootPath
	// (Root "bind").
	Root        string
	RootPath    string
	Dir         string
	ConfigFiles []string
	EnvFiles    []string
	Origin      string
	Status      StackDeploymentStatus
	// Applied is the last revision deployed successfully (nil: never
	// deployed by DockYard).
	Applied   *RevisionRef
	AppliedAt *time.Time
	// Observed is the newest revision seen on disk.
	Observed   *RevisionRef
	ObservedAt *time.Time
	// Failed is the revision of the last failed deploy (cleared by a
	// successful one).
	Failed *RevisionRef
	// Images are the images applied by the last successful deploy.
	Images   []StackImage
	Services []StackServiceDef
	Binds    []StackBind
	// PreviousState is the Engine state before the last deploy (recovery
	// data of a failed deploy: the previous images are still on the host).
	PreviousState []StackServiceState
	// Engine* is the live state last observed on the Engine.
	EngineState      StackEngineState
	EngineServices   []StackServiceState
	EngineObservedAt *time.Time
	LastJobID        string
	LastJobKind      JobKind
	// Revision is the edit revision (ETag) of the stack record.
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RevisionRef identifies a revision.
type RevisionRef struct {
	ID   string
	Seq  int64
	Hash string
}

// UndeployedChanges reports whether the definition on disk differs from
// the last applied revision (true when DockYard never deployed it).
func (s Stack) UndeployedChanges() bool {
	if s.Observed == nil {
		return false
	}
	return s.Applied == nil || s.Applied.Hash != s.Observed.Hash
}

// StackFile is one definition file of a revision.
type StackFile struct {
	Path    string
	SHA256  string
	Size    int64
	Content []byte
}

// StackRevision is an immutable snapshot of a stack's definition.
type StackRevision struct {
	ID      string
	StackID string
	// Seq numbers a stack's revisions from 1.
	Seq    int64
	Hash   string
	Source RevisionSource
	// AuthorUserID / AuthorTokenID are audit metadata of the change.
	AuthorUserID  string
	AuthorTokenID string
	// JobID is the deploy job of source "deploy".
	JobID string
	// RestoredFrom is the revision a "restore" wrote back.
	RestoredFrom string
	Files        []StackFile
	// ContentOmitted: only hashes were captured (the definition exceeded
	// the size a deploy result carries).
	ContentOmitted bool
	CreatedAt      time.Time
}

// Ref returns the revision's reference.
func (r StackRevision) Ref() *RevisionRef { return &RevisionRef{ID: r.ID, Seq: r.Seq, Hash: r.Hash} }

// StackFilter selects stacks.
type StackFilter struct {
	EnvironmentID string
	AfterID       string
	Limit         int
}

// StackRoot is where a stack's files live, for the file manager (#15) and
// backups (#10).
type StackRoot struct {
	StackID       string
	EnvironmentID string
	ProjectName   string
	Root          string
	RootPath      string
	// Dir is relative to the root; DefinitionFiles are the project-relative
	// definition files of the newest observed revision (edits need
	// stack.definition.* in addition to the file capabilities, #17).
	Dir             string
	DefinitionFiles []string
}

// StackIssue is a validation finding.
type StackIssue struct {
	Code    string
	Message string
	Service string
}

// Stable stack error codes (StackError.Code); the API returns them as its
// error codes.
const (
	StackErrOffline                = "environment_offline"
	StackErrProjectExists          = "compose_project_exists"
	StackErrDirectoryExists        = "stack_directory_exists"
	StackErrDefinitionChanged      = "stack_definition_changed"
	StackErrNotAdoptable           = "stack_not_adoptable"
	StackErrRootUnavailable        = "stack_root_unavailable"
	StackErrContentUnavailable     = "revision_content_unavailable"
	StackErrInvalidDefinition      = "invalid_definition"
	StackErrAgent                  = "engine_error"
	StackErrEngineUnavailable      = "engine_unavailable"
	StackErrAgentTimeout           = "timeout"
	StackErrDefinitionTooLarge     = "definition_too_large"
	StackErrProjectNotFound        = "compose_project_not_found"
	StackErrEnvironmentUnsupported = "agent_unsupported"
)

// StackError is a stack operation failure with a stable code; Issues
// carries validation findings of invalid_definition.
type StackError struct {
	Code    string
	Message string
	Issues  []StackIssue
}

func (e *StackError) Error() string { return e.Code + ": " + e.Message }

// Stack errors.
var (
	ErrStackNotFound         = errors.New("stack not found")
	ErrStackRevisionNotFound = errors.New("stack revision not found")
	ErrStackNameTaken        = errors.New("a stack with this project name already exists in the environment")
	ErrStackRevisionStale    = errors.New("stack revision changed")
)
