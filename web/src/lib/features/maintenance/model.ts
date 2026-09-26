// Docker maintenance (#14): presentation and editing helpers of prune
// policies. Pure functions (model.spec.ts); the API validates rules.
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';
import { formatBytes } from '$lib/ui/format';

export type MaintenancePolicy = Schema<'MaintenancePolicy'>;
export type MaintenanceRule = Schema<'MaintenanceRule'>;
export type MaintenanceDefaults = Schema<'MaintenanceDefaults'>;
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

/** Fallback labels when the defaults (with the manager's labels) are not readable. */
export const CATEGORY_LABELS: Record<Category, string> = {
	stopped_containers: 'Stopped containers',
	dangling_images: 'Dangling images',
	unused_images: 'Unused images',
	unused_networks: 'Unused networks',
	build_cache: 'Build cache',
	anonymous_volumes: 'Anonymous volumes',
	named_volumes: 'Named volumes'
};

export function isVolumeCategory(c: Category): boolean {
	return c === 'anonymous_volumes' || c === 'named_volumes';
}

export function categoryLabel(c: Category, info?: CategoryInfo[]): string {
	return info?.find((i) => i.category === c)?.label ?? CATEGORY_LABELS[c] ?? c;
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
	const parts = [`${categoryLabel(r.category, info)} ${ageText(r.minAgeHours)}`];
	if (r.category === 'build_cache') {
		parts.push(r.buildCacheAll ? 'all unused records' : 'dangling records only');
		if (r.keepStorageBytes) parts.push(`keeping ${formatBytes(r.keepStorageBytes)}`);
	}
	if (r.exclude?.length) parts.push(`${r.exclude.length} excluded`);
	if (r.excludeLabels?.length) parts.push(`except labels ${r.excludeLabels.join(', ')}`);
	if (r.includeLabels?.length) parts.push(`only labels ${r.includeLabels.join(', ')}`);
	return parts.join(', ');
}

export function enabledRules(p: Pick<MaintenancePolicy, 'rules'>): MaintenanceRule[] {
	return normalizeRules(p.rules).filter((r) => r.enabled);
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
		title: 'Prune containers',
		what: 'stopped containers',
		categories: ['stopped_containers'],
		start: 'stopped_containers'
	},
	images: {
		title: 'Prune images',
		what: 'unused images',
		categories: ['dangling_images', 'unused_images'],
		start: 'dangling_images'
	},
	networks: {
		title: 'Prune networks',
		what: 'unused networks',
		categories: ['unused_networks'],
		start: 'unused_networks'
	},
	volumes: {
		title: 'Prune volumes',
		what: 'unused volumes',
		categories: ['anonymous_volumes', 'named_volumes'],
		start: 'anonymous_volumes'
	},
	build_cache: {
		title: 'Prune build cache',
		what: 'build cache',
		categories: ['build_cache'],
		start: 'build_cache'
	}
};

/** A one-off prune's safety margin: nothing younger than a day by default. */
export const MANUAL_PRUNE_MIN_AGE_HOURS = 24;

/**
 * Safe starting rules of a one-off prune: only the target's least
 * destructive category is on (dangling images, anonymous volumes, exited
 * or dead containers, dangling build cache), older than a day. Volume
 * rules still need their own opt-in before the prune can run.
 */
export function manualPruneRules(target: PruneTarget): MaintenanceRule[] {
	const t = PRUNE_TARGETS[target];
	return t.categories.map((category) => ({
		category,
		enabled: category === t.start,
		minAgeHours: MANUAL_PRUNE_MIN_AGE_HOURS,
		...(category === 'stopped_containers' ? { containerStates: ['exited', 'dead'] } : {})
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
