// Moving Docker Manager to a new server: the move of this manager (owner
// only). The new server's .env carries the move code and an enrollment
// token: it lives only in the component that shows it, never in a query,
// a URL or storage.
import { queryOptions } from '@tanstack/svelte-query';
import { api, ApiRequestError, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { withStepUp } from '$lib/auth/stepup.svelte';
import { liveKeys } from '$lib/live/keys';
import type { ManagerMove } from './model';

export const managerMoveKeys = {
	// No live event announces moves: the move pages poll while it changes.
	current: liveKeys.item('settings', 'manager-move')
};

/**
 * GET /manager/move (owner): the open or in-progress move, else the move
 * this manager arrived by (Move complete). null when there is none (404).
 */
export function managerMoveQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: managerMoveKeys.current,
		queryFn: async ({ signal }): Promise<ManagerMove | null> => {
			try {
				return await unwrap(client.GET('/api/v1/manager/move', { signal }));
			} catch (e) {
				if (e instanceof ApiRequestError && e.status === 404) return null;
				throw e;
			}
		},
		retry: false
	});
}

/**
 * Creates a move (owner, step-up): the new server's compose.yaml and .env,
 * shown once.
 */
export function createMove(
	body: Schema<'CreateManagerMoveInputBody'>,
	client: ApiClient = api
): Promise<Schema<'CreatedManagerMove'>> {
	return withStepUp(() => unwrap(client.POST('/api/v1/manager/moves', { body })));
}

/**
 * Ends the move (owner, step-up). After the handoff only with resumeHere
 * and the typed instance name.
 */
export function cancelMove(
	body: Schema<'CancelManagerMoveInputBody'> = {},
	client: ApiClient = api
): Promise<ManagerMove> {
	return withStepUp(() => unwrap(client.POST('/api/v1/manager/move/cancellations', { body })));
}

/**
 * States that the old manager is stopped or no longer uses this instance
 * although it never confirmed the move (owner, step-up): the confirmation
 * stops and counts as done.
 */
export function acknowledgeConfirmation(client: ApiClient = api): Promise<ManagerMove> {
	return withStepUp(() => unwrap(client.POST('/api/v1/manager/move/acknowledgements')));
}
