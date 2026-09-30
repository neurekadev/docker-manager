// In-app notices (#22 bell, #25 Q6, #159): what needs the user's eye until
// they dismiss it. Two sources feed the bell: the manager's alerts that
// fire and nobody dismissed (disks and RAID with problems, environments
// offline, failed scheduled jobs, available updates; setAlerts, from the
// active alerts query), and this tab's notices of the user's own jobs
// (push: a job they started finished or failed). The badge counts every
// item not dismissed and stays until each is dismissed. A server alert the
// user may dismiss is dismissed for everyone (through the API, by the
// bell); the others and the job notices are dismissed for this browser:
// their keys (`job:<id>`, `alert:<id>:<escalation>`: the manager counts
// up an alert's escalation whenever it gets worse, at a higher severity or
// with a new problem, so it shows again) are kept in localStorage, at most MAX_DISMISSED, UI
// state only (never API data). Keys deduplicate: pushing the same key
// again updates the notice instead of adding one.

import { severityTone, sortAlerts, type Alert } from '$lib/features/alerts/model';
import { routes } from '$lib/routes';

export type NoticeKind = 'job' | 'alert';
export type NoticeTone = 'ok' | 'warn' | 'danger' | 'info';

export interface AppNotice {
	key: string;
	kind: NoticeKind;
	tone: NoticeTone;
	title: string;
	body?: string;
	href?: string;
	at: number;
}

/** A server alert as the bell needs it. */
export type BellAlert = Pick<
	Alert,
	'id' | 'severity' | 'escalation' | 'title' | 'detail' | 'link' | 'startedAt' | 'actions'
>;

/** One line of the bell: a server alert or a job notice. */
export interface BellItem extends AppNotice {
	/** The key a dismissal for this browser keeps. */
	dismissKey: string;
	/** The server alert it shows. */
	alert?: BellAlert;
	/** The user may dismiss the alert for everyone (alert.dismiss). */
	serverDismiss: boolean;
}

/** Where the dismissed keys are kept (localStorage, per browser). */
export const DISMISSED_KEY = 'docker-manager:dismissed-notices';
/** The newest dismissed keys kept. */
export const MAX_DISMISSED = 200;

const KEY_RE = /^(job|alert):[\w.:-]{1,160}$/;

/** The key of an alert dismissed for this browser: it shows again once the alert gets worse (a new escalation). */
export function alertDismissKey(a: Pick<Alert, 'id' | 'escalation'>): string {
	return `alert:${a.id}:${a.escalation}`;
}

/** Parses the stored dismissed keys; anything unexpected is dropped. */
export function parseDismissed(raw: string | null | undefined): string[] {
	if (!raw) return [];
	let data: unknown;
	try {
		data = JSON.parse(raw);
	} catch {
		return [];
	}
	if (!Array.isArray(data)) return [];
	return data
		.filter((k): k is string => typeof k === 'string' && KEY_RE.test(k))
		.slice(-MAX_DISMISSED);
}

export interface StorageLike {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
}

function browserStorage(): StorageLike | null {
	try {
		return typeof window === 'undefined' ? null : window.localStorage;
	} catch {
		return null; // storage disabled
	}
}

/**
 * The bell's items: the alerts (critical first, then the newest), then the
 * job notices (newest first), without the dismissed ones.
 */
export function bellItems(
	alerts: readonly BellAlert[],
	notices: readonly AppNotice[],
	dismissedKeys: readonly string[]
): BellItem[] {
	// A plain lookup, rebuilt with each list.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	const dismissed = new Set(dismissedKeys);
	const out: BellItem[] = [];
	for (const a of sortAlerts(alerts)) {
		const dismissKey = alertDismissKey(a);
		if (dismissed.has(dismissKey)) continue;
		out.push({
			key: `alert:${a.id}`,
			kind: 'alert',
			tone: severityTone(a.severity),
			title: a.title,
			body: a.detail,
			href: a.link,
			at: Date.parse(a.startedAt),
			dismissKey,
			alert: a,
			serverDismiss: a.actions.includes('alert.dismiss')
		});
	}
	for (const n of notices) {
		if (dismissed.has(n.key)) continue;
		out.push({ ...n, dismissKey: n.key, serverDismiss: false });
	}
	return out;
}

export class Notices {
	/** This tab's job notices, newest first. */
	items = $state<AppNotice[]>([]);
	/** The server's active alerts (setAlerts). */
	alerts = $state<BellAlert[]>([]);
	#dismissed = $state<string[]>([]);
	/** What the bell lists: everything not dismissed. */
	readonly list = $derived(bellItems(this.alerts, this.items, this.#dismissed));
	/** The badge: items not dismissed. */
	readonly count = $derived(this.list.length);
	/** Newest first, at most this many job notices. */
	readonly max = 50;
	#now: () => number;
	#storage: StorageLike | null;

	constructor(
		now: () => number = () => Date.now(),
		storage: StorageLike | null = browserStorage()
	) {
		this.#now = now;
		this.#storage = storage;
		this.#dismissed = this.#read();
	}

	push(n: Omit<AppNotice, 'at'> & { at?: number }) {
		const notice: AppNotice = { ...n, at: n.at ?? this.#now() };
		this.items = [notice, ...this.items.filter((x) => x.key !== n.key)].slice(0, this.max);
	}

	/** Removes a notice whose condition ended. */
	resolve(key: string) {
		this.items = this.items.filter((n) => n.key !== key);
	}

	/** The active alerts from the server (replaces the previous list). */
	setAlerts(alerts: readonly BellAlert[]) {
		this.alerts = [...alerts];
	}

	/** Drops alerts dismissed for everyone until the next list arrives. */
	forgetAlerts(ids: readonly string[]) {
		this.alerts = this.alerts.filter((a) => !ids.includes(a.id));
	}

	/** Dismisses items for this browser (kept across reloads). */
	dismiss(...keys: string[]) {
		const add = keys.filter((k) => KEY_RE.test(k));
		if (!add.length) return;
		this.#dismissed = [...this.#dismissed.filter((k) => !add.includes(k)), ...add].slice(
			-MAX_DISMISSED
		);
		try {
			this.#storage?.setItem(DISMISSED_KEY, JSON.stringify(this.#dismissed));
		} catch {
			// Quota or access errors: dismissed for this visit only.
		}
	}

	/** Whether a key was dismissed in this browser. */
	isDismissed(key: string): boolean {
		return this.#dismissed.includes(key);
	}

	/** Reads the dismissed keys again (another tab dismissed something). */
	reload() {
		this.#dismissed = this.#read();
	}

	/**
	 * Follows dismissals in other tabs (the storage event); returns the
	 * function that stops.
	 */
	listen(): () => void {
		if (typeof window === 'undefined') return () => {};
		const on = (e: StorageEvent) => {
			if (e.key === DISMISSED_KEY || e.key === null) this.reload();
		};
		window.addEventListener('storage', on);
		return () => window.removeEventListener('storage', on);
	}

	/** Forgets this tab's notices and alerts (sign-out); dismissals stay. */
	clear() {
		this.items = [];
		this.alerts = [];
	}

	#read(): string[] {
		try {
			return parseDismissed(this.#storage?.getItem(DISMISSED_KEY));
		} catch {
			return [];
		}
	}
}

/** Where a notice leads: its own link, else the page of its kind. */
export function noticeHref(n: Pick<AppNotice, 'kind' | 'href'>): string {
	if (n.href) return n.href;
	return n.kind === 'job' ? routes.jobs() : routes.alerts();
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export const notices = new Notices();

/** The parts of a job the job notices need. */
export interface NoticeJob {
	id: string;
	kind: string;
	state: string;
	createdAt: string;
	initiatorUserId?: string;
	/** manual, scheduled or api_token. */
	origin?: string;
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
 * live job events) and pushes a notice when one of the user's own manual
 * jobs finishes, whatever the outcome. Failed scheduled jobs and jobs of
 * API tokens are the manager's alerts (#159); other people's jobs are
 * theirs. Jobs already finished when the tab first saw them are not
 * announced. Keys match JobProgress's (`job:<id>`), so a watched job is
 * announced once.
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
			if (!mine || (j.origin !== undefined && j.origin !== 'manual')) continue;
			const problem = j.state !== 'succeeded' && j.state !== 'cancelled';
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

/** The parts of an update policy its label needs. */
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
