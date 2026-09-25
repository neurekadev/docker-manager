// Compose sources of a stack (#7, #15, #25 Q1): saving one of them records
// a new stack revision and marks undeployed changes; nothing is deployed.
// Mirrors stacks.IsDefinitionFile on the manager (the manager decides; this
// only chooses what the editor tells the user).

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

/** Whether `path` (root-relative) is a Compose source of the stack. */
export function isDefinitionFile(path: string, configFiles: readonly string[] = []): boolean {
	return DEFINITION_NAMES.includes(path) || configFiles.includes(path);
}
