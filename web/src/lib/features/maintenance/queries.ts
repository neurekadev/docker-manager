// The maintenance settings (#14, #238) for Svelte Query (live topic
// 'policies': maintenance_policy changes).
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import type { MaintenanceSettings } from './model';

export const maintenanceKeys = {
	settings: liveKeys.list('policies', 'maintenance')
};

export function maintenanceSettingsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: maintenanceKeys.settings,
		queryFn: ({ signal }): Promise<MaintenanceSettings> =>
			unwrap(client.GET('/api/v1/maintenance-settings', { signal })),
		staleTime: 15_000
	});
}
