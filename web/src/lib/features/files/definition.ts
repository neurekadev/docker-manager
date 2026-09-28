// Compose sources of a stack (#7, #15, #25 Q1): saving one of them records
// a new stack revision and marks undeployed changes; nothing is deployed.
// A save that would leave the definition invalid is refused
// (invalid_definition) and nothing is written.
// Mirrors stacks.IsDefinitionFile on the manager (the manager decides; this
// only chooses what the editor tells the user).
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

/** Compose files searched in the project directory, in order (agent compose.DefaultConfigFiles). */
export const DEFAULT_COMPOSE_FILES = [
	'compose.yaml',
	'compose.yml',
	'docker-compose.yaml',
	'docker-compose.yml'
];

/** What the file manager knows of the stack whose files it shows. */
export interface StackFiles {
	name: string;
	configFiles: string[];
	revisionsHref?: string;
	/** Validation after saving a Compose source (POST /stacks/validations). */
	environmentId?: string;
	/** The Compose project name (the stack's `name`). */
	projectName?: string;
	/** The saved definition differs from the deployed one. */
	undeployed?: boolean;
	/** Starts a deploy of the definition on disk; absent without stack.deploy. */
	deploy?: () => Promise<void>;
}

/** The definition files a validation reads (root-relative paths). */
export interface DefinitionFiles {
	compose: string;
	override?: string;
	env?: string;
}

/**
 * The files POST /stacks/validations takes for a stack (compose, override,
 * .env), from its explicit Compose files or the names in its project
 * directory (the first default Compose file and its matching
 * `<name>.override.<ext>`, like the agent). Null when the definition does
 * not fit that request: no Compose file, or more than two explicit files.
 */
export function definitionFiles(
	configFiles: readonly string[],
	rootNames: readonly string[]
): DefinitionFiles | null {
	const env = rootNames.includes('.env') ? '.env' : undefined;
	if (configFiles.length > 2) return null;
	if (configFiles.length) return { compose: configFiles[0], override: configFiles[1], env };
	const compose = DEFAULT_COMPOSE_FILES.find((n) => rootNames.includes(n));
	if (!compose) return null;
	const ext = compose.slice(compose.lastIndexOf('.'));
	const override = `${compose.slice(0, -ext.length)}.override${ext}`;
	return { compose, override: rootNames.includes(override) ? override : undefined, env };
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
