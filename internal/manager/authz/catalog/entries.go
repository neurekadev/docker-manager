package catalog

// The v1 catalog. Keep entries grouped by resource type in editor order;
// common actions first, then Advanced ones.

func resourceTypes() []ResourceType {
	return []ResourceType{
		{Key: TypeInstance, Label: "Docker Manager"},
		{Key: TypeEnvironment, Label: "Environments", EnvironmentBound: true, Read: "environment.read",
			Minimal: "id, name, status, online"},
		{Key: TypeAgent, Label: "Agents", Scopable: true, EnvironmentBound: true, Read: "agent.read",
			Minimal: "id, environmentId, status, connected"},
		{Key: TypeStack, Label: "Stacks", Scopable: true, EnvironmentBound: true, Read: "stack.read",
			Minimal: "id, name, environmentId, status"},
		{Key: TypeService, Label: "Stack Services", Scopable: true, EnvironmentBound: true, Parents: []string{TypeStack},
			Minimal: "stack ID and service name"},
		{Key: TypeContainer, Label: "Containers", Scopable: true, EnvironmentBound: true, NamedPerEnvironment: true,
			Parents: []string{TypeService, TypeStack}, Read: "container.details.read",
			Minimal: "id, name, environmentId, state, health, stack and service identity"},
		{Key: TypeImage, Label: "Images", Scopable: true, EnvironmentBound: true, NamedPerEnvironment: true, Read: "image.read",
			Minimal: "id, references, environmentId"},
		{Key: TypeVolume, Label: "Volumes", Scopable: true, EnvironmentBound: true, NamedPerEnvironment: true,
			Parents: []string{TypeStack}, Read: "volume.read", Minimal: "name, environmentId, in-use flag"},
		{Key: TypeNetwork, Label: "Networks", Scopable: true, EnvironmentBound: true, NamedPerEnvironment: true,
			Parents: []string{TypeStack}, Read: "network.read", Minimal: "id, name, environmentId"},
		{Key: TypeBuildDefinition, Label: "Build Definitions", Scopable: true, EnvironmentBound: true, Read: "build_definition.read",
			Minimal: "id, name, environmentId"},
		{Key: TypeUpdatePolicy, Label: "Update Policies", Scopable: true, EnvironmentBound: true, Read: "update_policy.read",
			Parents: []string{TypeStack, TypeContainer}, Minimal: "id, name, environmentId, target"},
		// The one maintenance setup (#238): instance-wide, never one
		// resource of a rule.
		{Key: TypeMaintenancePolicy, Label: "Maintenance", EnvironmentBound: true, Read: "maintenance_policy.read",
			Minimal: "id, enabled"},
		{Key: TypeBackupRepository, Label: "Backup Repositories", Scopable: true, Read: "backup_repository.read",
			Minimal: "id, name, health"},
		{Key: TypeBackupPolicy, Label: "Backup Policies", Scopable: true, Read: "backup_policy.read", Minimal: "id, name, enabled"},
		{Key: TypeBackup, Label: "Backups", Scopable: true, Parents: []string{TypeBackupRepository}, Read: "backup.read",
			Minimal: "id, time, repository, status"},
		{Key: TypeRegistry, Label: "Registry Connections", Scopable: true, Read: "registry.read",
			Minimal: "id, name, registry host (never credentials)"},
		{Key: TypeGitCredential, Label: "Git Credentials", Scopable: true, Read: "git_credential.read",
			Minimal: "id, name, host (never credentials)"},
		{Key: TypeTemplate, Label: "Stack Templates", Scopable: true, Read: "template.read",
			Minimal: "id, name, visibility, icon"},
		{Key: TypeJob, Label: "Jobs", Read: "job.read"},
		// Alerts (#159) are read through their source (the environment's
		// system information, the job, the update policy): no read key.
		{Key: TypeAlert, Label: "Alerts"},
		{Key: TypeSchedule, Label: "Schedules", Read: "schedule.read"},
		{Key: TypeAPIToken, Label: "API Tokens"},
		{Key: TypeAudit, Label: "Audit Log"},
		{Key: TypeSettings, Label: "Settings"},
		{Key: TypeSystem, Label: "Diagnostics"},
		{Key: TypeAdministration, Label: "Administration (Owner Only)"},
	}
}

// entry builders.

type scopes struct {
	instance, environment bool
	resources             []string
}

var (
	instanceOnly = scopes{instance: true}
	instEnv      = scopes{instance: true, environment: true}
)

func res(types ...string) scopes { return scopes{instance: true, environment: true, resources: types} }

func instRes(types ...string) scopes { return scopes{instance: true, resources: types} }

func capability(key, typ, label, desc string, s scopes, risk Risk) Capability {
	return Capability{Key: key, Type: typ, Label: label, Description: desc, Instance: s.instance, Environment: s.environment,
		Resources: s.resources, Risk: risk, Since: 1}
}

func normal(key, typ, label, desc string, s scopes) Capability {
	return capability(key, typ, label, desc, s, RiskNormal)
}

func high(key, typ, label, desc string, s scopes) Capability {
	return capability(key, typ, label, desc, s, RiskHigh)
}

func adv(c Capability) Capability { c.Advanced = true; return c }

func ownerOnly(key, label, desc string) Capability {
	return Capability{Key: key, Type: TypeAdministration, Label: label, Description: desc, Instance: true, Risk: RiskHigh,
		OwnerOnly: true, Since: 1}
}

// containerScopes: all containers, one environment, one stack or service
// (current and future service containers), or one container.
var containerScopes = res(TypeStack, TypeService, TypeContainer)

// fileCaps builds the file-manager capabilities of a root type (stack
// project directories, volumes). Each root type has its own keys, so a
// rule on "all stacks" never opens volume contents and vice versa.
func fileCaps(root, what string, s scopes) []Capability {
	k := func(verb string) string { return root + ".files." + verb }
	return []Capability{
		high(k("read"), root, "Browse and View Files", "List directories and read file contents in "+what+". Files can hold secrets.", s),
		high(k("download"), root, "Download Files", "Download files or streamed archives from "+what+".", s),
		high(k("write"), root, "Edit and Upload Files", "Create, edit and upload files in "+what+".", s),
		adv(normal(k("copy"), root, "Copy Files", "Copy files and directories within "+what+".", s)),
		adv(normal(k("move"), root, "Move and Rename Files", "Move or rename files and directories within "+what+".", s)),
		adv(high(k("delete"), root, "Delete Files", "Delete files and directories in "+what+".", s)),
		adv(normal(k("archive"), root, "Create Archives", "Pack files into an archive inside "+what+".", s)),
		adv(high(k("extract"), root, "Extract Archives", "Unpack an archive inside "+what+" (may overwrite files).", s)),
		adv(high(k("chmod"), root, "Change File Permissions", "Change file modes (chmod), optionally recursively, in "+what+".", s)),
		adv(high(k("chown"), root, "Change File Ownership", "Change file owners (chown), optionally recursively, in "+what+".", s)),
	}
}

func capabilities() []Capability {
	var out []Capability
	add := func(cs ...Capability) { out = append(out, cs...) }

	// Environments (an environment is one agent plus its Docker Engine).
	add(
		normal("environment.read", TypeEnvironment, "View Environment Details", "See the environment's full record (Engine ID, agent, addresses). Any other capability in an environment shows only its name and status.", instEnv),
		normal("environment.metrics.read", TypeEnvironment, "View Host Stats", "Host CPU, memory, disk and capacity charts.", instEnv),
		normal("environment.system.read", TypeEnvironment, "View System Information", "Engine and agent versions, transport, verified storage roots and diagnostics.", instEnv),
		normal("environment.events.read", TypeEnvironment, "Watch Docker Events", "Stream Docker Engine events; each event is still filtered by the capability of its resource.", instEnv),
		adv(normal("environment.manage", TypeEnvironment, "Rename and Edit Environments", "Change the server/display name and the service address.", instEnv)),
		adv(high("environment.remove", TypeEnvironment, "Archive Environments", "Archive an environment and revoke its agent's credential.", instEnv)),
	)

	// Agents.
	agentScope := res(TypeAgent)
	add(
		normal("agent.read", TypeAgent, "View Agents", "See agents, their versions and connection state.", agentScope),
		adv(high("agent.enroll", TypeAgent, "Enroll Agents", "Create one-use agent enrollment tokens (adds hosts to Docker Manager).", instanceOnly)),
		adv(high("agent.manage", TypeAgent, "Manage Agents", "Edit agent labels and rotate agent credentials.", agentScope)),
		adv(high("agent.remove", TypeAgent, "Remove Agents", "Revoke an agent's credential and detach its environment.", agentScope)),
	)

	// Stacks.
	stackScope := res(TypeStack)
	add(
		normal("stack.read", TypeStack, "View Stacks", "See a stack's details, services and events. Does not open its containers, files or Compose definition.", stackScope),
		normal("stack.deploy", TypeStack, "Deploy", "Deploy a stack from its current on-disk Compose sources.", stackScope),
		normal("stack.start", TypeStack, "Start Stack", "Start all services of a stack.", stackScope),
		normal("stack.stop", TypeStack, "Stop Stack", "Stop all services of a stack.", stackScope),
		normal("stack.restart", TypeStack, "Restart Stack", "Restart all services of a stack.", stackScope),
		high("stack.definition.read", TypeStack, "View Compose Definition", "Read the stack's Compose files, override files, env files and revisions, also as part of a folder. They may contain secrets.", stackScope),
		high("stack.definition.write", TypeStack, "Edit Compose Definition", "Change the stack's Compose files, override files and env files, also as part of a folder, and restore revisions.", stackScope),
		// High risk (#12 security review): creating a stack writes its whole
		// Compose definition, which may bind host paths or the Docker socket
		// and run privileged containers once deployed.
		adv(high("stack.create", TypeStack, "Create Stacks", "Create managed stacks in an environment from a Compose definition. Deployed, it can mount host paths and run privileged containers.", instEnv)),
		adv(normal("stack.import", TypeStack, "Import Stacks", "Discover and adopt existing Compose projects.", instEnv)),
		adv(normal("stack.manage", TypeStack, "Edit Stack Settings", "Edit a stack's display name, description, links and service descriptions.", stackScope)),
		adv(normal("stack.down", TypeStack, "Take Stack Down", "Stop and remove a stack's containers and networks (volumes are kept).", stackScope)),
		adv(normal("stack.build", TypeStack, "Build Stack Images", "Build the images of a stack's Compose build sections.", stackScope)),
		adv(normal("stack.update", TypeStack, "Update Stack Images", "Pull a stack's images (Pull) and recreate the services whose image changed (image updates).", stackScope)),
		adv(high("stack.remove", TypeStack, "Delete Stacks", "Remove a stack and its containers, optionally with the volumes it owns.", stackScope)),
		adv(high("stack.rename", TypeStack, "Rename Stack Project", "Change a stack's Compose project name: it stops and starts again, its volumes and project directory move to the new name, and containers outside the stack that use those volumes are stopped and recreated.", stackScope)),
		adv(high("stack.migrate", TypeStack, "Migrate Stacks", "Move a stack and its volumes to another environment (also needs stack.create on the target environment). Stack rules follow the stack; environment rules do not.", stackScope)),
	)
	add(fileCaps(TypeStack, "the stack's project directory (its Compose files, override files and env files additionally need the Compose definition capabilities)", stackScope)...)

	// Containers: stack- and service-scoped rules apply to the stack's
	// current and future service containers for the capabilities selected.
	add(
		normal("container.metrics.read", TypeContainer, "View Stats", "CPU, memory, network and I/O charts. Shows the container's name and state only.", containerScopes),
		normal("container.details.read", TypeContainer, "View Details", "Inspect a container: configuration, environment variables, mounts and labels.", containerScopes),
		high("container.logs.read", TypeContainer, "View Logs", "Read and follow container logs. Logs can contain secrets.", containerScopes),
		normal("container.restart", TypeContainer, "Restart", "Restart a container. Does not allow start, stop, logs, terminal or files.", containerScopes),
		normal("container.start", TypeContainer, "Start", "Start a stopped container.", containerScopes),
		normal("container.stop", TypeContainer, "Stop", "Stop a running container.", containerScopes),
		high("container.exec", TypeContainer, "Open Terminal", "Run an interactive shell inside a container (full access to its data).", containerScopes),
		adv(normal("container.pause", TypeContainer, "Pause", "Freeze a container's processes.", containerScopes)),
		adv(normal("container.unpause", TypeContainer, "Unpause", "Resume a paused container.", containerScopes)),
		adv(normal("container.update", TypeContainer, "Change Resources", "Change a container's resource limits or restart policy.", containerScopes)),
		adv(high("container.remove", TypeContainer, "Remove", "Remove a container.", containerScopes)),
		adv(high("container.create", TypeContainer, "Create Containers", "Create standalone containers in an environment. Bind mounts give them access to host files.", instEnv)),
	)

	// Images.
	imageScope := res(TypeImage)
	add(
		normal("image.read", TypeImage, "View Images", "See images, their tags, digests and build records.", imageScope),
		normal("image.pull", TypeImage, "Pull Images", "Pull images, using a matching registry connection without revealing its credentials.", instEnv),
		adv(normal("image.tag", TypeImage, "Tag Images", "Add tags to an image.", imageScope)),
		adv(high("image.remove", TypeImage, "Remove Images", "Remove an image.", imageScope)),
		adv(normal("image.build", TypeImage, "Build Images", "Build images from a Git URL, context or build definition.", res(TypeBuildDefinition))),
	)

	// Volumes.
	volumeScope := res(TypeVolume)
	add(
		normal("volume.read", TypeVolume, "View Volumes", "See volumes, their driver, size and users.", volumeScope),
		adv(normal("volume.create", TypeVolume, "Create Volumes", "Create named volumes.", instEnv)),
		adv(high("volume.remove", TypeVolume, "Remove Volumes", "Delete a volume and its data.", volumeScope)),
		adv(high("volume.migrate", TypeVolume, "Migrate Volumes", "Copy a volume to another environment (also needs volume.create on the target environment).", volumeScope)),
	)
	add(fileCaps(TypeVolume, "the volume", volumeScope)...)

	// Networks.
	networkScope := res(TypeNetwork)
	add(
		normal("network.read", TypeNetwork, "View Networks", "See networks and their connected containers.", networkScope),
		adv(normal("network.create", TypeNetwork, "Create Networks", "Create networks.", instEnv)),
		adv(high("network.remove", TypeNetwork, "Remove Networks", "Remove a network.", networkScope)),
	)

	// Builds (#33).
	buildScope := res(TypeBuildDefinition)
	add(
		adv(normal("build_definition.read", TypeBuildDefinition, "View Build Definitions", "See build definitions (Git URL, context, arguments; never credentials).", buildScope)),
		adv(normal("build_definition.manage", TypeBuildDefinition, "Manage Build Definitions", "Create, edit and delete build definitions.", buildScope)),
	)

	// Updates (#20).
	// Environment policies authorize checks and runs in their environment;
	// target jobs still accept grants scoped to stacks or containers.
	updateScope := res(TypeUpdatePolicy, TypeStack, TypeContainer)
	add(
		normal("update_policy.read", TypeUpdatePolicy, "View Update Policies", "See update policies, candidates and their state.", res(TypeUpdatePolicy)),
		adv(normal("update_policy.manage", TypeUpdatePolicy, "Manage Update Policies", "Create, edit and delete update policies.", res(TypeUpdatePolicy))),
		adv(normal("update.check", TypeUpdatePolicy, "Check for Updates", "Check registries for newer digests of fixed tags.", updateScope)),
		adv(normal("update.run", TypeUpdatePolicy, "Apply Updates", "Pull updated images and recreate services or containers (no automatic rollback).", updateScope)),
	)

	// Maintenance (#14, #238): the setup covers every environment, so its
	// settings need instance grants; previews and runs on one environment
	// are one-off prunes there (maintenance's own need instance grants).
	add(
		adv(normal("maintenance_policy.read", TypeMaintenancePolicy, "View Maintenance", "See the maintenance settings and the last results.", instanceOnly)),
		adv(normal("maintenance_policy.manage", TypeMaintenancePolicy, "Manage Maintenance", "Change the maintenance settings: rules, schedule and environments left out.", instanceOnly)),
		adv(normal("maintenance.preview", TypeMaintenancePolicy, "Preview Prune", "List what a prune would remove: maintenance's on all environments, a one-off prune's on one environment.", instEnv)),
		adv(high("maintenance.run", TypeMaintenancePolicy, "Run Prune", "Remove unused containers, images, networks, volumes and build cache: run maintenance on all environments, or a one-off prune on one environment.", instEnv)),
	)

	// Schedules (#13).
	add(adv(normal("schedule.read", TypeSchedule, "View Schedules", "See the cross-policy schedule overview (entries are still filtered by their policy's read capability).", instEnv)))

	// Backups (#10): repositories, policies, snapshots and backup jobs are
	// instance resources, never personal assets of their creator.
	repoScope := instRes(TypeBackupRepository)
	policyScope := instRes(TypeBackupPolicy)
	snapScope := instRes(TypeBackupRepository, TypeBackup)
	add(
		normal("backup.read", TypeBackup, "View Backups", "See the backup list and snapshot metadata. Does not open snapshot contents.", snapScope),
		normal("backup.run", TypeBackup, "Run Backups", "Run a backup now. Needs the capability on every stack, volume and repository the backup touches.", res(TypeBackupPolicy, TypeBackupRepository, TypeStack, TypeVolume)),
		high("backup.restore", TypeBackup, "Restore Backups", "Restore stacks, volumes or files from a snapshot (overwrites data; needs the capability on every restored target).", res(TypeBackupRepository, TypeBackup, TypeStack, TypeVolume)),
		adv(high("backup.contents.read", TypeBackup, "Browse Backup Contents", "List files inside snapshots. Snapshots can contain secrets.", snapScope)),
		adv(high("backup.contents.download", TypeBackup, "Download From Backups", "Download single files from snapshots. Snapshots can contain secrets.", snapScope)),
		adv(normal("backup.verify", TypeBackup, "Verify Backups", "Check repository and snapshot integrity.", snapScope)),
		adv(high("backup.retention", TypeBackup, "Apply Retention", "Forget and prune old snapshots according to a policy.", instRes(TypeBackupPolicy, TypeBackupRepository))),
		adv(normal("backup_repository.read", TypeBackupRepository, "View Backup Repositories", "See repositories and their health (never credentials or the Recovery Key).", repoScope)),
		adv(high("backup_repository.manage", TypeBackupRepository, "Manage Backup Repositories", "Create, edit, test and delete repositories and rotate their keys.", repoScope)),
		adv(normal("backup_policy.read", TypeBackupPolicy, "View Backup Policies", "See backup policies and scope/retention previews.", policyScope)),
		adv(normal("backup_policy.manage", TypeBackupPolicy, "Manage Backup Policies", "Create, edit and delete backup policies.", policyScope)),
	)

	// Registries and Git credentials (#19, #33): metadata only; credential
	// administration is owner-only.
	add(
		adv(normal("registry.read", TypeRegistry, "View Registry Connections", "See registry connection metadata (never credentials).", instRes(TypeRegistry))),
		adv(normal("git_credential.read", TypeGitCredential, "View Git Credentials", "See Git credential metadata (never secrets).", instRes(TypeGitCredential))),
	)

	// Stack templates (template registry): instance resources. Version
	// contents include .env files, so using them is high risk; drafts are
	// file roots with their own file capabilities.
	tmplScope := instRes(TypeTemplate)
	add(
		normal("template.read", TypeTemplate, "View Templates", "See templates, their tags and published versions (not their files).", tmplScope),
		high("template.use", TypeTemplate, "Use Templates", "Read the files of published versions (.env included) and create stacks from them (also needs stack.create).", tmplScope),
		normal("template.create", TypeTemplate, "Create Templates", "Create new templates (the creator still needs template capabilities to edit them).", instanceOnly),
		normal("template.manage", TypeTemplate, "Edit Templates", "Change a template's name, description, tags and icon.", tmplScope),
		high("template.publish", TypeTemplate, "Publish Templates", "Publish and delete versions and make templates public: every file of a public template, .env included, becomes readable by anyone with the registry URL.", tmplScope),
		high("template.remove", TypeTemplate, "Delete Templates", "Delete templates with their draft and versions (stacks created from them keep working).", tmplScope),
	)
	add(fileCaps(TypeTemplate, "the template's draft (compose.yaml, .env and the files next to them)", tmplScope)...)

	// Jobs: job.read/job.cancel are evaluated on the job's targets. Holding
	// the job kind's own capability on every target also shows and cancels
	// the job (a restart-only user follows their restart).
	jobScope := res(TypeStack, TypeService, TypeContainer, TypeImage, TypeVolume, TypeNetwork, TypeBackupRepository, TypeTemplate)
	add(
		normal("job.read", TypeJob, "View Jobs", "See jobs and their progress for the targeted resources, regardless of who started them.", jobScope),
		adv(normal("job.cancel", TypeJob, "Cancel Jobs", "Cancel jobs acting on the targeted resources, regardless of who started them.", jobScope)),
	)

	// Alerts (#159): shown to whoever sees their source; dismissing one
	// (for everyone) is scoped like that source: the environment (disks,
	// RAID, offline), the failed job's targets or the update policy.
	add(
		normal("alert.dismiss", TypeAlert, "Dismiss Alerts", "Dismiss alerts for everyone (they stay in the Alerts list and open again when they get worse). Scoped like the alert's source: the environment, the failed job's targets or the update policy.",
			res(TypeStack, TypeService, TypeContainer, TypeImage, TypeVolume, TypeNetwork, TypeBackupRepository, TypeTemplate, TypeUpdatePolicy)),
	)

	// Manager-wide.
	add(
		adv(high("audit.read", TypeAudit, "View Audit Log", "Read every audit record. All-or-nothing: records reveal activity on resources the reader cannot otherwise see.", instanceOnly)),
		adv(high("audit.export", TypeAudit, "Export Audit Log", "Download audit records as NDJSON or CSV. All-or-nothing, like View Audit Log.", instanceOnly)),
		adv(normal("api_tokens.create", TypeAPIToken, "Create API Tokens", "Create API tokens limited to a subset of one's own permissions (needs recent authentication).", instanceOnly)),
		adv(normal("settings.read", TypeSettings, "View Settings", "See instance settings and schedule defaults.", instanceOnly)),
		adv(high("settings.manage", TypeSettings, "Change Settings", "Change instance settings and schedule defaults (not the security policy).", instanceOnly)),
		adv(normal("system.metrics.read", TypeSystem, "Scrape Internal Metrics", "Read Docker Manager's own Prometheus metrics (job queue, agent sessions, streams, database size); meant for a monitoring API token. The endpoint is off unless DOCKER_MANAGER_METRICS_ENABLED is set.", instanceOnly)),
	)

	// Owner surface: never grantable (#16, #17, #31).
	add(
		ownerOnly("users.manage", "Manage Users", "Invite, edit, disable and delete users; reset their factors and passwords."),
		ownerOnly("groups.manage", "Manage Groups and Permissions", "Create groups, edit group and user rules, choose the default group."),
		ownerOnly("security_settings.manage", "Change the Security Policy", "Password rules, required sign-in factors and enrollment grace."),
		ownerOnly("registry.manage", "Manage Registry Credentials", "Create, rotate and delete registry connections."),
		ownerOnly("git_credential.manage", "Manage Git Credentials", "Create, rotate and delete Git credentials."),
		ownerOnly("notification_channel.manage", "Manage Notification Channels", "Add, edit, test and delete notification channels and view their addresses (they hold webhook tokens and passwords)."),
		ownerOnly("template_registry.manage", "Manage Template Registries", "Add, sync and remove other Docker Manager instances' template registries."),
		ownerOnly("api_tokens.manage", "Manage Other Users' API Tokens", "List and revoke API tokens of every user."),
		ownerOnly("manager.backup", "Back Up the Manager", "Back up Docker Manager's own state (database, keys)."),
		ownerOnly("update_policy.manage_all", "Manage Updates Across All Environments", "Create and change an update policy covering current and future environments."),
		ownerOnly("backup.import", "Import Backup Repositories", "Import an existing repository into a fresh manager (first-run recovery)."),
		ownerOnly("system.restore", "Restore the Manager", "Restore Docker Manager itself from a manager backup."),
		ownerOnly("manager.move", "Move the Manager", "Move Docker Manager to a new server: create and cancel move codes; a fresh manager receives the moved state."),
		ownerOnly("system.support_bundle", "Download Support Bundles", "Download a diagnostics bundle: versions, redacted configuration, recent logs, agent states and audit chain verification (never secrets)."),
	)
	return out
}
