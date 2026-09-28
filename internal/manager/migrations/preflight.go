package migrations

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Finding codes (stable; the API returns them in blockers and warnings).
const (
	FindingSameEnvironment       = "same_environment"
	FindingEnvironmentOffline    = "environment_offline"
	FindingAgentUnsupported      = "agent_unsupported"
	FindingStorageUnavailable    = "storage_unavailable"
	FindingDockerManagerResource = "docker_manager_resource"
	FindingPlatformMismatch      = "platform_mismatch"
	FindingImageNotPullable      = "image_not_pullable"
	FindingImageUnverified       = "image_unverified"
	FindingImageRebuild          = "image_rebuild"
	FindingImageTransfer         = "image_transfer"
	FindingRegistrySelection     = "registry_selection"
	FindingStackNameConflict     = "stack_name_conflict"
	FindingProjectNameConflict   = "project_name_conflict"
	FindingDirectoryConflict     = "directory_conflict"
	FindingContainerConflict     = "container_name_conflict"
	FindingVolumeConflict        = "volume_name_conflict"
	FindingNetworkConflict       = "network_name_conflict"
	FindingPortConflict          = "port_conflict"
	FindingExternalNetwork       = "external_network_missing"
	FindingExternalVolume        = "external_volume_missing"
	FindingExternalBind          = "external_bind_path"
	FindingDevice                = "device_mapping"
	FindingVolumeDefinitionOnly  = "volume_definition_only"
	FindingAnonymousVolume       = "anonymous_volume_skipped"
	FindingVolumeMissing         = "volume_missing"
	FindingInsufficientSpace     = "insufficient_space"
	FindingSizeEstimated         = "size_estimated"
	FindingPlainHTTP             = "plain_http_transport"
	FindingContainersRunning     = "containers_running"
	FindingCrashConsistency      = "crash_consistency_acknowledged"
	FindingLeftovers             = "leftovers_removed"
	FindingProjectWarning        = "project_warning"
	FindingHostPortsUnknown      = "host_ports_unverified"
)

// Finding is one preflight result.
type Finding struct {
	Code    string
	Message string
	// Service or Resource the finding is about (optional).
	Service  string
	Resource string
}

// Image actions.
const (
	ImagePull     = "pull"
	ImageRebuild  = "rebuild"
	ImageTransfer = "transfer"
	ImagePresent  = "present"
)

// ServicePlan is what happens to a service's image.
type ServicePlan struct {
	Name     string
	Image    string
	Action   string
	Platform string
	// RegistryConnectionID is the connection a pull uses ("" anonymous).
	RegistryConnectionID string
	Reason               string
}

// Volume actions.
const (
	VolumeCopy           = "copy"
	VolumeSkip           = "skip"
	VolumeDefinitionOnly = "definition_only"
	VolumeExternal       = "external"
)

// VolumePlan is what happens to a volume.
type VolumePlan struct {
	Source    string
	Target    string
	Key       string
	Action    string
	Anonymous bool
	Bytes     int64
	Entries   int64
	Truncated bool
	Reason    string
	Labels    map[string]string
}

// DataPlan sizes the transfer.
type DataPlan struct {
	ProjectBytes int64
	VolumeBytes  int64
	ImageBytes   int64
	TotalBytes   int64
	// Truncated: a scan hit its budget; sizes are lower bounds.
	Truncated         bool
	TargetStacksFree  int64
	TargetVolumesFree int64
}

// Downtime is the expected downtime of a cold stack migration.
type Downtime struct {
	EstimatedSeconds int64
	Basis            string
}

// Transport describes the relay.
type Transport struct {
	SourcePlainHTTP      bool
	DestinationPlainHTTP bool
	BandwidthLimit       int64
}

// Exclusion is a Docker Manager resource left out (#32).
type Exclusion struct {
	Name   string
	Reason string
}

// Plan is a migration preview: computed before anything stops.
type Plan struct {
	Kind                domain.MigrationKind
	StackID             string
	SourceEnvironmentID string
	TargetEnvironmentID string
	ProjectName         string
	TargetDir           string
	Blockers            []Finding
	Warnings            []Finding
	Services            []ServicePlan
	Volumes             []VolumePlan
	Data                DataPlan
	Downtime            Downtime
	Transport           Transport
	// Leftovers are earlier unsuccessful migrations whose partial data on
	// the destination is removed before this one starts.
	Leftovers []string
	Excluded  []Exclusion
	Access    AccessPreview
}

// Allowed reports whether the plan has no blockers.
func (p Plan) Allowed() bool { return len(p.Blockers) == 0 }

// RegistryCheck is the result of checking one image reference on the
// destination's registry connection (#19).
type RegistryCheck struct {
	ConnectionID string
	// Class is "" when the image resolved for the destination platform,
	// else a regclient class (not_found, unauthorized, platform_not_found,
	// rate_limited, registry_unavailable, ...) or a selection failure
	// (ambiguous_registry_connection, registry_connection_revoked).
	Class   string
	Message string
	// SinglePlatform: the tag names a single-platform manifest (its
	// platform is not known from the index).
	SinglePlatform bool
}

// Selection is the user's choice for a stack migration.
type Selection struct {
	// ExcludeVolumes are named volumes (source names) whose data is not
	// copied; every other supported named volume is.
	ExcludeVolumes []string
	// AnonymousVolumes are anonymous volumes to copy (skipped otherwise).
	AnonymousVolumes []string
	// TransferImages are images copied through the relay instead of being
	// pulled or rebuilt on the destination.
	TransferImages []string
	// AcknowledgeCrashConsistency: copy a volume while containers use it
	// (volume migrations).
	AcknowledgeCrashConsistency bool
	// TargetName renames a migrated volume (volume migrations).
	TargetName string
}

// PreflightInput is everything Evaluate needs. It is gathered from both
// agents, the manager's records and the registry connections.
type PreflightInput struct {
	Kind                domain.MigrationKind
	StackID             string
	SourceEnvironmentID string
	TargetEnvironmentID string
	SourceOnline        bool
	TargetOnline        bool
	// SourceSupports / TargetSupports: the agents serve the migration
	// requests and streams.
	SourceSupports  bool
	TargetSupports  bool
	SourcePlainHTTP bool
	TargetPlainHTTP bool
	Source          *protocol.MigrationSourceFacts
	Target          *protocol.MigrationDestinationFacts
	// StackNameTaken: a Docker Manager stack with the project name already
	// exists on the destination.
	StackNameTaken bool
	// TargetDir is the planned project directory.
	TargetDir string
	// Registry holds checks per image reference (pull candidates).
	Registry  map[string]RegistryCheck
	Selection Selection
	// Leftovers are earlier partial migrations of the same stack/volume to
	// the destination: their volumes and directory are not conflicts.
	Leftovers       []domain.Migration
	BandwidthLimit  int64
	AssumedRate     int64
	StopGraceSecond int64
}

func (p *Plan) block(code, format string, args ...any) *Finding {
	p.Blockers = append(p.Blockers, Finding{Code: code, Message: fmt.Sprintf(format, args...)})
	return &p.Blockers[len(p.Blockers)-1]
}

func (p *Plan) warn(code, format string, args ...any) *Finding {
	p.Warnings = append(p.Warnings, Finding{Code: code, Message: fmt.Sprintf(format, args...)})
	return &p.Warnings[len(p.Warnings)-1]
}

// normPlatform reduces an os/arch[/variant] platform for comparisons.
func normPlatform(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	parts := strings.Split(p, "/")
	if len(parts) >= 2 {
		switch parts[1] {
		case "x86_64", "x86-64":
			parts[1] = "amd64"
		case "aarch64":
			parts[1] = "arm64"
		}
		if parts[1] == "arm64" && len(parts) == 3 && parts[2] == "v8" {
			parts = parts[:2]
		}
	}
	return strings.Join(parts, "/")
}

func archOf(p string) string {
	parts := strings.Split(normPlatform(p), "/")
	if len(parts) >= 2 {
		return strings.Join(parts[:2], "/")
	}
	return p
}

// Evaluate computes a migration plan: blockers stop the migration before
// anything stops; warnings are shown and accepted by starting it.
func Evaluate(in PreflightInput) Plan {
	p := Plan{Kind: in.Kind, StackID: in.StackID, SourceEnvironmentID: in.SourceEnvironmentID, TargetEnvironmentID: in.TargetEnvironmentID,
		TargetDir: in.TargetDir, Services: []ServicePlan{}, Volumes: []VolumePlan{}, Blockers: []Finding{}, Warnings: []Finding{},
		Leftovers: []string{}, Excluded: []Exclusion{},
		Transport: Transport{SourcePlainHTTP: in.SourcePlainHTTP, DestinationPlainHTTP: in.TargetPlainHTTP, BandwidthLimit: in.BandwidthLimit}}
	for _, l := range in.Leftovers {
		p.Leftovers = append(p.Leftovers, l.ID)
	}
	if in.SourceEnvironmentID == in.TargetEnvironmentID {
		p.block(FindingSameEnvironment, "the destination is the stack's own environment")
		return p
	}
	if !in.SourceOnline {
		p.block(FindingEnvironmentOffline, "the source environment's agent is offline").Resource = in.SourceEnvironmentID
	}
	if !in.TargetOnline {
		p.block(FindingEnvironmentOffline, "the destination environment's agent is offline").Resource = in.TargetEnvironmentID
	}
	if in.SourceOnline && !in.SourceSupports {
		p.block(FindingAgentUnsupported, "the source environment's agent does not support migrations yet; upgrade it")
	}
	if in.TargetOnline && !in.TargetSupports {
		p.block(FindingAgentUnsupported, "the destination environment's agent does not support migrations yet; upgrade it")
	}
	if in.SourcePlainHTTP || in.TargetPlainHTTP {
		var which []string
		if in.SourcePlainHTTP {
			which = append(which, "source")
		}
		if in.TargetPlainHTTP {
			which = append(which, "destination")
		}
		p.warn(FindingPlainHTTP, "the %s agent reaches the manager over plain HTTP (internal URL): the data crosses that network unencrypted",
			strings.Join(which, " and "))
	}
	if in.Source == nil || in.Target == nil {
		return p
	}
	if len(in.Leftovers) > 0 {
		p.warn(FindingLeftovers, "partial data of %d earlier unsuccessful migration(s) on the destination is removed before this one starts", len(in.Leftovers))
	}
	switch in.Kind {
	case domain.MigrationKindStack:
		evaluateStack(&p, in)
	case domain.MigrationKindVolume:
		evaluateVolume(&p, in)
	}
	sortFindings(p.Blockers)
	sortFindings(p.Warnings)
	return p
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Code < fs[j].Code })
}

// leftover reports whether a destination object belongs to an earlier
// partial migration that is cleaned up first.
func leftoverVolume(in PreflightInput, name, migration string) bool {
	if migration == "" {
		return false
	}
	for _, l := range in.Leftovers {
		if l.ID == migration && slices.Contains(l.VolumeTargets(), name) {
			return true
		}
	}
	return false
}

func leftoverDir(in PreflightInput, dir string) bool {
	for _, l := range in.Leftovers {
		if l.TargetDir == dir {
			return true
		}
	}
	return false
}

func evaluateStack(p *Plan, in PreflightInput) {
	src, dst := in.Source.Project, in.Target
	if src == nil {
		return
	}
	p.ProjectName = src.Name
	if src.Protected {
		p.block(FindingDockerManagerResource, "this is Docker Manager's own Compose project (%s); it cannot be migrated", src.ProtectionReason)
	}
	if !dst.StacksOK {
		p.block(FindingStorageUnavailable, "the destination's stacks volume did not pass the storage check: %s", dst.Reason)
	}
	srcPlatform, dstPlatform := archOf(in.Source.Platform), archOf(dst.Platform)
	// Name conflicts.
	if in.StackNameTaken {
		p.block(FindingStackNameConflict, "the destination already has a Docker Manager stack named %q", src.Name)
	}
	if len(dst.ProjectContainers) > 0 && !leftoverDir(in, in.TargetDir) {
		p.block(FindingProjectNameConflict, "the destination already runs a Compose project named %q (%s)", src.Name,
			strings.Join(dst.ProjectContainers, ", "))
	}
	if dst.DirExists && !leftoverDir(in, in.TargetDir) {
		p.block(FindingDirectoryConflict, "the directory %s already exists in the destination's stacks volume", in.TargetDir)
	}
	for _, c := range dst.Containers {
		if leftoverDir(in, in.TargetDir) && slices.Contains(dst.ProjectContainers, c) {
			continue
		}
		p.block(FindingContainerConflict, "a container named %s already exists on the destination", c).Resource = c
	}
	for _, n := range dst.Networks {
		p.block(FindingNetworkConflict, "a network named %s already exists on the destination", n).Resource = n
	}
	for _, n := range dst.MissingNetworks {
		p.block(FindingExternalNetwork, "the external network %s does not exist on the destination; create it first", n).Resource = n
	}
	for _, v := range dst.MissingVolumes {
		p.block(FindingExternalVolume, "the external volume %s does not exist on the destination; create it first", v).Resource = v
	}
	for _, c := range dst.PortConflicts {
		f := p.block(FindingPortConflict, "host port %d/%s is already published by %s on the destination", c.Port.Published, orTCP(c.Port.Protocol), c.Container)
		f.Resource = c.Container
	}
	if hasPublished(src) {
		p.warn(FindingHostPortsUnknown, "published ports are checked against the destination's containers only; processes outside Docker may also hold them")
	}
	// Binds and devices.
	for _, b := range src.Binds {
		if b.External {
			// The path comes from the definition (#7: stack.definition.read);
			// the API shows Resource only to callers holding it.
			f := p.warn(FindingExternalBind, "service %s binds a host path outside the project directory: it is not migrated and must exist on the destination", b.Service)
			f.Service, f.Resource = b.Service, b.Source
		}
	}
	for _, s := range src.Services {
		for _, d := range s.Devices {
			f := p.warn(FindingDevice, "service %s maps the host device %s: it must exist on the destination", s.Name, d)
			f.Service, f.Resource = s.Name, d
		}
	}
	for _, w := range src.Warnings {
		p.warn(FindingProjectWarning, "%s", w.Message)
	}
	// Images.
	existing := dst.ImagesPresent
	for _, s := range src.Services {
		sp := ServicePlan{Name: s.Name, Image: s.Image, Platform: s.ImagePlatform}
		transfer := slices.Contains(in.Selection.TransferImages, s.Image)
		localOnly := s.ImageID != "" && len(s.RepoDigests) == 0 && !s.Build
		rc, checked := in.Registry[s.Image]
		switch {
		case s.ImageProtected:
			p.block(FindingDockerManagerResource, "service %s runs Docker Manager's own image %s", s.Name, s.Image).Service = s.Name
			sp.Action = ImagePresent
		case slices.Contains(existing, s.Image) && !transfer:
			sp.Action = ImagePresent
		case transfer || localOnly:
			sp.Action = ImageTransfer
			switch {
			case s.ImageID == "":
				p.block(FindingImageNotPullable, "service %s: image %s is not on the source Engine, so it cannot be copied", s.Name, s.Image).Service = s.Name
			case s.ImagePlatform != "" && archOf(s.ImagePlatform) != dstPlatform:
				p.block(FindingPlatformMismatch, "service %s: image %s is %s but the destination runs %s; copying it would not run",
					s.Name, s.Image, s.ImagePlatform, dstPlatform).Service = s.Name
			default:
				why := "it was built locally or loaded and is in no registry"
				if transfer {
					why = "selected"
				}
				f := p.warn(FindingImageTransfer, "service %s: image %s is copied through the manager (%s)", s.Name, s.Image, why)
				f.Service = s.Name
				sp.Reason = why
			}
		case s.Build:
			sp.Action = ImageRebuild
			f := p.warn(FindingImageRebuild, "service %s: image %s is rebuilt on the destination from its build section", s.Name, s.Image)
			f.Service = s.Name
			if srcPlatform != dstPlatform {
				f.Message += fmt.Sprintf(" (for %s instead of %s)", dstPlatform, srcPlatform)
			}
		default:
			sp.Action = ImagePull
			sp.RegistryConnectionID = rc.ConnectionID
			switch {
			case !checked:
				p.warn(FindingImageUnverified, "service %s: image %s was not checked on a registry", s.Name, s.Image).Service = s.Name
			case rc.Class == "ambiguous_registry_connection" || rc.Class == "registry_connection_revoked":
				p.block(FindingRegistrySelection, "service %s: image %s: %s", s.Name, s.Image, rc.Message).Service = s.Name
			case rc.Class == "platform_not_found":
				p.block(FindingPlatformMismatch, "service %s: image %s has no %s variant in its registry (the source runs %s)",
					s.Name, s.Image, dstPlatform, orDash(s.ImagePlatform)).Service = s.Name
			case rc.Class == "not_found" || rc.Class == "unauthorized" || rc.Class == "forbidden":
				f := p.block(FindingImageNotPullable, "service %s: image %s cannot be pulled on the destination (%s)", s.Name, s.Image, rc.Class)
				f.Service = s.Name
				if s.ImageID != "" && archOf(s.ImagePlatform) == dstPlatform {
					f.Message += "; select it for copying through the manager instead"
				}
			case rc.Class != "":
				p.warn(FindingImageUnverified, "service %s: image %s could not be verified on its registry (%s); the deploy pulls it",
					s.Name, s.Image, rc.Class).Service = s.Name
			case rc.SinglePlatform && s.ImagePlatform != "" && archOf(s.ImagePlatform) != dstPlatform:
				p.warn(FindingPlatformMismatch, "service %s: image %s is a single-platform image (%s on the source); it may not run on %s",
					s.Name, s.Image, s.ImagePlatform, dstPlatform).Service = s.Name
			}
		}
		if sp.Action == ImageTransfer {
			p.Data.ImageBytes += s.ImageSize
		}
		p.Services = append(p.Services, sp)
	}
	// Volumes.
	p.Data.ProjectBytes, p.Data.Truncated = src.DirBytes, src.DirTruncated
	existingVols := map[string]string{}
	for _, v := range dst.Volumes {
		existingVols[v.Name] = v.Migration
	}
	for _, v := range src.Volumes {
		// Docker Manager's own labels never travel (a source agent of the
		// previous version still sends those under the current prefix).
		vp := VolumePlan{Source: v.Name, Target: v.Name, Key: v.Key, Anonymous: v.Anonymous, Bytes: v.Bytes, Entries: v.Entries,
			Truncated: v.Truncated, Labels: protocol.WithoutOwnLabels(v.Labels)}
		selected := !v.Anonymous && !slices.Contains(in.Selection.ExcludeVolumes, v.Name)
		if v.Anonymous {
			selected = slices.Contains(in.Selection.AnonymousVolumes, v.Name)
		}
		switch {
		case v.External:
			vp.Action, vp.Reason = VolumeExternal, "external: must already exist on the destination; its data is not migrated"
		case v.Protected:
			vp.Action, vp.Reason = VolumeSkip, v.Reason
			p.Excluded = append(p.Excluded, Exclusion{Name: v.Name, Reason: v.Reason})
		case !v.Exists:
			vp.Action, vp.Reason = VolumeSkip, "the volume does not exist on the source yet (Compose creates it on the destination)"
		case !selected && v.Anonymous:
			vp.Action, vp.Reason = VolumeSkip, "anonymous volume: skipped unless selected"
			f := p.warn(FindingAnonymousVolume, "anonymous volume %s is skipped (select it to copy it; the service gets a new anonymous volume either way)", v.Name)
			f.Resource = v.Name
		case !selected:
			vp.Action, vp.Reason = VolumeSkip, "excluded: Compose creates it empty on the destination"
		case !v.Supported:
			vp.Action, vp.Reason = VolumeDefinitionOnly, v.Reason
			p.warn(FindingVolumeDefinitionOnly, "volume %s: %s; its data is not migrated in v1", v.Name, v.Reason).Resource = v.Name
		default:
			vp.Action = VolumeCopy
			p.Data.VolumeBytes += v.Bytes
			p.Data.Truncated = p.Data.Truncated || v.Truncated
			if v.Anonymous {
				p.warn(FindingAnonymousVolume, "anonymous volume %s is copied as a volume of the same name; the service gets a new anonymous volume unless the definition names it", v.Name).Resource = v.Name
			}
		}
		if vp.Action == VolumeCopy || (vp.Action != VolumeExternal && v.Key != "") {
			if mig, ok := existingVols[vp.Target]; ok && !leftoverVolume(in, vp.Target, mig) {
				p.block(FindingVolumeConflict, "a volume named %s already exists on the destination", vp.Target).Resource = vp.Target
			}
		}
		p.Volumes = append(p.Volumes, vp)
	}
	sizeAndDowntime(p, in, len(src.Services))
}

func evaluateVolume(p *Plan, in PreflightInput) {
	v, dst := in.Source.Volume, in.Target
	if v == nil {
		return
	}
	target := in.Selection.TargetName
	if target == "" {
		target = v.Name
	}
	vp := VolumePlan{Source: v.Name, Target: target, Action: VolumeCopy, Bytes: v.Bytes, Entries: v.Entries, Truncated: v.Truncated,
		Labels: protocol.WithoutOwnLabels(v.Labels)}
	switch {
	case v.Protected:
		p.block(FindingDockerManagerResource, "volume %s is Docker Manager's own (%s); it cannot be migrated", v.Name, v.Reason)
		vp.Action = VolumeSkip
		p.Excluded = append(p.Excluded, Exclusion{Name: v.Name, Reason: v.Reason})
	case !v.Supported:
		p.block(FindingVolumeDefinitionOnly, "volume %s cannot be copied: %s", v.Name, v.Reason)
		vp.Action = VolumeDefinitionOnly
	}
	if !dst.VolumesOK {
		p.block(FindingStorageUnavailable, "the destination's volume directory did not pass the storage check: %s", dst.Reason)
	}
	for _, e := range dst.Volumes {
		if e.Name == target && !leftoverVolume(in, target, e.Migration) {
			p.block(FindingVolumeConflict, "a volume named %s already exists on the destination", target).Resource = target
		}
	}
	var running []string
	for _, u := range v.UsedBy {
		if u.Running {
			running = append(running, u.Name)
		}
	}
	if len(running) > 0 {
		if in.Selection.AcknowledgeCrashConsistency {
			p.warn(FindingCrashConsistency, "containers %s keep using the volume during the copy: the copy is crash-consistent only (acknowledged)",
				strings.Join(running, ", "))
		} else {
			p.block(FindingContainersRunning, "containers %s use the volume: stop them for a consistent copy, or acknowledge a crash-consistent copy",
				strings.Join(running, ", "))
		}
	}
	p.Volumes = append(p.Volumes, vp)
	if vp.Action == VolumeCopy {
		p.Data.VolumeBytes, p.Data.Truncated = v.Bytes, v.Truncated
	}
	sizeAndDowntime(p, in, 0)
}

// perEntryOverhead approximates the tar framing per entry.
const perEntryOverhead = 1024

func sizeAndDowntime(p *Plan, in PreflightInput, services int) {
	dst := in.Target
	var entries int64
	if in.Source.Project != nil {
		entries += in.Source.Project.DirEntries
	}
	for _, v := range p.Volumes {
		if v.Action == VolumeCopy {
			entries += v.Entries
		}
	}
	p.Data.TotalBytes = p.Data.ProjectBytes + p.Data.VolumeBytes + p.Data.ImageBytes + entries*perEntryOverhead
	p.Data.TargetStacksFree, p.Data.TargetVolumesFree = dst.StacksFree, dst.VolumesFree
	if dst.StacksFree >= 0 && p.Data.ProjectBytes > dst.StacksFree {
		p.block(FindingInsufficientSpace, "the project directory needs %s but the destination's stacks volume has %s free",
			human(p.Data.ProjectBytes), human(dst.StacksFree))
	}
	if dst.VolumesFree >= 0 && p.Data.VolumeBytes+p.Data.ImageBytes > dst.VolumesFree {
		p.block(FindingInsufficientSpace, "the volumes and images need %s but the destination's Docker data root has %s free",
			human(p.Data.VolumeBytes+p.Data.ImageBytes), human(dst.VolumesFree))
	}
	if p.Data.Truncated {
		p.warn(FindingSizeEstimated, "the data was too large to measure completely: sizes are lower bounds")
	}
	rate := in.BandwidthLimit
	if rate <= 0 {
		rate = in.AssumedRate
	}
	if rate <= 0 {
		rate = 50_000_000
	}
	grace := in.StopGraceSecond
	if grace <= 0 {
		grace = 10
	}
	transferS := (p.Data.TotalBytes + rate - 1) / rate
	if in.Kind == domain.MigrationKindVolume {
		p.Downtime = Downtime{EstimatedSeconds: 0, Basis: fmt.Sprintf(
			"The source volume stays in use; the copy takes about %ds at %s/s.", transferS, human(rate))}
		return
	}
	stopS, startS := grace*int64(services), 15*int64(services)
	p.Downtime = Downtime{EstimatedSeconds: stopS + transferS + startS, Basis: fmt.Sprintf(
		"About %ds to stop %d services, %ds to copy %s at %s/s and %ds to start them on the destination; image pulls and builds add to this.",
		stopS, services, transferS, human(p.Data.TotalBytes), human(rate), startS)}
}

func hasPublished(src *protocol.MigrationProjectFacts) bool {
	for _, s := range src.Services {
		for _, port := range s.Ports {
			if port.Published > 0 {
				return true
			}
		}
	}
	return false
}

func orTCP(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// human formats bytes (binary units).
func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
