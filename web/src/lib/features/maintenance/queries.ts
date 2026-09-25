// Maintenance policies and defaults (#14) for Svelte Query (live topic
// 'policies': maintenance_policy and maintenance_default changes).
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import { fetchAllPages } from '$lib/features/common/data';
import type { MaintenanceDefaults, MaintenancePolicy } from './model';

export const maintenanceKeys = {
	list: (environmentId: string | null) =>
		liveKeys.list('policies', 'maintenance', environmentId ?? ''),
	detail: (id: string) => liveKeys.item('policies', id),
	defaults: liveKeys.item('policies', 'maintenance-defaults')
};

export function maintenancePoliciesQuery(environmentId: string | null, client: ApiClient = api) {
	return queryOptions({
		queryKey: maintenanceKeys.list(environmentId),
		queryFn: ({ signal }): Promise<MaintenancePolicy[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/maintenance-policies', {
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

export function maintenancePolicyQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: maintenanceKeys.detail(id),
		queryFn: ({ signal }): Promise<MaintenancePolicy> =>
			unwrap(
				client.GET('/api/v1/maintenance-policies/{policyId}', {
					params: { path: { policyId: id } },
					signal
				})
			)
	});
}

export function maintenanceDefaultsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: maintenanceKeys.defaults,
		queryFn: ({ signal }): Promise<MaintenanceDefaults> =>
			unwrap(client.GET('/api/v1/maintenance-defaults', { signal })),
		staleTime: 60_000,
		retry: false
	});
}
