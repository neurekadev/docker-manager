// Docker maintenance (#14, #238): presentation and editing helpers of the
// maintenance settings and one-off prunes. Pure functions (model.spec.ts);
// the API validates rules.
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';
import { describeCron } from '$lib/ui/cron';
import { formatBytes } from '$lib/ui/format';

export type MaintenanceSettings = Schema<'MaintenanceSettings'>;
export type MaintenanceRule = Schema<'MaintenanceRule'>;
export type CategoryInfo = Schema<'PruneCategoryInfo'>;
export type PrunePreview = Schema<'PrunePreview'>;
export type PruneItem = Schema<'PruneItemView'>;
export type Category = MaintenanceRule['category'];

/** Display order: from the least to the most destructive. */
export const CATEGORIES: Category[] = [
	'stopped_containers',
	'dangling_images',
	'unused_images',
	'unused_networks',
	'build_cache',
	'anonymous_volumes',
	'named_volumes'
];

/** Fallback labels when the settings (with the manager's labels) are not readable. */
export const CATEGORY_LABELS: Record<Category, string> = {
	stopped_containers: 'Stopped Containers',
	dangling_images: 'Dangling Images',
	unused_images: 'Unused Images',
	unused_networks: 'Unused Networks',
	build_cache: 'Build Cache',
	anonymous_volumes: 'Anonymous Volumes',
	named_volumes: 'Named Volumes'
};

export function isVolumeCategory(c: Category): boolean {
	return c === 'anonymous_volumes' || c === 'named_volumes';
}

export function categoryLabel(c: Category, info?: CategoryInfo[]): string {
	return info?.find((i) => i.category === c)?.label ?? CATEGORY_LABELS[c] ?? c;
}

/**
 * A category label inside a sentence: "Stopped Containers" reads
 * "stopped containers" ("Stopped containers" at the start); words with
 * more than one capital (acronyms) stay as they are.
 */
function categoryPhrase(label: string, first: boolean): string {
	return label
		.split(' ')
		.map((w, i) => ((first && i === 0) || /[A-Z].*[A-Z]/.test(w) ? w : w.toLowerCase()))
		.join(' ');
}

/** Rules in display order, one per category (missing ones disabled, 30 days). */
export function normalizeRules(rules: MaintenanceRule[] | undefined): MaintenanceRule[] {
	return CATEGORIES.map(
		(category) =>
			rules?.find((r) => r.category === category) ?? {
				category,
				enabled: false,
				minAgeHours: 720
			}
	);
}

/** "older than 30 days" / "any age" / "older than 12 hours". */
export function ageText(hours: number): string {
	if (hours <= 0) return 'any age';
	if (hours % 24 === 0) {
		const d = hours / 24;
		return `older than ${d} ${d === 1 ? 'day' : 'days'}`;
	}
	return `older than ${hours} ${hours === 1 ? 'hour' : 'hours'}`;
}

/** One line per enabled rule, e.g. "Stopped containers older than 30 days". */
export function ruleSummary(r: MaintenanceRule, info?: CategoryInfo[]): string {
	const label = categoryPhrase(categoryLabel(r.category, info), true);
	const parts = [`${label} ${ageText(r.minAgeHours)}`];
	if (r.category === 'build_cache') {
		parts.push(r.buildCacheAll ? 'all unused records' : 'dangling records only');
		if (r.keepStorageBytes) parts.push(`keeping ${formatBytes(r.keepStorageBytes)}`);
	}
	if (r.exclude?.length) parts.push(`${r.exclude.length} excluded`);
	if (r.excludeLabels?.length) parts.push(`except labels ${r.excludeLabels.join(', ')}`);
	if (r.includeLabels?.length) parts.push(`only labels ${r.includeLabels.join(', ')}`);
	return parts.join(', ');
}

export function enabledRules(p: Pick<MaintenanceSettings, 'rules'>): MaintenanceRule[] {
	return normalizeRules(p.rules).filter((r) => r.enabled);
}

/** "3 of 7 rules on" (the rule count comes from normalizeRules, one per category). */
export function rulesOnText(p: Pick<MaintenanceSettings, 'rules'>): string {
	return `${enabledRules(p).length} of ${normalizeRules(p.rules).length} rules on`;
}

/** Why a rule cannot be saved as it is (null: fine). Mirrors the API's checks. */
export function ruleProblem(r: MaintenanceRule): string | null {
	if (!Number.isInteger(r.minAgeHours) || r.minAgeHours < 0)
		return 'The age must be a whole number of hours or days, 0 or more.';
	if (isVolumeCategory(r.category) && r.enabled && !r.volumeOptIn)
		return 'Confirm that removing volumes deletes their data before turning this rule on.';
	if (r.category === 'build_cache' && (r.includeLabels?.length || r.excludeLabels?.length))
		return 'Build cache records have no labels: remove the label filters.';
	return null;
}

export const DECISION: Record<PruneItem['decision'], { tone: BadgeTone; label: string }> = {
	remove: { tone: 'danger', label: 'Removed' },
	protected: { tone: 'ok', label: 'Protected' },
	excluded: { tone: 'neutral', label: 'Excluded' },
	retained: { tone: 'neutral', label: 'Kept' }
};

/** Bytes of an item: -1 means the Engine does not report it. */
export function itemBytes(b: number): string {
	return b < 0 ? 'Unknown' : formatBytes(b);
}

export { linesToList, listToLines } from '$lib/features/common/text';

/** Hours ↔ the editor's days-or-hours value. */
export function splitAge(hours: number): { value: number; unit: 'days' | 'hours' } {
	return hours > 0 && hours % 24 === 0
		? { value: hours / 24, unit: 'days' }
		: { value: hours, unit: 'hours' };
}

export function joinAge(value: number, unit: 'days' | 'hours'): number {
	return unit === 'days' ? value * 24 : value;
}

/** Run summary of a finished prune job, e.g. "Removed 12, 1 failed, 1.2 GB reclaimed". */
export function runSummaryText(s: Schema<'MaintenanceRunSummary'>): string {
	const parts = [`Removed ${s.removed}`];
	if (s.failed) parts.push(`${s.failed} failed`);
	if (s.skipped) parts.push(`${s.skipped} skipped`);
	if (s.deferred) parts.push(`${s.deferred} left for the next run`);
	parts.push(`${formatBytes(s.bytesReclaimed)} reclaimed`);
	return parts.join(', ');
}

/** The categories the turned-on rules clean, e.g. "Stopped containers and unused images". */
export function rulesText(p: Pick<MaintenanceSettings, 'rules'>, info?: CategoryInfo[]): string {
	const labels = enabledRules(p).map((r, i) =>
		categoryPhrase(categoryLabel(r.category, info), i === 0)
	);
	if (!labels.length) return 'Every rule is off';
	if (labels.length === 1) return labels[0];
	return `${labels.slice(0, -1).join(', ')} and ${labels[labels.length - 1]}`;
}

/** What a resource page's one-off prune cleans (#14). */
export type PruneTarget = 'containers' | 'images' | 'networks' | 'volumes' | 'build_cache';

export interface PruneTargetInfo {
	/** Dialog title and button label. */
	title: string;
	/** What goes, for sentences: "Pruned unused images on Silo". */
	what: string;
	/** The categories the page offers, least destructive first. */
	categories: Category[];
	/** The one category turned on to start with. */
	start: Category;
}

export const PRUNE_TARGETS: Record<PruneTarget, PruneTargetInfo> = {
	containers: {
		title: 'Prune Containers',
		what: 'stopped containers',
		categories: ['stopped_containers'],
		start: 'stopped_containers'
	},
	images: {
		title: 'Prune Images',
		what: 'unused images',
		categories: ['dangling_images', 'unused_images'],
		start: 'unused_images'
	},
	networks: {
		title: 'Prune Networks',
		what: 'unused networks',
		categories: ['unused_networks'],
		start: 'unused_networks'
	},
	volumes: {
		title: 'Prune Volumes',
		what: 'unused volumes',
		categories: ['anonymous_volumes', 'named_volumes'],
		start: 'anonymous_volumes'
	},
	build_cache: {
		title: 'Prune Build Cache',
		what: 'build cache',
		categories: ['build_cache'],
		start: 'build_cache'
	}
};

/** A one-off prune removes objects of any age unless the user sets one. */
export const MANUAL_PRUNE_MIN_AGE_HOURS = 0;

/**
 * Starting rules of a one-off prune, like the Docker CLI's prunes with
 * `--all`: exited or dead containers, every unused image (dangling ones
 * included), unused networks, anonymous volumes and all unused build
 * cache, of any age. Named volumes start off, and volume rules still need
 * their own opt-in before the prune can run.
 */
export function manualPruneRules(target: PruneTarget): MaintenanceRule[] {
	const t = PRUNE_TARGETS[target];
	return t.categories.map((category) => ({
		category,
		enabled: category === t.start,
		minAgeHours: MANUAL_PRUNE_MIN_AGE_HOURS,
		...(category === 'stopped_containers' ? { containerStates: ['exited', 'dead'] } : {}),
		...(category === 'build_cache' ? { buildCacheAll: true } : {})
	}));
}

/**
 * Why the rules can't be previewed (forPreview) or run (null: fine). A
 * preview evaluates volume rules without their opt-in; a run needs it.
 */
export function manualPruneProblem(rules: MaintenanceRule[], forPreview: boolean): string | null {
	if (!rules.some((r) => r.enabled)) return 'Turn on at least one rule.';
	for (const r of rules) {
		const p = ruleProblem(forPreview ? { ...r, volumeOptIn: true } : r);
		if (p) return p;
	}
	return null;
}

/**
 * The status sentence of the Maintenance page: when it runs and what its
 * last run did. `viewer` is the viewer's zone (tests).
 */
export function maintenanceStatusText(
	p: Pick<MaintenanceSettings, 'rules' | 'schedule' | 'lastRun' | 'enabled'>,
	viewer?: string
): string {
	if (!enabledRules(p).length) return 'Every rule is off: maintenance removes nothing.';
	const s = p.schedule;
	const words = describeCron(s.cron, s.timeZone, viewer);
	const when =
		p.enabled && !s.invalidReason && words
			? `Runs ${/^[A-Z][a-z]/.test(words) ? words[0].toLowerCase() + words.slice(1) : words}.`
			: 'Runs only when you start it.';
	const run = p.lastRun ? runSummaryText(p.lastRun) : '';
	const last = run ? ` Last run: ${run[0].toLowerCase()}${run.slice(1)}.` : ' Not run yet.';
	return when + last;
}

/**
 * Which environments maintenance covers, for labels: "All Environments",
 * or "2 of 3 Environments" when some are left out (counted among the
 * environments that are not archived when they are known).
 */
export function coveredEnvironmentsText(
	p: Pick<MaintenanceSettings, 'excludeEnvironments'>,
	environments?: { id: string; status: string }[]
): string {
	if (!p.excludeEnvironments.length) return 'All Environments';
	const n = p.excludeEnvironments.length;
	if (!environments) return `All but ${n} ${n === 1 ? 'Environment' : 'Environments'}`;
	const active = environments.filter((e) => e.status !== 'archived');
	const covered = active.filter((e) => !p.excludeEnvironments.includes(e.id)).length;
	if (covered === active.length) return 'All Environments';
	return `${covered} of ${active.length} ${active.length === 1 ? 'Environment' : 'Environments'}`;
}
