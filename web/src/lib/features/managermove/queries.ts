// Moving Docker Manager to a new server: the move of this manager (owner
// only). The new server's .env carries the move code and an enrollment
// token: it lives only in the component that shows it, never in a query,
// a URL or storage. The move's queries follow the live stream (topic
// manager: the move and the session's move lock); only the new server's
// status page polls (no sign-in there, so no stream).
import { queryOptions } from '@tanstack/svelte-query';
import {
	api,
	ApiRequestError,
	unwrap,
	type ApiClient,
	type Job,
	type Schema
} from '$lib/api/client';
import { withStepUp } from '$lib/auth/stepup.svelte';
import type { JobMatch } from '$lib/features/jobs/active';
import { liveKeys } from '$lib/live/keys';
import {
	MOVE_JOB_KIND,
	type ManagerMove,
	type ManagerMoveDefaults,
	type MoveStatus
} from './model';

export const managerMoveKeys = {
	// Live events of the move (topic manager) refresh both by prefix.
	current: liveKeys.managerMove(),
	defaults: liveKeys.managerMove('defaults'),
	// Public, outside the signed-in data: the new manager's waiting status.
	status: ['move-status'] as const
};

/** How often the new server's status page reads the move (ms; it has no live stream). */
export const WAIT_POLL_MS = 3_000;

/** The running Move everything (manager.move), found again after a reload. */
export const managerMoveJobMatch: JobMatch = { kinds: [MOVE_JOB_KIND] };

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
 * New setup files (owner, step-up): the new server's compose.yaml and .env
 * again, with a new pairing code (the old files stop working) and, unless
 * the new server's agent already enrolled, a new enrollment token. Shown
 * once, like the first ones.
 */
export function createSetupFiles(client: ApiClient = api): Promise<Schema<'CreatedManagerMove'>> {
	return withStepUp(() => unwrap(client.POST('/api/v1/manager/move/setup-files')));
}

/**
 * Whether Docker Manager answers (GET /api/v1/health, not cached): the
 * wait for its restart after a resume. A plain request, so a manager that
 * is down for a moment is not reported as a failure anywhere else.
 */
export async function managerAnswers(signal?: AbortSignal): Promise<boolean> {
	try {
		const res = await fetch('/api/v1/health', {
			cache: 'no-store',
			credentials: 'same-origin',
			signal
		});
		return res.ok;
	} catch {
		return false;
	}
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

/** GET /manager/move/defaults (owner): this server's address and the environment next to it. */
export function managerMoveDefaultsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: managerMoveKeys.defaults,
		queryFn: ({ signal }): Promise<ManagerMoveDefaults> =>
			unwrap(client.GET('/api/v1/manager/move/defaults', { signal })),
		retry: false,
		staleTime: 60_000
	});
}

/**
 * Move everything (owner, step-up): moves the apps, then the new manager
 * gets the handoff. Answers the manager.move job.
 */
export function startMoveRun(client: ApiClient = api): Promise<Job> {
	return withStepUp(() => unwrap(client.POST('/api/v1/manager/move/runs')));
}

/**
 * GET /move/status (public): the waiting mode's status on a new manager;
 * phase none on any other. Read before any sign-in or setup routing: a
 * waiting manager answers every other route 503 manager_move_waiting.
 */
export function moveStatusQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: managerMoveKeys.status,
		queryFn: ({ signal }): Promise<MoveStatus> =>
			unwrap(client.GET('/api/v1/move/status', { signal })),
		retry: false
	});
}
