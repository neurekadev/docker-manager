// Create stack view model (#7): why the dialog cannot create (or check)
// the stack yet, in words shown beside its disabled buttons. Pure; tested
// in creating.spec.ts.
import { nameError } from './model';

export interface CreateDraft {
	/** The chosen environment, if any. */
	environment?: { name: string; online: boolean };
	name: string;
	compose: string;
}

/**
 * What keeps Validate (`offlineBlocks` false) or Create (true) disabled,
 * or undefined when the draft is ready.
 */
export function createBlocker(d: CreateDraft, offlineBlocks = true): string | undefined {
	if (!d.environment) return 'Choose an environment for the stack.';
	if (!d.name.trim()) return 'Enter a name for the stack.';
	if (nameError(d.name)) return 'Fix the name to continue.';
	if (!d.compose.trim()) return 'Add a Compose file.';
	if (offlineBlocks && !d.environment.online)
		return `${d.environment.name} is offline. Create the stack when it is back.`;
	return undefined;
}
