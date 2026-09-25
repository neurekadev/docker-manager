package domain

import "time"

// Docker resources of an environment (#6): containers, images, volumes and
// networks live on the environment's Engine and are read through its agent;
// the manager keeps only what the Engine cannot: the recreate
// specification of DockYard-managed standalone containers.

// DockerError is a stable failure of a Docker resource operation. Code is
// one of the Docker* codes below; the API maps it to its error catalog
// (docs/api/errors.md).
type DockerError struct {
	Code    string
	Message string
	// Field is set for input problems (a JSON body member).
	Field string
}

func (e *DockerError) Error() string { return e.Code + ": " + e.Message }

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
	// DockerInvalid: an input the Engine or DockYard refused (422).
	DockerInvalid = "validation_failed"
	// DockerConflict: generic state conflict.
	DockerConflict = "conflict"
	// DockerStackManaged: the object belongs to a DockYard-managed stack;
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
)

// ManagedContainer is the saved recreate specification of a standalone
// container created through DockYard (#6). The container carries its ID in
// the dev.neureka.dockyard.spec label; automatic updates (#20) recreate it
// from Spec with its tagged image reference and prior running state.
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
