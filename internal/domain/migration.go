package domain

import (
	"errors"
	"time"
)

// Environment migration (#35): a stack (its project directory and named
// volumes) or a standalone volume copied from one environment to another
// by a stack.migrate / volume.migrate job. The migration ID is the job ID.

// MigrationKind is what a migration moves.
type MigrationKind string

// Migration kinds.
const (
	MigrationKindStack  MigrationKind = "stack"
	MigrationKindVolume MigrationKind = "volume"
)

// MigrationState is where a migration stands.
type MigrationState string

// Migration states.
const (
	// MigrationRunning: the job is queued or running.
	MigrationRunning MigrationState = "running"
	// MigrationCompleted: the destination runs the stack (or holds the
	// volume copy); a stack's source is stopped and untouched until the
	// user confirms its removal.
	MigrationCompleted MigrationState = "completed"
	// MigrationSourceRemoved: the user confirmed and the source was removed.
	MigrationSourceRemoved MigrationState = "source_removed"
	// MigrationFailed, MigrationCancelled, MigrationInterrupted: the job
	// did not complete; the source is recoverable (restarted by the job's
	// compensation when possible) and the destination may hold a partial
	// copy (TargetPartial) that the next migration of the stack removes.
	MigrationFailed      MigrationState = "failed"
	MigrationCancelled   MigrationState = "cancelled"
	MigrationInterrupted MigrationState = "interrupted"
)

// Terminal reports whether the migration's job finished.
func (s MigrationState) Terminal() bool { return s != MigrationRunning }

// MigrationSource locates a migrated stack's source project.
type MigrationSource struct {
	Root     string
	RootPath string
	Dir      string
	Project  string
}

// MigrationVolume is one volume of a migration.
type MigrationVolume struct {
	Source string
	Target string
	// Anonymous volumes are copied only when selected.
	Anonymous bool
	// Copied, Bytes and SHA256 (tar payload) once transferred.
	Copied bool
	Bytes  int64
	SHA256 string
}

// MigrationPart is a transferred part with its verified checksum.
type MigrationPart struct {
	// Name is "project", "volume:<name>" or "image".
	Name   string
	Bytes  int64
	SHA256 string
	Chunks int64
}

// Migration is the record of one migration.
type Migration struct {
	// ID is the migration's job ID.
	ID                  string
	Kind                MigrationKind
	StackID             string
	SourceEnvironmentID string
	TargetEnvironmentID string
	Source              MigrationSource
	// TargetDir is the new project directory in the destination's stacks
	// volume (stack migrations).
	TargetDir string
	Volumes   []MigrationVolume
	// Images are the locally built images copied through the relay.
	Images []string
	Parts  []MigrationPart
	// SourceRunning are the services that ran before the source stopped.
	SourceRunning []string
	State         MigrationState
	// CutOver: the stack record moved to the destination.
	CutOver bool
	// TargetPartial: the destination may hold data of this migration
	// (staging directory, committed directory, volumes, containers).
	TargetPartial bool
	// Bytes is the total transferred payload.
	Bytes int64
	// DeployJobID is the destination's stack.deploy job; RemovalJobID the
	// stack.remove_source job of a confirmed removal.
	DeployJobID  string
	RemovalJobID string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	FinishedAt   *time.Time
}

// VolumeTargets lists the destination volume names.
func (m Migration) VolumeTargets() []string {
	out := make([]string, 0, len(m.Volumes))
	for _, v := range m.Volumes {
		out = append(out, v.Target)
	}
	return out
}

// Migration errors.
var (
	ErrMigrationNotFound = errors.New("migration not found")
	// ErrEnvironmentMigrationNotFound: no environment migration with the ID.
	ErrEnvironmentMigrationNotFound = errors.New("environment migration not found")
)

// EnvironmentMigration moves the stacks of an environment to another one
// (environment.migrate, #35). The stacks move in groups: stacks linked by a
// network or volume that one creates and another joins as external form a
// group, stop together and move one after the other, the creating stack
// first. Each stack moves as its own stack migration. The ID is the job ID.
type EnvironmentMigration struct {
	ID                  string
	SourceEnvironmentID string
	TargetEnvironmentID string
	State               MigrationState
	// Groups are the stack IDs of each group in the order they move.
	Groups [][]string
	Stacks []EnvironmentMigrationStack
	// Networks are the networks created on the destination first: made on
	// the source outside any stack and joined as external by a moving stack.
	Networks   []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

// EnvironmentStackState is where one stack of an environment migration
// stands.
type EnvironmentStackState string

// Environment migration stack states.
const (
	// EnvironmentStackPending: not started yet.
	EnvironmentStackPending EnvironmentStackState = "pending"
	// EnvironmentStackMoving: its stack migration runs.
	EnvironmentStackMoving EnvironmentStackState = "moving"
	// EnvironmentStackMoved: its stack migration completed.
	EnvironmentStackMoved EnvironmentStackState = "moved"
	// EnvironmentStackFailed: its stack migration did not complete; the
	// stack is back on the source.
	EnvironmentStackFailed EnvironmentStackState = "failed"
)

// EnvironmentMigrationStack is one stack of an environment migration.
type EnvironmentMigrationStack struct {
	StackID string
	// Name is the stack's Compose project name when the migration started.
	Name string
	// MigrationID is its stack migration ("" until it starts).
	MigrationID string
	State       EnvironmentStackState
	// SourceRemoved: the stopped copy on the source was removed (read
	// from its stack migration; not stored with the record).
	SourceRemoved bool
}

// Stack returns the entry of a stack (false when it is not part of it).
func (m EnvironmentMigration) Stack(id string) (EnvironmentMigrationStack, bool) {
	for _, s := range m.Stacks {
		if s.StackID == id {
			return s, true
		}
	}
	return EnvironmentMigrationStack{}, false
}

// SetStack updates the entry of a stack.
func (m *EnvironmentMigration) SetStack(id string, fn func(s *EnvironmentMigrationStack)) {
	for i := range m.Stacks {
		if m.Stacks[i].StackID == id {
			fn(&m.Stacks[i])
		}
	}
}

// StackPlacement is where a stack record points and what Docker Manager last did
// there: a migration moves it to the destination at cut-over and restores
// it when the migration stops before completing (#35).
type StackPlacement struct {
	EnvironmentID    string
	Root             string
	RootPath         string
	Dir              string
	Status           StackDeploymentStatus
	Applied          *RevisionRef
	AppliedAt        *time.Time
	Failed           *RevisionRef
	Images           []StackImage
	Services         []StackServiceDef
	Binds            []StackBind
	PreviousState    []StackServiceState
	EngineState      StackEngineState
	EngineServices   []StackServiceState
	EngineObservedAt *time.Time
}

// PlacementOf returns a stack's placement.
func PlacementOf(s Stack) StackPlacement {
	return StackPlacement{EnvironmentID: s.EnvironmentID, Root: s.Root, RootPath: s.RootPath, Dir: s.Dir, Status: s.Status,
		Applied: s.Applied, AppliedAt: s.AppliedAt, Failed: s.Failed, Images: s.Images, Services: s.Services, Binds: s.Binds,
		PreviousState: s.PreviousState, EngineState: s.EngineState, EngineServices: s.EngineServices, EngineObservedAt: s.EngineObservedAt}
}

// Apply sets the placement's fields on s.
func (p StackPlacement) Apply(s *Stack) {
	s.EnvironmentID, s.Root, s.RootPath, s.Dir, s.Status = p.EnvironmentID, p.Root, p.RootPath, p.Dir, p.Status
	s.Applied, s.AppliedAt, s.Failed, s.Images, s.Services, s.Binds = p.Applied, p.AppliedAt, p.Failed, p.Images, p.Services, p.Binds
	s.PreviousState, s.EngineState, s.EngineServices, s.EngineObservedAt = p.PreviousState, p.EngineState, p.EngineServices, p.EngineObservedAt
}
