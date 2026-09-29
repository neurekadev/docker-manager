// Environment migration queries (#35). An environment migration's ID is
// its environment.migrate job's ID, so its record is keyed under that job
// (liveKeys.item('jobs', id, …)): the job's events refresh it while the
// stacks move. The environment's list of migrations is keyed under the
// jobs list (liveKeys.list('jobs', …)): every job event refreshes it
// (throttled like any list), so the migration's own job, its stack
// migrations and the removals of the old copies ending all show without a
// reload.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import type { EnvironmentMigration } from './environment-migration';

export const environmentMigrationKeys = {
	list: (environmentId: string) => liveKeys.list('jobs', 'environment-migrations', environmentId),
	record: (migrationId: string) => liveKeys.item('jobs', migrationId, 'environment-migration')
};

/** GET /environments/{id}/migrations/{migrationId}: the run's groups and each stack's state. */
export function environmentMigrationQuery(
	environmentId: string,
	migrationId: string,
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: environmentMigrationKeys.record(migrationId),
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

/**
 * GET /environments/{id}/migrations: the latest migrations away from the
 * environment (newest first, at most 20), each with the stacks the caller
 * can see. 403 for callers who may migrate none of their stacks.
 */
export function environmentMigrationsQuery(environmentId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: environmentMigrationKeys.list(environmentId),
		queryFn: async ({ signal }): Promise<EnvironmentMigration[]> =>
			(
				await unwrap(
					client.GET('/api/v1/environments/{environmentId}/migrations', {
						params: { path: { environmentId } },
						signal
					})
				)
			).items,
		staleTime: 5_000
	});
}
