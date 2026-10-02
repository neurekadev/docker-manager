// Environment migration wizard view model (#35): every stack of an
// environment moves to another environment. Which stacks the user can
// choose (Docker Manager's own stack moves with Docker Manager; stacks the
// caller may not migrate are shown but off), the request body, the check's
// headline, the order the stacks move in (groups that share a network or
// volume stop together and move one after the other), the stacks left out
// and why, the outcome of a run (what moved, what failed, the old copies
// left stopped on the source), the ended run the migrate page opens on
// while it still has something to act on, and the environment page's
// notice about old copies. Pure; tested in environment-migration.spec.ts.
import type { Job, Schema } from '$lib/api/client';
import type { JobMatch } from '$lib/features/jobs/active';
import { count } from '$lib/features/stacks/migration';

export type EnvironmentMigrationPreview = Schema<'EnvironmentMigrationPreview'>;
export type EnvironmentMigration = Schema<'EnvironmentMigration'>;
export type EnvironmentMigrationBody = Schema<'EnvironmentMigrationBody'>;
export type StackMove = Schema<'EnvironmentMigrationStackPreview'>;
export type MovedStack = Schema<'EnvironmentMigrationStack'>;
export type SkippedStack = Schema<'EnvironmentMigrationSkipped'>;

/** Resolves a stack's ID to the title users know (its display name), else its project name. */
export type TitleOf = (stackId: string, name: string) => string;
const plainTitle: TitleOf = (_id, name) => name;

/** The running environment migrations away from `environmentId` (the move step reopens on them). */
export function environmentMigrationMatch(environmentId: string): JobMatch {
	return { kinds: ['environment.migrate'], environmentId };
}

/** Removals of moved stacks' old copies on `environmentId` (the result view follows them). */
export function oldCopyRemovalMatch(environmentId: string): JobMatch {
	return { kinds: ['stack.remove_source'], environmentId };
}

/** The stacks a job acts on (a removal acts on one). */
export function jobStackIds(job: Pick<Job, 'targets'> | undefined): string[] {
	return (job?.targets ?? []).filter((t) => t.type === 'stack').map((t) => t.id);
}

/**
 * Docker Manager's own stacks: the stacks its protected containers belong
 * to (the stack list does not say; the check confirms it).
 */
export function ownStackIds(
	containers: readonly { protection?: unknown; stack?: { stackId?: string } | null }[] | undefined
): string[] {
	const out: string[] = [];
	for (const c of containers ?? []) {
		const id = c.stack?.stackId;
		if (c.protection && id && !out.includes(id)) out.push(id);
	}
	return out;
}

/** `own` plus the stacks a check left out as Docker Manager's own. */
export function withOwnFromCheck(
	own: readonly string[],
	p: Pick<EnvironmentMigrationPreview, 'skipped'>
): string[] {
	const out = [...own];
	for (const s of p.skipped)
		if (s.reason === 'docker_manager' && !out.includes(s.stackId)) out.push(s.stackId);
	return out;
}

/** One stack of the source in the wizard's list. */
export interface StackChoice {
	id: string;
	title: string;
	/** Why it cannot be chosen (Docker Manager's own, or not permitted). */
	blocked?: 'docker_manager' | 'not_permitted';
}

/** Why a stack of the list cannot be chosen, in words. */
export const CHOICE_REASONS: Record<NonNullable<StackChoice['blocked']>, string> = {
	docker_manager: 'Moves with Docker Manager',
	not_permitted: 'You may not migrate this stack.'
};

/** The source's stacks, by title: Docker Manager's own and those the caller may not migrate are off. */
export function stackChoices(
	stacks: readonly {
		id: string;
		name: string;
		displayName?: string;
		environmentId: string;
		actions: string[];
	}[],
	source: string,
	own: readonly string[]
): StackChoice[] {
	return stacks
		.filter((s) => s.environmentId === source)
		.map((s): StackChoice => {
			const title = s.displayName?.trim() || s.name;
			if (own.includes(s.id)) return { id: s.id, title, blocked: 'docker_manager' };
			if (!s.actions.includes('stack.migrate'))
				return { id: s.id, title, blocked: 'not_permitted' };
			return { id: s.id, title };
		})
		.sort((a, b) => a.title.localeCompare(b.title));
}

/** What the user chose: the destination and the stacks they unticked. */
export interface EnvironmentMigrationSelection {
	/** The destination environment's ID ('' while none is chosen). */
	target: string;
	/** Stacks the user unticked (every other stack that can move is chosen). */
	deselected: string[];
}

/** The stacks the migration moves: every one that can, but the unticked ones. */
export function chosenStacks(
	choices: readonly StackChoice[],
	deselected: readonly string[]
): string[] {
	return choices.filter((c) => !c.blocked && !deselected.includes(c.id)).map((c) => c.id);
}

/**
 * The request body of a check or a start. With nothing unticked the
 * server takes every stack the caller may migrate (and reports Docker
 * Manager's own stack as such); otherwise it names the chosen stacks.
 */
export function environmentMigrationBody(
	s: EnvironmentMigrationSelection,
	choices: readonly StackChoice[]
): EnvironmentMigrationBody {
	const unticked = s.deselected.filter((id) => choices.some((c) => c.id === id && !c.blocked));
	return {
		targetEnvironmentId: s.target,
		stacks: unticked.length ? chosenStacks(choices, unticked).sort() : undefined
	};
}

/** A key of a selection; the order of the ticks does not matter. */
export function selectionKey(s: EnvironmentMigrationSelection): string {
	return JSON.stringify([s.target, [...new Set(s.deselected)].sort()]);
}

/** A destination in the picker: offline ones cannot be chosen and say so. */
export interface DestinationOption {
	value: string;
	label: string;
	disabled: boolean;
}

/** The destinations offered (the other active environments). */
export function destinationOptions(
	targets: readonly { id: string; name: string; online: boolean }[]
): DestinationOption[] {
	return targets.map((e) => ({
		value: e.id,
		label: e.online ? e.name : `${e.name} (offline)`,
		disabled: !e.online
	}));
}

/** Problems of the whole migration and of every stack. */
export function problemCount(p: Pick<EnvironmentMigrationPreview, 'blockers' | 'stacks'>): number {
	return p.blockers.length + p.stacks.reduce((n, s) => n + s.preview.blockers.length, 0);
}

/** Warnings of the whole migration and of every stack. */
export function warningCount(p: Pick<EnvironmentMigrationPreview, 'warnings' | 'stacks'>): number {
	return p.warnings.length + p.stacks.reduce((n, s) => n + s.preview.warnings.length, 0);
}

/** The one-line result of a check. */
export function environmentCheckHeadline(
	p: Pick<EnvironmentMigrationPreview, 'blockers' | 'warnings' | 'stacks'>,
	destination: string
): { tone: 'danger' | 'warn' | 'info'; title: string } {
	const problems = problemCount(p);
	if (problems)
		return { tone: 'danger', title: `Fix ${count(problems, 'problem')} before migrating` };
	const ready = `Ready to migrate ${count(p.stacks.length, 'stack')} to ${destination}`;
	const warnings = warningCount(p);
	if (warnings) return { tone: 'warn', title: `${ready}, with ${count(warnings, 'warning')}` };
	return { tone: 'info', title: ready };
}

/** "a", "a and b", "a, b and c". */
export function andList(names: readonly string[]): string {
	if (names.length <= 1) return names.join('');
	return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
}

/** One group of the move order. */
export interface OrderGroup {
	key: string;
	/** Its stacks in the order they move, each with the stacks it waits for. */
	stacks: { stackId: string; title: string; after: string[] }[];
}

/** The groups in the order they move (a group of one moves on its own). */
export function moveOrder(
	p: Pick<EnvironmentMigrationPreview, 'groups' | 'stacks'>,
	titleOf: TitleOf = plainTitle
): OrderGroup[] {
	const byId = new Map(p.stacks.map((s) => [s.stackId, s]));
	const title = (id: string) => {
		const s = byId.get(id);
		return s ? titleOf(id, s.name) : 'another stack';
	};
	return p.groups.map((group, i) => ({
		key: `${i}:${group.join(',')}`,
		stacks: group.map((id) => ({
			stackId: id,
			title: title(id),
			after: (byId.get(id)?.dependsOn ?? []).map(title)
		}))
	}));
}

/** Why a stack is not moved, in words. */
export const SKIP_REASONS: Record<SkippedStack['reason'], string> = {
	docker_manager: "Docker Manager's own stack. It moves when you move Docker Manager.",
	not_permitted: 'You may not migrate this stack.',
	not_selected: 'Not selected.'
};

/**
 * The stacks the check left out, with the reason. Docker Manager's own
 * stack keeps its reason when the request named the other stacks (the
 * server then reports it as not selected).
 */
export function skippedRows(
	p: Pick<EnvironmentMigrationPreview, 'skipped'>,
	own: readonly string[],
	titleOf: TitleOf = plainTitle
): { stackId: string; title: string; reason: string }[] {
	return p.skipped.map((s) => ({
		stackId: s.stackId,
		title: titleOf(s.stackId, s.name),
		reason: SKIP_REASONS[own.includes(s.stackId) ? 'docker_manager' : s.reason]
	}));
}

/** What a stack's detail summarises: volumes copied, problems and warnings. */
export function stackMoveSummary(s: StackMove): string {
	const copied = s.preview.volumes.filter((v) => v.action === 'copy').length;
	const parts = [copied ? `${count(copied, 'volume')} copied` : 'no volume data'];
	if (s.preview.blockers.length) parts.push(count(s.preview.blockers.length, 'problem'));
	if (s.preview.warnings.length) parts.push(count(s.preview.warnings.length, 'warning'));
	return parts.join(', ');
}

export type MoveTone = 'ok' | 'danger' | 'info' | 'neutral';

const MOVE_STATES: Record<MovedStack['state'], { label: string; tone: MoveTone }> = {
	pending: { label: 'Not Started', tone: 'neutral' },
	moving: { label: 'Moving', tone: 'info' },
	moved: { label: 'Moved', tone: 'ok' },
	failed: { label: 'Did Not Move', tone: 'danger' }
};

/** A stack's state in a run, in words, with its badge tone. */
export function moveState(state: MovedStack['state']): { label: string; tone: MoveTone } {
	return MOVE_STATES[state] ?? MOVE_STATES.pending;
}

/** What became of a moved stack's old copy on the source, in words (nothing for the others). */
export function oldCopyState(s: Pick<MovedStack, 'state' | 'sourceRemoved'>): string | undefined {
	if (s.state !== 'moved') return undefined;
	return s.sourceRemoved ? 'Old Copy Removed' : 'Old Copy Kept';
}

const ENDED_RUNS: readonly EnvironmentMigration['state'][] = [
	'completed',
	'failed',
	'cancelled',
	'interrupted'
];

/** The run has ended (its result stays to read; nothing moves any more). */
export function migrationEnded(r: Pick<EnvironmentMigration, 'state'>): boolean {
	return ENDED_RUNS.includes(r.state);
}

/** A run's stacks by outcome. */
export function migrationOutcome(r: Pick<EnvironmentMigration, 'stacks'>): {
	moved: MovedStack[];
	failed: MovedStack[];
	/** Every stack still on the source (failed or not started). */
	left: MovedStack[];
} {
	return {
		moved: r.stacks.filter((s) => s.state === 'moved'),
		failed: r.stacks.filter((s) => s.state === 'failed'),
		left: r.stacks.filter((s) => s.state !== 'moved')
	};
}

/** Every stack of the run moved (the source can be archived). */
export function everyStackMoved(r: Pick<EnvironmentMigration, 'stacks'>): boolean {
	return r.stacks.length > 0 && r.stacks.every((s) => s.state === 'moved');
}

/** A moved stack's stopped copy on the source, removable through its stack migration. */
export interface OldCopy {
	stackId: string;
	migrationId: string;
	title: string;
}

/** The old copies a run left stopped on the source (moved stacks with their stack migration). */
export function oldCopies(
	r: Pick<EnvironmentMigration, 'stacks'>,
	titleOf: TitleOf = plainTitle
): OldCopy[] {
	const out: OldCopy[] = [];
	for (const s of r.stacks)
		// A copy already removed (from the stack's page or earlier) is left out.
		if (s.state === 'moved' && s.migrationId && !s.sourceRemoved)
			out.push({
				stackId: s.stackId,
				migrationId: s.migrationId,
				title: titleOf(s.stackId, s.name)
			});
	return out;
}

/** A moved stack's old copy still on the source, with where the stack went. */
export interface PendingCopy extends OldCopy {
	/** The environment the stack moved to. */
	destinationId: string;
}

/**
 * The old copies still on the source after the ended runs (`records`
 * newest first, as the list answers): one per stack, the newest run's.
 * A run still moving is left out (its copies are offered once it ended).
 */
export function pendingCopies(
	records: readonly EnvironmentMigration[],
	titleOf: TitleOf = plainTitle
): PendingCopy[] {
	const out: PendingCopy[] = [];
	const seen = new Set<string>();
	for (const r of records) {
		if (!migrationEnded(r)) continue;
		for (const c of oldCopies(r, titleOf)) {
			if (seen.has(c.stackId)) continue;
			seen.add(c.stackId);
			out.push({ ...c, destinationId: r.targetEnvironmentId });
		}
	}
	return out;
}

/**
 * The stacks of a run still on the source (they did not move or did not
 * start) that can move again: `onSource` says which are there now (a
 * later migration or the stack's own may have moved one since).
 */
export function stacksLeft(
	r: Pick<EnvironmentMigration, 'stacks'>,
	onSource: (stackId: string) => boolean
): MovedStack[] {
	return r.stacks.filter((s) => s.state !== 'moved' && onSource(s.stackId));
}

/**
 * The run the migrate page opens on instead of a new migration: the
 * latest one (`records` newest first) once it ended, while something is
 * left to do: old copies to remove (of this run or an earlier one) or
 * stacks of this run still on the source. A running latest one is not
 * restored here: the running list reopens it at the move step.
 */
export function restoredMigration(
	records: readonly EnvironmentMigration[],
	onSource: (stackId: string) => boolean
): EnvironmentMigration | null {
	const latest = records[0];
	if (!latest || !migrationEnded(latest)) return null;
	if (stacksLeft(latest, onSource).length || pendingCopies(records).length) return latest;
	return null;
}

/**
 * The result view's notice about the moved stacks and their old copies:
 * `moved` stacks of this run, `copies` old copies left to remove (of this
 * run and earlier ones). Null: nothing moved and nothing to remove.
 */
export function resultNotice(
	moved: number,
	copies: number,
	names: { source: string; destination: string }
): { title: string; body: string } | null {
	const one = copies === 1;
	const keep = `Remove ${one ? 'it' : 'them'} once you are sure.`;
	if (moved) {
		const title = `${count(moved, 'stack')} ${moved === 1 ? 'runs' : 'run'} on ${names.destination} now.`;
		if (!copies)
			return {
				title,
				body: `${moved === 1 ? 'Its old copy was' : 'Their old copies were'} removed from ${names.source}.`
			};
		if (copies === moved)
			return {
				title,
				body: `${one ? 'Its old copy' : 'Their old copies'} on ${names.source} ${one ? 'is' : 'are'} stopped and kept. ${keep}`
			};
		return {
			title,
			body: `Old copies of ${count(copies, 'stack')} are stopped and kept on ${names.source}. ${keep}`
		};
	}
	if (!copies) return null;
	return {
		title: `${count(copies, 'old copy', 'old copies')} still on ${names.source}`,
		body: `${one ? 'It is' : 'They are'} stopped and kept from an earlier migration. ${keep}`
	};
}

/**
 * The environment page's notice while moved stacks' old copies are still
 * on it: "3 stacks moved to NAS" / "Their old copies are still on this
 * server." (null: nothing to remove).
 */
export function oldCopiesNotice(
	records: readonly EnvironmentMigration[],
	envName: (id: string) => string
): { title: string; body: string; count: number } | null {
	const copies = pendingCopies(records);
	if (!copies.length) return null;
	const to = [...new Set(copies.map((c) => c.destinationId))];
	const where = to.length === 1 ? envName(to[0]) : 'other environments';
	return {
		title: `${count(copies.length, 'stack')} moved to ${where}`,
		body:
			copies.length === 1
				? 'Its old copy is still on this server.'
				: 'Their old copies are still on this server.',
		count: copies.length
	};
}

/** The toast when a run ends, and the job "Open Job" opens (the failed stack's migration). */
export interface FinishToast {
	tone: 'success' | 'warn' | 'error';
	title: string;
	body?: string;
	jobId?: string;
}

/**
 * The toast of an ended run: "Migrated 6 stacks to NAS", or which stack
 * did not move and what that means for the others. Without the record
 * (it could not be read) the job's state decides alone.
 */
export function finishToast(
	r: Pick<EnvironmentMigration, 'stacks'> | undefined,
	job: Pick<Job, 'id' | 'state'>,
	names: { source: string; destination: string },
	titleOf: TitleOf = plainTitle
): FinishToast {
	const o = r ? migrationOutcome(r) : { moved: [], failed: [], left: [] };
	if (job.state === 'succeeded')
		return {
			tone: 'success',
			title: r
				? `Migrated ${count(o.moved.length, 'stack')} to ${names.destination}`
				: `Migrated ${names.source} to ${names.destination}`
		};
	const rest = o.moved.length
		? `${count(o.moved.length, 'stack')} moved to ${names.destination} and ${o.moved.length === 1 ? 'stays' : 'stay'} there. The stacks that did not move run on ${names.source} again; migrate again to move them.`
		: `Nothing moved: the stacks run on ${names.source} again.`;
	if (job.state === 'cancelled')
		return {
			tone: 'warn',
			title: `The migration of ${names.source} was cancelled`,
			body: rest
		};
	const failed = o.failed[0];
	if (failed)
		return {
			tone: 'error',
			title: `${titleOf(failed.stackId, failed.name)} did not move to ${names.destination}`,
			body: rest,
			jobId: failed.migrationId ?? job.id
		};
	return {
		tone: 'error',
		title: `The migration of ${names.source} stopped`,
		body: rest,
		jobId: job.id
	};
}
