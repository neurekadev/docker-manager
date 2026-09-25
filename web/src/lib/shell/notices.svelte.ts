// In-app notices (#22 bell, #25 Q6: in-app only in v1): job finished or
// failed, update available, environment offline. Sources push notices; the
// bell shows the unread count and the list. Notices live in memory for the
// tab only (no persistence of API data). Keys deduplicate: pushing the same
// key again updates the notice instead of adding one.

import { routes } from '$lib/routes';

export type NoticeKind = 'job' | 'environment' | 'update';
export type NoticeTone = 'ok' | 'warn' | 'danger' | 'info';

export interface AppNotice {
	key: string;
	kind: NoticeKind;
	tone: NoticeTone;
	title: string;
	body?: string;
	href?: string;
	at: number;
	read: boolean;
}

export class Notices {
	items = $state<AppNotice[]>([]);
	readonly unread = $derived(this.items.filter((n) => !n.read).length);
	/** Newest first, at most this many. */
	readonly max = 50;
	#now: () => number;

	constructor(now: () => number = () => Date.now()) {
		this.#now = now;
	}

	push(n: Omit<AppNotice, 'at' | 'read'> & { at?: number }) {
		const notice: AppNotice = { ...n, at: n.at ?? this.#now(), read: false };
		this.items = [notice, ...this.items.filter((x) => x.key !== n.key)].slice(0, this.max);
	}

	/** Removes a notice whose condition ended (environment back online). */
	resolve(key: string) {
		this.items = this.items.filter((n) => n.key !== key);
	}

	markAllRead() {
		if (this.unread) this.items = this.items.map((n) => (n.read ? n : { ...n, read: true }));
	}

	clear() {
		this.items = [];
	}
}

export const notices = new Notices();

/**
 * Tracks environment online/offline transitions from successive
 * environment lists and pushes/resolves "offline" notices. Returns the
 * function to feed with each new list.
 */
export function environmentNotices(target: Notices = notices) {
	let seen: Map<string, boolean> | null = null;
	return (envs: readonly { id: string; name: string; online: boolean }[]) => {
		const first = seen === null;
		// Plain bookkeeping between calls, not reactive state.
		// eslint-disable-next-line svelte/prefer-svelte-reactivity
		const next = new Map(envs.map((e) => [e.id, e.online]));
		for (const e of envs) {
			const was = seen?.get(e.id);
			const key = `environment-offline:${e.id}`;
			if (!e.online && (first || was !== false)) {
				target.push({
					key,
					kind: 'environment',
					tone: 'warn',
					title: `${e.name} is offline`,
					body: 'Its agent is not connected. DockYard shows its last known state.',
					href: routes.environment(e.id)
				});
			} else if (e.online && was === false) {
				target.resolve(key);
			}
		}
		seen = next;
	};
}

/** The parts of a job the job notices need. */
export interface NoticeJob {
	id: string;
	kind: string;
	state: string;
	createdAt: string;
	initiatorUserId?: string;
	error?: { recovery: string };
	targets?: { type: string; id: string }[];
}

const TERMINAL = new Set(['succeeded', 'failed', 'partial', 'cancelled', 'interrupted']);
const JOB_TONES: Record<string, NoticeTone> = {
	succeeded: 'ok',
	partial: 'warn',
	failed: 'danger',
	interrupted: 'danger',
	cancelled: 'info'
};
const JOB_OUTCOMES: Record<string, string> = {
	succeeded: 'succeeded',
	partial: 'partly failed',
	failed: 'failed',
	interrupted: 'was interrupted',
	cancelled: 'was cancelled'
};

/**
 * Tracks job states from successive lists of recent jobs (refreshed by
 * live job events) and pushes a notice when a job finishes: the user's
 * own jobs whatever the outcome, anyone's (also scheduled) failures. Jobs
 * already finished when the tab first saw them are not announced. Keys
 * match JobProgress's (`job:<id>`), so a watched job is announced once.
 */
export function jobNotices(
	userId: () => string,
	label: (kind: string) => string,
	target: Notices = notices
) {
	let seen: Map<string, string> | null = null;
	let since = '';
	return (jobs: readonly NoticeJob[]) => {
		const first = seen === null;
		// Plain bookkeeping between calls, not reactive state.
		// eslint-disable-next-line svelte/prefer-svelte-reactivity
		const next = new Map(jobs.map((j) => [j.id, j.state]));
		if (first) since = jobs.reduce((m, j) => (j.createdAt > m ? j.createdAt : m), '');
		for (const j of jobs) {
			if (first || !TERMINAL.has(j.state)) continue;
			const was = seen?.get(j.id);
			// Finished since the last list: it was active, or it is new (a fast job).
			const finishedNow = was !== undefined ? !TERMINAL.has(was) : j.createdAt > since;
			if (!finishedNow) continue;
			const me = userId();
			const mine = !!me && j.initiatorUserId === me;
			const problem = j.state !== 'succeeded' && j.state !== 'cancelled';
			if (!mine && !problem) continue;
			const what = j.targets?.[0]?.id;
			target.push({
				key: `job:${j.id}`,
				kind: 'job',
				tone: JOB_TONES[j.state] ?? 'info',
				title: `${label(j.kind)}${what ? ` ${what}` : ''} ${JOB_OUTCOMES[j.state] ?? j.state}`,
				body: problem ? j.error?.recovery : undefined,
				href: routes.job(j.id)
			});
		}
		seen = next;
	};
}

/** The parts of an update policy the update notices need. */
export interface NoticePolicy {
	id: string;
	name: string;
	summary?: { available: number };
}

/**
 * "Updates available" notices from the update policies' summaries (#20):
 * one per policy with available updates, resolved when none are left.
 */
export function updateNotices(target: Notices = notices) {
	// Plain bookkeeping between calls, not reactive state.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	const shown = new Map<string, number>();
	return (policies: readonly NoticePolicy[]) => {
		// eslint-disable-next-line svelte/prefer-svelte-reactivity
		const present = new Set<string>();
		for (const p of policies) {
			const n = p.summary?.available ?? 0;
			const key = `update:${p.id}`;
			present.add(key);
			if (n > 0 && shown.get(key) !== n) {
				target.push({
					key,
					kind: 'update',
					tone: 'warn',
					title: `${n} ${n === 1 ? 'update' : 'updates'} available for ${p.name}`,
					body: 'Review the update preview before applying it.',
					href: routes.updatePolicy(p.id)
				});
				shown.set(key, n);
			} else if (n === 0 && shown.has(key)) {
				target.resolve(key);
				shown.delete(key);
			}
		}
		for (const key of [...shown.keys()])
			if (!present.has(key)) {
				target.resolve(key);
				shown.delete(key);
			}
	};
}
