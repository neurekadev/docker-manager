// Compose sources of a stack (#7, #15, #25 Q1): saving one of them records
// a new stack revision and marks undeployed changes; nothing is deployed.
// A save that would leave the definition invalid is refused
// (invalid_definition) and nothing is written.
// Mirrors stacks.IsDefinitionFile on the manager (the manager decides; this
// only chooses what the editor tells the user).
import type { Schema } from '$lib/api/client';
import { errorView } from '$lib/ui/errors';

export const DEFINITION_NAMES = [
	'compose.yaml',
	'compose.yml',
	'docker-compose.yaml',
	'docker-compose.yml',
	'compose.override.yaml',
	'compose.override.yml',
	'docker-compose.override.yaml',
	'docker-compose.override.yml',
	'.env'
];

/** What the file manager knows of the stack whose files it shows. */
export interface StackFiles {
	name: string;
	configFiles: string[];
	revisionsHref?: string;
	/**
	 * Validates the stack's definition on disk after a Compose source was
	 * saved (POST /stacks/{stackId}/validations); absent without
	 * stack.definition.write.
	 */
	validate?: () => Promise<Schema<'StackValidation'>>;
	/** The saved definition differs from the deployed one. */
	undeployed?: boolean;
	/** Starts a deploy of the definition on disk; absent without stack.deploy. */
	deploy?: () => Promise<void>;
}

/** Whether `path` (root-relative) is a Compose source of the stack. */
export function isDefinitionFile(path: string, configFiles: readonly string[] = []): boolean {
	return DEFINITION_NAMES.includes(path) || configFiles.includes(path);
}

/**
 * The toast body of a save refused because the Compose definition would
 * be invalid (422 invalid_definition), or null for any other error.
 */
export function definitionRefusal(e: unknown): string | null {
	const v = errorView(e);
	if (v.code !== 'invalid_definition') return null;
	const findings = v.fields.map((f) => f.message).slice(0, 3);
	const more = v.fields.length > 3 ? ` (and ${v.fields.length - 3} more)` : '';
	return `Nothing was written: the Compose definition would be invalid${
		findings.length ? `: ${findings.join('; ')}${more}` : ''
	}. Your edits are kept; fix them and save again.`;
}
