package jobspec

import (
	"fmt"
	"slices"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// The v1 job kind catalog. Naming: <resource>.<verb>, with the resource the
// primary target type. A new kind needs a constant here AND a registered
// Spec below; TestEveryJobKindHasLockDefinition fails otherwise. After
// changing the catalog run scripts/generate.sh to refresh the lock-matrix
// table in docs/internal/architecture/job-engine.md.
const (
	ImagePull   domain.JobKind = "image.pull"
	ImageBuild  domain.JobKind = "image.build"
	ImageRemove domain.JobKind = "image.remove"

	ContainerCreate  domain.JobKind = "container.create"
	ContainerStart   domain.JobKind = "container.start"
	ContainerStop    domain.JobKind = "container.stop"
	ContainerRestart domain.JobKind = "container.restart"
	ContainerPause   domain.JobKind = "container.pause"
	ContainerUnpause domain.JobKind = "container.unpause"
	ContainerRemove  domain.JobKind = "container.remove"
	ContainerUpdate  domain.JobKind = "container.update"

	StackDeploy  domain.JobKind = "stack.deploy"
	StackStart   domain.JobKind = "stack.start"
	StackStop    domain.JobKind = "stack.stop"
	StackRestart domain.JobKind = "stack.restart"
	StackDown    domain.JobKind = "stack.down"
	StackRemove  domain.JobKind = "stack.remove"
	StackBuild   domain.JobKind = "stack.build"
	StackUpdate  domain.JobKind = "stack.update"
	StackMigrate domain.JobKind = "stack.migrate"
	// StackImport imports a discovered project by copying its directory
	// from the agent's import mount into the stacks volume (#7).
	StackImport domain.JobKind = "stack.import"
	// StackRemoveSource removes a migrated stack's source after the user
	// confirmed the migration (#35).
	StackRemoveSource domain.JobKind = "stack.remove_source"
	// StackRename moves a stack's Compose project to a new project name:
	// its volumes and project directory follow (#7).
	StackRename domain.JobKind = "stack.rename"
	// StackPull pulls a stack's images without recreating anything: the
	// next deploy runs them (#20: tags move only on request).
	StackPull domain.JobKind = "stack.pull"

	VolumeCreate  domain.JobKind = "volume.create"
	VolumeRemove  domain.JobKind = "volume.remove"
	VolumeMigrate domain.JobKind = "volume.migrate"

	NetworkCreate domain.JobKind = "network.create"
	NetworkRemove domain.JobKind = "network.remove"

	UpdateCheck domain.JobKind = "update.check"
	UpdateRun   domain.JobKind = "update.run"

	PruneRun domain.JobKind = "prune.run"

	BackupRun       domain.JobKind = "backup.run"
	BackupRetention domain.JobKind = "backup.retention"
	BackupVerify    domain.JobKind = "backup.verify"
	BackupImport    domain.JobKind = "backup.import"
	RestoreRun      domain.JobKind = "restore.run"

	FilesArchive  domain.JobKind = "files.archive"
	FilesExtract  domain.JobKind = "files.extract"
	FilesMetadata domain.JobKind = "files.metadata"
	FilesCopy     domain.JobKind = "files.copy"
	FilesMove     domain.JobKind = "files.move"
	FilesDelete   domain.JobKind = "files.delete"

	ManagerBackup    domain.JobKind = "manager.backup"
	ManagerRetention domain.JobKind = "manager.retention"
	ManagerVerify    domain.JobKind = "manager.verify"
)

// Compensation names shared by several kinds.
const (
	CompStartContainers = "start_containers"
	CompStartSource     = "start_source"
	// CompRemoveImportCopy removes the copy a stack.import made before the
	// project switched to it.
	CompRemoveImportCopy = "remove_import_copy"
	// CompUndoRename moves the volumes and the project directory a
	// stack.rename moved back to the old name, before the project switched.
	CompUndoRename = "undo_rename"
)

// Default offline deadlines.
const (
	deadlineInteractive = 10 * time.Minute
	deadlineLong        = 30 * time.Minute
	deadlineScheduled   = time.Hour
)

// Lock rule helpers.
func hostShared() LockRule {
	return LockRule{Scope: domain.LockHost, Mode: domain.LockShared, Source: FromEnvironments}
}

func all(scope domain.LockScope, mode domain.LockMode) LockRule {
	return LockRule{Scope: scope, Mode: mode, Source: AllInEnvironments}
}

func target(scope domain.LockScope, mode domain.LockMode, tt domain.TargetType) LockRule {
	return LockRule{Scope: scope, Mode: mode, Source: FromTargets, TargetType: tt}
}

func optional(r LockRule) LockRule { r.Optional = true; return r }

const (
	shared    = domain.LockShared
	exclusive = domain.LockExclusive
)

func step(name string, idempotent, safePoint bool, recovery string) Step {
	return Step{Name: name, Idempotent: idempotent, SafePoint: safePoint, Recovery: recovery}
}

// idem is an idempotent step that is also a cancellation safe point.
func idem(name string) Step { return step(name, true, true, "") }

var compStartContainers = Compensation{Name: CompStartContainers,
	Description: "start the containers this job stopped, in dependency order"}

// containerKind builds the spec of a single-step container operation.
func containerKind(kind domain.JobKind, verb, summary string) Spec {
	return Spec{
		Kind: kind, Summary: summary, Capability: string(kind), Executor: domain.ExecutorAgent,
		Locks: []LockRule{hostShared(),
			target(domain.LockContainer, exclusive, domain.TargetContainer),
			optional(target(domain.LockStack, shared, domain.TargetStack))},
		OfflineDeadline: deadlineInteractive,
		Steps:           []Step{idem(verb)},
	}
}

// starts marks a kind that starts containers (Spec.StartsContainers).
func starts(s Spec) Spec { s.StartsContainers = true; return s }

// stackKind builds the spec of a stack operation exclusive on the stack.
func stackKind(kind domain.JobKind, summary string, deadline time.Duration, steps ...Step) Spec {
	return Spec{
		Kind: kind, Summary: summary, Capability: string(kind), Executor: domain.ExecutorAgent,
		Locks:           []LockRule{hostShared(), target(domain.LockStack, exclusive, domain.TargetStack)},
		OfflineDeadline: deadline,
		Steps:           steps,
	}
}

// filesKind builds a file-manager job spec. File jobs act inside one stack
// project directory or volume: their capability is per root type
// (stack.files.copy, volume.files.copy; #17).
func filesKind(kind domain.JobKind, summary string, locks []LockRule, steps ...Step) Spec {
	return Spec{
		Kind: kind, Summary: summary, Capability: string(kind), RootScoped: true, Executor: domain.ExecutorAgent,
		Locks: append(append([]LockRule{hostShared()}, locks...),
			optional(target(domain.LockVolume, shared, domain.TargetVolume)),
			optional(target(domain.LockStack, shared, domain.TargetStack))),
		OfflineDeadline: deadlineInteractive,
		Steps:           steps,
	}
}

func catalogSpecs() []Spec {
	return []Spec{
		// Images.
		{
			Kind: ImagePull, Summary: "Pull an image", Capability: "image.pull", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockImage, exclusive, domain.TargetImage)},
			OfflineDeadline: deadlineLong, ConcurrencyClass: ClassPull,
			Steps: []Step{idem("pull")},
		},
		{
			Kind: ImageBuild, Summary: "Build an image from a Git URL or context", Capability: "image.build", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockImage, exclusive, domain.TargetImage)},
			OfflineDeadline: deadlineLong, ConcurrencyClass: ClassBuild,
			Steps: []Step{idem("fetch_context"), idem("build")},
		},
		{
			Kind: ImageRemove, Summary: "Remove an image", Capability: "image.remove", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockImage, exclusive, domain.TargetImage)},
			OfflineDeadline: deadlineInteractive,
			Steps:           []Step{idem("remove")},
		},

		// Containers.
		{
			Kind: ContainerCreate, Summary: "Create a container", Capability: "container.create", Executor: domain.ExecutorAgent,
			Locks: []LockRule{hostShared(),
				target(domain.LockContainer, exclusive, domain.TargetContainer),
				optional(target(domain.LockStack, shared, domain.TargetStack))},
			OfflineDeadline: deadlineInteractive,
			// create is the only safe point: once the container exists, the
			// job finishes connecting and (optionally) starting it.
			Steps: []Step{step("create", false, true,
				"The container may or may not have been created. Check the environment's container list for it before creating it again."),
				step("connect_networks", true, false, ""), step("start", true, false, "")},
		},
		starts(containerKind(ContainerStart, "start", "Start a container")),
		containerKind(ContainerStop, "stop", "Stop a container"),
		starts(containerKind(ContainerRestart, "restart", "Restart a container")),
		containerKind(ContainerPause, "pause", "Pause a container"),
		starts(containerKind(ContainerUnpause, "unpause", "Unpause a container")),
		containerKind(ContainerRemove, "remove", "Remove a container"),
		containerKind(ContainerUpdate, "update", "Update a container's resources or restart policy"),

		// Stacks.
		starts(stackKind(StackDeploy, "Deploy a stack from its on-disk Compose sources", deadlineLong,
			idem("resolve_sources"), idem("pull_images"), idem("build_images"), idem("apply"))),
		starts(stackKind(StackStart, "Start a stack", deadlineInteractive, idem("start"))),
		stackKind(StackStop, "Stop a stack", deadlineInteractive, idem("stop")),
		starts(stackKind(StackRestart, "Restart a stack", deadlineInteractive, idem("restart"))),
		stackKind(StackDown, "Stop and remove a stack's containers and networks", deadlineInteractive, idem("down")),
		// Deleting a stack takes it down (volumes and the project directory
		// are kept) and then forgets it in the manager (finish hook, #7).
		stackKind(StackRemove, "Take a stack down and remove it from Docker Manager (files are kept; volumes too unless the removal asks to remove the stack's own)", deadlineInteractive, idem("down")),
		func() Spec {
			s := stackKind(StackBuild, "Build a stack's images", deadlineLong, idem("fetch_sources"), idem("build_images"))
			s.ConcurrencyClass = ClassBuild
			return s
		}(),
		func() Spec {
			s := stackKind(StackUpdate, "Pull a stack's images and recreate changed services", deadlineLong,
				idem("pull_images"), idem("apply"))
			s.ConcurrencyClass = ClassPull
			s.StartsContainers = true
			return s
		}(),
		// Pull only: the images are downloaded, no container changes; the
		// capability is stack.update's (pulling is its first half).
		func() Spec {
			s := stackKind(StackPull, "Pull a stack's images without deploying them", deadlineLong, idem("pull_images"))
			s.Capability = "stack.update"
			s.ConcurrencyClass = ClassPull
			return s
		}(),
		// Import by copy (#7): the project is stopped, its whole directory
		// (Compose files and everything next to them) is copied from the
		// agent's read-only import mount into a new directory of the
		// stacks volume, its containers are recreated from the copy
		// (anonymous volumes inherited) and the services that ran before
		// start again. The original directory is only read. Until the
		// recreate starts, a failure removes the copy and starts the old
		// containers again; afterwards the project lives in the stacks
		// volume (no automatic rollback, #25).
		{
			Kind: StackImport, Summary: "Import a Compose project by copying its directory into the stacks volume",
			Capability: "stack.import", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockStack, exclusive, domain.TargetStack)},
			OfflineDeadline: deadlineInteractive,
			Steps: []Step{idem("prepare"), idem("stop_containers"), idem("copy_files"), idem("recreate"),
				step("start_containers", true, false, "")},
			Compensations: []Compensation{compStartContainers, {Name: CompRemoveImportCopy,
				Description: "remove the copy of the project directory from the stacks volume while the project does not use it yet"}},
			StartsContainers: true,
		},
		// Rename (#7): the stack stops, its named volumes move to the new
		// project's names (containers outside the stack that mount them
		// stop and are recreated on the new names), its directory is
		// renamed, the old project's containers are removed (the switch),
		// the project is created under the new name and what ran starts
		// again. Before the switch every failure is undone.
		{
			Kind: StackRename, Summary: "Rename a stack's Compose project, moving its volumes and project directory to the new name",
			Capability: "stack.rename", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockStack, exclusive, domain.TargetStack)},
			OfflineDeadline: deadlineInteractive,
			Steps: []Step{idem("prepare"), idem("stop_containers"), idem("move"), step("recreate", true, false, ""),
				step("start_containers", true, false, "")},
			Compensations: []Compensation{compStartContainers, {Name: CompUndoRename,
				Description: "move the volumes and the project directory back to the old name while the project has not switched yet"}},
			StartsContainers: true,
		},
		// Environment migration (#35): the manager relays the data between
		// the two agents. Targets: the stack (its environment is the source),
		// the source volumes and the destination's new volumes (lock only;
		// the executor checks stack.create/stack.deploy and volume.create on
		// the destination). The destination deploy is a stack.deploy job
		// with its own stack lock.
		{
			Kind: StackMigrate, Summary: "Cold-migrate a stack and its volumes to another environment",
			Capability: "stack.migrate", Executor: domain.ExecutorManager,
			Locks: []LockRule{hostShared(),
				target(domain.LockStack, exclusive, domain.TargetStack),
				optional(target(domain.LockVolume, exclusive, domain.TargetVolume))},
			Steps: []Step{idem("prepare"), idem("stop_source"), idem("transfer"), idem("deploy_destination"),
				step("finalize", true, false, "")},
			Compensations: []Compensation{{Name: CompStartSource,
				Description: "put the stack back on its source environment and start the services that ran before, when the migration stops before cut-over"}},
			OnManagerRestart: RestartInterrupt,
			LockOnly:         LockOnlyRule{Types: []domain.TargetType{domain.TargetVolume}, OtherEnvironments: true},
		},
		{
			Kind: StackRemoveSource, Summary: "Remove a migrated stack's containers, volumes and files from its source environment",
			Capability: "stack.migrate", Executor: domain.ExecutorAgent,
			Locks: []LockRule{hostShared(),
				target(domain.LockStack, exclusive, domain.TargetStack),
				optional(target(domain.LockVolume, exclusive, domain.TargetVolume))},
			OfflineDeadline: deadlineLong,
			Steps:           []Step{idem("down"), idem("remove_volumes"), step("remove_files", true, false, "")},
			LockOnly:        LockOnlyRule{Types: []domain.TargetType{domain.TargetVolume}},
			// The stack lives in the destination now; the job acts on its source.
			FormerStackLocation: true,
		},

		// Volumes and networks.
		{
			Kind: VolumeCreate, Summary: "Create a volume", Capability: "volume.create", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockVolume, exclusive, domain.TargetVolume)},
			OfflineDeadline: deadlineInteractive,
			Steps:           []Step{idem("create")},
		},
		{
			Kind: VolumeRemove, Summary: "Remove a volume", Capability: "volume.remove", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockVolume, exclusive, domain.TargetVolume)},
			OfflineDeadline: deadlineInteractive,
			Steps:           []Step{idem("remove")},
		},
		{
			Kind: VolumeMigrate, Summary: "Copy a volume to another environment",
			Capability: "volume.migrate", Executor: domain.ExecutorManager,
			Locks:            []LockRule{hostShared(), target(domain.LockVolume, exclusive, domain.TargetVolume)},
			Steps:            []Step{idem("prepare"), idem("transfer"), step("finalize", true, false, "")},
			OnManagerRestart: RestartInterrupt,
			LockOnly:         LockOnlyRule{OtherEnvironments: true},
		},
		{
			Kind: NetworkCreate, Summary: "Create a network", Capability: "network.create", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockNetwork, exclusive, domain.TargetNetwork)},
			OfflineDeadline: deadlineInteractive,
			Steps: []Step{step("create", false, true,
				"Docker allows duplicate network names: check whether the network exists before creating it again.")},
		},
		{
			Kind: NetworkRemove, Summary: "Remove a network", Capability: "network.remove", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockNetwork, exclusive, domain.TargetNetwork)},
			OfflineDeadline: deadlineInteractive,
			Steps:           []Step{idem("remove")},
		},

		// Updates.
		// An update policy targets one stack or one Docker Manager-managed
		// standalone container (#20): the check reads registries on the
		// manager, the run pulls and recreates on the agent.
		{
			Kind: UpdateCheck, Summary: "Check registries for newer digests of the fixed tags of a stack or container",
			Capability: "update.check", Executor: domain.ExecutorManager,
			Locks: []LockRule{hostShared(),
				optional(target(domain.LockStack, shared, domain.TargetStack)),
				optional(target(domain.LockContainer, shared, domain.TargetContainer))},
			Steps:            []Step{idem("check")},
			OnManagerRestart: RestartResume,
		},
		{
			Kind: UpdateRun, Summary: "Apply an image update to a stack or standalone container (no automatic rollback)",
			Capability: "update.run", Executor: domain.ExecutorAgent,
			Locks: []LockRule{hostShared(),
				optional(target(domain.LockStack, exclusive, domain.TargetStack)),
				optional(target(domain.LockContainer, exclusive, domain.TargetContainer))},
			OfflineDeadline: deadlineScheduled, ConcurrencyClass: ClassPull,
			Steps:            []Step{idem("pull_images"), idem("recreate"), step("wait_healthy", true, false, "")},
			StartsContainers: true,
		},

		// Maintenance.
		{
			Kind: PruneRun, Summary: "Prune unused containers, images, networks, volumes and build cache",
			Capability: "maintenance.run", Executor: domain.ExecutorAgent,
			// Shared locks on every stack, container, image, network and
			// volume of the environment: a prune runs alongside other
			// prunes and reads, but waits for (and holds back) deploys,
			// builds, updates, pulls, migrations, backup shutdowns and
			// restores, which take exclusive locks (#14, #26).
			Locks: []LockRule{hostShared(),
				all(domain.LockStack, shared),
				all(domain.LockContainer, shared), all(domain.LockImage, shared),
				all(domain.LockNetwork, shared), all(domain.LockVolume, shared)},
			OfflineDeadline: deadlineScheduled,
			// delete revalidates every candidate immediately before
			// removing it and honors cancellation between items.
			Steps: []Step{idem("collect_candidates"), idem("delete_candidates")},
		},

		// Backups.
		{
			Kind: BackupRun, Summary: "Back up stacks, volumes and paths to a restic repository",
			Capability: "backup.run", Executor: domain.ExecutorAgent,
			Locks: []LockRule{hostShared(),
				optional(target(domain.LockStack, exclusive, domain.TargetStack)),
				optional(target(domain.LockVolume, shared, domain.TargetVolume)),
				optional(target(domain.LockFilePath, shared, domain.TargetPath)),
				target(domain.LockRepository, shared, domain.TargetRepository)},
			OfflineDeadline: deadlineScheduled,
			Steps: []Step{idem("prepare"), idem("stop_containers"),
				step("snapshot", false, true,
					"A restic snapshot may or may not have been written. Check the repository's snapshot list; run the backup again to get a verified snapshot."),
				step("start_containers", true, false, ""), idem("record")},
			Compensations: []Compensation{compStartContainers},
		},
		{
			Kind: RestoreRun, Summary: "Restore stacks, volumes or paths from a snapshot",
			Capability: "backup.restore", Executor: domain.ExecutorAgent,
			// Containers outside the restored stacks that use the data (a
			// standalone container mounting a restored volume) are
			// lock-only targets: the restore stops them, and nothing starts
			// them until it ends (Spec.StartsContainers).
			Locks: []LockRule{hostShared(),
				optional(target(domain.LockStack, exclusive, domain.TargetStack)),
				optional(target(domain.LockContainer, exclusive, domain.TargetContainer)),
				optional(target(domain.LockVolume, exclusive, domain.TargetVolume)),
				optional(target(domain.LockFilePath, exclusive, domain.TargetDestinationPath)),
				target(domain.LockRepository, shared, domain.TargetRepository)},
			LockOnly:        LockOnlyRule{Types: []domain.TargetType{domain.TargetContainer}},
			OfflineDeadline: deadlineScheduled,
			Steps: []Step{idem("prepare"), idem("stop_containers"),
				step("restore_data", false, true,
					"The restore target may be partially written. Inspect it, then run the restore again with the overwrite mode you need."),
				step("start_containers", true, false, "")},
			Compensations: []Compensation{compStartContainers},
		},
		// Backup repositories keep one restic repository per scope (#10):
		// an environment's data is written, pruned and checked by its agent
		// (backup.*), the manager's state by the manager (manager.*).
		{
			Kind: BackupRetention, Summary: "Apply a policy's retention (forget and prune) to an environment's repository",
			Capability: "backup.retention", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockRepository, exclusive, domain.TargetRepository)},
			OfflineDeadline: deadlineScheduled,
			Steps:           []Step{idem("forget"), idem("prune_repository")},
		},
		{
			Kind: BackupVerify, Summary: "Verify an environment's backup repository",
			Capability: "backup.verify", Executor: domain.ExecutorAgent,
			Locks:           []LockRule{hostShared(), target(domain.LockRepository, shared, domain.TargetRepository)},
			OfflineDeadline: deadlineScheduled,
			Steps:           []Step{idem("check")},
		},
		{
			Kind: ManagerRetention, Summary: "Apply a policy's retention (forget and prune) to the manager-state repository",
			Capability: "backup.retention", Executor: domain.ExecutorManager,
			Locks:            []LockRule{target(domain.LockRepository, exclusive, domain.TargetRepository)},
			Steps:            []Step{idem("forget"), idem("prune_repository")},
			OnManagerRestart: RestartResume,
		},
		{
			Kind: ManagerVerify, Summary: "Verify the manager-state backup repository",
			Capability: "backup.verify", Executor: domain.ExecutorManager,
			Locks:            []LockRule{target(domain.LockRepository, shared, domain.TargetRepository)},
			Steps:            []Step{idem("check")},
			OnManagerRestart: RestartResume,
		},
		{
			Kind: BackupImport, Summary: "Import an existing repository's snapshots into the backup index",
			Capability: "backup.import", Executor: domain.ExecutorManager,
			Locks:            []LockRule{target(domain.LockRepository, shared, domain.TargetRepository)},
			Steps:            []Step{idem("scan"), idem("import_index")},
			OnManagerRestart: RestartResume,
		},
		{
			Kind: ManagerBackup, Summary: "Back up the manager's own state",
			Capability: "manager.backup", Executor: domain.ExecutorManager,
			Locks: []LockRule{target(domain.LockRepository, exclusive, domain.TargetRepository)},
			Steps: []Step{idem("snapshot_database"),
				step("backup", false, true, "A manager snapshot may or may not have been written. Check the repository, then run the manager backup again."),
				idem("write_manifest")},
			OnManagerRestart: RestartInterrupt,
		},

		// Files.
		filesKind(FilesArchive, "Create an archive from files",
			[]LockRule{target(domain.LockFilePath, shared, domain.TargetPath),
				target(domain.LockFilePath, exclusive, domain.TargetDestinationPath)},
			idem("archive")),
		filesKind(FilesExtract, "Extract an archive",
			[]LockRule{target(domain.LockFilePath, shared, domain.TargetPath),
				target(domain.LockFilePath, exclusive, domain.TargetDestinationPath)},
			step("extract", false, true, "The destination may contain a partial extraction. Inspect it before extracting again.")),
		func() Spec {
			s := filesKind(FilesMetadata, "Change ownership or permissions recursively",
				[]LockRule{target(domain.LockFilePath, exclusive, domain.TargetPath)},
				idem("apply"))
			// The input's "chmod" and/or "chown" objects select the
			// capabilities (each requested change needs its own, #17).
			s.CapabilityByInput = map[string]string{"chmod": "files.chmod", "chown": "files.chown"}
			return s
		}(),
		filesKind(FilesCopy, "Copy files",
			[]LockRule{target(domain.LockFilePath, shared, domain.TargetPath),
				target(domain.LockFilePath, exclusive, domain.TargetDestinationPath)},
			step("copy", false, true, "The destination may contain a partial copy. Inspect it before copying again.")),
		filesKind(FilesMove, "Move files",
			[]LockRule{target(domain.LockFilePath, exclusive, domain.TargetPath),
				target(domain.LockFilePath, exclusive, domain.TargetDestinationPath)},
			step("move", false, true, "Some files may already have been moved. Compare source and destination before moving again.")),
		filesKind(FilesDelete, "Delete files",
			[]LockRule{target(domain.LockFilePath, exclusive, domain.TargetPath)},
			idem("delete")),
	}
}

var registry = func() map[domain.JobKind]Spec {
	m := map[domain.JobKind]Spec{}
	for _, s := range catalogSpecs() {
		if err := s.Validate(); err != nil {
			panic(fmt.Sprintf("jobspec: invalid catalog entry: %v", err))
		}
		if _, dup := m[s.Kind]; dup {
			panic(fmt.Sprintf("jobspec: duplicate kind %s", s.Kind))
		}
		m[s.Kind] = s
	}
	return m
}()

// Lookup returns the spec of a kind.
func Lookup(kind domain.JobKind) (Spec, bool) {
	s, ok := registry[kind]
	return s, ok
}

// Catalog returns every spec sorted by kind.
func Catalog() []Spec {
	out := make([]Spec, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Spec) int {
		switch {
		case a.Kind < b.Kind:
			return -1
		case a.Kind > b.Kind:
			return 1
		}
		return 0
	})
	return out
}

// Kinds returns every registered kind name, sorted.
func Kinds() []domain.JobKind {
	var out []domain.JobKind
	for _, s := range Catalog() {
		out = append(out, s.Kind)
	}
	return out
}
