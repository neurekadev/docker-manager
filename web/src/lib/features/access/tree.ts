// The resource tree of the permission editor (#17): All Resources >
// environment > stacks, containers, volumes, … and the instance-wide
// resources (backup repositories and policies, registry connections, Git
// credentials). Each category lists its resources lazily with the API the
// feature pages use; rules on resources that are not listed (deleted, or
// the environment is offline) still appear from the rules themselves.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import {
	containersQuery,
	fetchAllPages,
	stacksQuery,
	volumesQuery
} from '$lib/features/common/data';
import { backupPoliciesQuery, repositoriesQuery } from '$lib/features/backups/queries';
import { maintenancePoliciesQuery } from '$lib/features/maintenance/queries';
import { updatePoliciesQuery } from '$lib/features/updates/queries';
import { scopeKey, type Rule, type Scope, type ScopeNode } from './permissions';

/** A category lists its resources as tree nodes. */
export interface Category {
	type: string;
	label: string;
	/** Listed per environment (else instance-wide). */
	perEnvironment: boolean;
	nodes: (environmentId: string) => ReturnType<typeof nodeQuery>;
}

/**
 * Types whose resources are named per environment (Docker names): their
 * scopes carry the environment; every other type has a global ID and the
 * manager rejects a scope naming an environment. Mirrors the catalog's
 * namedPerEnvironment (internal/manager/authz/catalog/entries.go).
 */
export const NAMED_PER_ENVIRONMENT: ReadonlySet<string> = new Set([
	'container',
	'image',
	'volume',
	'network'
]);

/** A resource as a tree node, listed under environmentId if given. */
export function resourceNode(
	type: string,
	id: string,
	environmentId: string | undefined,
	label: string,
	detail?: string
): ScopeNode {
	const scope: Scope = { kind: 'resource', resourceType: type, resourceId: id };
	if (environmentId && NAMED_PER_ENVIRONMENT.has(type)) scope.environmentId = environmentId;
	return { key: scopeKey(scope), label, scope, type, detail, environmentId };
}

/** Wraps a list query so the tree gets nodes; its own key keeps shapes apart. */
function nodeQuery<T>(
	base: { queryKey: readonly unknown[]; queryFn?: unknown },
	map: (item: T) => ScopeNode
) {
	return queryOptions({
		queryKey: [...base.queryKey, 'tree-nodes'],
		queryFn: async (ctx): Promise<ScopeNode[]> => {
			const items = (await (base.queryFn as (c: typeof ctx) => Promise<T[]>)(ctx)) ?? [];
			return items.map(map);
		},
		staleTime: 30_000,
		retry: false
	});
}

function listQuery<T>(
	key: readonly unknown[],
	load: (signal: AbortSignal, cursor?: string) => Promise<{ items: T[]; nextCursor?: string }>
) {
	return queryOptions({
		queryKey: key,
		queryFn: ({ signal }): Promise<T[]> => fetchAllPages((cursor) => load(signal, cursor))
	});
}

type Named = { id: string; name: string; environmentId?: string };

export const CATEGORIES: Category[] = [
	{
		type: 'stack',
		label: 'Stacks',
		perEnvironment: true,
		nodes: (env) =>
			nodeQuery(
				stacksQuery(env),
				(s: {
					id: string;
					name: string;
					displayName?: string;
					services?: { name: string }[];
				}) => ({
					...resourceNode('stack', s.id, env, s.displayName || s.name),
					children: serviceNodes(s.id, s.services ?? [], env)
				})
			)
	},
	{
		type: 'container',
		label: 'Containers',
		perEnvironment: true,
		nodes: (env) =>
			nodeQuery(containersQuery(env), (c: { name: string; image?: string }) =>
				resourceNode('container', c.name, env, c.name, c.image)
			)
	},
	{
		type: 'volume',
		label: 'Volumes',
		perEnvironment: true,
		nodes: (env) =>
			nodeQuery(volumesQuery(env), (v: { name: string }) =>
				resourceNode('volume', v.name, env, v.name)
			)
	},
	{
		type: 'image',
		label: 'Images',
		perEnvironment: true,
		nodes: (env) =>
			nodeQuery(
				listQuery(liveKeys.list('images', 'b5-tree', env), (signal, cursor) =>
					unwrap(
						api.GET('/api/v1/environments/{environmentId}/images', {
							params: { path: { environmentId: env }, query: { cursor, limit: 200 } },
							signal
						})
					)
				),
				(i: { id: string; repoTags: string[] }) =>
					resourceNode(
						'image',
						i.id,
						env,
						i.repoTags[0] ?? i.id.replace(/^sha256:/, '').slice(0, 12)
					)
			)
	},
	{
		type: 'network',
		label: 'Networks',
		perEnvironment: true,
		nodes: (env) =>
			nodeQuery(
				listQuery(liveKeys.list('networks', 'b5-tree', env), (signal, cursor) =>
					unwrap(
						api.GET('/api/v1/environments/{environmentId}/networks', {
							params: { path: { environmentId: env }, query: { cursor, limit: 200 } },
							signal
						})
					)
				),
				(n: Named) => resourceNode('network', n.name, env, n.name)
			)
	},
	{
		type: 'agent',
		label: 'Agents',
		perEnvironment: true,
		nodes: (env) =>
			nodeQuery(
				listQuery(liveKeys.list('agents', 'b5-tree', env), (signal, cursor) =>
					unwrap(
						api.GET('/api/v1/agents', {
							params: { query: { cursor, limit: 200, environmentId: env } },
							signal
						})
					)
				),
				(a: { id: string; hostname?: string }) =>
					resourceNode('agent', a.id, env, a.hostname || a.id.slice(0, 8))
			)
	},
	{
		type: 'update_policy',
		label: 'Update Policies',
		perEnvironment: true,
		nodes: (env) =>
			nodeQuery(updatePoliciesQuery(env), (p: Named) =>
				resourceNode('update_policy', p.id, env, p.name)
			)
	},
	{
		type: 'maintenance_policy',
		label: 'Maintenance Policies',
		perEnvironment: true,
		nodes: (env) =>
			nodeQuery(maintenancePoliciesQuery(env), (p: Named) =>
				resourceNode('maintenance_policy', p.id, env, p.name)
			)
	},
	{
		type: 'backup_repository',
		label: 'Backup Repositories',
		perEnvironment: false,
		nodes: () =>
			nodeQuery(repositoriesQuery(), (r: Named) =>
				resourceNode('backup_repository', r.id, undefined, r.name)
			)
	},
	{
		type: 'backup_policy',
		label: 'Backup Policies',
		perEnvironment: false,
		nodes: () =>
			nodeQuery(backupPoliciesQuery(), (p: Named) =>
				resourceNode('backup_policy', p.id, undefined, p.name)
			)
	},
	{
		type: 'registry',
		label: 'Registry Connections',
		perEnvironment: false,
		nodes: () =>
			nodeQuery(
				listQuery(liveKeys.list('registries', 'b5-tree'), (signal, cursor) =>
					unwrap(
						api.GET('/api/v1/registries', {
							params: { query: { cursor, limit: 200 } },
							signal
						})
					)
				),
				(r: Named) => resourceNode('registry', r.id, undefined, r.name)
			)
	},
	{
		type: 'git_credential',
		label: 'Git Credentials',
		perEnvironment: false,
		nodes: () =>
			nodeQuery(
				listQuery(liveKeys.list('registries', 'b5-tree-git'), (signal, cursor) =>
					unwrap(
						api.GET('/api/v1/git-credentials', {
							params: { query: { cursor, limit: 200 } },
							signal
						})
					)
				),
				(r: Named) => resourceNode('git_credential', r.id, undefined, r.name)
			)
	}
];

/**
 * Nodes named by rules that the lists did not return (deleted, offline).
 * A rule on a type with a global ID names no environment: it shows under
 * the environment `homeOf` knows for its resource (a stack's, from the
 * stack list), or in every environment that does not list it when that
 * is unknown (deleted).
 */
export function nodesFromRules(
	rules: Rule[],
	type: string,
	environmentId: string | undefined,
	known: ReadonlySet<string>,
	homeOf?: (type: string, id: string) => string | undefined
): ScopeNode[] {
	const perEnvironment = NAMED_PER_ENVIRONMENT.has(type);
	const out: ScopeNode[] = [];
	for (const r of rules) {
		const s = r.scope;
		if (s.kind !== 'resource' || s.resourceType !== type) continue;
		if (perEnvironment && (s.environmentId || undefined) !== environmentId) continue;
		const home = perEnvironment ? undefined : homeOf?.(type, s.resourceId ?? '');
		if (home && environmentId && home !== environmentId) continue;
		const n = resourceNode(
			type,
			s.resourceId ?? '',
			perEnvironment ? environmentId : undefined,
			s.resourceId ?? '',
			perEnvironment ? 'Not listed now' : 'Not listed here'
		);
		if (known.has(n.key) || out.some((o) => o.key === n.key)) continue;
		out.push(n);
	}
	return out;
}

/** Services of a stack as nodes (resource ID `<stackId>/<service>`). */
export function serviceNodes(
	stackId: string,
	services: { name: string }[],
	env: string
): ScopeNode[] {
	return services.map((s) => resourceNode('service', `${stackId}/${s.name}`, env, s.name));
}

export function instanceNode(): ScopeNode {
	return {
		key: 'instance',
		label: 'All Resources',
		scope: { kind: 'instance' },
		type: 'instance'
	};
}

export function environmentNode(e: { id: string; name: string }): ScopeNode {
	return {
		key: `env:${e.id}`,
		label: e.name,
		scope: { kind: 'environment', environmentId: e.id },
		type: 'environment'
	};
}
