// Update policies (#20) for Svelte Query. Policies are live topic
// 'policies' (resource type update_policy): a policy change or a finished
// check/run job refreshes these keys.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import { fetchAllPages } from '$lib/features/common/data';
import type { UpdateCandidate, UpdatePolicy } from './model';

export type EnvironmentUpdatePolicy = Schema<'EnvironmentUpdatePolicy'>;
export const environmentUpdateKeys = {
	list: () => liveKeys.list('policies', 'environment-updates'),
	detail: (id: string) => liveKeys.item('policies', 'environment-update', id),
	targets: (id: string) => liveKeys.item('policies', 'environment-update-targets', id)
};

export function environmentUpdatePoliciesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: environmentUpdateKeys.list(),
		queryFn: async ({ signal }): Promise<EnvironmentUpdatePolicy[]> =>
			(await unwrap(client.GET('/api/v1/environment-update-policies', { signal }))).items
	});
}

export function environmentUpdatePolicyQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: environmentUpdateKeys.detail(id),
		queryFn: ({ signal }): Promise<EnvironmentUpdatePolicy> =>
			unwrap(
				client.GET('/api/v1/environment-update-policies/{policyId}', {
					params: { path: { policyId: id } },
					signal
				})
			)
	});
}

export const updateKeys = {
	list: (environmentId: string | null) =>
		liveKeys.list('policies', 'updates', environmentId ?? ''),
	detail: (id: string) => liveKeys.item('policies', id),
	candidates: (id: string) => liveKeys.item('policies', id, 'candidates')
};

export function updatePoliciesQuery(environmentId: string | null, client: ApiClient = api) {
	return queryOptions({
		queryKey: updateKeys.list(environmentId),
		queryFn: ({ signal }): Promise<UpdatePolicy[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/update-policies', {
						params: {
							query: { cursor, limit: 200, environmentId: environmentId ?? undefined }
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
		queryKey: updateKeys.detail(id),
		queryFn: ({ signal }): Promise<UpdatePolicy> =>
			unwrap(
				client.GET('/api/v1/update-policies/{policyId}', {
					params: { path: { policyId: id } },
					signal
				})
			)
	});
}

export function candidatesQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: updateKeys.candidates(id),
		queryFn: async ({ signal }): Promise<UpdateCandidate[]> =>
			(
				await unwrap(
					client.GET('/api/v1/update-policies/{policyId}/candidates', {
						params: { path: { policyId: id } },
						signal
					})
				)
			).items
	});
}
