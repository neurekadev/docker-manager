// The update settings and the records of their targets (#20, #240) for
// Svelte Query: live topic 'policies' (resource type update_policy); a
// settings change or a finished check/run job refreshes these keys.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import { fetchAllPages } from '$lib/features/common/data';
import type { UpdateCandidate, UpdatePolicy } from './model';

export type UpdateSettings = Schema<'UpdateSettings'>;
export type UpdateSettingsTarget = Schema<'UpdateSettingsTarget'>;

export const updateSettingsKeys = {
	settings: liveKeys.list('policies', 'update-settings'),
	targets: liveKeys.list('policies', 'update-targets')
};

/** The update settings (update_policy.read on all environments). */
export function updateSettingsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: updateSettingsKeys.settings,
		queryFn: ({ signal }): Promise<UpdateSettings> =>
			unwrap(client.GET('/api/v1/update-settings', { signal }))
	});
}

/** Every target record of the update settings, covered or not. */
export function updateTargetsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: updateSettingsKeys.targets,
		queryFn: async ({ signal }): Promise<UpdateSettingsTarget[]> =>
			(await unwrap(client.GET('/api/v1/update-settings/targets', { signal }))).items
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
