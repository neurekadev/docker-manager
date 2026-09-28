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

/** Where a notice leads: its own link, else the page of its kind. */
export function noticeHref(n: Pick<AppNotice, 'kind' | 'href'>): string {
	if (n.href) return n.href;
	switch (n.kind) {
		case 'job':
			return routes.jobs();
		case 'environment':
			return routes.environments();
		default:
			return routes.updates();
	}
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

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
					body: 'Its agent is not connected. Docker Manager shows its last known state.',
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
			// Opaque IDs (stacks, policies) are left out rather than shown raw.
			const id = j.targets?.[0]?.id;
			const what = id && !UUID.test(id) ? id : '';
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
	environmentId?: string;
	/** What the policy updates (a stack ID or a container name). */
	target?: { type: string; id: string };
	/** The target's name as users know it (the manager resolves it). */
	targetName?: string;
	summary?: { available: number };
}

/**
 * Earlier manager versions named the per-target policies of an
 * environment-wide policy "Automatic update <id>"; the manager renames them
 * after their target ("Automatic updates for zerobyte") the next time it
 * reconciles the policy, so only records not reconciled yet match.
 */
export function isGeneratedPolicyName(p: Pick<NoticePolicy, 'id' | 'name'>): boolean {
	return (
		p.name === `Automatic update ${p.id}` || /^Automatic update [0-9a-f-]{8,}$/i.test(p.name)
	);
}

/**
 * A policy as the user knows it: what it updates (the manager's
 * `targetName`), else its name. An old ID-based name without a target
 * name falls back to `nameOf` or the kind of target.
 */
export function policyLabel(
	p: NoticePolicy,
	nameOf?: (target: { type: string; id: string }) => string | undefined
): string {
	if (p.targetName) return p.targetName;
	if (!isGeneratedPolicyName(p)) return p.name;
	const t = p.target;
	const named = t ? nameOf?.(t) : undefined;
	if (named) return named;
	if (t?.type === 'container' && !UUID.test(t.id)) return `container ${t.id}`;
	return t?.type === 'stack' ? 'a stack' : 'a container';
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** "6 stacks have updates available", "2 stacks and 1 container have …". */
function summaryTitle(policies: readonly NoticePolicy[]): string {
	const stacks = policies.filter((p) => p.target?.type === 'stack').length;
	const containers = policies.filter((p) => p.target?.type === 'container').length;
	const other = policies.length - stacks - containers;
	const parts = [
		stacks ? plural(stacks, 'stack', 'stacks') : '',
		containers ? plural(containers, 'container', 'containers') : '',
		other ? plural(other, 'update policy', 'update policies') : ''
	].filter(Boolean);
	const what =
		parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}` : parts[0];
	return `${what} ${policies.length === 1 ? 'has' : 'have'} updates available`;
}

/** "Silo, Media and 4 more." */
function namesBody(names: string[]): string {
	const shown = names.slice(0, 3);
	const rest = names.length - shown.length;
	if (rest > 0) return `${shown.join(', ')} and ${rest} more.`;
	return shown.length > 1
		? `${shown.slice(0, -1).join(', ')} and ${shown.at(-1)}.`
		: `${shown[0] ?? ''}.`;
}

/**
 * "Updates available" notices from the update policies' summaries (#20).
 * One policy with updates gets its own notice ("2 updates available for
 * Silo images", linking to the policy); several collapse into one ("6
 * stacks have updates available", naming them, linking to Updates), so a
 * check of many targets does not flood the bell. Policies are named as the
 * user knows them (policyLabel: the target's name; `nameOf` resolves stack
 * IDs of old records). A notice is
 * pushed again only when its text changes, and resolved when no update is
 * left.
 */
export function updateNotices(
	target: Notices = notices,
	nameOf?: (target: { type: string; id: string }) => string | undefined
) {
	const SUMMARY = 'update:summary';
	let shownKey: string | null = null;
	let shownText = '';
	return (policies: readonly NoticePolicy[]) => {
		// One entry per updated target (an environment policy and its
		// per-target policy count once).
		// eslint-disable-next-line svelte/prefer-svelte-reactivity
		const byTarget = new Map<string, NoticePolicy>();
		for (const p of policies) {
			if ((p.summary?.available ?? 0) <= 0) continue;
			const k = p.target ? `${p.environmentId ?? ''}/${p.target.type}/${p.target.id}` : p.id;
			if (!byTarget.has(k)) byTarget.set(k, p);
		}
		const pending = [...byTarget.values()];
		let next: Omit<AppNotice, 'at' | 'read'> | null = null;
		if (pending.length === 1) {
			const p = pending[0];
			const n = p.summary?.available ?? 0;
			next = {
				key: `update:${p.id}`,
				kind: 'update',
				tone: 'warn',
				title: `${plural(n, 'update', 'updates')} available for ${policyLabel(p, nameOf)}`,
				body: 'Review the update preview before applying it.',
				href: routes.updatePolicy(p.id)
			};
		} else if (pending.length > 1) {
			next = {
				key: SUMMARY,
				kind: 'update',
				tone: 'warn',
				title: summaryTitle(pending),
				body: namesBody(pending.map((p) => policyLabel(p, nameOf))),
				href: routes.updates()
			};
		}
		if (shownKey && shownKey !== next?.key) target.resolve(shownKey);
		if (!next) {
			shownKey = null;
			shownText = '';
			return;
		}
		const text = `${next.title}|${next.body}`;
		if (next.key !== shownKey || text !== shownText) target.push(next);
		shownKey = next.key;
		shownText = text;
	};
}
