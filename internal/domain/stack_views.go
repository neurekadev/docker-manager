package domain

import "time"

// Requests and views of the stack service (#7), shared with the API.

// StackDefinition is a definition submitted by a user: its Compose file,
// optional override file and .env (Files carry Path and Content).
type StackDefinition struct {
	EnvironmentID string
	// Name is the Compose project name (and project directory name).
	Name  string
	Files []StackFile
}

// StackCreate creates a stack from a definition.
type StackCreate struct {
	StackDefinition
	DisplayName string
	Meta        DisplayMeta
}

// StackImport adopts a discovered Compose project: in place (Files empty)
// or from an explicit Compose source written into a new directory.
type StackImport struct {
	EnvironmentID string
	ProjectName   string
	DisplayName   string
	Meta          DisplayMeta
	Files         []StackFile
}

// StackPatch edits display metadata (never Compose files).
type StackPatch struct {
	DisplayName *string
	Description *string
	Icon        *string
	// Services replaces the metadata of the named services (a zero value
	// clears it).
	Services map[string]DisplayMeta
}

// StackDeployOptions configure a deploy.
type StackDeployOptions struct {
	// Pull "always" pulls every image first; default "missing".
	Pull          string
	Build         bool
	ForceRecreate bool
	RemoveOrphans bool
	// BuildTimeoutSeconds bounds the images built by the deploy (0 = the
	// default build timeout).
	BuildTimeoutSeconds int
}

// StackBuildOptions configure an explicit stack build (#33).
type StackBuildOptions struct {
	NoCache bool
	// Pull pulls newer base images.
	Pull bool
	// TimeoutSeconds bounds the build (0 = the default build timeout).
	TimeoutSeconds int
	// RegistryIDs name the registry connections for base images; empty
	// offers the environment's host-wide connection per registry.
	RegistryIDs []string
}

// StackJobRequest carries the common options of stack jobs.
type StackJobRequest struct {
	IdempotencyKey string
	// Services narrows the operation (empty = the whole stack).
	Services       []string
	TimeoutSeconds int
}

// StackServiceInfo is a validated service with its label metadata.
type StackServiceInfo struct {
	StackServiceDef
	Profiles []string
	Meta     DisplayMeta
}

// StackValidation is the result of validating a definition.
type StackValidation struct {
	Valid       bool
	ProjectName string
	Errors      []StackIssue
	Warnings    []StackIssue
	Services    []StackServiceInfo
	Binds       []StackBind
}

// DiscoveredService is a service of a discovered Compose project.
type DiscoveredService struct {
	Name       string
	Image      string
	Containers int
	Running    int
}

// DiscoveredStack is a Compose project found on an Engine from container
// labels (read-only until imported).
type DiscoveredStack struct {
	Name        string
	WorkingDir  string
	ConfigFiles []string
	// Root and Dir locate WorkingDir under a verified stack root.
	Root      string
	Dir       string
	Services  []DiscoveredService
	Adoptable bool
	// Copyable: it can be imported by copying its directory into the
	// stacks volume (the agent reads it through an import mount).
	Copyable bool
	// SourceDir is where a copyable project's files are on the host (may
	// differ from WorkingDir, a manager's internal path).
	SourceDir string
	Reason    string
	// StackID is the Docker Manager stack managing the project, if any.
	StackID string
	// Protected: Docker Manager's own project (#32); an import by copy
	// copies it while it runs, restarting nothing.
	Protected bool
}

// PortMapping is a published container port.
type PortMapping struct {
	PrivatePort uint16
	PublicPort  uint16
	HostIP      string
	Protocol    string
}

// StackContainer is a service container as seen on the Engine.
type StackContainer struct {
	ID            string
	Name          string
	Service       string
	Image         string
	ImageID       string
	State         string
	Health        string
	ExitCode      int
	OneOff        bool
	RestartPolicy string
	Ports         []PortMapping
	NanoCPUs      int64
	CPUShares     int64
	Memory        int64
	PidsLimit     *int64
	CreatedAt     time.Time
	StartedAt     *time.Time
	Networks      []ContainerAddress
}

// ContainerAddress is a container's addresses on one network (empty while
// it is stopped).
type ContainerAddress struct {
	Network string
	IPv4    string
	IPv6    string
}

// StackServiceView is a service with its expected definition, display
// metadata, containers and drift from Docker Manager's intent.
type StackServiceView struct {
	Name       string
	Expected   *StackServiceDef
	Meta       DisplayMeta
	Applied    *StackImage
	Containers []StackContainer
	// Status: running, partial, exited, created or missing.
	Status string
	Drift  []string
}

// StackServicesView lists a stack's services.
type StackServicesView struct {
	Services []StackServiceView
	// Live: read from the Engine now; otherwise the last observed summary
	// (environment offline).
	Live       bool
	ObservedAt *time.Time
	Drift      bool
}

// StackImageView is a service's applied image and its eligibility for
// digest-driven updates (#20).
type StackImageView struct {
	Service  string
	Image    string
	ImageID  string
	Digest   string
	Platform string
	Build    bool
	Eligible bool
	// Reason explains ineligibility (domain.UpdateReason*: build_only,
	// digest_pinned, untagged, pull_policy_conflict, invalid_reference);
	// ReasonMessage in plain language (also the warning of an eligible
	// non-version tag).
	Reason        string
	ReasonMessage string
	// NonVersionTag: eligible, but the tag ("latest", "main") can change
	// meaning.
	NonVersionTag bool
}

// StackRestore is the outcome of a revision restore.
type StackRestore struct {
	Stack    Stack
	Revision StackRevision
	// DeployOffered: the restored bytes differ from the last applied
	// revision; the client offers a deploy (never started automatically).
	DeployOffered bool
}

// StackRemoveOptions are the choices of a stack removal.
type StackRemoveOptions struct {
	// Volumes also removes the volumes the stack owns (declared, not
	// external, created by Compose for the project; anonymous volumes of
	// its containers). Others are always kept.
	Volumes bool
}
