// Stack migration wizard view model (#35): the destinations, the user's
// selection (images copied through the manager, volumes copied or not),
// whether the check on screen still matches that selection, and what the
// migration copies, in plain words. Pure; tested in migration.spec.ts.
import type { Schema } from '$lib/api/client';

export type MigrationPreview = Schema<'MigrationPreview'>;
export type ServicePlan = Schema<'MigrationServicePlan'>;
export type VolumePlan = Schema<'MigrationVolumePlan'>;
export type MigrationAccessChange = Schema<'MigrationAccessChange'>;

/** What the user chose in the wizard. */
export interface MigrationSelection {
	/** The destination environment's ID ('' while none is chosen). */
	target: string;
	/** Named volumes whose data is not copied. */
	excluded: string[];
	/** Anonymous volumes to copy (skipped unless chosen). */
	anonymous: string[];
	/** Images copied through the manager instead of pulled. */
	transfer: string[];
}

/** The request body of a preview or a migration for a selection. */
export function migrationBody(s: MigrationSelection): Schema<'StackMigrationBody'> {
	return {
		targetEnvironmentId: s.target,
		excludeVolumes: s.excluded.length ? [...s.excluded] : undefined,
		anonymousVolumes: s.anonymous.length ? [...s.anonymous] : undefined,
		transferImages: s.transfer.length ? [...s.transfer] : undefined
	};
}

const set = (xs: string[]) => [...new Set(xs)].sort();

/** A key of a selection; the order of the ticks does not matter. */
export function selectionKey(s: MigrationSelection): string {
	return JSON.stringify([s.target, set(s.excluded), set(s.anonymous), set(s.transfer)]);
}

/**
 * Whether the check on screen was run for another selection than the
 * current one (then its findings, sizes and copied volumes are stale).
 * No check yet (`checkedKey` null) is not stale.
 */
export function selectionChanged(checkedKey: string | null, s: MigrationSelection): boolean {
	return checkedKey !== null && checkedKey !== selectionKey(s);
}

/** `list` with `item` in it (on) or without it (off). */
export function toggled(list: string[], item: string, on: boolean): string[] {
	const without = list.filter((x) => x !== item);
	return on ? [...without, item] : without;
}

/**
 * The volumes whose data the migration copies: the check's copies, minus
 * those unticked since (named volumes excluded, anonymous volumes no
 * longer chosen).
 */
export function copiedVolumes(
	volumes: VolumePlan[],
	s: Pick<MigrationSelection, 'excluded' | 'anonymous'>
): VolumePlan[] {
	return volumes.filter(
		(v) =>
			v.action === 'copy' &&
			(v.anonymous ? s.anonymous.includes(v.source) : !s.excluded.includes(v.source))
	);
}

/** Whether the user can choose to copy a service's image through the manager. */
export function canCopyImage(s: ServicePlan, transfer: string[]): boolean {
	return s.action === 'pull' || transfer.includes(s.image);
}

/**
 * Which "Copy data" choice a volume offers: anonymous volumes can be
 * added, named volumes the check copies (or the user excluded) can be
 * left out; external, missing and definition-only volumes offer none.
 */
export function volumeChoice(v: VolumePlan, excluded: string[]): 'anonymous' | 'named' | null {
	if (v.anonymous) return 'anonymous';
	if (v.action === 'copy' || excluded.includes(v.source)) return 'named';
	return null;
}

const IMAGE_ACTIONS: Record<string, string> = {
	pull: 'Downloaded on the destination',
	rebuild: 'Built again on the destination',
	transfer: 'Copied through Docker Manager',
	present: 'Already there'
};

/** How a service gets its image on the destination, in words. */
export function imageActionLabel(action: string): string {
	return IMAGE_ACTIONS[action] ?? 'Handled on the destination';
}

const VOLUME_ACTIONS: Record<string, string> = {
	copy: 'Data copied',
	skip: 'Created empty',
	definition_only: 'Created empty (data not supported)',
	external: 'Must already exist there'
};

/** What happens to a volume, in words. */
export function volumeActionLabel(action: string): string {
	return VOLUME_ACTIONS[action] ?? 'Not copied';
}

/** "1 volume", "3 volumes". */
export function count(n: number, one: string, many = `${one}s`): string {
	return `${n} ${n === 1 ? one : many}`;
}

/** The one-line result of a check: can it move, and with how many warnings. */
export function checkHeadline(p: Pick<MigrationPreview, 'blockers' | 'warnings'>): {
	tone: 'danger' | 'warn' | 'info';
	title: string;
} {
	const b = p.blockers.length;
	const w = p.warnings.length;
	if (b)
		return {
			tone: 'danger',
			title: `${count(b, 'problem')} must be fixed before the stack can move.`
		};
	if (w)
		return {
			tone: 'warn',
			title: `Ready to move, with ${count(w, 'warning')}. Starting the migration accepts ${w === 1 ? 'it' : 'them'}.`
		};
	return { tone: 'info', title: 'Ready to move. Nothing needs your attention.' };
}

/** One user's access change in words ("gains Deploy; loses Delete"). */
export function accessChangeText(c: Pick<MigrationAccessChange, 'gained' | 'lost'>): string {
	const parts: string[] = [];
	if (c.gained.length) parts.push(`gains ${c.gained.join(', ')}`);
	if (c.lost.length) parts.push(`loses ${c.lost.join(', ')}`);
	return parts.join('; ');
}

/** The environments a stack can move to: every other active one. */
export function migrationTargets<E extends { id: string; status: string }>(
	environments: readonly E[] | undefined,
	source: string
): E[] {
	return (environments ?? []).filter((e) => e.id !== source && e.status === 'active');
}

/** A sentence with a capital first letter. */
export function sentence(t: string): string {
	return t ? t.charAt(0).toUpperCase() + t.slice(1) : t;
}
