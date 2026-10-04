// In-app notices (#22 bell, #25 Q6, #159): what needs the user's eye until
// they dismiss it. The manager's built-in In App channel chooses what of
// its events the bell shows (its kinds, outcomes and environments; the
// manager filters the lists with inApp): the alerts that fire and nobody
// dismissed (disks and RAID with problems, hosts running hot or low on
// disk space or memory, environments offline, failed scheduled jobs,
// available updates; setAlerts), alerts resolved lately (setResolved) and
// finished backups, restores, prunes and update runs (setRuns). This
// tab's notices of the user's own jobs come on top (push: a job they
// started finished or failed); a run's notification replaces the notice
// of its job (same key), and a run whose failure shows as an alert is
// shown once, as the alert. The badge counts every item not dismissed and
// stays until each is dismissed. A server alert the user may dismiss is
// dismissed for everyone (through the API, by the bell); the others are
// dismissed for this browser: their keys (`job:<id>`,
// `alert:<id>:<escalation>`: the manager counts up an alert's escalation
// whenever it gets worse, at a higher severity or with a new problem, so
// it shows again; `alert:<id>:resolved`) are kept in localStorage, at
// most MAX_DISMISSED, UI state only (never API data). Keys deduplicate:
// pushing the same key again updates the notice instead of adding one.

import { severityLabel, severityTone, sortAlerts, type Alert } from '$lib/features/alerts/model';
import type { Notification } from '$lib/features/notification-history/model';
import { eventKind } from '$lib/features/notifications/model';
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
	/** How bad or how it went, in the tone's color ("Critical", "Backups · Success"). */
	label?: string;
}

/** A server alert as the bell needs it. */
export type BellAlert = Pick<
	Alert,
	'id' | 'severity' | 'escalation' | 'title' | 'detail' | 'link' | 'startedAt' | 'actions'
> &
	Partial<Pick<Alert, 'facts' | 'resolvedAt'>>;

/** A finished run (a notification) as the bell needs it. */
export type BellRun = Pick<
	Notification,
	'id' | 'kind' | 'outcome' | 'jobId' | 'title' | 'detail' | 'link' | 'createdAt'
>;

/** One line of the bell: a server alert, a finished run or a job notice. */
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
/** The newest dismissed keys kept (more than the bell can list at once). */
export const MAX_DISMISSED = 500;

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

/** The key of a resolved alert dismissed for this browser. */
export function resolvedDismissKey(a: Pick<Alert, 'id'>): string {
	return `alert:${a.id}:resolved`;
}

const RUN_TONES: Record<string, NoticeTone> = { success: 'ok', warning: 'warn', failure: 'danger' };

/** A run's status line, as its message has it: "Backups · Success", "Image Updates · Applied". */
export function runLabel(r: Pick<BellRun, 'kind' | 'outcome'>): string {
	const k = eventKind(r.kind);
	const o = k?.outcomes.find((x) => x.outcome === r.outcome)?.label ?? r.outcome;
	return k ? `${k.label} · ${o}` : o;
}

/**
 * The bell's items: the active alerts (critical first, then the newest),
 * then the resolved alerts, finished runs and job notices (newest first),
 * without the dismissed ones. A run replaces the job notice of its job; a
 * run whose job an alert names is left to the alert.
 */
export function bellItems(
	alerts: readonly BellAlert[],
	notices: readonly AppNotice[],
	dismissedKeys: readonly string[],
	resolved: readonly BellAlert[] = [],
	runs: readonly BellRun[] = []
): BellItem[] {
	// Plain lookups, rebuilt with each list.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	const dismissed = new Set(dismissedKeys);
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	const alertJobs = new Set(
		[...alerts, ...resolved].map((a) => a.facts?.jobId).filter((id): id is string => !!id)
	);
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
			label: severityLabel(a.severity),
			dismissKey,
			alert: a,
			serverDismiss: a.actions.includes('alert.dismiss')
		});
	}
	const rest: BellItem[] = [];
	for (const a of resolved) {
		const dismissKey = resolvedDismissKey(a);
		if (dismissed.has(dismissKey)) continue;
		rest.push({
			key: `resolved:${a.id}`,
			kind: 'alert',
			tone: 'ok',
			title: `Resolved: ${a.title}`,
			href: a.link,
			at: Date.parse(a.resolvedAt ?? a.startedAt),
			label: 'Resolved',
			dismissKey,
			serverDismiss: false
		});
	}
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	const runKeys = new Set<string>();
	for (const r of runs) {
		const key = `job:${r.jobId}`;
		if (alertJobs.has(r.jobId) || runKeys.has(key)) continue;
		runKeys.add(key);
		if (dismissed.has(key)) continue;
		rest.push({
			key,
			kind: 'job',
			tone: RUN_TONES[r.outcome] ?? 'info',
			title: r.title,
			body: r.detail,
			href: r.link,
			at: Date.parse(r.createdAt),
			label: runLabel(r),
			dismissKey: key,
			serverDismiss: false
		});
	}
	for (const n of notices) {
		if (dismissed.has(n.key) || runKeys.has(n.key)) continue;
		rest.push({ ...n, dismissKey: n.key, serverDismiss: false });
	}
	rest.sort((x, y) => (y.at || 0) - (x.at || 0));
	return [...out, ...rest];
}

export class Notices {
	/** This tab's job notices, newest first. */
	items = $state<AppNotice[]>([]);
	/** The server's active alerts the In App channel shows (setAlerts). */
	alerts = $state<BellAlert[]>([]);
	/** Alerts resolved lately that it shows (setResolved). */
	resolved = $state<BellAlert[]>([]);
	/** Finished runs it shows (setRuns). */
	runs = $state<BellRun[]>([]);
	#dismissed = $state<string[]>([]);
	/** What the bell lists: everything not dismissed. */
	readonly list = $derived(
		bellItems(this.alerts, this.items, this.#dismissed, this.resolved, this.runs)
	);
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

	/** The alerts resolved lately (replaces the previous list). */
	setResolved(alerts: readonly BellAlert[]) {
		this.resolved = [...alerts];
	}

	/** The finished runs (replaces the previous list). */
	setRuns(runs: readonly BellRun[]) {
		this.runs = [...runs];
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

	/** Forgets this tab's notices, alerts and runs (sign-out); dismissals stay. */
	clear() {
		this.items = [];
		this.alerts = [];
		this.resolved = [];
		this.runs = [];
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
