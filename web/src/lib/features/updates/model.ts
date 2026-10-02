// Digest-driven updates (#20): presentation of policies and candidates.
// Pure functions (tested in model.spec.ts); the API decides eligibility.
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';
import { describeCron } from '$lib/ui/cron';
import { formatRelative } from '$lib/ui/format';
import { routes } from '$lib/routes';

export type UpdatePolicy = Schema<'UpdatePolicy'>;
export type UpdateCandidate = Schema<'UpdateCandidate'>;
export type UpdatePreview = Schema<'UpdatePreview'>;
export type UpdateWindow = Schema<'UpdateWindow'>;
export type CandidateStatus = UpdateCandidate['status'];

/**
 * Where a link to a target's record leads (notifications and alerts sent
 * before they linked the environment policy named the record, which has
 * no page): the environment policy that manages it, else Updates.
 */
export function recordPolicyHref(p: Pick<UpdatePolicy, 'id' | 'parentId'>): string {
	return p.parentId && p.parentId !== p.id ? routes.updatePolicy(p.parentId) : routes.updates();
}

export interface Presentation {
	tone: BadgeTone;
	label: string;
}

const STATUS: Record<CandidateStatus, Presentation> = {
	update_available: { tone: 'warn', label: 'Update Available' },
	up_to_date: { tone: 'ok', label: 'Up to Date' },
	unchecked: { tone: 'neutral', label: 'Not Checked Yet' },
	ineligible: { tone: 'neutral', label: 'Not Eligible' },
	quarantined: { tone: 'danger', label: 'Quarantined' },
	check_failed: { tone: 'danger', label: 'Check Failed' },
	run_failed: { tone: 'danger', label: 'Update Failed' }
};

export function candidateStatus(s: CandidateStatus): Presentation {
	return STATUS[s] ?? { tone: 'neutral', label: s };
}

/** Why a candidate is not eligible, in the words of the user's definition. */
export const REASON_LABELS: Record<NonNullable<UpdateCandidate['reason']>, string> = {
	build_only: 'Built from source (no image to follow)',
	digest_pinned: 'Pinned by @sha256 digest',
	untagged: 'No tag in the image reference',
	pull_policy_conflict: 'Its pull_policy prevents remote updates',
	invalid_reference: 'The image reference is not valid',
	not_deployed: 'Not deployed yet',
	no_applied_digest: 'The applied digest is unknown (deploy it once)',
	excluded: 'Excluded by this policy',
	protected: "Docker Manager's own container",
	no_recreate_spec: 'No saved specification to recreate it',
	stack_managed: 'Part of a stack (use the stack policy)'
};

export function reasonLabel(c: UpdateCandidate): string | null {
	if (!c.reason) return null;
	return REASON_LABELS[c.reason] ?? c.reasonMessage ?? c.reason;
}

/** Registry error classes of a failed check, in plain words. */
export const CHECK_ERRORS: Record<string, string> = {
	unauthorized: 'The registry refused the credentials (401).',
	forbidden: 'The registry denied access to this repository (403).',
	rate_limited: 'The registry rate-limited the check (429).',
	registry_unavailable: 'The registry could not be reached.',
	not_found: 'The tag does not exist in the registry.',
	platform_not_found: "The registry has no image for this host's platform.",
	ambiguous_registry_connection: 'More than one registry connection matches this image.',
	registry_connection_revoked: 'The registry connection was revoked.'
};

export function checkErrorText(c: UpdateCandidate): string | null {
	if (!c.errorClass) return null;
	return CHECK_ERRORS[c.errorClass] ?? c.errorMessage ?? c.errorClass;
}

/** One-line summary of a policy's candidates for lists. */
export function summaryText(p: UpdatePolicy): { text: string; tone: BadgeTone } {
	const s = p.summary;
	if (!s) return { text: 'Not Checked Yet', tone: 'neutral' };
	if (s.quarantined > 0) return { text: `${s.quarantined} quarantined`, tone: 'danger' };
	if (s.failed > 0) return { text: `${s.failed} failed`, tone: 'danger' };
	if (s.available > 0)
		return {
			text: `${s.available} ${s.available === 1 ? 'update' : 'updates'} available`,
			tone: 'warn'
		};
	if (!s.lastCheckAt) return { text: 'Not Checked Yet', tone: 'neutral' };
	if (s.upToDate > 0) return { text: 'Up to Date', tone: 'ok' };
	return { text: 'Nothing Eligible', tone: 'neutral' };
}

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

/** "Mon–Fri 01:00–05:00" style text of an update window (null: any time). */
export function windowText(w: UpdateWindow | undefined | null): string {
	if (!w) return 'Any time';
	const days = w.days && w.days.length > 0 && w.days.length < 7 ? daysText(w.days) : 'Every day';
	return `${days}, ${w.start}–${w.end}`;
}

export function daysText(days: number[]): string {
	const sorted = [...new Set(days)].sort((a, b) => a - b);
	// Collapse consecutive runs: [1,2,3,4,5] → Mon–Fri.
	const parts: string[] = [];
	let i = 0;
	while (i < sorted.length) {
		let j = i;
		while (j + 1 < sorted.length && sorted[j + 1] === sorted[j] + 1) j++;
		parts.push(
			j - i >= 2
				? `${DAYS[sorted[i]]}–${DAYS[sorted[j]]}`
				: sorted
						.slice(i, j + 1)
						.map((d) => DAYS[d])
						.join(', ')
		);
		i = j + 1;
	}
	return parts.join(', ');
}

export const DAY_OPTIONS = DAYS.map((label, value) => ({ value, label }));

/** HH:MM (24 h) validation of window bounds. */
export function isClock(s: string): boolean {
	return /^([01]\d|2[0-3]):[0-5]\d$/.test(s);
}

/** Candidates a run would apply (the preview's default selection). */
export function runnable(candidates: UpdateCandidate[]): UpdateCandidate[] {
	return candidates.filter((c) => c.eligible && c.status === 'update_available');
}

/**
 * Manual recovery of a quarantined or failed candidate (#20: no automatic
 * rollback). The manager's guidance wins; this is the fallback text.
 */
export function recoveryText(c: UpdateCandidate): string {
	if (c.guidance) return c.guidance;
	const pin = c.previousDigest ?? c.currentDigest;
	const repo = c.repository ?? c.reference.replace(/[:@].*$/, '');
	return pin
		? `Pin the previous image in your own Compose file, e.g. ${repo}@${pin}, then deploy the stack. Docker Manager never edits your files.`
		: 'Pin a known-good digest (image@sha256:…) in your own Compose file and deploy the stack. Docker Manager never edits your files.';
}

export type UpdateSummary = Schema<'UpdatePolicySummary'>;

function plural(n: number, one: string, many: string): string {
	return `${n} ${n === 1 ? one : many}`;
}

/** One target's state from its candidate summary (lists, policy targets). */
export function summaryState(s: UpdateSummary | undefined, inactive = false): Presentation {
	if (inactive) return { tone: 'neutral', label: 'Excluded' };
	if (!s) return { tone: 'neutral', label: 'Not Checked Yet' };
	const failures = s.failed + s.quarantined;
	if (failures) return { tone: 'danger', label: plural(failures, 'failure', 'failures') };
	if (s.available)
		return { tone: 'warn', label: `${plural(s.available, 'update', 'updates')} available` };
	if (!s.lastCheckAt) return { tone: 'neutral', label: 'Not Checked Yet' };
	return { tone: 'ok', label: 'Up to Date' };
}

/** Counts of targets by state and the newest check (KPI row of Updates). */
export function summarizeTargets(summaries: (UpdateSummary | undefined)[]): {
	withUpdates: number;
	/** Images (services or containers) with a newer digest in the targets with updates. */
	images: number;
	failing: number;
	upToDate: number;
	unchecked: number;
	lastCheckAt?: string;
} {
	const out = {
		withUpdates: 0,
		images: 0,
		failing: 0,
		upToDate: 0,
		unchecked: 0,
		lastCheckAt: undefined as string | undefined
	};
	for (const s of summaries) {
		if (!s || !s.lastCheckAt) out.unchecked++;
		else if (s.failed + s.quarantined) out.failing++;
		else if (s.available) {
			out.withUpdates++;
			out.images += s.available;
		} else out.upToDate++;
		if (s?.lastCheckAt && (!out.lastCheckAt || s.lastCheckAt > out.lastCheckAt))
			out.lastCheckAt = s.lastCheckAt;
	}
	return out;
}

/** The policy covering an update target, and whether the user may check it now. */
export interface PolicyRef {
	id: string;
	canCheck: boolean;
}

/** `<environment>/<stack|container>/<stack ID or container name>`. */
const targetKey = (environmentId: string, type: string, id: string) =>
	`${environmentId}/${type}/${id}`;

/** The policies by target, for the update badges of lists (containers, services). */
export function policiesByTarget(
	policies: readonly Pick<UpdatePolicy, 'id' | 'environmentId' | 'target' | 'actions'>[]
): Map<string, PolicyRef> {
	return new Map(
		policies.map((p) => [
			targetKey(p.environmentId, p.target.type, p.target.id),
			{ id: p.id, canCheck: (p.actions ?? []).includes('update.check') }
		])
	);
}

/** The policy covering a container: its stack's (Docker Manager stacks) or its own. */
export function containerPolicy(
	index: ReadonlyMap<string, PolicyRef>,
	c: { environmentId: string; name: string; stack?: { stackId?: string } }
): PolicyRef | undefined {
	return c.stack
		? c.stack.stackId
			? index.get(targetKey(c.environmentId, 'stack', c.stack.stackId))
			: undefined
		: index.get(targetKey(c.environmentId, 'container', c.name));
}

/**
 * What the Updates KPI counts, in one sentence: "6 images in 5 stacks",
 * "1 image in 1 container", "3 images in 2 stacks and containers". Both the
 * KPI and the policy rows count targets (stacks and containers) with at
 * least one newer image; the images are the services behind them.
 */
export function updatesText(
	images: number,
	targets: { stacks: number; containers: number }
): string {
	const n = targets.stacks + targets.containers;
	if (!n) return 'Nothing waiting';
	const where =
		targets.stacks && targets.containers
			? plural(n, 'stack and container', 'stacks and containers')
			: targets.containers
				? plural(n, 'container', 'containers')
				: plural(n, 'stack', 'stacks');
	return `${plural(Math.max(images, n), 'image', 'images')} in ${where}`;
}

/** Why a target of an environment policy is not covered (null: it is covered). */
export type InactiveReason = 'excluded' | 'missing';

/**
 * The manager says why an inactive target is not covered: excluded (by
 * the policy or the container's label) or missing (a deleted stack, a
 * removed container, one that no longer qualifies); it keeps such records
 * for their history.
 */
export function inactiveReason(t: {
	inactive: boolean;
	inactiveReason?: InactiveReason;
}): InactiveReason | null {
	if (!t.inactive) return null;
	return t.inactiveReason ?? 'missing';
}

/** A target's badge: excluded and missing targets say so, the rest their candidates' state. */
export function targetState(
	s: UpdateSummary | undefined,
	reason: InactiveReason | null
): Presentation {
	if (reason === 'excluded') return { tone: 'neutral', label: 'Excluded' };
	if (reason === 'missing') return { tone: 'neutral', label: 'No Longer Found' };
	return summaryState(s);
}

/** "published 3 days ago" of a candidate's newer image (null: the registry did not say). */
export function publishedText(
	c: { publishedAt?: string | null },
	now: Date = new Date()
): string | null {
	return c.publishedAt ? `published ${formatRelative(c.publishedAt, now)}` : null;
}

interface ScheduleLike {
	cron?: string;
	timeZone?: string;
	enabled: boolean;
}

function lowerFirst(s: string): string {
	return s && /^[A-Z][a-z]/.test(s) ? s[0].toLowerCase() + s.slice(1) : s;
}

/**
 * An update policy's two schedules in one sentence: "Checks every hour,
 * updates daily at 04:00 (UTC)", "Checks daily at 03:00, updates only by
 * hand", "Checks and updates only by hand". `viewer` is the viewer's zone
 * (tests).
 */
export function policySchedulesText(
	check: ScheduleLike,
	run: ScheduleLike,
	viewer?: string
): string {
	const words = (s: ScheduleLike) =>
		lowerFirst(describeCron(s.cron ?? '', s.timeZone ?? '', viewer));
	if (!check.enabled && !run.enabled) return 'Checks and updates only by hand';
	const c = check.enabled ? `Checks ${words(check)}` : 'Checks only by hand';
	const r = run.enabled ? `updates ${words(run)}` : 'updates only by hand';
	return `${c}, ${r}`;
}

/** "nginx:1.27" of a candidate: its tagged reference, without Docker Hub's implied prefix. */
export function imageLabel(c: Pick<UpdateCandidate, 'reference'>): string {
	return c.reference.replace(
		/^(docker\.io|index\.docker\.io|registry-1\.docker\.io)\/(library\/)?/,
		''
	);
}

/** updatesText of environment policy targets (stacks and containers counted apart). */
export function targetsUpdateText(
	targets: readonly { type: string; candidateSummary?: UpdateSummary }[]
): string {
	const of = (type: string) =>
		summarizeTargets(targets.filter((t) => t.type === type).map((t) => t.candidateSummary));
	const stacks = of('stack');
	const containers = of('container');
	return updatesText(stacks.images + containers.images, {
		stacks: stacks.withUpdates,
		containers: containers.withUpdates
	});
}

const CONTAINER_ID = /^[0-9a-f]{12,64}$/i;

/**
 * The name of a standalone container target: its ID is the container's
 * name; older records may hold the Engine ID, which is looked up in the
 * environment's containers and never shown itself.
 */
export function containerTargetName(
	id: string,
	containers?: readonly { id: string; name: string }[]
): { name: string; found: boolean } {
	const c = containers?.find((x) => x.name === id || x.id === id);
	if (c) return { name: c.name, found: true };
	if (CONTAINER_ID.test(id)) return { name: 'Removed Container', found: false };
	return { name: id, found: !containers };
}

/**
 * The covered (active) targets of several environment policies, once each,
 * in one environment (null: all).
 */
export function coveredTargets<
	T extends { policyId: string; environmentId: string; inactive: boolean }
>(lists: readonly (readonly T[] | undefined)[], environmentId: string | null): T[] {
	const seen = new Map<string, T>();
	for (const list of lists)
		for (const t of list ?? [])
			if (!t.inactive && (!environmentId || t.environmentId === environmentId))
				seen.set(t.policyId, t);
	return [...seen.values()];
}

interface TargetRow {
	policyId: string;
	environmentId: string;
	type: 'stack' | 'container';
	id: string;
	inactive: boolean;
	inactiveReason?: InactiveReason;
	candidateSummary: UpdateSummary;
}

const EMPTY_SUMMARY: UpdateSummary = {
	available: 0,
	upToDate: 0,
	quarantined: 0,
	ineligible: 0,
	failed: 0,
	unchecked: 0
};

/**
 * A policy's targets plus the stacks and containers it excludes that never
 * got a target record (excluded before the first check), so every
 * exclusion is listed by name. `stackEnvironment` finds a stack's
 * environment.
 */
export function withExclusions<T extends TargetRow>(
	targets: readonly T[],
	p: {
		scope: 'all' | 'environment';
		environmentId?: string;
		excludeStacks: string[];
		excludeContainers: string[];
	},
	stackEnvironment: (stackId: string) => string | undefined
): (T | TargetRow)[] {
	const has = (type: string, id: string, env?: string) =>
		targets.some((t) => t.type === type && t.id === id && (!env || t.environmentId === env));
	const extra: TargetRow[] = [];
	for (const id of p.excludeStacks) {
		const env = stackEnvironment(id);
		if (!env || has('stack', id)) continue;
		extra.push({
			policyId: `excluded:stack:${id}`,
			environmentId: env,
			type: 'stack',
			id,
			inactive: true,
			inactiveReason: 'excluded',
			candidateSummary: EMPTY_SUMMARY
		});
	}
	for (const key of p.excludeContainers) {
		const [env, name] =
			p.scope === 'all' && key.includes('/')
				? [key.slice(0, key.indexOf('/')), key.slice(key.indexOf('/') + 1)]
				: [p.environmentId ?? '', key];
		if (!env || has('container', name, env)) continue;
		extra.push({
			policyId: `excluded:container:${env}/${name}`,
			environmentId: env,
			type: 'container',
			id: name,
			inactive: true,
			inactiveReason: 'excluded',
			candidateSummary: EMPTY_SUMMARY
		});
	}
	return [...targets, ...extra];
}

/**
 * The status sentence of an update policy page: what needs doing, from
 * its covered targets.
 */
export function policyStatusText(
	totals: ReturnType<typeof summarizeTargets>,
	covered: number,
	updates: string
): string {
	if (!covered) return 'Nothing in scope to update yet.';
	if (totals.unchecked === covered) return 'Not checked yet. Check now to look for newer images.';
	const parts: string[] = [];
	if (totals.failing)
		parts.push(
			`${plural(totals.failing, 'stack or container', 'stacks or containers')} failed the last check or update.`
		);
	if (totals.withUpdates) parts.push(`Newer images: ${updates}. Preview them to update.`);
	if (!parts.length)
		return totals.unchecked
			? `Up to date; ${totals.unchecked} not checked yet.`
			: 'Everything it covers is up to date.';
	return parts.join(' ');
}
