// Query-key conventions for live invalidation (#23).
//
// Every server resource a view caches must use one of these key shapes so
// the live client (client.svelte.ts) can invalidate exactly what changed.
// TanStack Query matches by prefix: invalidating ['containers', 'item',
// 'e1', 'web'] also refreshes ['containers', 'item', 'e1', 'web', 'logs'].
//
//   [topic, 'list', ...filters]          lists (throttled: at most every 1 s)
//   [topic, 'item', id, ...sub]          details of instance-wide resources
//                                        (stacks, jobs, environments, agents,
//                                        policies, backups, registries,
//                                        settings, permissions)
//   [topic, 'item', envId, id, ...sub]   details of Docker objects (containers,
//                                        images, volumes, networks), named per
//                                        environment
//   ['metrics', 'item', envId, ...sub]   charts (throttled: at most every 10 s)
//   ['stacks', 'services', stackId]      a stack's containers (refreshed on
//                                        container events, throttled)
//   ['files', kind, scopeId, part, path] scoped files; kind stack|volume, scopeId
//                                        the stack ID or `<envId>/<volume>`;
//                                        part list|stat|content, path
//                                        root-relative ('.' for the root)
//   ['me', 'permissions']                the caller's effective permissions
//
// Build keys with liveKeys so they stay consistent.
import type { components } from '$lib/api/schema';

export type LiveInvalidate = components['schemas']['LiveInvalidate'];
export type LiveFilesChanged = components['schemas']['LiveFilesChanged'];
export type LiveJob = components['schemas']['LiveJob'];
export type LiveReset = components['schemas']['LiveReset'];

export const TOPICS = [
	'environments',
	'agents',
	'containers',
	'images',
	'volumes',
	'networks',
	'stacks',
	'jobs',
	'files',
	'policies',
	'backups',
	'registries',
	'settings',
	'permissions',
	'metrics',
	'templates'
] as const;
export type Topic = (typeof TOPICS)[number];

/** Topics whose resource names are only unique within an environment. */
const ENV_SCOPED = new Set<string>(['containers', 'images', 'volumes', 'networks', 'metrics']);

export type QueryKey = readonly unknown[];

/** How often a key may be refreshed by live events. */
export type RefreshClass = 'detail' | 'list' | 'metrics';

/** A key to invalidate and how fast. */
export interface Invalidation {
	key: QueryKey;
	class: RefreshClass;
}

export interface FileScopeRef {
	kind: 'stack' | 'volume' | 'template';
	/** Stack ID, `<environmentId>/<volume name>` or template ID. */
	id: string;
}

export const liveKeys = {
	list: (topic: Topic, ...filters: unknown[]): QueryKey => [topic, 'list', ...filters],
	item: (topic: Topic, ...ids: string[]): QueryKey => [topic, 'item', ...ids],
	stackServices: (stackId: string): QueryKey => ['stacks', 'services', stackId],
	metrics: (environmentId: string, ...sub: unknown[]): QueryKey => [
		'metrics',
		'item',
		environmentId,
		...sub
	],
	files: (scope: FileScopeRef, part?: 'list' | 'stat' | 'content', path?: string): QueryKey => {
		const k: unknown[] = ['files', scope.kind, scope.id];
		if (part) k.push(part);
		if (part && path !== undefined) k.push(path);
		return k;
	},
	myPermissions: ['me', 'permissions'] as QueryKey
};

/** The file scope of a files.changed event, or null for a whole environment. */
export function fileScopeOf(e: LiveFilesChanged): FileScopeRef | null {
	switch (e.scope.kind) {
		case 'stack':
			return { kind: 'stack', id: e.scope.id };
		case 'volume':
			return { kind: 'volume', id: `${e.scope.environmentId}/${e.scope.id}` };
		case 'template':
			return { kind: 'template', id: e.scope.id };
	}
	return null;
}

function parentOf(path: string): string {
	const i = path.lastIndexOf('/');
	return i < 0 ? '.' : path.slice(0, i);
}

/** Keys to refresh for an `invalidate` event. */
export function keysForInvalidate(e: LiveInvalidate): Invalidation[] {
	const env = e.environmentId ?? '';
	const topic = e.topic as Topic;
	// New samples refresh the environment's charts and the overview's
	// latest usage (dashboard CPU and memory), both at most every 10 s.
	if (topic === 'metrics')
		return [
			{ key: liveKeys.metrics(e.resourceId), class: 'metrics' },
			{ key: ['overview'], class: 'metrics' }
		];
	if (e.kind === 'inventory') {
		return [
			{ key: liveKeys.item('environments', e.resourceId), class: 'detail' },
			{ key: ['overview'], class: 'list' }
		];
	}
	const item =
		ENV_SCOPED.has(topic) && env !== ''
			? liveKeys.item(topic, env, e.resourceId)
			: liveKeys.item(topic, e.resourceId);
	const out: Invalidation[] = [
		{ key: liveKeys.list(topic), class: 'list' },
		{ key: item, class: 'detail' }
	];
	if (topic === 'environments' || topic === 'agents')
		out.push({ key: ['overview'], class: 'list' });
	if (topic === 'containers') out.push({ key: ['stacks', 'services'], class: 'list' });
	if (topic === 'permissions') out.push({ key: liveKeys.myPermissions, class: 'detail' });
	return out;
}

/** Keys to refresh for a `job` event. */
export function keysForJob(e: LiveJob): Invalidation[] {
	return [
		{ key: liveKeys.item('jobs', e.jobId), class: 'detail' },
		{ key: liveKeys.list('jobs'), class: 'list' }
	];
}

/** Keys to refresh for an `agent` (connection state) event. */
export function keysForAgent(environmentId: string): Invalidation[] {
	return [
		{ key: liveKeys.item('environments', environmentId), class: 'detail' },
		{ key: liveKeys.list('environments'), class: 'list' },
		{ key: ['overview'], class: 'list' }
	];
}

/**
 * Keys to refresh for a `files.changed` event: the listing of each path's
 * directory and of the path itself, the path's metadata and content (an
 * open editor keeps its unsaved buffer and compares ETags, #15), or every
 * key of the scope on overflow.
 */
export function keysForFiles(e: LiveFilesChanged): Invalidation[] {
	const scope = fileScopeOf(e);
	if (!scope) return [{ key: ['files'], class: 'list' }];
	if (e.overflow || e.paths.length === 0) return [{ key: liveKeys.files(scope), class: 'list' }];
	const out: Invalidation[] = [];
	const seen = new Set<string>();
	const add = (key: QueryKey, cls: RefreshClass) => {
		const id = JSON.stringify(key);
		if (seen.has(id)) return;
		seen.add(id);
		out.push({ key, class: cls });
	};
	for (const p of e.paths) {
		add(liveKeys.files(scope, 'list', parentOf(p)), 'detail');
		add(liveKeys.files(scope, 'list', p), 'detail');
		add(liveKeys.files(scope, 'stat', p), 'detail');
		add(liveKeys.files(scope, 'content', p), 'detail');
	}
	return out;
}

/** Whether a query key belongs to environment envId (environment resets). */
export function keyInEnvironment(key: QueryKey, envId: string): boolean {
	const [topic, part, first] = key;
	if (topic === 'environments' && part === 'item') return first === envId;
	if (typeof topic === 'string' && ENV_SCOPED.has(topic) && part === 'item')
		return first === envId;
	if (topic === 'files' && key[1] === 'volume') return String(key[2]).startsWith(envId + '/');
	// Lists and instance-wide resources may contain the environment's data.
	return part === 'list' || topic === 'files' || topic === 'stacks' || topic === 'overview';
}
