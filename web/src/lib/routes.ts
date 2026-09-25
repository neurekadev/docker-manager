// UI URLs (#22). Build links with these helpers, never by string
// concatenation in views, so every page agrees on the route layout:
//
//   /                                   Dashboard
//   /environments[/{id}]                environments and environment detail
//   /stacks[/{stackId}[/files|logs|terminal|revisions|policies|activity]]
//   /containers, /images, /volumes, /networks   lists (all or the selected environment)
//   /environments/{id}/containers/{containerId} and images/volumes/networks details
//   /builds, /registries, /jobs[/{jobId}], /schedules
//   /environments/add[?reattach={id}]   enroll an agent (new environment or re-attach)
//   /backups[/{backupId}[/restore]|/policies[/new|/{id}]|/repositories[/new|/{id}]]
//   /updates[/new|/{policyId}[/edit]], /maintenance[/new|/defaults|/{policyId}[/edit]]
//   /access[/users/{id}|/groups[/{id}]|/invitations]
//   /settings[/security|/tokens[/all]|/sign-in|/schedules|/audit|/diagnostics]
//   /volumes/{env}/{volume}/files, /containers/{env}/{id}/logs|terminal,
//   /popout/logs?stack=|environment=&container= (files, logs, terminals)
//   /setup, /sign-in, /enroll, /invitation, /password-reset (public)
//
// IDs are path-encoded; environment-scoped Docker objects live under their
// environment because their IDs are only unique within one Engine.

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
		tab?: 'files' | 'logs' | 'terminal' | 'revisions' | 'policies' | 'activity'
	) => `/stacks/${e(id)}${tab ? `/${tab}` : ''}`,
	containers: () => '/containers',
	container: (env: string, id: string) => `/environments/${e(env)}/containers/${e(id)}`,
	images: () => '/images',
	image: (env: string, id: string) => `/environments/${e(env)}/images/${e(id)}`,
	volumes: () => '/volumes',
	volume: (env: string, name: string) => `/environments/${e(env)}/volumes/${e(name)}`,
	networks: () => '/networks',
	network: (env: string, id: string) => `/environments/${e(env)}/networks/${e(id)}`,
	builds: () => '/builds',
	registries: () => '/registries',
	backups: () => '/backups',
	backup: (id: string) => `/backups/${e(id)}`,
	backupRestore: (id: string) => `/backups/${e(id)}/restore`,
	backupPolicies: () => '/backups/policies',
	backupPolicyNew: () => '/backups/policies/new',
	backupPolicy: (id: string) => `/backups/policies/${e(id)}`,
	backupRepositories: () => '/backups/repositories',
	backupRepositoryNew: () => '/backups/repositories/new',
	backupRepository: (id: string) => `/backups/repositories/${e(id)}`,
	updates: () => '/updates',
	updatePolicyNew: () => '/updates/new',
	updatePolicy: (id: string) => `/updates/${e(id)}`,
	updatePolicyEdit: (id: string) => `/updates/${e(id)}/edit`,
	maintenance: () => '/maintenance',
	maintenanceNew: () => '/maintenance/new',
	maintenancePolicy: (id: string) => `/maintenance/${e(id)}`,
	maintenanceEdit: (id: string) => `/maintenance/${e(id)}/edit`,
	maintenanceDefaults: () => '/maintenance/defaults',
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
