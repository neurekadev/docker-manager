// Environment migration queries (#35). An environment migration's ID is
// its environment.migrate job's ID, so its record is keyed under that job
// (liveKeys.item('jobs', id, …)): the job's events refresh it while the
// stacks move.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import type { EnvironmentMigration } from './environment-migration';

/** GET /environments/{id}/migrations/{migrationId}: the run's groups and each stack's state. */
export function environmentMigrationQuery(
	environmentId: string,
	migrationId: string,
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: liveKeys.item('jobs', migrationId, 'environment-migration'),
		queryFn: ({ signal }): Promise<EnvironmentMigration> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/migrations/{migrationId}', {
					params: { path: { environmentId, migrationId } },
					signal
				})
			),
		staleTime: 5_000
	});
}
