// Package protection is the one place that decides what Docker Manager may do to
// its own containers, images, volumes and networks (#32). The agent
// identifies them (internal/agent/protect) and annotates its inventory with
// protocol.Protection; the manager and the agent both call Check before any
// destructive operation (the agent refuses regardless of the manager), and
// every feature that selects Docker objects in bulk — prune (#14),
// automatic updates (#20), backup and restore shutdown plans (#10), bulk
// selections and migrations (#35) — drops protected objects with Excluded /
// Filter. The instance owner cannot override a refusal in v1: host-level
// Docker access is the escape hatch.
package protection

import "github.com/neurekadev/docker-manager/internal/protocol"

// Roles of protected objects (protocol.Protection.Role).
const (
	// RoleAgent is a Docker Agent container (Self: the connected one).
	RoleAgent = "agent"
	// RoleManager is a Docker Manager container (Self: this instance's,
	// matched through manager.identity).
	RoleManager = "manager"
	// RoleProject is another container of Docker Manager's own Compose project
	// (e.g. a reverse proxy added to the documented compose.yaml).
	RoleProject = "docker_manager_project"
	// RoleImage is an image a Docker Manager container runs.
	RoleImage = "docker_manager_image"
	// RoleManagerData is the manager's data volume.
	RoleManagerData = "manager_data"
	// RoleAgentState is the agent's state volume (its credential).
	RoleAgentState = "agent_state"
	// RoleStacks is the stacks volume (#28).
	RoleStacks = "stacks"
	// RoleVolume is another volume mounted into a Docker Manager container (for
	// example a local backup repository, #10) or of Docker Manager's project.
	RoleVolume = "docker_manager_volume"
	// RoleNetwork is a network of Docker Manager's containers or project.
	RoleNetwork = "docker_manager_network"
)

// Action is an operation on a Docker object.
type Action string

// Actions checked by Check.
const (
	Start   Action = "start"
	Unpause Action = "unpause"
	Stop    Action = "stop"
	Pause   Action = "pause"
	Restart Action = "restart"
	Remove  Action = "remove"
	// Update covers in-place changes and recreation.
	Update Action = "update"
	// Mount is mounting a volume into a new container.
	Mount Action = "mount"
	// Stack actions on Docker Manager's own Compose project (#7). Deploy
	// (import, redeploy, digest updates) is allowed: Docker Manager manages
	// itself, and the agent hands its own container to a helper container
	// (internal/agent/selfupdate). Down (and stack removal) is refused: it
	// would delete Docker Manager.
	Deploy Action = "deploy"
	Down   Action = "down"
)

// Stable codes of refusals (API error codes and job error classes).
const (
	CodeProtected            = "protected"
	CodeConfirmationRequired = "confirmation_required"
)

// Refusal is a refused operation on a protected object. It is a
// jobexec.ClassedError (class = Code).
type Refusal struct {
	Code   string
	Reason string
	Action Action
}

func (r *Refusal) Error() string { return r.Reason }

// ErrorClass implements jobexec.ClassedError.
func (r *Refusal) ErrorClass() string { return r.Code }

// Recovery implements jobexec.ClassedError.
func (r *Refusal) Recovery() string {
	if r.Code == CodeConfirmationRequired {
		return "Confirm the restart: the Docker Manager UI and API disconnect until the manager is back."
	}
	return "Docker Manager does not change its own containers, images and volumes; use Docker on the host if you really need to."
}

// Check decides whether action may run on an object with protection p
// (nil: not protected). confirmed is the caller's explicit confirmation of
// a restart that interrupts Docker Manager (the co-located manager).
// Starting and deploying are always allowed; everything that would stop,
// disable or delete Docker Manager is refused.
func Check(p *protocol.Protection, action Action, confirmed bool) error {
	if p == nil {
		return nil
	}
	switch action {
	case Start, Unpause, Deploy:
		return nil
	case Restart:
		if p.RestartAllowed {
			if confirmed {
				return nil
			}
			return &Refusal{Code: CodeConfirmationRequired, Action: action,
				Reason: "restarting this container interrupts Docker Manager (" + p.Reason + "); confirm the restart to continue"}
		}
	}
	return &Refusal{Code: CodeProtected, Action: action, Reason: "refused to " + string(action) + " a protected Docker Manager resource: " + p.Reason}
}

// Excluded reports whether an object must be left out of prune candidates,
// automatic update policies, backup/restore shutdown plans, bulk
// selections and migrations.
func Excluded(p *protocol.Protection) bool { return p != nil }

// Exclusion is an item Filter dropped, with the reason to show.
type Exclusion[T any] struct {
	Item   T
	Reason string
}

// Filter splits items into those a bulk operation may touch and the
// protected ones.
func Filter[T any](items []T, protectionOf func(T) *protocol.Protection) (kept []T, excluded []Exclusion[T]) {
	for _, it := range items {
		if p := protectionOf(it); p != nil {
			excluded = append(excluded, Exclusion[T]{Item: it, Reason: p.Reason})
			continue
		}
		kept = append(kept, it)
	}
	return kept, excluded
}

// RestartAllowed reports whether a role may be restarted after an explicit
// confirmation. The connected agent never restarts itself through a job
// (it would kill the job's executor).
func RestartAllowed(role string, self bool) bool {
	switch role {
	case RoleManager, RoleProject:
		return true
	case RoleAgent:
		return !self
	}
	return false
}
