// UI URLs (#22). Build links with these helpers, never by string
// concatenation in views, so every page agrees on the route layout:
//
//   /                                   Dashboard
//   /environments[/{id}]                environments and environment detail
//   /stacks[/{stackId}[/files|logs|terminal|revisions|policies|activity]]
//   /containers, /images, /volumes, /networks   lists (all or the selected environment)
//   /environments/{id}/containers/{containerId} and images/volumes/networks details
//   /builds, /registries, /backups, /updates, /maintenance, /jobs[/{jobId}], /schedules
//   /environments/add[?reattach={id}]   enroll an agent (new environment or re-attach)
//   /access (users, groups, invitations), /settings[/security|/tokens|/audit]
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
	updates: () => '/updates',
	maintenance: () => '/maintenance',
	jobs: () => '/jobs',
	job: (id: string) => `/jobs/${e(id)}`,
	schedules: () => '/schedules',
	access: () => '/access',
	settings: () => '/settings',
	security: () => '/settings/security',
	apiTokens: () => '/settings/tokens',
	setup: () => '/setup',
	signIn: (next?: string, reason?: 'expired' | 'signed-out') => {
		const q = new URLSearchParams();
		if (next && next !== '/' && next.startsWith('/') && !next.startsWith('//'))
			q.set('next', next);
		if (reason) q.set('reason', reason);
		const s = q.toString();
		return `/sign-in${s ? `?${s}` : ''}`;
	},
	enroll: () => '/enroll'
};

/** A safe in-app redirect target from ?next= (never another origin). */
export function safeNext(next: string | null | undefined): string {
	if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\'))
		return '/';
	return next;
}
