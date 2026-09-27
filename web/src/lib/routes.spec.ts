// Every routes.* builder must resolve to a page that exists (#22): a link
// built with a helper can never point at a route file that was renamed or
// never created (e.g. container details under /environments/... while the
// page lives at /containers/{env}/{name}).
import { readdirSync, statSync } from 'node:fs';
import { dirname, join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { routes } from './routes';

const routesDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'routes');

/** Every +page.svelte below src/routes as a path pattern. */
function pagePatterns(): { file: string; re: RegExp }[] {
	const out: { file: string; re: RegExp }[] = [];
	const walk = (dir: string) => {
		for (const name of readdirSync(dir)) {
			const full = join(dir, name);
			if (statSync(full).isDirectory()) walk(full);
			else if (name === '+page.svelte') {
				const segments = relative(routesDir, dir)
					.split(sep)
					.filter((s) => s && !/^\(.+\)$/.test(s)); // (app), (auth): layout groups
				const re = segments
					.map((s) =>
						/^\[\.\.\..+\]$/.test(s)
							? '.+'
							: /^\[.+\]$/.test(s)
								? '[^/]+'
								: s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
					)
					.join('/');
				out.push({ file: relative(routesDir, full), re: new RegExp(`^/${re}$`) });
			}
		}
	};
	walk(routesDir);
	return out;
}

const pages = pagePatterns();

function resolves(url: string): boolean {
	const path = new URL(url, 'http://docker-manager.test').pathname;
	return pages.some((p) => p.re.test(path));
}

// One call per builder and variant. Keys must cover every builder.
const calls: Record<keyof typeof routes, string[]> = {
	dashboard: [routes.dashboard()],
	environments: [routes.environments()],
	environment: [
		routes.environment('env-1'),
		routes.environment('env-1', 'system'),
		routes.environment('env-1', 'agents'),
		routes.environment('env-1', 'jobs')
	],
	addEnvironment: [routes.addEnvironment(), routes.addEnvironment('env-1')],
	stacks: [routes.stacks()],
	stack: [
		routes.stack('st-1'),
		...(
			[
				'files',
				'logs',
				'terminal',
				'revisions',
				'backups',
				'policies',
				'activity',
				'migrate'
			] as const
		).map((t) => routes.stack('st-1', t))
	],
	stackTerminal: [routes.stackTerminal('st-1'), routes.stackTerminal('st-1', 'silo-web-1')],
	newStack: [routes.newStack(), routes.newStack('env-1')],
	importStack: [routes.importStack(), routes.importStack('env-1')],
	stackFromTemplate: [routes.stackFromTemplate(), routes.stackFromTemplate('tp-1', 'env-1')],
	containers: [routes.containers()],
	newContainer: [routes.newContainer(), routes.newContainer('env-1', 'nginx:1')],
	container: [
		routes.container('env-1', 'web'),
		routes.container('env-1', 'web', 'logs'),
		routes.container('env-1', 'web', 'terminal')
	],
	images: [routes.images()],
	image: [routes.image('env-1', 'sha256:abc')],
	volumes: [routes.volumes()],
	volume: [
		routes.volume('env-1', 'data'),
		routes.volume('env-1', 'data', 'files'),
		routes.volume('env-1', 'data', 'backups'),
		routes.volume('env-1', 'data', 'migrate')
	],
	networks: [routes.networks()],
	network: [routes.network('env-1', 'bridge')],
	builds: [routes.builds()],
	buildDefinitions: [routes.buildDefinitions()],
	newBuild: [routes.newBuild(), routes.newBuild('env-1', 'def-1')],
	build: [routes.build('env-1', 'b-1')],
	templates: [routes.templates(), routes.templates('web')],
	newTemplate: [routes.newTemplate()],
	template: [
		routes.template('tp-1'),
		...(['files', 'versions', 'settings'] as const).map((t) => routes.template('tp-1', t))
	],
	registries: [routes.registries()],
	gitCredentials: [routes.gitCredentials()],
	registryMatches: [routes.registryMatches()],
	backups: [routes.backups()],
	backup: [routes.backup('bk-1')],
	backupRestore: [routes.backupRestore('bk-1')],
	backupPolicies: [routes.backupPolicies()],
	backupList: [routes.backupList()],
	backupSnapshots: [routes.backupSnapshots()],
	backupPolicyNew: [routes.backupPolicyNew()],
	backupPolicy: [routes.backupPolicy('bp-1')],
	backupPolicyEdit: [routes.backupPolicyEdit('pol-1')],
	backupRepositories: [routes.backupRepositories()],
	backupRepositoryNew: [routes.backupRepositoryNew()],
	backupRepository: [routes.backupRepository('br-1')],
	updates: [routes.updates()],
	updatePolicyNew: [routes.updatePolicyNew()],
	updatePolicy: [routes.updatePolicy('up-1')],
	updatePolicyEdit: [routes.updatePolicyEdit('up-1')],
	maintenance: [routes.maintenance()],
	maintenanceNew: [routes.maintenanceNew()],
	maintenancePolicy: [routes.maintenancePolicy('mp-1')],
	maintenanceEdit: [routes.maintenanceEdit('mp-1')],
	maintenanceDefaults: [routes.maintenanceDefaults()],
	jobs: [routes.jobs()],
	job: [routes.job('job-1')],
	schedules: [routes.schedules()],
	access: [routes.access()],
	accessUser: [routes.accessUser('u-1')],
	accessGroups: [routes.accessGroups()],
	accessGroup: [routes.accessGroup('g-1')],
	accessInvitations: [routes.accessInvitations()],
	settings: [routes.settings()],
	security: [routes.security()],
	apiTokens: [routes.apiTokens()],
	apiTokenNew: [routes.apiTokenNew()],
	allApiTokens: [routes.allApiTokens()],
	signInPolicy: [routes.signInPolicy()],
	scheduleDefaults: [routes.scheduleDefaults()],
	audit: [routes.audit()],
	diagnostics: [routes.diagnostics()],
	setup: [routes.setup()],
	setupImport: [routes.setupImport()],
	signIn: [routes.signIn(), routes.signIn('/stacks', 'expired')],
	enroll: [routes.enroll()],
	volumeFiles: [routes.volumeFiles('env-1', 'data')],
	containerLogs: [routes.containerLogs('env-1', 'web')],
	containerTerminal: [routes.containerTerminal('env-1', 'web')],
	logsWindow: [
		routes.logsWindow({ stackId: 'st-1' }),
		routes.logsWindow({ environmentId: 'env-1', containerId: 'web' })
	]
};

describe('routes', () => {
	it('finds the route files', () => {
		expect(pages.length).toBeGreaterThan(50);
		expect(resolves('/does/not/exist')).toBe(false);
	});

	it('has a call for every builder', () => {
		expect(Object.keys(calls).sort()).toEqual(Object.keys(routes).sort());
	});

	it.each(Object.entries(calls).flatMap(([k, urls]) => urls.map((u) => [k, u])))(
		'routes.%s → %s resolves to a page',
		(_key, url) => {
			expect(resolves(url), `${url} has no +page.svelte`).toBe(true);
		}
	);

	it('encodes IDs as one path segment', () => {
		expect(routes.container('env 1', 'a/b')).toBe('/containers/env%201/a%2Fb');
		expect(resolves(routes.image('env-1', 'sha256:a/b'))).toBe(true);
	});
});
