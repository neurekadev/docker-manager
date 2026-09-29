// UI URLs (#22). Build links with these helpers, never by string
// concatenation in views, so every page agrees on the route layout:
//
//   /                                   Dashboard
//   /environments[/{id}[/migrate]]      environments, environment detail, environment migration
//   /stacks[/{stackId}[/files|logs|terminal|revisions|policies|activity|migrate]]
//   /stacks?create=1, /stacks?import=1, /stacks?fromTemplate=1[&template=] (create, import and template dialogs)
//   /containers, /images, /volumes, /networks   lists (all or the selected environment)
//   /containers/new, /containers/{env}/{name}[/logs|terminal]   create; detail tabs
//   /images/{env}/{imageId}, /networks/{env}/{name}
//   /volumes/{env}/{name}[/files|backups|migrate] volume detail, files, backups, migration
//   /builds[/new|/definitions[?create=1|?edit={id}]], /builds/{env}/{buildId}
//   /registries[/git][?create=1|?test=1] registry connections, Git credentials, add, test an image
//   /templates[?create=1], /templates/{id}[/files|versions|settings]   stack templates
//   /templates/registries, /templates/remote/{instanceId}/{templateId}  registries, registry templates
//   /registry                           this instance's public template registry (public)
//   /jobs[/{jobId}][?kind=&policyId=&state=], /schedules
//   /environments/add[?reattach={id}]   enroll an agent (new environment or re-attach)
//   /backups[/{backupId}[/restore]|/all|/snapshots|/policies[/{id}]|/repositories[/new|/{id}]]
//   /updates[/{policyId}], /maintenance[/{policyId}]
//   Create and edit forms of stacks and policies are dialogs over their
//   list or detail page, opened by a query parameter (?create=1, ?edit=1,
//   ?defaults=1) so links can open them.
//   /access[/users/{id}|/groups[/{id}]|/invitations]
//   /profile[/tokens[/new]|/sessions]   the caller's own account, API tokens and signed-in devices
//   /settings[/tokens/all|/sign-in|/schedules|/audit|/diagnostics]   instance administration
//   (/settings/security and /settings/tokens[/new] redirect to /profile[/tokens[/new]])
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
	/** Move every stack of the environment to another one (#35). */
	environmentMigrate: (id: string) => `/environments/${e(id)}/migrate`,
	/** Enroll an agent: a new environment, or re-attach an archived one. */
	addEnvironment: (reattach?: string) =>
		`/environments/add${reattach ? `?reattach=${e(reattach)}` : ''}`,
	stacks: () => '/stacks',
	stack: (
		id: string,
		tab?:
			| 'files'
			| 'logs'
			| 'terminal'
			| 'revisions'
			| 'backups'
			| 'policies'
			| 'activity'
			| 'migrate'
	) => `/stacks/${e(id)}${tab ? `/${tab}` : ''}`,
	/** The stack's terminal with a service container preselected (#8). */
	stackTerminal: (id: string, container?: string) =>
		`/stacks/${e(id)}/terminal${container ? `?container=${e(container)}` : ''}`,
	/** The stack's logs with one service selected (#8). */
	stackLogs: (id: string, service?: string) =>
		`/stacks/${e(id)}/logs${service ? `?service=${e(service)}` : ''}`,
	/** The stack list with the create dialog open. */
	newStack: (environmentId?: string | null) =>
		`/stacks?create=1${environmentId ? `&environment=${e(environmentId)}` : ''}`,
	/** The stack list with the create-from-template dialog open. */
	stackFromTemplate: (
		templateId?: string,
		environmentId?: string | null,
		instanceId?: string | null
	) => {
		const q = new URLSearchParams({ fromTemplate: '1' });
		if (templateId) q.set('template', templateId);
		if (instanceId) q.set('registry', instanceId);
		if (environmentId) q.set('environment', environmentId);
		return `/stacks?${q}`;
	},
	/** The stack list with the import dialog open (discovered projects). */
	importStack: (environmentId?: string | null) =>
		`/stacks?import=1${environmentId ? `&environment=${e(environmentId)}` : ''}`,
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
	volume: (env: string, name: string, tab?: 'files' | 'backups' | 'migrate') =>
		`/volumes/${e(env)}/${e(name)}${tab ? `/${tab}` : ''}`,
	networks: () => '/networks',
	network: (env: string, name: string) => `/networks/${e(env)}/${e(name)}`,
	builds: () => '/builds',
	buildDefinitions: () => '/builds/definitions',
	/** The definitions with the create dialog open. */
	buildDefinitionNew: () => '/builds/definitions?create=1',
	/** The definitions with a definition's edit dialog open. */
	buildDefinitionEdit: (id: string) => `/builds/definitions?edit=${e(id)}`,
	/** The build form; `from` prefills it from an earlier build of `env`. */
	newBuild: (env?: string, definition?: string, from?: string) => {
		const q = new URLSearchParams();
		if (env) q.set('environment', env);
		if (definition) q.set('definition', definition);
		if (from) q.set('from', from);
		const s = q.toString();
		return `/builds/new${s ? `?${s}` : ''}`;
	},
	build: (env: string, id: string) => `/builds/${e(env)}/${e(id)}`,
	/** This instance's public template registry page (no sign-in). */
	registry: () => '/registry',
	templates: (tag?: string) => `/templates${tag ? `?tag=${e(tag)}` : ''}`,
	/** The templates page with the create dialog open. */
	newTemplate: () => '/templates?create=1',
	template: (id: string, tab?: 'files' | 'versions' | 'settings') =>
		`/templates/${e(id)}${tab ? `/${tab}` : ''}`,
	templateRegistries: () => '/templates/registries',
	/** A template of an added registry. */
	remoteTemplate: (instanceId: string, templateId: string) =>
		`/templates/remote/${e(instanceId)}/${e(templateId)}`,
	registries: () => '/registries',
	gitCredentials: () => '/registries/git',
	/** The registry connections with the "Test an image" dialog open (/registries/matches redirects here). */
	registryMatches: () => '/registries?test=1',
	backups: () => '/backups',
	backup: (id: string) => `/backups/${e(id)}`,
	backupRestore: (id: string) => `/backups/${e(id)}/restore`,
	backupList: () => '/backups/all',
	/** restic's own snapshots (a repository page's "Raw snapshots"), optionally of one repository. */
	backupSnapshots: (repositoryId?: string) =>
		`/backups/snapshots${repositoryId ? `?repository=${e(repositoryId)}` : ''}`,
	backupPolicies: () => '/backups/policies',
	/** The Backups overview with the create-policy wizard open. */
	backupPolicyNew: () => '/backups?create=1',
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
	/**
	 * The jobs list, optionally with its kind filter (?kind=update.check),
	 * its policy filter (?policyId=, the runs of one policy) and its state
	 * filter (?state=active: the jobs in progress) set.
	 */
	jobs: (kind?: string, o: { policyId?: string; state?: 'active' } = {}) => {
		const q = new URLSearchParams();
		if (kind) q.set('kind', kind);
		if (o.policyId) q.set('policyId', o.policyId);
		if (o.state) q.set('state', o.state);
		const s = q.toString();
		return `/jobs${s ? `?${s}` : ''}`;
	},
	job: (id: string) => `/jobs/${e(id)}`,
	schedules: () => '/schedules',
	access: () => '/access',
	accessUser: (id: string) => `/access/users/${e(id)}`,
	accessGroups: () => '/access/groups',
	accessGroup: (id: string) => `/access/groups/${e(id)}`,
	accessInvitations: () => '/access/invitations',
	/** The caller's own account: password, authenticator app, passkeys, recovery codes. */
	profile: () => '/profile',
	/** The caller's own API tokens (every user's tokens: allApiTokens). */
	apiTokens: () => '/profile/tokens',
	apiTokenNew: () => '/profile/tokens/new',
	/** The caller's own signed-in devices (browser sessions). */
	mySessions: () => '/profile/sessions',
	settings: () => '/settings',
	/** Every user's API tokens (owner only), a Settings tab. */
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
