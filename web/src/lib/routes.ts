// UI URLs (#22). Build links with these helpers, never by string
// concatenation in views, so every page agrees on the route layout:
//
//   /                                   Dashboard
//   /environments[/{id}]                environments and environment detail
//   /stacks[/{stackId}[/files|logs|terminal|revisions|policies|activity|migrate]]
//   /stacks?create=1, /stacks/discovered (create dialog; discovery and import)
//   /containers, /images, /volumes, /networks   lists (all or the selected environment)
//   /containers/new, /containers/{env}/{name}[/logs|terminal]   create; detail tabs
//   /images/{env}/{imageId}, /networks/{env}/{name}
//   /volumes/{env}/{name}[/files|migrate]       volume detail, file manager, migration
//   /builds[/new|/definitions], /builds/{env}/{buildId}
//   /registries[/git|/matches]          registry connections, Git credentials, match preview
//   /jobs[/{jobId}], /schedules
//   /environments/add[?reattach={id}]   enroll an agent (new environment or re-attach)
//   /backups[/{backupId}[/restore]|/snapshots|/policies[/{id}]|/repositories[/new|/{id}]]
//   /updates[/{policyId}], /maintenance[/{policyId}]
//   Create and edit forms of stacks and policies are dialogs over their
//   list or detail page, opened by a query parameter (?create=1, ?edit=1,
//   ?defaults=1) so links can open them.
//   /access[/users/{id}|/groups[/{id}]|/invitations]
//   /settings[/security|/tokens[/all]|/sign-in|/schedules|/audit|/diagnostics]
//   /volumes/{env}/{volume}/files, /containers/{env}/{id}/logs|terminal,
//   /popout/logs?stack=|environment=&container= (files, logs, terminals)
//   /setup, /sign-in, /enroll, /invitation, /password-reset (public)
//
// IDs are path-encoded; environment-scoped Docker objects carry their
// environment in the path because their names are only unique within one
// Engine. Containers, volumes and networks are addressed by name (the #17
// permission and #23 live-key identity), images by ID.

const e = encodeURIComponent;

export const routes = {
	dashboard: () => '/',
	environments: () => '/environments',
	environment: (id: string, tab?: 'system' | 'agents' | 'jobs') =>
		`/environments/${e(id)}${tab ? `?tab=${tab}` : ''}`,
	/** Enroll an agent: a new environment, or re-attach an archived one. */
	addEnvironment: (reattach?: string) =>
		`/environments/add${reattach ? `?reattach=${e(reattach)}` : ''}`,
	stacks: () => '/stacks',
	stack: (
		id: string,
		tab?: 'files' | 'logs' | 'terminal' | 'revisions' | 'policies' | 'activity' | 'migrate'
	) => `/stacks/${e(id)}${tab ? `/${tab}` : ''}`,
	/** The stack's terminal with a service container preselected (#8). */
	stackTerminal: (id: string, container?: string) =>
		`/stacks/${e(id)}/terminal${container ? `?container=${e(container)}` : ''}`,
	/** The stack list with the create dialog open. */
	newStack: (environmentId?: string | null) =>
		`/stacks?create=1${environmentId ? `&environment=${e(environmentId)}` : ''}`,
	discoveredStacks: (environmentId?: string | null) =>
		`/stacks/discovered${environmentId ? `?environment=${e(environmentId)}` : ''}`,
	containers: () => '/containers',
	newContainer: (env?: string, image?: string) => {
		const q = new URLSearchParams();
		if (env) q.set('environment', env);
		if (image) q.set('image', image);
		const s = q.toString();
		return `/containers/new${s ? `?${s}` : ''}`;
	},
	container: (env: string, name: string, tab?: 'logs' | 'terminal') =>
		`/containers/${e(env)}/${e(name)}${tab ? `/${tab}` : ''}`,
	images: () => '/images',
	image: (env: string, id: string) => `/images/${e(env)}/${e(id)}`,
	volumes: () => '/volumes',
	volume: (env: string, name: string, tab?: 'files' | 'migrate') =>
		`/volumes/${e(env)}/${e(name)}${tab ? `/${tab}` : ''}`,
	networks: () => '/networks',
	network: (env: string, name: string) => `/networks/${e(env)}/${e(name)}`,
	builds: () => '/builds',
	buildDefinitions: () => '/builds/definitions',
	newBuild: (env?: string, definition?: string) => {
		const q = new URLSearchParams();
		if (env) q.set('environment', env);
		if (definition) q.set('definition', definition);
		const s = q.toString();
		return `/builds/new${s ? `?${s}` : ''}`;
	},
	build: (env: string, id: string) => `/builds/${e(env)}/${e(id)}`,
	registries: () => '/registries',
	gitCredentials: () => '/registries/git',
	registryMatches: () => '/registries/matches',
	backups: () => '/backups',
	backup: (id: string) => `/backups/${e(id)}`,
	backupRestore: (id: string) => `/backups/${e(id)}/restore`,
	backupSnapshots: () => '/backups/snapshots',
	backupPolicies: () => '/backups/policies',
	backupPolicyNew: () => '/backups/policies?create=1',
	backupPolicy: (id: string) => `/backups/policies/${e(id)}`,
	backupPolicyEdit: (id: string) => `/backups/policies/${e(id)}?edit=1`,
	backupRepositories: () => '/backups/repositories',
	backupRepositoryNew: () => '/backups/repositories/new',
	backupRepository: (id: string) => `/backups/repositories/${e(id)}`,
	updates: () => '/updates',
	updatePolicyNew: () => '/updates?create=1',
	updatePolicy: (id: string) => `/updates/${e(id)}`,
	updatePolicyEdit: (id: string) => `/updates/${e(id)}?edit=1`,
	maintenance: () => '/maintenance',
	maintenanceNew: () => '/maintenance?create=1',
	maintenancePolicy: (id: string) => `/maintenance/${e(id)}`,
	maintenanceEdit: (id: string) => `/maintenance/${e(id)}?edit=1`,
	maintenanceDefaults: () => '/maintenance?defaults=1',
	jobs: () => '/jobs',
	job: (id: string) => `/jobs/${e(id)}`,
	schedules: () => '/schedules',
	access: () => '/access',
	accessUser: (id: string) => `/access/users/${e(id)}`,
	accessGroups: () => '/access/groups',
	accessGroup: (id: string) => `/access/groups/${e(id)}`,
	accessInvitations: () => '/access/invitations',
	settings: () => '/settings',
	security: () => '/settings/security',
	apiTokens: () => '/settings/tokens',
	apiTokenNew: () => '/settings/tokens/new',
	allApiTokens: () => '/settings/tokens/all',
	signInPolicy: () => '/settings/sign-in',
	scheduleDefaults: () => '/settings/schedules',
	audit: () => '/settings/audit',
	diagnostics: () => '/settings/diagnostics',
	setup: () => '/setup',
	setupImport: () => '/setup/import',
	signIn: (next?: string, reason?: 'expired' | 'signed-out') => {
		const q = new URLSearchParams();
		if (next && next !== '/' && next.startsWith('/') && !next.startsWith('//'))
			q.set('next', next);
		if (reason) q.set('reason', reason);
		const s = q.toString();
		return `/sign-in${s ? `?${s}` : ''}`;
	},
	enroll: () => '/enroll',
	// Files, logs and terminals (#15, #8; track B3). The stack tabs are
	// routes.stack(id, 'files' | 'logs' | 'terminal').
	volumeFiles: (env: string, name: string) => `/volumes/${e(env)}/${e(name)}/files`,
	containerLogs: (env: string, id: string) => `/containers/${e(env)}/${e(id)}/logs`,
	containerTerminal: (env: string, id: string) => `/containers/${e(env)}/${e(id)}/terminal`,
	/** The log viewer in its own window (no app shell). */
	logsWindow: (target: { stackId: string } | { environmentId: string; containerId: string }) => {
		const q =
			'stackId' in target
				? new URLSearchParams({ stack: target.stackId })
				: new URLSearchParams({
						environment: target.environmentId,
						container: target.containerId
					});
		return `/popout/logs?${q}`;
	}
};

/** A safe in-app redirect target from ?next= (never another origin). */
export function safeNext(next: string | null | undefined): string {
	if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\'))
		return '/';
	return next;
}
