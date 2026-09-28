package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// Compose stack payloads (#7): the inputs and outputs of the compose.*
// requests and the input/output of the stack.* job kinds. They are shared by
// the manager (internal/manager/stacks) and the agent (internal/agent/stacks)
// and never carry registry credentials (#19 adds them per operation).

// Stack roots (ProjectRef.Root).
const (
	// RootStacks is the environment's stacks volume (#28).
	RootStacks = "stacks"
	// RootBind is a registered stack root (DOCKER_AGENT_STACK_ROOTS, #28).
	RootBind = "bind"
)

// Source file bounds. A Compose definition (compose files, override files
// and env files) is small; the bounds keep requests, revisions and job
// results well below MaxFrameSize.
const (
	// MaxSourceFile bounds one definition file.
	MaxSourceFile = 256 << 10
	// MaxSourceTotal bounds all definition files of a project.
	MaxSourceTotal = 512 << 10
	// MaxSourceFiles bounds the number of definition files.
	MaxSourceFiles = 32
	// MaxInlineSources bounds the file contents a stack.deploy result
	// carries; larger definitions are reported by hash only.
	MaxInlineSources = 96 << 10
)

// ProjectRef locates a stack's Compose project on the agent.
type ProjectRef struct {
	// Root is RootStacks or RootBind.
	Root string `json:"root"`
	// RootPath is the registered root's host path (RootBind only).
	RootPath string `json:"rootPath,omitempty"`
	// Dir is the project directory relative to the root: clean,
	// slash-separated, never "." or escaping the root.
	Dir string `json:"dir"`
	// ProjectName is the Compose project name.
	ProjectName string `json:"projectName"`
	// ConfigFiles are relative to Dir; empty selects compose.yaml (or its
	// alternatives) plus the matching override file.
	ConfigFiles []string `json:"configFiles,omitempty"`
	// EnvFiles are relative to Dir; empty uses .env when present.
	EnvFiles []string `json:"envFiles,omitempty"`
	Profiles []string `json:"profiles,omitempty"`
}

// Validate checks the reference's shape (the agent resolves and guards it).
func (r ProjectRef) Validate() error {
	switch r.Root {
	case RootStacks:
		if r.RootPath != "" {
			return errors.New("stack ref: rootPath applies to bind roots only")
		}
	case RootBind:
		if !strings.HasPrefix(r.RootPath, "/") || path.Clean(r.RootPath) != r.RootPath {
			return errors.New("stack ref: rootPath must be a clean absolute path")
		}
	default:
		return fmt.Errorf("stack ref: unknown root %q", r.Root)
	}
	if !ValidRelativePath(r.Dir) || r.Dir == "." {
		return fmt.Errorf("stack ref: invalid project directory %q", r.Dir)
	}
	if !ValidProjectName(r.ProjectName) {
		return fmt.Errorf("stack ref: invalid project name %q", r.ProjectName)
	}
	for _, f := range append(slices.Clone(r.ConfigFiles), r.EnvFiles...) {
		if !ValidRelativePath(f) || f == "." {
			return fmt.Errorf("stack ref: invalid file %q", f)
		}
	}
	return nil
}

// ValidProjectName reports whether s is a normalized Compose project name:
// lower-case letters, digits, '-' and '_', starting with a letter or digit,
// at most 63 bytes (it also names the project directory).
func ValidProjectName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// SourceFile is one Compose definition file (compose file, override file,
// .env or a service env_file inside the project directory).
type SourceFile struct {
	// Path is relative to the project directory (slash-separated).
	Path string `json:"path"`
	// Content is the file's bytes (base64 in JSON); absent when only the
	// hash is reported.
	Content []byte `json:"content,omitempty"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// SourceSnapshot is a Compose definition as found on disk.
type SourceSnapshot struct {
	// Hash identifies the definition: SourceHash of Files.
	Hash  string       `json:"hash"`
	Files []SourceFile `json:"files"`
	// ContentOmitted: Files carry hashes only (the contents exceeded the
	// inline budget of a job result).
	ContentOmitted bool `json:"contentOmitted,omitempty"`
}

// FileHash returns the hex SHA-256 of b.
func FileHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// SourceHash is the definition hash: SHA-256 over "<sha256>  <path>\n"
// lines sorted by path. Manager and agent compute it identically, so a
// revision recorded by the manager and bytes found on disk compare equal.
func SourceHash(files []SourceFile) string {
	lines := make([]string, 0, len(files))
	for _, f := range files {
		lines = append(lines, f.SHA256+"  "+f.Path+"\n")
	}
	slices.SortFunc(lines, func(a, b string) int {
		return strings.Compare(a[strings.Index(a, "  ")+2:], b[strings.Index(b, "  ")+2:])
	})
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewSourceSnapshot builds a snapshot with hashes computed from contents.
func NewSourceSnapshot(files []SourceFile) SourceSnapshot {
	out := make([]SourceFile, 0, len(files))
	for _, f := range files {
		f.SHA256 = FileHash(f.Content)
		f.Size = int64(len(f.Content))
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b SourceFile) int { return strings.Compare(a.Path, b.Path) })
	return SourceSnapshot{Hash: SourceHash(out), Files: out}
}

// ValidateSources checks a definition file set sent to the agent (paths,
// duplicates and bounds).
func ValidateSources(files []SourceFile) error {
	if len(files) > MaxSourceFiles {
		return fmt.Errorf("at most %d definition files", MaxSourceFiles)
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range files {
		if !ValidRelativePath(f.Path) || f.Path == "." {
			return fmt.Errorf("invalid file path %q", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("duplicate file %q", f.Path)
		}
		seen[f.Path] = true
		if len(f.Content) > MaxSourceFile {
			return fmt.Errorf("%s is larger than %d bytes", f.Path, MaxSourceFile)
		}
		total += len(f.Content)
	}
	if total > MaxSourceTotal {
		return fmt.Errorf("definition files are larger than %d bytes in total", MaxSourceTotal)
	}
	return nil
}

// ComposeIssue is a validation finding.
type ComposeIssue struct {
	// Code is stable: invalid_project, unsupported_compose_feature,
	// obsolete_version, bind_outside_project, agent_self_update, or a
	// storage_* code (#28).
	Code    string `json:"code"`
	Message string `json:"message"`
	Service string `json:"service,omitempty"`
}

// Validation issue codes.
const (
	IssueInvalidProject      = "invalid_project"
	IssueUnsupportedFeature  = "unsupported_compose_feature"
	IssueObsoleteVersion     = "obsolete_version"
	IssueBindOutsideProject  = "bind_outside_project"
	IssueProjectNameMismatch = "project_name_mismatch"
	// IssueAgentSelfUpdate (deploy warning, #32): the agent's own service
	// is recreated by a helper container right after the job.
	IssueAgentSelfUpdate = "agent_self_update"
)

// ComposeDependency is a depends_on entry.
type ComposeDependency struct {
	Service   string `json:"service"`
	Condition string `json:"condition"`
	Required  bool   `json:"required"`
	Restart   bool   `json:"restart,omitempty"`
}

// ComposeService summarizes a service of a loaded project.
type ComposeService struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	// Build is true for services with a build section (#33).
	Build     bool                `json:"build,omitempty"`
	DependsOn []ComposeDependency `json:"dependsOn,omitempty"`
	Profiles  []string            `json:"profiles,omitempty"`
	// Description and Icon come from the dev.neureka.docker-manager.description /
	// .icon labels; the manager imports them as display metadata once.
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	// PullPolicy is the service's pull_policy (empty: Compose's default,
	// missing); #20 refuses policies that conflict with digest updates.
	PullPolicy string `json:"pullPolicy,omitempty"`
}

// ComposeBind is a resolved bind-mount source (#7, #10).
type ComposeBind struct {
	Service string `json:"service"`
	// Source is the resolved host path.
	Source string `json:"source"`
	Target string `json:"target"`
	// RelPath is the source relative to the project directory when it lies
	// inside it (stack backups include it by default, #10).
	RelPath string `json:"relPath,omitempty"`
	// External: outside the project directory; backups need an explicit
	// opt-in and allowlist (#10).
	External bool `json:"external,omitempty"`
	ReadOnly bool `json:"readOnly,omitempty"`
}

// ComposeValidateInput is the input of compose.validate. With Files the
// given content is validated in memory as if it were in the project
// directory (nothing is written); without, the on-disk definition is.
type ComposeValidateInput struct {
	Stack ProjectRef   `json:"stack"`
	Files []SourceFile `json:"files,omitempty"`
}

// ComposeValidateOutput is the output of compose.validate.
type ComposeValidateOutput struct {
	Valid       bool             `json:"valid"`
	ProjectName string           `json:"projectName,omitempty"`
	Errors      []ComposeIssue   `json:"errors"`
	Warnings    []ComposeIssue   `json:"warnings"`
	Services    []ComposeService `json:"services"`
	Binds       []ComposeBind    `json:"binds"`
}

// ComposeReadInput is the input of compose.read.
type ComposeReadInput struct {
	Stack ProjectRef `json:"stack"`
}

// ComposeReadOutput is the output of compose.read: the definition on disk.
type ComposeReadOutput struct {
	Snapshot SourceSnapshot `json:"snapshot"`
	// Missing: the project directory or its Compose file does not exist.
	Missing bool `json:"missing,omitempty"`
}

// Write modes of compose.write.
const (
	// WriteCreate creates the project directory; it must not exist (or be
	// empty). Nothing is ever overwritten.
	WriteCreate = "create"
	// WriteReplace replaces definition files of an existing project; the
	// current definition must hash to ExpectHash.
	WriteReplace = "replace"
)

// ComposeWriteInput is the input of compose.write (mutating).
type ComposeWriteInput struct {
	Stack ProjectRef   `json:"stack"`
	Mode  string       `json:"mode"`
	Files []SourceFile `json:"files"`
	// ExpectHash is the definition hash the replaced files must have
	// (WriteReplace); a different hash answers conflict.
	ExpectHash string `json:"expectHash,omitempty"`
	// Remove lists definition files to delete (WriteReplace: files of the
	// current definition that the written revision does not contain).
	Remove []string `json:"remove,omitempty"`
}

// ComposeWriteOutput is the output of compose.write.
type ComposeWriteOutput struct {
	Snapshot SourceSnapshot `json:"snapshot"`
}

// DiscoveredService is a service of a discovered project.
type DiscoveredService struct {
	Name       string `json:"name"`
	Image      string `json:"image"`
	Containers int    `json:"containers"`
	Running    int    `json:"running"`
}

// DiscoveredProject is a Compose project found through container labels,
// or (Containerless) through its Compose file in a folder of a stack root
// or an import mount.
type DiscoveredProject struct {
	Name string `json:"name"`
	// WorkingDir and ConfigFiles come from the com.docker.compose.* labels
	// (host paths). Labels never reconstruct the source. A containerless
	// project's WorkingDir is its folder's host path and ConfigFiles are
	// empty (Compose's default files).
	WorkingDir  string   `json:"workingDir,omitempty"`
	ConfigFiles []string `json:"configFiles,omitempty"`
	EnvFiles    []string `json:"envFiles,omitempty"`
	// Root, RootPath and Dir locate WorkingDir under a verified stack root
	// (adoptable in place); empty otherwise.
	Root     string              `json:"root,omitempty"`
	RootPath string              `json:"rootPath,omitempty"`
	Dir      string              `json:"dir,omitempty"`
	Services []DiscoveredService `json:"services"`
	// Adoptable: the project directory is under the stacks volume or a
	// verified root and its config files are inside it; Reason explains
	// otherwise.
	Adoptable bool   `json:"adoptable"`
	Reason    string `json:"reason,omitempty"`
	// Copyable: not adoptable in place, but the agent reads the project
	// directory through an import mount (below /import, #7) and its
	// config files are inside it: stack.import copies the directory into
	// a new directory <name> of the stacks volume. Absent from older
	// agents.
	Copyable bool `json:"copyable,omitempty"`
	// SourceDir is the copyable project's directory on the host: WorkingDir,
	// or the path a manager such as Arcane saw inside its own container
	// translated through that container's mounts. The manager sends it as
	// StackImportSource.WorkingDir (older agents: WorkingDir).
	SourceDir string `json:"sourceDir,omitempty"`
	// Protected: Docker Manager's own project (#32); an import by copy
	// copies it while it runs (StackImportReport.Live). Absent from older
	// agents.
	Protected bool `json:"protected,omitempty"`
	// Containerless: the project has no container on the Engine (never
	// started, or taken down); it was found through its Compose file in a
	// direct subfolder of a stack root or up to two levels below an import
	// mount, and its Services come from that file (no containers). A
	// project with containers of the same name wins. Absent from older
	// agents.
	Containerless bool `json:"containerless,omitempty"`
	// Volumes are the existing named Docker volumes of the project (sorted,
	// at most MaxDiscoveredVolumes): those labeled with its project name,
	// those its containers mount and those its Compose file names; Docker
	// Manager's own volumes are left out. Absent from older agents.
	Volumes []string `json:"volumes,omitempty"`
}

// MaxDiscoveredVolumes bounds DiscoveredProject.Volumes.
const MaxDiscoveredVolumes = 100

// ComposeDiscoverOutput is the output of compose.discover (no input).
type ComposeDiscoverOutput struct {
	Projects []DiscoveredProject `json:"projects"`
}

// ComposeServicesInput is the input of compose.services.
type ComposeServicesInput struct {
	ProjectName string `json:"projectName"`
}

// PortMapping is a published container port.
type PortMapping struct {
	PrivatePort uint16 `json:"privatePort"`
	PublicPort  uint16 `json:"publicPort,omitempty"`
	HostIP      string `json:"hostIp,omitempty"`
	Protocol    string `json:"protocol"`
}

// ContainerResources are a container's configured limits (0 = unlimited).
type ContainerResources struct {
	NanoCPUs  int64  `json:"nanoCpus,omitempty"`
	CPUShares int64  `json:"cpuShares,omitempty"`
	Memory    int64  `json:"memory,omitempty"`
	PidsLimit *int64 `json:"pidsLimit,omitempty"`
}

// StackContainer is a container of a Compose project as seen on the Engine.
type StackContainer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Service  string `json:"service"`
	Image    string `json:"image"`
	ImageID  string `json:"imageId"`
	State    string `json:"state"`
	Health   string `json:"health,omitempty"`
	ExitCode int    `json:"exitCode,omitempty"`
	// OneOff marks `compose run` containers.
	OneOff        bool               `json:"oneOff,omitempty"`
	RestartPolicy string             `json:"restartPolicy,omitempty"`
	Ports         []PortMapping      `json:"ports,omitempty"`
	Resources     ContainerResources `json:"resources"`
	// ConfigHash is Compose's service configuration hash label.
	ConfigHash string     `json:"configHash,omitempty"`
	CreatedAt  time.Time  `json:"createdAt,omitzero"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	// Networks are the container's endpoints with their addresses (name,
	// network ID, IPv4 and IPv6 only; absent from older agents).
	Networks []ContainerNetwork `json:"networks,omitempty"`
	// Volumes are the container's volume mounts, sorted by destination
	// (bind mounts and tmpfs are not volumes; absent from older agents).
	Volumes []StackContainerVolume `json:"volumes,omitempty"`
}

// StackContainerVolume is a volume a stack container mounts.
type StackContainerVolume struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly,omitempty"`
	// Anonymous: a volume the Engine created for an anonymous mount (its
	// name is a random 64-digit hex ID).
	Anonymous bool `json:"anonymous,omitempty"`
}

// AnonymousVolumeName reports whether name has the form the Engine gives
// anonymous volumes (64 lowercase hex digits). Compose names its volumes
// <project>_<key>, which never has that form.
func AnonymousVolumeName(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ComposeServicesOutput is the output of compose.services.
type ComposeServicesOutput struct {
	Containers []StackContainer `json:"containers"`
}

// Stack job inputs (stack.deploy, stack.start, stack.stop, stack.restart,
// stack.down, stack.remove).

// StackJobInput is the input of every stack.* job kind.
type StackJobInput struct {
	// StackID is the Docker Manager stack (for logs; the agent does not need it).
	StackID string     `json:"stackId"`
	Stack   ProjectRef `json:"stack"`
	// Services narrows start/stop/restart/deploy (empty = all).
	Services []string `json:"services,omitempty"`
	// Pull is "missing" (default) or "always" (deploy).
	Pull string `json:"pull,omitempty"`
	// Build rebuilds every build section (deploy; otherwise only missing images).
	Build         bool `json:"build,omitempty"`
	ForceRecreate bool `json:"forceRecreate,omitempty"`
	RemoveOrphans bool `json:"removeOrphans,omitempty"`
	// NoCache builds without the build cache and PullBase pulls newer base
	// images (stack.build, #33).
	NoCache  bool `json:"noCache,omitempty"`
	PullBase bool `json:"pullBase,omitempty"`
	// BuildTimeoutSeconds bounds the builds of a stack.build or a deploy's
	// build step (0 = jobspec.DefaultBuildTimeout, at most
	// jobspec.MaxBuildTimeout).
	BuildTimeoutSeconds int `json:"buildTimeoutSeconds,omitempty"`
	// TimeoutSeconds bounds stop grace periods (0 = service defaults).
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// RegistryConnections are the registry connections (#19) the manager
	// selected for the stack's images (jobspec.CredentialRefs); their
	// credentials arrive with each dispatch in the command's secrets, never
	// in the input.
	RegistryConnections []string `json:"registryConnections,omitempty"`
	// RemoveVolumes (stack.remove) also removes the volumes the stack
	// owns: the named volumes its definition declares (not external) that
	// Compose created for this project, and the anonymous volumes of its
	// containers. A volume another container uses, a protected one (#32)
	// or one in KeepVolumes stays. Sent only to agents announcing
	// FeatureStackRemoveVolumes.
	RemoveVolumes bool `json:"removeVolumes,omitempty"`
	// KeepVolumes are volumes the manager holds (a migrated stack's
	// retained source with the same project name, #35).
	KeepVolumes []string `json:"keepVolumes,omitempty"`
	// Import (stack.import) is the discovered project to copy into Stack
	// (a new directory of the stacks volume). Sent only to agents
	// announcing FeatureStackImportCopy.
	Import *StackImportSource `json:"import,omitempty"`
	// Rename (stack.rename) is the project's new name and directory. Sent
	// only to agents announcing FeatureStackRename.
	Rename *StackRename `json:"rename,omitempty"`
}

// FeatureStackRename is the capabilities feature of agents that execute
// stack.rename and serve compose.rename_preview.
const FeatureStackRename = "stack.rename"

// StackRename moves a stack's Compose project to another project name
// (stack.rename, compose.rename_preview): its containers stop, its named
// volumes move to the new project's names (so their data follows), the
// containers outside the stack that mount them are recreated on the new
// names, the project directory is renamed and the stack starts again.
type StackRename struct {
	// ProjectName is the new Compose project name.
	ProjectName string `json:"projectName"`
	// Dir is the new project directory relative to the root: the current
	// one renamed to ProjectName when it is named after the project, else
	// the current one.
	Dir string `json:"dir"`
}

// Validate checks the rename of from.
func (r StackRename) Validate(from ProjectRef) error {
	if !ValidProjectName(r.ProjectName) {
		return fmt.Errorf("rename: invalid project name %q", r.ProjectName)
	}
	if r.ProjectName == from.ProjectName {
		return errors.New("rename: the project already has this name")
	}
	if r.Dir == from.Dir {
		return nil
	}
	if !ValidRelativePath(r.Dir) || r.Dir == "." || path.Dir(r.Dir) != path.Dir(from.Dir) ||
		path.Base(r.Dir) != r.ProjectName || path.Base(from.Dir) != from.ProjectName {
		return errors.New("rename: only a directory named after the project is renamed, in place, to the new name")
	}
	return nil
}

// Rename volume actions (StackRenameVolume.Action).
const (
	// RenameVolumeMove: a local volume whose data moves to a new volume
	// with the new name (a rename on the same disk, nothing is copied).
	RenameVolumeMove = "move"
	// RenameVolumeRecreate: a volume with driver options (a host path,
	// NFS, ...): a new volume with the same driver and options replaces
	// it; the data stays where the options point.
	RenameVolumeRecreate = "recreate"
	// RenameVolumeAbsent: the volume does not exist yet; Compose creates
	// it under the new name.
	RenameVolumeAbsent = "absent"
)

// StackRenameVolume is a volume a rename moves.
type StackRenameVolume struct {
	// Key is the Compose key of a named volume ("" for an anonymous one).
	Key string `json:"key,omitempty"`
	// Name is the volume's current name; NewName the name after the rename
	// (an anonymous volume's is only known once its container is created).
	Name    string `json:"name"`
	NewName string `json:"newName,omitempty"`
	// Service, Number and Target locate an anonymous volume's mount.
	Service string `json:"service,omitempty"`
	Number  string `json:"number,omitempty"`
	Target  string `json:"target,omitempty"`
	Action  string `json:"action"`
	// Done: the data is under NewName (journaled per volume).
	Done bool `json:"done,omitempty"`
}

// StackRenameContainer is a container outside the stack that mounts a
// volume the rename moves: it is stopped and recreated on the new names
// with its complete configuration.
type StackRenameContainer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	// Volumes are the moved volumes it mounts (current names).
	Volumes []string `json:"volumes"`
	// NewID is the recreated container.
	NewID string `json:"newId,omitempty"`
}

// StackRenamePlan is what a rename does (compose.rename_preview, and the
// plan a stack.rename journals before it changes anything).
type StackRenamePlan struct {
	From    string `json:"from"`
	To      string `json:"to"`
	FromDir string `json:"fromDir"`
	ToDir   string `json:"toDir"`
	// DeclaredName is the top-level name: of the Compose files ("" none).
	DeclaredName string `json:"declaredName,omitempty"`
	// Running are the services that run (stopped and started again).
	Running    []string               `json:"running,omitempty"`
	Volumes    []StackRenameVolume    `json:"volumes,omitempty"`
	Containers []StackRenameContainer `json:"containers,omitempty"`
	// Blockers refuse the rename (nothing changes); Warnings do not.
	Blockers []ComposeIssue `json:"blockers,omitempty"`
	Warnings []ComposeIssue `json:"warnings,omitempty"`
}

// StackRenameReport is what a stack.rename did.
type StackRenameReport struct {
	StackRenamePlan
	// Stopped: the stack and the outside containers were stopped.
	Stopped bool `json:"stopped,omitempty"`
	// DirMoved: the project directory has its new name.
	DirMoved bool `json:"dirMoved,omitempty"`
	// Switched: the old project's containers were removed; the stack lives
	// under the new name even if the job failed afterwards.
	Switched bool `json:"switched,omitempty"`
}

// ComposeRenamePreviewInput is compose.rename_preview's input.
type ComposeRenamePreviewInput struct {
	Stack  ProjectRef  `json:"stack"`
	Rename StackRename `json:"rename"`
	// KeepVolumes are volumes the manager holds (see StackJobInput).
	KeepVolumes []string `json:"keepVolumes,omitempty"`
}

// FeatureStackRemoveVolumes is the capabilities feature of agents whose
// stack.remove honors StackJobInput.RemoveVolumes.
const FeatureStackRemoveVolumes = "stack.remove_volumes"

// FeatureStackImportCopy is the capabilities feature of agents that
// execute stack.import (import a project by copying its directory).
const FeatureStackImportCopy = "stack.import_copy"

// FeatureStackImportContainerless is the capabilities feature of agents
// whose stack.import accepts StackImportSource.Containerless (import a
// project that has no containers by copy).
const FeatureStackImportContainerless = "stack.import_containerless"

// StackImportSource is where a discovered project lives now (from its
// containers' labels, or the folder discovery found it in).
type StackImportSource struct {
	// WorkingDir is the project directory's host path.
	WorkingDir string `json:"workingDir"`
	// Containerless: discovery found the project without containers
	// (DiscoveredProject.Containerless). The import refuses it once the
	// project has containers, stops, recreates and starts nothing, and only
	// switches the project to the copy. Sent only to agents announcing
	// FeatureStackImportContainerless.
	Containerless bool `json:"containerless,omitempty"`
}

// Validate checks the source's shape (the agent maps it to an import
// mount).
func (s StackImportSource) Validate() error {
	if !strings.HasPrefix(s.WorkingDir, "/") || path.Clean(s.WorkingDir) != s.WorkingDir || s.WorkingDir == "/" {
		return errors.New("import source: workingDir must be a clean absolute path below /")
	}
	return nil
}

// StackImportReport is what a stack.import did with the files.
type StackImportReport struct {
	// Entries and Bytes count the copied tree (bytes of regular files).
	Entries int64 `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// Skipped entries could not be copied (sockets, device nodes).
	Skipped      []MigrationSkipped `json:"skipped,omitempty"`
	SkippedCount int64              `json:"skippedCount,omitempty"`
	// Copied: the complete copy is in place in the stacks volume.
	Copied bool `json:"copied,omitempty"`
	// Switched: the containers were (being) recreated from the copy; the
	// project now lives in the stacks volume even if the job failed.
	Switched bool `json:"switched,omitempty"`
	// WasRunning are the services that ran before the import (started
	// again at the end).
	WasRunning []string `json:"wasRunning,omitempty"`
	// Live: Docker Manager's own project (#32), copied while it runs; its
	// containers are neither stopped nor recreated (the stack's next
	// deploy moves them onto the copy).
	Live bool `json:"live,omitempty"`
}

// ComposeVolumeLabel is the Compose key of a volume Compose created.
const ComposeVolumeLabel = "com.docker.compose.volume"

// StackVolume is a volume a stack.remove with RemoveVolumes found the
// stack owns, and what became of it.
type StackVolume struct {
	Name string `json:"name"`
	// Key is the Compose key of a declared volume ("" for an anonymous one).
	Key       string `json:"key,omitempty"`
	Anonymous bool   `json:"anonymous,omitempty"`
	// Status is pending, removed or kept.
	Status string `json:"status"`
	// Reason tells why a volume was kept.
	Reason string `json:"reason,omitempty"`
}

// Stack volume statuses.
const (
	StackVolumePending = "pending"
	StackVolumeRemoved = "removed"
	StackVolumeKept    = "kept"
)

// AppliedImage is the image a service runs after a deploy (#20 baseline).
type AppliedImage struct {
	Service string `json:"service"`
	// Image is the resolved reference from the Compose model.
	Image   string `json:"image"`
	ImageID string `json:"imageId,omitempty"`
	// Digest is the repository digest of Image on this host (empty for
	// images built locally or never pulled).
	Digest   string `json:"digest,omitempty"`
	Platform string `json:"platform,omitempty"`
	Build    bool   `json:"build,omitempty"`
}

// ServiceState records a service's containers before or after an
// operation (the agent journals it for recovery, #7).
type ServiceState struct {
	Service    string `json:"service"`
	Containers int    `json:"containers"`
	Running    int    `json:"running"`
	// ImageIDs of the service's containers (the previous images of a
	// failed deploy are still on the host).
	ImageIDs []string `json:"imageIds,omitempty"`
}

// StackJobOutput is the result output of stack.* jobs.
type StackJobOutput struct {
	// Sources is the definition the deploy used (stack.deploy), recorded as
	// the applied revision.
	Sources  *SourceSnapshot  `json:"sources,omitempty"`
	Services []ComposeService `json:"services,omitempty"`
	Images   []AppliedImage   `json:"images,omitempty"`
	// Built are the images this job built from build sections
	// (stack.build, and the build step of stack.deploy).
	Built    []AppliedImage `json:"built,omitempty"`
	Binds    []ComposeBind  `json:"binds,omitempty"`
	Warnings []ComposeIssue `json:"warnings,omitempty"`
	// Before is the Engine state captured before the operation changed
	// anything; After the state when it finished (also on failure).
	Before []ServiceState `json:"before"`
	After  []ServiceState `json:"after"`
	// Volumes (stack.remove with RemoveVolumes) are the stack's own
	// volumes, determined before anything was taken down.
	Volumes        []StackVolume `json:"volumes,omitempty"`
	VolumesPlanned bool          `json:"volumesPlanned,omitempty"`
	// Import (stack.import) reports the copy and whether the project
	// switched to it.
	Import *StackImportReport `json:"import,omitempty"`
	// Rename (stack.rename) reports the plan and how far the rename got.
	Rename *StackRenameReport `json:"rename,omitempty"`
	// Unchanged (stack.deploy): the deploy started no container (none was
	// created, recreated or restarted): the Engine already ran the
	// definition. The manager then keeps the stack's last deploy time.
	// Older agents never set it.
	Unchanged bool `json:"unchanged,omitempty"`
	// Pulled (stack.pull) are the services whose image reference names a
	// different image after the pull than before: the next deploy runs it.
	Pulled []string `json:"pulled,omitempty"`
}

// FeatureStackPull is the capabilities feature of agents that execute
// stack.pull (pull a stack's images without recreating containers).
const FeatureStackPull = "stack.pull"
