// Stack queries (#22 track B2, #7): query-key entries and queryOptions
// factories for the stack pages. Keys follow the live conventions
// (liveKeys, $lib/live/keys.ts) so the #23 live client refreshes them:
// stack events refresh ['stacks', 'item', id, ...], container events the
// services, job events the job lists, metrics events the charts.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Job, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import { pollWhileDown } from '$lib/live/status.svelte';

export type Stack = Schema<'Stack'>;
export type StackServices = Schema<'StackServices'>;
export type StackServiceStatus = Schema<'StackServiceStatus'>;
export type StackContainer = Schema<'StackContainer'>;
export type StackRevision = Schema<'StackRevision'>;
export type StackImageStatus = Schema<'StackImageStatus'>;
export type ContainerMetrics = Schema<'ContainerMetrics'>;
export type EnvironmentCapacity = Schema<'EnvironmentCapacity'>;
export type UpdatePolicy = Schema<'UpdatePolicy'>;
export type UpdateCandidate = Schema<'UpdateCandidate'>;
export type AuditEvent = Schema<'AuditEvent'>;
export type DiscoveredStack = Schema<'DiscoveredStack'>;

export const stackKeys = {
	all: ['stacks'] as const,
	list: (environmentId: string | null) => liveKeys.list('stacks', environmentId ?? ''),
	detail: (id: string) => liveKeys.item('stacks', id),
	services: (id: string) => liveKeys.stackServices(id),
	revisions: (id: string) => liveKeys.item('stacks', id, 'revisions'),
	revision: (id: string, revisionId: string) =>
		liveKeys.item('stacks', id, 'revisions', revisionId),
	imageStatus: (id: string) => liveKeys.item('stacks', id, 'image-status'),
	// Discovery asks the agent for every Compose project: not refreshed by
	// each stack event (the page has a Refresh action instead).
	discovered: (environmentId: string) => ['stacks', 'discovered', environmentId] as const,
	jobs: (id: string) => liveKeys.list('jobs', `stack:${id}`),
	// Audit records have no live topic; job events refresh the jobs list
	// beside them and the audit list is re-read with it (staleTime).
	audit: (id: string) => ['audit', 'list', `stack:${id}`] as const,
	metrics: (environmentId: string, containers: string[]) =>
		liveKeys.metrics(environmentId, 'stack-containers', containers.join(',')),
	capacity: (environmentId: string) => liveKeys.metrics(environmentId, 'capacity'),
	updatePolicies: (environmentId: string) => liveKeys.list('policies', 'update', environmentId),
	updatePolicy: (id: string) => liveKeys.item('policies', id),
	candidates: (policyId: string) => liveKeys.item('policies', policyId, 'candidates')
};

/** Reads every page of a cursor-paged list. */
async function allPages<T>(
	page: (cursor: string | undefined) => Promise<{ items: T[]; nextCursor?: string }>,
	max = 50
): Promise<T[]> {
	const out: T[] = [];
	let cursor: string | undefined;
	for (let i = 0; i < max; i++) {
		const p = await page(cursor);
		out.push(...p.items);
		cursor = p.nextCursor;
		if (!cursor) break;
	}
	return out;
}

/** Stacks of one environment, or of every visible one (null). */
export function stacksQuery(environmentId: string | null, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.list(environmentId),
		queryFn: ({ signal }): Promise<Stack[]> =>
			allPages((cursor) =>
				unwrap(
					client.GET('/api/v1/stacks', {
						params: {
							query: { limit: 200, cursor, environmentId: environmentId ?? undefined }
						},
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

export function stackQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.detail(id),
		queryFn: ({ signal }): Promise<Stack> =>
			unwrap(
				client.GET('/api/v1/stacks/{stackId}', {
					params: { path: { stackId: id } },
					signal
				})
			),
		staleTime: 10_000
	});
}

export function stackServicesQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.services(id),
		queryFn: ({ signal }): Promise<StackServices> =>
			unwrap(
				client.GET('/api/v1/stacks/{stackId}/services', {
					params: { path: { stackId: id } },
					signal
				})
			),
		staleTime: 10_000
	});
}

export function stackRevisionsQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.revisions(id),
		queryFn: ({ signal }): Promise<StackRevision[]> =>
			allPages(
				(cursor) =>
					unwrap(
						client.GET('/api/v1/stacks/{stackId}/revisions', {
							params: { path: { stackId: id }, query: { limit: 100, cursor } },
							signal
						})
					),
				5
			),
		staleTime: 15_000
	});
}

/** One revision with its file contents (every read is audited, #7). */
export function stackRevisionQuery(id: string, revisionId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.revision(id, revisionId),
		queryFn: ({ signal }): Promise<StackRevision> =>
			unwrap(
				client.GET('/api/v1/stacks/{stackId}/revisions/{revisionId}', {
					params: { path: { stackId: id, revisionId } },
					signal
				})
			),
		// Revisions are immutable.
		staleTime: Infinity
	});
}

export function stackImageStatusQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.imageStatus(id),
		queryFn: ({ signal }): Promise<StackImageStatus[]> =>
			unwrap(
				client.GET('/api/v1/stacks/{stackId}/image-status', {
					params: { path: { stackId: id } },
					signal
				})
			).then((r) => r.images),
		staleTime: 30_000
	});
}

/** Last hour of CPU and memory of the stack's containers (#5 metrics). */
export function stackMetricsQuery(
	environmentId: string,
	containers: string[],
	client: ApiClient = api,
	now: () => Date = () => new Date()
) {
	return queryOptions({
		queryKey: stackKeys.metrics(environmentId, containers),
		queryFn: async ({ signal }): Promise<ContainerMetrics[]> => {
			const to = now();
			const from = new Date(to.getTime() - 3600_000);
			const settled = await Promise.allSettled(
				containers.map((name) =>
					unwrap(
						client.GET(
							'/api/v1/environments/{environmentId}/containers/{containerId}/metrics',
							{
								params: {
									path: { environmentId, containerId: name },
									query: {
										from: from.toISOString(),
										to: to.toISOString(),
										stepSeconds: 60,
										series: [
											'cpu.percent',
											'memory.used_bytes',
											'memory.limit_bytes'
										]
									}
								},
								// The API reads arrays comma-separated (explode: false).
								querySerializer: { array: { style: 'form', explode: false } },
								signal
							}
						)
					)
				)
			);
			// A container the caller cannot chart (403) or that has no
			// samples yet is simply absent; the KPIs say so.
			return settled.flatMap((s) => (s.status === 'fulfilled' ? [s.value] : []));
		},
		enabled: containers.length > 0,
		staleTime: 10_000,
		// New stored samples (every 10 s) refresh it through metrics events
		// (live CPU and memory values do not: the KPIs read those from
		// latestContainerMetricsQuery). Polling only while the stream is down.
		refetchInterval: pollWhileDown(60_000)
	});
}

export function capacityQuery(environmentId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.capacity(environmentId),
		queryFn: ({ signal }): Promise<EnvironmentCapacity> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/capacity', {
					params: { path: { environmentId } },
					signal
				})
			),
		staleTime: 30_000,
		retry: false
	});
}

/**
 * Update policies of an environment, or of all (null); a stack's own is
 * found by its target.
 */
export function updatePoliciesQuery(environmentId: string | null, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.updatePolicies(environmentId ?? ''),
		queryFn: ({ signal }): Promise<UpdatePolicy[]> =>
			allPages((cursor) =>
				unwrap(
					client.GET('/api/v1/update-policies', {
						params: {
							query: { limit: 200, cursor, environmentId: environmentId ?? undefined }
						},
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

export function updatePolicyQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.updatePolicy(id),
		queryFn: ({ signal }): Promise<UpdatePolicy> =>
			unwrap(
				client.GET('/api/v1/update-policies/{policyId}', {
					params: { path: { policyId: id } },
					signal
				})
			),
		staleTime: 10_000
	});
}

export function candidatesQuery(policyId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.candidates(policyId),
		queryFn: ({ signal }): Promise<UpdateCandidate[]> =>
			unwrap(
				client.GET('/api/v1/update-policies/{policyId}/candidates', {
					params: { path: { policyId } },
					signal
				})
			).then((p) => p.items),
		staleTime: 10_000
	});
}

/** The stack's jobs, newest first (one page). */
export function stackJobsQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.jobs(id),
		queryFn: ({ signal }): Promise<Job[]> =>
			unwrap(
				client.GET('/api/v1/jobs', {
					params: { query: { target: `stack:${id}`, limit: 50 } },
					signal
				})
			).then((p) => p.items),
		staleTime: 10_000
	});
}

/**
 * Audit records touching the stack (audit.read only), newest first: the
 * first `pages` pages of 50 ("Load more" asks for one page more); `more`
 * says whether older records exist.
 */
export function stackAuditQuery(id: string, pages = 1, client: ApiClient = api) {
	return queryOptions({
		queryKey: [...stackKeys.audit(id), pages] as const,
		queryFn: async ({ signal }): Promise<{ items: AuditEvent[]; more: boolean }> => {
			const items: AuditEvent[] = [];
			let cursor: string | undefined;
			for (let i = 0; i < pages; i++) {
				const p = await unwrap(
					client.GET('/api/v1/audit', {
						params: { query: { resource: `stack:${id}`, limit: 50, cursor } },
						signal
					})
				);
				items.push(...p.items);
				cursor = p.nextCursor;
				if (!cursor) break;
			}
			return { items, more: !!cursor };
		},
		staleTime: 15_000
	});
}

export function discoveredQuery(environmentId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: stackKeys.discovered(environmentId),
		queryFn: ({ signal }): Promise<DiscoveredStack[]> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/stacks/discovered', {
					params: { path: { environmentId } },
					signal
				})
			).then((r) => r.projects),
		staleTime: 15_000,
		retry: false
	});
}
