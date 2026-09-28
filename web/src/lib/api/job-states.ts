// Job states (#26): the one list of the states of a job that has not ended
// (views show a progress bar for it, the jobs list's "In progress" filter
// and the running-jobs query use it) and of the terminal ones. Pure.
import type { Job } from './client';

/** States of a job that has not ended yet. */
export const ACTIVE_JOB_STATES = [
	'queued',
	'blocked',
	'dispatched',
	'running',
	'cancelling'
] as const satisfies readonly Job['state'][];

/** States of a job that ended. */
export const TERMINAL_STATES = [
	'succeeded',
	'failed',
	'partial',
	'cancelled',
	'interrupted'
] as const satisfies readonly Job['state'][];

/** The job has not ended (queued, blocked, dispatched, running, cancelling). */
export function isActiveJobState(state: string | undefined): boolean {
	return !!state && (ACTIVE_JOB_STATES as readonly string[]).includes(state);
}

/** The job ended (succeeded, failed, partial, cancelled, interrupted). */
export function isTerminal(state: string | undefined): boolean {
	return !!state && (TERMINAL_STATES as readonly string[]).includes(state);
}
