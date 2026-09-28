package domain

import "time"

// Docker resources of an environment (#6): containers, images, volumes and
// networks live on the environment's Engine and are read through its agent;
// the manager keeps only what the Engine cannot: the recreate
// specification of Docker Manager-managed standalone containers.

// DockerError is a stable failure of a Docker resource operation. Code is
// one of the Docker* codes below; the API maps it to its error catalog
// (docs/internal/api/errors.md).
type DockerError struct {
	Code    string
	Message string
	// Field is set for input problems (a JSON body member).
	Field string
}

func (e *DockerError) Error() string { return e.Code + ": " + e.Message }

// VolumeUsageReport is the disk usage of an environment's volumes as the
// Engine last computed it (cached briefly by the manager).
type VolumeUsageReport struct {
	// ComputedAt is when the agent answered (zero when Unsupported).
	ComputedAt time.Time
	// Unsupported: the environment's agent predates volume.usage.
	Unsupported bool
	// Sizes are bytes by volume name; -1 when the Engine does not know.
	Sizes map[string]int64
}

// Docker operation error codes.
const (
	// DockerNotFound: the object does not exist on the environment's Engine.
	DockerNotFound = "not_found"
	// DockerEnvironmentOffline: the environment's agent is not connected.
	DockerEnvironmentOffline = "environment_offline"
	// DockerEngineUnavailable: the agent cannot reach its Docker Engine.
	DockerEngineUnavailable = "engine_unavailable"
	// DockerUnsupportedAPIVersion: the Engine's API version is too old.
	DockerUnsupportedAPIVersion = "unsupported_api_version"
	// DockerEngineError: the Engine rejected the operation.
	DockerEngineError = "engine_error"
	// DockerAgentUnsupported: the agent does not serve the operation (it
	// predates it).
	DockerAgentUnsupported = "agent_unsupported"
	// DockerTimeout: the agent did not answer in time.
	DockerTimeout = "timeout"
	// DockerBusy: the agent is at its request limit; retry.
	DockerBusy = "unavailable"
	// DockerInvalid: an input the Engine or Docker Manager refused (422).
	DockerInvalid = "validation_failed"
	// DockerConflict: generic state conflict.
	DockerConflict = "conflict"
	// DockerStackManaged: the object belongs to a Docker Manager-managed stack;
	// change the stack instead.
	DockerStackManaged = "stack_managed"
	// DockerContainerRunning: removing a running container needs force.
	DockerContainerRunning = "container_running"
	// DockerImageInUse, DockerVolumeInUse, DockerNetworkInUse: still used by
	// containers.
	DockerImageInUse   = "image_in_use"
	DockerVolumeInUse  = "volume_in_use"
	DockerNetworkInUse = "network_in_use"
	// DockerNetworkBuiltin: bridge, host and none cannot be removed.
	DockerNetworkBuiltin = "network_builtin"
	// DockerNameTaken: another container, volume or network has the name.
	DockerNameTaken = "resource_name_taken"
	// DockerRecreateRequired: the change needs the container recreated.
	DockerRecreateRequired = "recreate_required"
	// DockerProtected: the object is one of Docker Manager's own (#32).
	DockerProtected = "protected"
	// DockerConfirmationRequired: restarting it interrupts Docker Manager; the
	// caller must confirm (#32).
	DockerConfirmationRequired = "confirmation_required"
	// DockerCommandNotFound: none of a terminal shell's paths exists in the
	// container (#8).
	DockerCommandNotFound = "command_not_found"
)

// ManagedContainer is the saved recreate specification of a standalone
// container created through Docker Manager (#6). The container carries its ID in
// the docker-manager.spec label (or its legacy key); automatic updates (#20)
// recreate it from Spec with its tagged image reference and prior running
// state.
type ManagedContainer struct {
	ID            string
	EnvironmentID string
	Name          string
	// CreateJobID is the container.create job that created it.
	CreateJobID string
	// Spec is the canonical JSON of the create-container form
	// (protocol.ContainerSpec), including environment values: sealed at
	// rest and never returned by the API.
	Spec []byte
	// Start records whether the container was started after creation.
	Start     bool
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}
