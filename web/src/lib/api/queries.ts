// Svelte Query integration for the generated client (docs/web.md).
//
// Each server resource gets a query-key factory entry and an options
// factory built with queryOptions(), so components (createQuery) and
// imperative code (queryClient.fetchQuery / invalidateQueries) share one
// definition. Resource keys follow the live conventions (liveKeys,
// src/lib/live/keys.ts), so the #23 live client refreshes them.
import {
	MutationCache,
	QueryCache,
	QueryClient,
	infiniteQueryOptions,
	queryOptions,
	type QueryKey
} from '@tanstack/svelte-query';
import { liveKeys } from '$lib/live/keys';
import { acrossEnvironments, allPages, type EnvList, type EnvTarget } from './multi-env';
import {
	api,
	ApiRequestError,
	unwrap,
	type Agent,
	type AgentEnrollment,
	type ApiClient,
	type Schema,
	type Environment,
	type EnvironmentCapacity,
	type EnvironmentMetrics,
	type EnvironmentSystem,
	type Job,
	type Schedule,
	type Stack,
	type UpdatePolicy,
	type MyPermissions,
	type Overview,
	type SchedulePreview,
	type SearchResults,
	type Session
} from './client';

/**
 * Query keys. Invalidate by prefix, e.g. queryKeys.environments.all.
 * Server resources use liveKeys shapes ([topic, 'list', ...filters],
 * [topic, 'item', id, ...sub]) so live events refresh them; session, setup
 * and health are not resources of the live stream.
 */
export const queryKeys = {
	health: ['health'] as const,
	setupStatus: ['setup', 'status'] as const,
	session: ['session'] as const,
	me: ['me'] as const,
	overview: ['overview'] as const,
	myPermissions: liveKeys.myPermissions,
	environments: {
		all: ['environments'] as const,
		list: () => liveKeys.list('environments'),
		detail: (id: string) => liveKeys.item('environments', id),
		system: (id: string) => liveKeys.item('environments', id, 'system')
	},
	jobs: {
		all: ['jobs'] as const,
		detail: (id: string) => liveKeys.item('jobs', id)
	},
	containers: {
		all: ['containers'] as const,
		list: (envIds: string[]) => liveKeys.list('containers', envIds.join(',')),
		detail: (env: string, name: string) => liveKeys.item('containers', env, name)
	},
	containerMetrics: (env: string, name: string, rangeSeconds: number) =>
		liveKeys.metrics(env, 'container', name, rangeSeconds),
	// Refreshed by metrics.sampled (at most every 10 s, #23).
	latestContainerMetrics: (env: string) => liveKeys.metrics(env, 'containers-latest'),
	// Under the volume lists: volume events refresh it too (the manager
	// answers from its one-minute cache).
	volumeUsage: (env: string) => liveKeys.list('volumes', 'usage', env),
	images: {
		all: ['images'] as const,
		list: (envIds: string[]) => liveKeys.list('images', envIds.join(',')),
		detail: (env: string, id: string) => liveKeys.item('images', env, id),
		// Builds and build definitions are published on the images topic (#23).
		builds: (envIds: string[]) => liveKeys.list('images', 'builds', envIds.join(',')),
		build: (env: string, id: string) => liveKeys.item('images', env, id),
		definitions: (envIds: string[]) => liveKeys.list('images', 'definitions', envIds.join(',')),
		definition: (env: string, id: string) => liveKeys.item('images', env, id)
	},
	volumes: {
		all: ['volumes'] as const,
		list: (envIds: string[]) => liveKeys.list('volumes', envIds.join(',')),
		detail: (env: string, name: string) => liveKeys.item('volumes', env, name)
	},
	networks: {
		all: ['networks'] as const,
		list: (envIds: string[]) => liveKeys.list('networks', envIds.join(',')),
		detail: (env: string, name: string) => liveKeys.item('networks', env, name)
	},
	// Registry connections and Git credentials share the registries topic.
	registries: {
		all: ['registries'] as const,
		list: () => liveKeys.list('registries'),
		gitList: () => liveKeys.list('registries', 'git'),
		match: (ref: string, env: string, stack: string, registryId: string) =>
			['registries', 'match', ref, env, stack, registryId] as const
	},
	search: (q: string, environmentId: string | null) =>
		['search', q, environmentId ?? ''] as const,
	schedulePreview: (cron: string, timeZone: string, kind?: string) =>
		['schedules', 'preview', cron, timeZone, kind ?? ''] as const
} satisfies Record<string, unknown>;

/** GET /api/v1/health (liveness, public). */
export function healthQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.health,
		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/health', { signal })),
		staleTime: 30_000
	});
}

/** GET /api/v1/setup/status (public): whether the owner exists. */
export function setupStatusQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.setupStatus,
		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/setup/status', { signal })),
		staleTime: 10_000
	});
}

/**
 * GET /api/v1/auth/session. Resolves to null when there is no session
 * (401), so "signed out" is data, not an error.
 */
export function sessionQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.session,
		queryFn: async ({ signal }): Promise<Session | null> => {
			try {
				return await unwrap(client.GET('/api/v1/auth/session', { signal }));
			} catch (e) {
				if (e instanceof ApiRequestError && e.status === 401) return null;
				throw e;
			}
		},
		staleTime: 60_000
	});
}

/** GET /api/v1/me/permissions: effective permissions and visible environments. */
export function myPermissionsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.myPermissions,
		queryFn: ({ signal }): Promise<MyPermissions> =>
			unwrap(client.GET('/api/v1/me/permissions', { signal })),
		staleTime: 60_000
	});
}

/** GET /api/v1/overview: counts and usage of the visible environments. */
export function overviewQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.overview,
		queryFn: ({ signal }): Promise<Overview> =>
			unwrap(client.GET('/api/v1/overview', { signal })),
		staleTime: 15_000,
		// Live events refresh it: connection and inventory changes, and new
		// metric samples (latest usage, at most every 10 s; keysForInvalidate).
		// The interval is only a safety net for a stalled stream.
		refetchInterval: 60_000
	});
}

/** Every active environment the caller may see (all pages). */
export function environmentsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.environments.list(),
		queryFn: async ({ signal }): Promise<Environment[]> => {
			const out: Environment[] = [];
			let cursor: string | undefined;
			do {
				const page = await unwrap(
					client.GET('/api/v1/environments', {
						params: { query: { limit: 200, cursor } },
						signal
					})
				);
				out.push(...page.items);
				cursor = page.nextCursor;
			} while (cursor);
			return out;
		},
		staleTime: 30_000
	});
}

/** GET /environments/{id}/system (environment.system.read): Engine version etc. */
export function environmentSystemQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.environments.system(id),
		queryFn: ({ signal }): Promise<EnvironmentSystem> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/system', {
					params: { path: { environmentId: id } },
					signal
				})
			),
		staleTime: 60_000
	});
}

/** GET /api/v1/jobs/{jobId}. */
export function jobQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.jobs.detail(id),
		queryFn: ({ signal }): Promise<Job> =>
			unwrap(client.GET('/api/v1/jobs/{jobId}', { params: { path: { jobId: id } }, signal }))
	});
}

// --- Environments, agents, metrics, jobs and schedules (#22 track B1) ---

/** GET /environments/{id}: the environment with its revision (If-Match). */
export function environmentQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.environments.detail(id),
		queryFn: ({ signal }): Promise<Environment> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}', {
					params: { path: { environmentId: id } },
					signal
				})
			),
		staleTime: 15_000
	});
}

/** Archived environments (status=archived), for re-attaching. */
export function archivedEnvironmentsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('environments', 'archived'),
		queryFn: async ({ signal }): Promise<Environment[]> => {
			const page = await unwrap(
				client.GET('/api/v1/environments', {
					params: { query: { limit: 200, status: ['archived'] } },
					signal
				})
			);
			return page.items;
		},
		staleTime: 30_000
	});
}

/** GET /environments/{id}/agents: the environment's agents (active and revoked). */
export function environmentAgentsQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('agents', { environmentId: id }),
		queryFn: async ({ signal }): Promise<Agent[]> => {
			const page = await unwrap(
				client.GET('/api/v1/environments/{environmentId}/agents', {
					params: { path: { environmentId: id }, query: { limit: 200 } },
					signal
				})
			);
			return page.items;
		},
		staleTime: 15_000
	});
}

/** GET /agent-enrollments (agent.enroll): tokens issued, newest first. */
export function enrollmentsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('agents', 'enrollments'),
		queryFn: async ({ signal }): Promise<AgentEnrollment[]> => {
			const page = await unwrap(
				client.GET('/api/v1/agent-enrollments', {
					params: { query: { limit: 200 } },
					signal
				})
			);
			return page.items;
		},
		staleTime: 10_000
	});
}

/** GET /environments/{id}/capacity: cores, memory, disks and the latest usage. */
export function environmentCapacityQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.metrics(id, 'capacity'),
		queryFn: ({ signal }): Promise<EnvironmentCapacity> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/capacity', {
					params: { path: { environmentId: id } },
					signal
				})
			),
		staleTime: 10_000
	});
}

/**
 * GET /environments/{id}/metrics over the last `seconds` (read at fetch
 * time, so live refreshes move the window). Keyed by the range, not the
 * instant: `metrics` live events refresh it at most every 10 s.
 */
export function environmentMetricsQuery(
	id: string,
	seconds: number,
	opts: { series?: string[]; stepSeconds?: number } = {},
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: liveKeys.metrics(
			id,
			'host',
			seconds,
			opts.series?.join(',') ?? '',
			opts.stepSeconds ?? 0
		),
		queryFn: ({ signal }): Promise<EnvironmentMetrics> => {
			const to = new Date();
			const from = new Date(to.getTime() - seconds * 1000);
			return unwrap(
				client.GET('/api/v1/environments/{environmentId}/metrics', {
					params: {
						path: { environmentId: id },
						query: {
							from: from.toISOString(),
							to: to.toISOString(),
							stepSeconds: opts.stepSeconds,
							series: opts.series?.length ? [opts.series.join(',')] : undefined
						}
					},
					signal
				})
			);
		},
		staleTime: 10_000,
		placeholderData: (prev) => prev
	});
}

/** Filters of the jobs list (GET /jobs). */
export interface JobFilters {
	/** Comma-free state names; several are ORed. */
	states?: Job['state'][];
	kind?: string;
	environmentId?: string;
	origins?: Job['origin'][];
	target?: string;
}

/** One page of GET /jobs (newest first). `state` is comma-separated on the wire. */
export async function fetchJobsPage(
	f: JobFilters,
	cursor: string | undefined,
	limit: number,
	signal?: AbortSignal,
	client: ApiClient = api
): Promise<{ items: Job[]; nextCursor?: string }> {
	return unwrap(
		client.GET('/api/v1/jobs', {
			params: {
				query: {
					limit,
					cursor,
					// The state parameter is not exploded: one comma-separated value.
					state: f.states?.length ? ([f.states.join(',')] as Job['state'][]) : undefined,
					kind: f.kind || undefined,
					environmentId: f.environmentId || undefined,
					origin: f.origins?.length ? f.origins : undefined,
					target: f.target || undefined
				}
			},
			signal
		})
	);
}

/** GET /jobs page by page (the jobs view's "Load more"); refreshed by job events. */
export function jobsInfiniteQuery(f: JobFilters, limit = 50, client: ApiClient = api) {
	return infiniteQueryOptions({
		queryKey: liveKeys.list('jobs', 'pages', f),
		queryFn: ({ signal, pageParam }) => fetchJobsPage(f, pageParam, limit, signal, client),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: (last) => last.nextCursor,
		staleTime: 10_000
	});
}

/** The newest jobs (dashboard, notices); refreshed by job events. */
export function recentJobsQuery(limit = 20, f: JobFilters = {}, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('jobs', 'recent', limit, f),
		queryFn: ({ signal }) => fetchJobsPage(f, undefined, limit, signal, client),
		staleTime: 10_000
	});
}

/** GET /schedules: every visible policy's schedule with next and recent runs. */
export function schedulesQuery(
	f: { kind?: string; environmentId?: string } = {},
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: liveKeys.list('policies', 'schedules', f),
		queryFn: async ({ signal }): Promise<Schedule[]> => {
			const out: Schedule[] = [];
			let cursor: string | undefined;
			do {
				const page = await unwrap(
					client.GET('/api/v1/schedules', {
						params: {
							query: {
								limit: 200,
								cursor,
								kind: f.kind || undefined,
								environmentId: f.environmentId || undefined
							}
						},
						signal
					})
				);
				out.push(...page.items);
				cursor = page.nextCursor;
			} while (cursor);
			return out;
		},
		staleTime: 30_000,
		// Next runs move on as time passes; runs change with job events.
		refetchInterval: 60_000
	});
}

/** GET /update-policies (first 200): per-policy update summaries. */
export function updatePoliciesSummaryQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('policies', 'update-summary'),
		queryFn: async ({ signal }): Promise<UpdatePolicy[]> => {
			const page = await unwrap(
				client.GET('/api/v1/update-policies', { params: { query: { limit: 200 } }, signal })
			);
			return page.items;
		},
		staleTime: 60_000
	});
}

/** GET /stacks (first 200), for per-environment counts on the dashboard. */
export function stacksSummaryQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('stacks', 'summary'),
		queryFn: async ({ signal }): Promise<Stack[]> => {
			const page = await unwrap(
				client.GET('/api/v1/stacks', { params: { query: { limit: 200 } }, signal })
			);
			return page.items;
		},
		staleTime: 30_000
	});
}

/** GET /api/v1/search (command palette). */
export function searchQuery(q: string, environmentId: string | null, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.search(q, environmentId),
		queryFn: ({ signal }): Promise<SearchResults> =>
			unwrap(
				client.GET('/api/v1/search', {
					params: { query: { q, limit: 20, environmentId: environmentId ?? undefined } },
					signal
				})
			),
		enabled: q.trim().length > 0,
		staleTime: 15_000
	});
}

/** POST /api/v1/schedules/previews: next runs of a cron expression (a read). */
export function schedulePreviewQuery(
	cron: string,
	timeZone: string,
	kind?: string,
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: queryKeys.schedulePreview(cron, timeZone, kind),
		queryFn: ({ signal }): Promise<SchedulePreview> =>
			unwrap(
				client.POST('/api/v1/schedules/previews', {
					body: { cron, timeZone: timeZone || undefined, kind, count: 5 },
					signal
				})
			),
		enabled: cron.trim().length > 0,
		staleTime: 60_000,
		retry: false
	});
}

// Docker resources (#6), builds (#33) and credentials (#19). Lists read the
// selected environment or every visible one (acrossEnvironments), all
// pages, and the views filter them; offline environments are reported in
// `unavailable`, not as errors.

export type Container = Schema<'Container'>;
export type Image = Schema<'Image'>;
export type Volume = Schema<'Volume'>;
export type Network = Schema<'Network'>;
export type ImageBuild = Schema<'ImageBuild'>;
export type BuildDefinition = Schema<'BuildDefinition'>;
export type RegistryConnection = Schema<'RegistryConnection'>;
export type GitCredential = Schema<'GitCredential'>;
export type ContainerMetrics = Schema<'ContainerMetrics'>;
export type RegistryMatch = Schema<'RegistryMatch'>;
export type LatestContainerMetric = Schema<'LatestContainerMetric'>;
export type VolumeUsageList = Schema<'VolumeUsageList'>;

const LIST_LIMIT = 200;
const envIds = (targets: EnvTarget[]) => targets.map((t) => t.id);

/** Containers of the target environments (every state). */
export function containersQuery(targets: EnvTarget[], client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.containers.list(envIds(targets)),
		queryFn: ({ signal }): Promise<EnvList<Container>> =>
			acrossEnvironments(targets, (env) =>
				allPages((cursor) =>
					unwrap(
						client.GET('/api/v1/environments/{environmentId}/containers', {
							params: {
								path: { environmentId: env.id },
								query: { limit: LIST_LIMIT, cursor }
							},
							signal
						})
					)
				)
			),
		staleTime: 10_000
	});
}

/** GET a container (full view with details, or minimal). */
export function containerQuery(env: string, name: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.containers.detail(env, name),
		queryFn: ({ signal }): Promise<Container> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/containers/{containerId}', {
					params: { path: { environmentId: env, containerId: name } },
					signal
				})
			),
		staleTime: 10_000
	});
}

/** CPU and memory of a container over the last rangeSeconds (#5 units). */
export function containerMetricsQuery(
	env: string,
	name: string,
	rangeSeconds: number,
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: queryKeys.containerMetrics(env, name, rangeSeconds),
		queryFn: ({ signal }): Promise<ContainerMetrics> =>
			unwrap(
				client.GET(
					'/api/v1/environments/{environmentId}/containers/{containerId}/metrics',
					{
						params: {
							path: { environmentId: env, containerId: name },
							query: {
								from: new Date(Date.now() - rangeSeconds * 1000).toISOString()
							}
						},
						signal
					}
				)
			),
		staleTime: 10_000,
		retry: false
	});
}

/**
 * The newest CPU and memory sample of each container of an environment the
 * caller may chart (#5), by container name. Live: metrics events refresh
 * it about every 10 s; the interval covers a stream outage.
 */
export function latestContainerMetricsQuery(env: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.latestContainerMetrics(env),
		queryFn: async ({ signal }): Promise<Record<string, LatestContainerMetric>> => {
			const r = await unwrap(
				client.GET('/api/v1/environments/{environmentId}/metrics/containers', {
					params: { path: { environmentId: env } },
					signal
				})
			);
			return Object.fromEntries(r.items.map((m) => [m.container, m]));
		},
		staleTime: 10_000,
		refetchInterval: 30_000,
		retry: false
	});
}

/**
 * The sizes of an environment's volumes (#6). The Engine walks the volumes
 * to compute them and the manager reuses the answer for a minute, so this
 * refreshes at most once a minute; the first answer may take a while.
 */
export function volumeUsageQuery(env: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.volumeUsage(env),
		queryFn: ({ signal }): Promise<VolumeUsageList> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/disk-usage/volumes', {
					params: { path: { environmentId: env } },
					signal
				})
			),
		staleTime: 60_000,
		refetchInterval: 60_000,
		retry: false
	});
}

/** Images of the target environments. */
export function imagesQuery(targets: EnvTarget[], client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.images.list(envIds(targets)),
		queryFn: ({ signal }): Promise<EnvList<Image>> =>
			acrossEnvironments(targets, (env) =>
				allPages((cursor) =>
					unwrap(
						client.GET('/api/v1/environments/{environmentId}/images', {
							params: {
								path: { environmentId: env.id },
								query: { limit: LIST_LIMIT, cursor }
							},
							signal
						})
					)
				)
			),
		staleTime: 10_000
	});
}

export function imageQuery(env: string, id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.images.detail(env, id),
		queryFn: ({ signal }): Promise<Image> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/images/{imageId}', {
					params: { path: { environmentId: env, imageId: id } },
					signal
				})
			),
		staleTime: 10_000
	});
}

/** Volumes of the target environments. */
export function volumesQuery(targets: EnvTarget[], client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.volumes.list(envIds(targets)),
		queryFn: ({ signal }): Promise<EnvList<Volume>> =>
			acrossEnvironments(targets, (env) =>
				allPages((cursor) =>
					unwrap(
						client.GET('/api/v1/environments/{environmentId}/volumes', {
							params: {
								path: { environmentId: env.id },
								query: { limit: LIST_LIMIT, cursor }
							},
							signal
						})
					)
				)
			),
		staleTime: 10_000
	});
}

export function volumeQuery(env: string, name: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.volumes.detail(env, name),
		queryFn: ({ signal }): Promise<Volume> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/volumes/{volumeId}', {
					params: { path: { environmentId: env, volumeId: name } },
					signal
				})
			),
		staleTime: 10_000
	});
}

/** Networks of the target environments. */
export function networksQuery(targets: EnvTarget[], client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.networks.list(envIds(targets)),
		queryFn: ({ signal }): Promise<EnvList<Network>> =>
			acrossEnvironments(targets, (env) =>
				allPages((cursor) =>
					unwrap(
						client.GET('/api/v1/environments/{environmentId}/networks', {
							params: {
								path: { environmentId: env.id },
								query: { limit: LIST_LIMIT, cursor }
							},
							signal
						})
					)
				)
			),
		staleTime: 10_000
	});
}

export function networkQuery(env: string, name: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.networks.detail(env, name),
		queryFn: ({ signal }): Promise<Network> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/networks/{networkId}', {
					params: { path: { environmentId: env, networkId: name } },
					signal
				})
			),
		staleTime: 10_000
	});
}

/**
 * The newest build records of the target environments (one page each),
 * newest first. Builds are manager records: offline environments answer too.
 */
export function imageBuildsQuery(targets: EnvTarget[], client: ApiClient = api) {
	const all = targets.map((t) => ({ ...t, online: true }));
	return queryOptions({
		queryKey: queryKeys.images.builds(envIds(targets)),
		queryFn: async ({ signal }): Promise<EnvList<ImageBuild>> => {
			const out = await acrossEnvironments(all, (env) =>
				allPages(
					(cursor) =>
						unwrap(
							client.GET('/api/v1/environments/{environmentId}/image-builds', {
								params: {
									path: { environmentId: env.id },
									query: { limit: LIST_LIMIT, cursor }
								},
								signal
							})
						),
					1
				)
			);
			out.items.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
			return out;
		},
		staleTime: 10_000
	});
}

export function imageBuildQuery(env: string, id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.images.build(env, id),
		queryFn: ({ signal }): Promise<ImageBuild> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/image-builds/{buildId}', {
					params: { path: { environmentId: env, buildId: id } },
					signal
				})
			),
		staleTime: 10_000
	});
}

/** Saved build definitions of the target environments (manager records). */
export function buildDefinitionsQuery(targets: EnvTarget[], client: ApiClient = api) {
	const all = targets.map((t) => ({ ...t, online: true }));
	return queryOptions({
		queryKey: queryKeys.images.definitions(envIds(targets)),
		queryFn: ({ signal }): Promise<EnvList<BuildDefinition>> =>
			acrossEnvironments(all, (env) =>
				allPages((cursor) =>
					unwrap(
						client.GET('/api/v1/environments/{environmentId}/build-definitions', {
							params: {
								path: { environmentId: env.id },
								query: { limit: LIST_LIMIT, cursor }
							},
							signal
						})
					)
				)
			),
		staleTime: 10_000
	});
}

export function buildDefinitionQuery(env: string, id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.images.definition(env, id),
		queryFn: ({ signal }): Promise<BuildDefinition> =>
			unwrap(
				client.GET(
					'/api/v1/environments/{environmentId}/build-definitions/{definitionId}',
					{
						params: { path: { environmentId: env, definitionId: id } },
						signal
					}
				)
			),
		staleTime: 10_000
	});
}

/** Registry connections (#19; owner-administered, secrets never returned). */
export function registriesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.registries.list(),
		queryFn: ({ signal }): Promise<RegistryConnection[]> =>
			allPages((cursor) =>
				unwrap(
					client.GET('/api/v1/registries', {
						params: { query: { limit: LIST_LIMIT, cursor } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

/** Git credentials (#33; they mirror registry connections). */
export function gitCredentialsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.registries.gitList(),
		queryFn: ({ signal }): Promise<GitCredential[]> =>
			allPages((cursor) =>
				unwrap(
					client.GET('/api/v1/git-credentials', {
						params: { query: { limit: LIST_LIMIT, cursor } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

/**
 * Which registry connection an image reference uses. POST /registries/matches
 * is a read: no secret, no side effect.
 */
export function registryMatchQuery(
	imageReference: string,
	opts: { environmentId?: string; stackId?: string; registryId?: string } = {},
	client: ApiClient = api
) {
	const ref = imageReference.trim();
	return queryOptions({
		queryKey: queryKeys.registries.match(
			ref,
			opts.environmentId ?? '',
			opts.stackId ?? '',
			opts.registryId ?? ''
		),
		queryFn: ({ signal }): Promise<RegistryMatch> =>
			unwrap(
				client.POST('/api/v1/registries/matches', {
					body: {
						imageReference: ref,
						environmentId: opts.environmentId || undefined,
						stackId: opts.stackId || undefined,
						registryId: opts.registryId || undefined
					},
					signal
				})
			),
		enabled: ref.length > 0,
		staleTime: 15_000,
		retry: false
	});
}

const MAX_RETRIES = 2;

/**
 * Retry policy: network failures and 5xx/408/429 are retried a bounded
 * number of times; other client errors (400, 401, 403, 404, 409, 412, ...)
 * are final. Mutations are never retried automatically (duplicate Docker
 * operations); they use idempotency keys instead (#4).
 */
export function shouldRetry(failureCount: number, error: unknown): boolean {
	if (failureCount >= MAX_RETRIES) return false;
	if (!(error instanceof ApiRequestError)) return false;
	if (error.status === null) return true;
	return error.status >= 500 || error.status === 408 || error.status === 429;
}

export type QueryOutcome = { ok: true } | { ok: false; error: unknown };

export interface QueryClientHooks {
	/**
	 * Called when a request answered 401 (the session ended or expired).
	 * The session query itself is excluded: it reports "signed out" as data.
	 */
	onUnauthenticated?: (key: QueryKey | undefined) => void;
}

function isUnauthenticated(error: unknown): boolean {
	return error instanceof ApiRequestError && error.status === 401;
}

/**
 * The app-wide QueryClient (one per page load). `observe` receives every
 * query outcome; the root layout feeds it to the connectivity state.
 */
export function createQueryClient(
	observe?: (outcome: QueryOutcome) => void,
	hooks: QueryClientHooks = {}
): QueryClient {
	return new QueryClient({
		queryCache: new QueryCache({
			onSuccess: () => observe?.({ ok: true }),
			onError: (error, query) => {
				observe?.({ ok: false, error });
				if (isUnauthenticated(error)) hooks.onUnauthenticated?.(query.queryKey);
			}
		}),
		mutationCache: new MutationCache({
			onError: (error) => {
				if (isUnauthenticated(error)) hooks.onUnauthenticated?.(undefined);
			}
		}),
		defaultOptions: {
			queries: { retry: shouldRetry, refetchOnWindowFocus: true },
			mutations: { retry: false }
		}
	});
}
