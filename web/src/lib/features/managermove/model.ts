// Moving Docker Manager to a new server (docs/internal/architecture/manager-move.md):
// the words of the move's states on the old manager and the shell banner
// of a locked manager. Pure (model.spec.ts). The move's pages (Settings →
// Move to a new server, the new server's status page, Move complete) are
// rebuilt for the new flow; their words go here first.
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui';

export type ManagerMove = Schema<'ManagerMove'>;
export type MoveState = ManagerMove['state'];
/** The move lock as every signed-in user reads it (GET /auth/session). */
export type MoveLock = Schema<'SessionManagerMove'>['state'];

/** The address shown when neither the settings nor the browser know one. */
export const EXAMPLE_ADDRESS = 'https://docker.example.com';

/**
 * This Docker Manager's address: its public URL when the settings name it,
 * else the address the browser opened it at.
 */
export function thisAddress(publicUrl?: string | null, origin?: string | null): string {
	return publicUrl || origin || EXAMPLE_ADDRESS;
}

/** States that change on their own: the move pages poll while one holds. */
export const POLLED_STATES: readonly MoveState[] = [
	'open',
	'moving',
	'ready',
	'draining',
	'handed_off'
];

/** States in which this manager is read-only (every change answers manager_moved). */
export const LOCKED_STATES: readonly MoveState[] = ['draining', 'handed_off', 'confirmed'];

export function isPolled(state: MoveState | undefined): boolean {
	return !!state && POLLED_STATES.includes(state);
}

export function isLocked(state: MoveState | undefined): boolean {
	return !!state && LOCKED_STATES.includes(state);
}

/** How the old manager shows its move. */
export interface MoveStatusView {
	/** The badge text. */
	label: string;
	tone: BadgeTone;
	pulse: boolean;
	title: string;
	/** cancel: "Cancel move" (until the handoff); resume: "Resume on this server" (handed off). */
	stop: 'cancel' | 'resume' | null;
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** The old manager's move in words. */
export function moveStatus(move: ManagerMove): MoveStatusView {
	switch (move.state) {
		case 'open':
			return {
				label: 'Waiting for the new server',
				tone: 'info',
				pulse: true,
				title: 'Waiting for the new server',
				stop: 'cancel'
			};
		case 'moving':
			return {
				label: 'Moving apps',
				tone: 'info',
				pulse: true,
				title: 'Moving your apps to the new server',
				stop: 'cancel'
			};
		case 'ready':
			return {
				label: 'Ready',
				tone: 'info',
				pulse: true,
				title: 'Your apps moved: handing Docker Manager over',
				stop: 'cancel'
			};
		case 'draining':
			return {
				label: 'Locked',
				tone: 'warn',
				pulse: true,
				title:
					move.jobsRunning > 0
						? `Locked: finishing ${plural(move.jobsRunning, 'running job', 'running jobs')}`
						: 'Locked: handing over',
				stop: 'cancel'
			};
		case 'handed_off':
			return {
				label: 'Handed over',
				tone: 'info',
				pulse: true,
				title: 'Handed over: waiting for the new Docker Manager to confirm',
				stop: 'resume'
			};
		case 'confirmed':
			return { label: 'Moved', tone: 'ok', pulse: false, title: 'Moved', stop: null };
		case 'cancelled':
			return {
				label: 'Cancelled',
				tone: 'neutral',
				pulse: false,
				title: 'The move was cancelled',
				stop: null
			};
		case 'expired':
			return {
				label: 'Expired',
				tone: 'neutral',
				pulse: false,
				title: 'The move expired',
				stop: null
			};
		case 'arrived':
			return {
				label: 'Arrived',
				tone: 'ok',
				pulse: false,
				title: 'Docker Manager moved here',
				stop: null
			};
	}
}

/** The shell's banner while this manager is read-only because it moves. */
export interface MoveBanner {
	title: string;
	body: string;
}

/** The lock of the owner's move state (the session says it for everyone). */
export function lockOf(state: MoveState | undefined): MoveLock | undefined {
	if (state === undefined) return undefined;
	if (state === 'confirmed') return 'moved';
	if (state === 'draining' || state === 'handed_off') return 'moving';
	return 'none';
}

/**
 * The banner of a locked manager: from the lock every session reads (the
 * owner's polled move state first) or, as a fallback, from a change the
 * manager refused with manager_moved. Null while nothing is locked.
 */
export function moveBanner(
	lock: MoveLock | undefined,
	refused: boolean,
	address: string
): MoveBanner | null {
	if (lock === 'moved')
		return {
			title: 'This Docker Manager moved to a new server',
			body: `Nothing can be changed here. Use ${address} once it leads to the new server.`
		};
	if (lock === 'moving' || refused)
		return {
			title: 'This Docker Manager is moving to a new server',
			body: 'Nothing can be changed here. Your apps keep running.'
		};
	return null;
}
