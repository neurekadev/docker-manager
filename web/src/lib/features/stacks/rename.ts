// Stack rename view model (#7): the checks of the header's inline rename
// (RenameStackInline) and whether a rename is running. Pure; tested in
// rename.spec.ts.
import type { Job, Schema } from '$lib/api/client';
import { isActiveJobState } from '$lib/api/job-states';
import { nameError } from './model';

export type RenamePreview = Schema<'StackRenamePreview'>;

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

/**
 * Why the server's rename preview refuses renaming the stack to `to`, in
 * words for the field's error: a Compose file whose `name:` fixes the
 * project name, else the preview's blockers (a name already taken, ...).
 * Undefined when the rename can start.
 */
export function renameRefusal(
	to: string,
	current: string,
	preview: Pick<RenamePreview, 'declaredName' | 'blockers'>
): string | undefined {
	const locked = lockedName(current, preview);
	if (locked && locked !== to)
		return `Its Compose file sets name: ${locked}. The stack can only take that name; to choose another, change name: in the file.`;
	if (preview.blockers.length) return preview.blockers.map((b) => b.message).join(' ');
	return undefined;
}

/** A rename of the stack that has not ended (the header turns its actions off). */
export function activeRename(jobs: readonly Job[] | undefined): Job | undefined {
	return jobs?.find((j) => j.kind === 'stack.rename' && isActiveJobState(j.state));
}

/** Guidance for a deploy refused because the files now set another project name. */
export function stackJobGuidance(
	error: { class?: string; message?: string; recovery?: string } | undefined
): string | undefined {
	if (error?.class === 'stack_project_renamed')
		return 'Its Compose file now sets a different project name (name:). Rename the stack to that name with the pencil next to its name, or change name: back in the file.';
	return error?.recovery ?? error?.message;
}
