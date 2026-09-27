// Stack rename view model (#7): what renaming a stack's Compose project does,
// in plain words, from the server's rename preview. Pure; tested in
// rename.spec.ts.
import type { Schema } from '$lib/api/client';
import { nameError } from './model';

export type RenamePreview = Schema<'StackRenamePreview'>;
export type RenameVolume = Schema<'StackRenameVolume'>;
export type RenameContainer = Schema<'StackRenameContainer'>;

/** The naming rule shown under the name field. */
export const RENAME_RULE =
	'Lower-case letters, digits, dashes and underscores, starting with a letter or digit (at most 63).';

/** Why `name` cannot be the stack's new project name, or undefined. */
export function renameNameError(name: string, current: string): string | undefined {
	const invalid = nameError(name);
	if (invalid) return invalid;
	if (name.trim() === current) return 'The stack already has this name.';
	return undefined;
}

/**
 * The only name a stack can take when its Compose file sets a top-level
 * `name:` other than its current name (the files decide the project name);
 * undefined when the user may choose freely.
 */
export function lockedName(current: string, preview?: Pick<RenamePreview, 'declaredName'>) {
	const declared = preview?.declaredName;
	return declared && declared !== current ? declared : undefined;
}

/** What happens to a volume, in words users can act on. */
export function volumeNote(v: Pick<RenameVolume, 'action'>): string {
	switch (v.action) {
		case 'move':
			return 'Its data moves to the new name.';
		case 'recreate':
			return 'Same host path or remote storage under the new name; the data stays where it is.';
		case 'absent':
			return 'Does not exist yet; created on the first start.';
	}
	return '';
}

/** "shop_data → store_data", or the anonymous volume's mount. */
export function volumeLabel(v: RenameVolume): string {
	const from = v.key ? v.name : `${v.service ?? 'anonymous'}:${v.target ?? v.name}`;
	return v.newName ? `${from} → ${v.newName}` : from;
}

/** An outside container's label (hidden ones are counted, not named). */
export function containerLabel(c: RenameContainer): string {
	return c.hidden ? 'A container you cannot see' : (c.name ?? c.id ?? 'container');
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/**
 * The consequences the confirmation lists: what stops, what moves and what
 * is recreated.
 */
export function renameConsequences(p: RenamePreview): string[] {
	const out: string[] = [];
	out.push(
		p.running.length
			? `Stops ${plural(p.running.length, 'running service')} (${p.running.join(', ')}) and starts them again as ${p.to}.`
			: `No service runs now; the containers are recreated as ${p.to} and stay stopped.`
	);
	const moved = p.volumes.filter((v) => v.action !== 'absent').length;
	if (moved) out.push(`Moves ${plural(moved, 'volume')} to the new name.`);
	out.push(
		p.fromDir === p.toDir
			? `Keeps the project folder ${p.fromDir}.`
			: `Renames the project folder ${p.fromDir} to ${p.toDir}.`
	);
	if (p.containers.length)
		out.push(
			`Stops and recreates ${plural(p.containers.length, 'container')} outside the stack that ${p.containers.length === 1 ? 'uses' : 'use'} these volumes.`
		);
	out.push('The stack keeps its history, policies and permissions.');
	return out;
}

/** The preview can be confirmed for `name`: it is the previewed name and nothing blocks. */
export function canRename(name: string, current: string, preview?: RenamePreview): boolean {
	return (
		!!preview &&
		!renameNameError(name, current) &&
		preview.to === name.trim() &&
		preview.blockers.length === 0
	);
}

/** Guidance for a deploy refused because the files now set another project name. */
export function stackJobGuidance(
	error: { class?: string; message?: string; recovery?: string } | undefined
): string | undefined {
	if (error?.class === 'stack_project_renamed')
		return 'Its Compose file now sets a different project name (name:). Rename the stack to that name under More stack actions → Rename, or change name: back in the file.';
	return error?.recovery ?? error?.message;
}
