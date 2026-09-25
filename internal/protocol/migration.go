package protocol

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Environment migration payloads (#35): the migration.* requests, the
// migration.send / migration.receive streams and the stack.remove_source
// job input. The manager's stack.migrate / volume.migrate jobs drive them;
// data flows source agent -> manager -> destination agent as a framed,
// checksummed tar stream (internal/transfer).
//
// Containment: the source reads only the stack's project directory and the
// selected volumes' data directories; the destination writes only into a
// staging directory of a migration below its stacks volume (renamed to the
// new project directory on commit) and into volumes it creates itself,
// labeled with the migration ID.

// LabelMigration marks the volumes a migration created on its destination
// (value: the migration ID, the job ID of the stack.migrate/volume.migrate
// job). Cleanups remove only volumes carrying the migration's own ID.
const LabelMigration = LabelPrefix + "migration"

// MigrationStagingDir is the directory of the stacks volume that holds
// in-progress migrations (<stacks>/.dockyard-migrations/<migrationId>).
const MigrationStagingDir = ".dockyard-migrations"

// Migration parts (one stream pair each).
const (
	// PartProject is a stack's project directory (its relative bind
	// directories included).
	PartProject = "project"
	// PartVolume is one volume's data.
	PartVolume = "volume"
	// PartImage is an Engine image archive (image save/load) of locally
	// built or unpublished images.
	PartImage = "image"
)

// Migration preview roles.
const (
	RoleSource      = "source"
	RoleDestination = "destination"
)

// MaxMigrationSkipped bounds the skipped entries a result lists.
const MaxMigrationSkipped = 100

// ValidMigrationID reports whether s looks like a job ID (UUID text).
func ValidMigrationID(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && c != '-' {
			return false
		}
	}
	return true
}

// ValidDirName reports whether s is one directory name (a new project
// directory in the stacks volume).
func ValidDirName(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 128 && !strings.ContainsAny(s, "/\\\x00") &&
		!strings.HasPrefix(s, ".") && ValidRelativePath(s)
}

// MigrationPort is a published port.
type MigrationPort struct {
	HostIP string `json:"hostIp,omitempty"`
	// Published is the host port (0: an ephemeral port, never a conflict).
	Published uint16 `json:"published"`
	Target    uint16 `json:"target,omitempty"`
	Protocol  string `json:"protocol"`
}

// MigrationServiceFacts describes a service of the source project.
type MigrationServiceFacts struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Build bool   `json:"build,omitempty"`
	// ContainerNames are the containers Compose creates for the service.
	ContainerNames []string `json:"containerNames,omitempty"`
	// Image on the source Engine (empty ImageID: not present).
	ImageID       string   `json:"imageId,omitempty"`
	ImagePlatform string   `json:"imagePlatform,omitempty"`
	ImageSize     int64    `json:"imageSize,omitempty"`
	RepoDigests   []string `json:"repoDigests,omitempty"`
	// ImageProtected: the image is DockYard's own (#32).
	ImageProtected bool            `json:"imageProtected,omitempty"`
	Ports          []MigrationPort `json:"ports,omitempty"`
	Devices        []string        `json:"devices,omitempty"`
	// Running is true when a container of the service runs.
	Running bool `json:"running,omitempty"`
}

// MigrationContainerUse is a container mounting a volume.
type MigrationContainerUse struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	// Project is the container's Compose project, if any.
	Project string `json:"project,omitempty"`
}

// MigrationVolumeFacts describes a volume on the source.
type MigrationVolumeFacts struct {
	// Key is the Compose volume key (project volumes; empty for anonymous
	// and standalone volumes).
	Key  string `json:"key,omitempty"`
	Name string `json:"name"`
	// External volumes must already exist on the destination.
	External  bool   `json:"external,omitempty"`
	Anonymous bool   `json:"anonymous,omitempty"`
	Driver    string `json:"driver,omitempty"`
	// Exists: the volume exists on the source Engine.
	Exists bool `json:"exists"`
	// Supported: a local volume whose data the agent can read (#28);
	// Reason explains otherwise (definition only, data not migrated).
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	// Protected: DockYard's own volume (#32), never migrated.
	Protected bool              `json:"protected,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	// Bytes and Entries measure the data (Truncated: a lower bound, the
	// scan hit its budget).
	Bytes     int64                   `json:"bytes"`
	Entries   int64                   `json:"entries"`
	Truncated bool                    `json:"truncated,omitempty"`
	UsedBy    []MigrationContainerUse `json:"usedBy,omitempty"`
}

// MigrationNetworkFacts is a network of the source project.
type MigrationNetworkFacts struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	External bool   `json:"external,omitempty"`
	Driver   string `json:"driver,omitempty"`
}

// MigrationProjectFacts describes the source stack's project.
type MigrationProjectFacts struct {
	Name string `json:"name"`
	// Dir is the project directory's host path.
	Dir string `json:"dir"`
	// Protected: DockYard's own Compose project (#32).
	Protected        bool                    `json:"protected,omitempty"`
	ProtectionReason string                  `json:"protectionReason,omitempty"`
	Services         []MigrationServiceFacts `json:"services"`
	Volumes          []MigrationVolumeFacts  `json:"volumes"`
	Networks         []MigrationNetworkFacts `json:"networks"`
	// Binds are the services' bind mounts: inside the project directory
	// (migrated with it) or external (not migrated).
	Binds []ComposeBind `json:"binds"`
	// DirBytes/DirEntries measure the project directory (Truncated: lower
	// bound); DirSkipped counts entries a transfer skips (sockets, devices).
	DirBytes     int64          `json:"dirBytes"`
	DirEntries   int64          `json:"dirEntries"`
	DirSkipped   int64          `json:"dirSkipped,omitempty"`
	DirTruncated bool           `json:"dirTruncated,omitempty"`
	Warnings     []ComposeIssue `json:"warnings,omitempty"`
}

// MigrationSourceQuery asks the source agent about a stack or a volume.
type MigrationSourceQuery struct {
	// Stack is set for a stack migration.
	Stack *ProjectRef `json:"stack,omitempty"`
	// Volume is set for a standalone volume migration.
	Volume string `json:"volume,omitempty"`
	// Measure walks the data to measure it (bounded).
	Measure bool `json:"measure,omitempty"`
}

// MigrationSourceFacts is the source agent's answer.
type MigrationSourceFacts struct {
	// Platform is the Engine's os/arch.
	Platform string                 `json:"platform"`
	Project  *MigrationProjectFacts `json:"project,omitempty"`
	Volume   *MigrationVolumeFacts  `json:"volume,omitempty"`
}

// MigrationDestinationQuery asks the destination agent what a migration
// would collide with.
type MigrationDestinationQuery struct {
	ProjectName string `json:"projectName,omitempty"`
	// Dir is the new project directory in the stacks volume.
	Dir              string          `json:"dir,omitempty"`
	ContainerNames   []string        `json:"containerNames,omitempty"`
	Volumes          []string        `json:"volumes,omitempty"`
	Networks         []string        `json:"networks,omitempty"`
	ExternalNetworks []string        `json:"externalNetworks,omitempty"`
	ExternalVolumes  []string        `json:"externalVolumes,omitempty"`
	Ports            []MigrationPort `json:"ports,omitempty"`
	Images           []string        `json:"images,omitempty"`
}

// MigrationPortConflict is a published port a running container holds.
type MigrationPortConflict struct {
	Port      MigrationPort `json:"port"`
	Container string        `json:"container"`
}

// MigrationExistingVolume is a volume that already exists on the
// destination; Migration is its LabelMigration value.
type MigrationExistingVolume struct {
	Name      string `json:"name"`
	Migration string `json:"migration,omitempty"`
}

// MigrationDestinationFacts is the destination agent's answer.
type MigrationDestinationFacts struct {
	Platform string `json:"platform"`
	// StacksOK/VolumesOK: the storage roots passed the #28 checks.
	StacksOK  bool   `json:"stacksOk"`
	VolumesOK bool   `json:"volumesOk"`
	Reason    string `json:"reason,omitempty"`
	// Free bytes of the stacks volume's and the volume directory's
	// filesystems (-1: unknown).
	StacksFree  int64 `json:"stacksFree"`
	VolumesFree int64 `json:"volumesFree"`
	// ProjectContainers are containers already labeled with the project.
	ProjectContainers []string                  `json:"projectContainers,omitempty"`
	DirExists         bool                      `json:"dirExists,omitempty"`
	Containers        []string                  `json:"containers,omitempty"`
	Volumes           []MigrationExistingVolume `json:"volumes,omitempty"`
	Networks          []string                  `json:"networks,omitempty"`
	MissingNetworks   []string                  `json:"missingNetworks,omitempty"`
	MissingVolumes    []string                  `json:"missingVolumes,omitempty"`
	PortConflicts     []MigrationPortConflict   `json:"portConflicts,omitempty"`
	ImagesPresent     []string                  `json:"imagesPresent,omitempty"`
	// Staged are migrations with a staging directory on this agent
	// (leftovers of interrupted migrations).
	Staged []string `json:"staged,omitempty"`
}

// MigrationPreviewInput is the input of migration.preview.
type MigrationPreviewInput struct {
	Role        string                     `json:"role"`
	Source      *MigrationSourceQuery      `json:"source,omitempty"`
	Destination *MigrationDestinationQuery `json:"destination,omitempty"`
}

// Validate checks the input's shape.
func (in MigrationPreviewInput) Validate() error {
	switch in.Role {
	case RoleSource:
		q := in.Source
		if q == nil || (q.Stack == nil) == (q.Volume == "") {
			return errors.New("source preview: name a stack or a volume")
		}
		if q.Stack != nil {
			return q.Stack.Validate()
		}
		if !ValidDockerName(q.Volume) {
			return errors.New("source preview: invalid volume name")
		}
	case RoleDestination:
		q := in.Destination
		if q == nil {
			return errors.New("destination preview: missing query")
		}
		if q.ProjectName != "" && !ValidProjectName(q.ProjectName) {
			return errors.New("destination preview: invalid project name")
		}
		if q.Dir != "" && !ValidDirName(q.Dir) {
			return errors.New("destination preview: invalid directory")
		}
		for _, n := range slices.Concat(q.Volumes, q.ExternalVolumes, q.Networks, q.ExternalNetworks, q.ContainerNames) {
			if !ValidDockerName(n) {
				return fmt.Errorf("destination preview: invalid name %q", n)
			}
		}
		if len(q.Volumes)+len(q.Networks)+len(q.ContainerNames)+len(q.Ports)+len(q.Images) > 4096 {
			return errors.New("destination preview: too many names")
		}
	default:
		return fmt.Errorf("unknown preview role %q", in.Role)
	}
	return nil
}

// MigrationPreviewOutput is the output of migration.preview.
type MigrationPreviewOutput struct {
	Source      *MigrationSourceFacts      `json:"source,omitempty"`
	Destination *MigrationDestinationFacts `json:"destination,omitempty"`
}

// MigrationSendInput is the input of the migration.send stream (source).
type MigrationSendInput struct {
	// MigrationID is the job ID of the migration.
	MigrationID string `json:"migrationId"`
	Part        string `json:"part"`
	// Stack is the project (PartProject).
	Stack *ProjectRef `json:"stack,omitempty"`
	// Volume is the source volume (PartVolume).
	Volume string `json:"volume,omitempty"`
	// Images are the image references to save (PartImage).
	Images []string `json:"images,omitempty"`
	// ChunkSize of the framing (0: default).
	ChunkSize int `json:"chunkSize,omitempty"`
}

// Validate checks the input's shape.
func (in MigrationSendInput) Validate() error {
	if !ValidMigrationID(in.MigrationID) {
		return errors.New("invalid migration ID")
	}
	switch in.Part {
	case PartProject:
		if in.Stack == nil {
			return errors.New("project part: missing stack")
		}
		return in.Stack.Validate()
	case PartVolume:
		if !ValidDockerName(in.Volume) {
			return errors.New("volume part: invalid volume name")
		}
	case PartImage:
		if len(in.Images) == 0 || len(in.Images) > 64 {
			return errors.New("image part: 1 to 64 images")
		}
		for _, r := range in.Images {
			if r == "" || len(r) > 512 || strings.ContainsAny(r, " \t\n") {
				return fmt.Errorf("image part: invalid reference %q", r)
			}
		}
	default:
		return fmt.Errorf("unknown part %q", in.Part)
	}
	return nil
}

// MigrationSkipped is an entry a transfer left out.
type MigrationSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// MigrationPartResult is a part's result, attached to the final close of
// both streams: the source reports what it sent, the destination what it
// verified and wrote.
type MigrationPartResult struct {
	// Bytes and SHA256 describe the payload (tar or image archive), Chunks
	// its framing; both ends must agree.
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
	Chunks  int64  `json:"chunks"`
	Entries int64  `json:"entries,omitempty"`
	// Skipped entries (at most MaxMigrationSkipped; SkippedCount is the
	// total).
	Skipped      []MigrationSkipped `json:"skipped,omitempty"`
	SkippedCount int64              `json:"skippedCount,omitempty"`
}

// MigrationVolumeSpec is a volume the destination creates.
type MigrationVolumeSpec struct {
	Name string `json:"name"`
	// Labels to set (the source volume's labels, e.g. Compose's); the
	// agent adds LabelMigration.
	Labels map[string]string `json:"labels,omitempty"`
}

// MigrationReceiveInput is the input of the migration.receive stream
// (destination).
type MigrationReceiveInput struct {
	MigrationID string `json:"migrationId"`
	Part        string `json:"part"`
	// Volume is the new volume (PartVolume); it must not exist, or exist
	// labeled with this migration (a retried part starts over).
	Volume *MigrationVolumeSpec `json:"volume,omitempty"`
	// Images are the references expected after loading (PartImage).
	Images []string `json:"images,omitempty"`
}

// Validate checks the input's shape.
func (in MigrationReceiveInput) Validate() error {
	if !ValidMigrationID(in.MigrationID) {
		return errors.New("invalid migration ID")
	}
	switch in.Part {
	case PartProject:
	case PartVolume:
		if in.Volume == nil || !ValidDockerName(in.Volume.Name) {
			return errors.New("volume part: invalid volume")
		}
		if err := validMigratedLabels(in.Volume.Labels); err != nil {
			return err
		}
	case PartImage:
		if len(in.Images) == 0 || len(in.Images) > 64 {
			return errors.New("image part: 1 to 64 images")
		}
	default:
		return fmt.Errorf("unknown part %q", in.Part)
	}
	return nil
}

// validMigratedLabels checks the labels copied from a source volume:
// Compose's labels are kept (so Compose adopts the volume), DockYard's own
// prefix is refused (the agent sets LabelMigration itself).
func validMigratedLabels(labels map[string]string) error {
	if len(labels) > 64 {
		return errors.New("volume part: at most 64 labels")
	}
	for k, v := range labels {
		switch {
		case k == "" || len(k) > 256 || strings.ContainsAny(k, " \t\r\n="):
			return fmt.Errorf("volume part: invalid label key %q", k)
		case strings.HasPrefix(k, LabelPrefix):
			return fmt.Errorf("volume part: label %q uses the reserved prefix %s", k, LabelPrefix)
		case len(v) > 4096:
			return fmt.Errorf("volume part: label %q value is too long", k)
		}
	}
	return nil
}

// MigrationStopInput is the input of migration.stop (source, mutating):
// stop the project's containers in reverse dependency order.
type MigrationStopInput struct {
	MigrationID    string     `json:"migrationId"`
	Stack          ProjectRef `json:"stack"`
	TimeoutSeconds int        `json:"timeoutSeconds,omitempty"`
}

// MigrationStartInput is the input of migration.start (source, mutating):
// start exactly the services that were running before (dependencies first).
type MigrationStartInput struct {
	MigrationID string     `json:"migrationId"`
	Stack       ProjectRef `json:"stack"`
	Services    []string   `json:"services"`
}

// MigrationLifecycleOutput is the output of migration.stop/start.
type MigrationLifecycleOutput struct {
	Before   []ServiceState `json:"before"`
	After    []ServiceState `json:"after"`
	Warnings []string       `json:"warnings,omitempty"`
}

// MigrationCommitInput is the input of migration.commit (destination,
// mutating): move the staged project directory to Dir in the stacks
// volume. Dir must not exist (unless this migration committed it).
type MigrationCommitInput struct {
	MigrationID string `json:"migrationId"`
	Dir         string `json:"dir"`
}

// MigrationCommitOutput is the output of migration.commit.
type MigrationCommitOutput struct {
	// Path is the project directory's host path.
	Path string `json:"path"`
}

// MigrationCleanupInput is the input of migration.cleanup (destination,
// mutating): remove what the migration created. Finished keeps the
// committed project directory and volumes (a completed migration) and
// removes only the staging bookkeeping.
type MigrationCleanupInput struct {
	MigrationID string `json:"migrationId"`
	// Project is the Compose project whose containers (working directory
	// = the committed directory) are removed.
	Project string `json:"project,omitempty"`
	// Volumes are removed when they carry LabelMigration = MigrationID.
	Volumes  []string `json:"volumes,omitempty"`
	Finished bool     `json:"finished,omitempty"`
}

// MigrationCleanupOutput lists what was removed.
type MigrationCleanupOutput struct {
	Removed []string `json:"removed"`
	Kept    []string `json:"kept,omitempty"`
}

// ValidateJobLinked checks the migration ID of a job-linked request.
func ValidateJobLinked(migrationID string) error {
	if !ValidMigrationID(migrationID) {
		return errors.New("invalid migration ID")
	}
	return nil
}

// SourceRemovalInput is the input of the stack.remove_source job (source
// agent): after a completed migration the user confirmed, take the source
// project down, remove the migrated volumes and the project directory.
type SourceRemovalInput struct {
	StackID     string     `json:"stackId"`
	MigrationID string     `json:"migrationId"`
	Stack       ProjectRef `json:"stack"`
	Volumes     []string   `json:"volumes,omitempty"`
}

// SourceRemovalOutput is the job's result output.
type SourceRemovalOutput struct {
	RemovedContainers []string `json:"removedContainers,omitempty"`
	RemovedVolumes    []string `json:"removedVolumes,omitempty"`
	RemovedDirectory  bool     `json:"removedDirectory,omitempty"`
}
