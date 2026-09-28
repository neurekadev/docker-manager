// Import project view model (#7): how Docker Manager imports a discovered
// Compose project (adopted in place, copied while it runs, copied after
// stopping it, or not at all; a project without containers is taken over
// without starting anything), in one line and in detail, what a copy that
// stops running services does, its volumes in one line, and the import's
// errors in words. Pure; tested in importing.spec.ts.
import type { Job, Schema } from '$lib/api/client';

type Project = Pick<
	Schema<'DiscoveredStack'>,
	'adoptable' | 'copyable' | 'protected' | 'reason' | 'services' | 'stackId' | 'containerless'
>;

/**
 * managed: Docker Manager manages it already; adopt: its files lie in a
 * stack root and it is taken over where it is; copy-live: Docker
 * Manager's own project, copied while it runs; copy-stop: copied after
 * stopping its running services; copy-stopped: copied, nothing runs
 * (also a project without containers, `containerless`); blocked: cannot
 * be imported as it is.
 */
export type ImportMode =
	'managed' | 'adopt' | 'copy-live' | 'copy-stop' | 'copy-stopped' | 'blocked';

export function importMode(p: Project): ImportMode {
	if (p.stackId) return 'managed';
	if (p.adoptable) return 'adopt';
	if (!p.copyable) return 'blocked';
	if (p.protected) return 'copy-live';
	return runningServices(p) > 0 ? 'copy-stop' : 'copy-stopped';
}

/** Services with at least one running container. */
export function runningServices(p: Pick<Project, 'services'>): number {
	return p.services.filter((s) => s.running > 0).length;
}

/** Running and all containers of a project. */
export function containerCounts(p: Pick<Project, 'services'>): { up: number; total: number } {
	return p.services.reduce((n, s) => ({ up: n.up + s.running, total: n.total + s.containers }), {
		up: 0,
		total: 0
	});
}

/** The status a project's containers add up to. */
export function projectStatus(c: { up: number; total: number }): 'stopped' | 'partial' | 'running' {
	if (c.up === 0) return 'stopped';
	return c.up < c.total ? 'partial' : 'running';
}

const services = (n: number) => `${n} running ${n === 1 ? 'service' : 'services'}`;

/** How the project is imported, in one line. */
export function importHow(p: Project): string {
	const mode = importMode(p);
	if (p.containerless && mode === 'adopt')
		return 'Taken over where it is. Nothing starts: deploy it afterwards.';
	if (p.containerless && mode === 'copy-stopped')
		return 'Copied into Docker Manager. Nothing starts: deploy it afterwards.';
	switch (mode) {
		case 'managed':
			return 'Docker Manager manages it already.';
		case 'adopt':
			return 'Taken over where it is. Nothing restarts.';
		case 'copy-live':
			return 'Copied while it keeps running. Nothing stops.';
		case 'copy-stop': {
			const n = runningServices(p);
			return `Copied into Docker Manager. Its ${services(n)} stop${n === 1 ? 's' : ''} briefly.`;
		}
		case 'copy-stopped':
			return 'Copied into Docker Manager. It stays stopped.';
		case 'blocked':
			return "Can't be imported as it is.";
	}
}

const CHECK =
	"Before anything changes it checks that the running containers match the project's files, and changes nothing if they do not. If you edited the files in another tool without redeploying, redeploy there first.";

const NO_CONTAINERS =
	"It has no containers, so nothing starts. Deploy the stack when you're ready: it keeps the project's name, so it uses the project's volumes again.";

/** The import in detail, one sentence per line (behind "Details"). */
export function importDetails(p: Project): string[] {
	const mode = importMode(p);
	if (p.containerless && mode === 'adopt')
		return [
			'Its files already lie in a folder Docker Manager manages, so the project is taken over where it is.',
			NO_CONTAINERS
		];
	if (p.containerless && mode === 'copy-stopped')
		return [
			'Copies its whole folder, data folders included (owners and permissions kept), into Docker Manager and checks the copy.',
			NO_CONTAINERS,
			'Leaves the original folder untouched.'
		];
	switch (mode) {
		case 'managed':
			return [];
		case 'adopt':
			return [
				'Its files already lie in a folder Docker Manager manages, so the project is taken over where it is.'
			];
		case 'copy-live':
			return [
				"Docker Manager's own project: its whole folder is copied and checked while it keeps running.",
				'Docker Manager moves onto the copy the next time you deploy the stack.',
				'The original folder is left untouched.',
				CHECK
			];
		case 'copy-stop':
			return [...importConsequences(p), CHECK];
		case 'copy-stopped':
			return [
				'Copies its whole folder, data folders included (owners and permissions kept), into Docker Manager and checks the copy.',
				'Recreates its containers from the copy; it stays stopped.',
				'Leaves the original folder untouched.',
				CHECK
			];
		case 'blocked':
			return [
				asSentence(p.reason ?? "Docker Manager can't read the project's folder."),
				'Usually the agent has to read its folder: mount it read-only below /import on the agent, then refresh.'
			];
	}
}

/**
 * What importing a project by copy does when that stops running services
 * (shown in a confirmation first); empty when nothing stops.
 */
export function importConsequences(p: Project): string[] {
	if (importMode(p) !== 'copy-stop') return [];
	const n = runningServices(p);
	return [
		`Stops its ${services(n)} while the files are copied.`,
		'Copies its whole folder, data folders included (owners and permissions kept), into Docker Manager and checks the copy.',
		`Starts ${n === 1 ? 'the service' : 'the services'} again from the copy.`,
		'Leaves the original folder untouched.'
	];
}

/** Whether Import asks for confirmation first (it stops running services). */
export function importNeedsConfirm(p: Project): boolean {
	return importMode(p) === 'copy-stop';
}

/** The stack an import job created (its stack target). */
export function jobStackId(job: Pick<Job, 'targets'>): string | undefined {
	return job.targets.find((t) => t.type === 'stack')?.id;
}

/** An agent's reason as a sentence: capital first letter, final stop. */
export function asSentence(s: string): string {
	const t = s.trim();
	return t ? `${t[0].toUpperCase()}${t.slice(1)}${/[.!?]$/.test(t) ? '' : '.'}` : t;
}

/**
 * A project's volumes in one short line: "Volumes: a, b, c +2 more" (the
 * first `max`); empty when it has none.
 */
export function volumesLine(volumes: string[] | undefined, max = 3): string {
	const list = volumes ?? [];
	if (list.length === 0) return '';
	const shown = list.slice(0, max).join(', ');
	return list.length > max ? `Volumes: ${shown} +${list.length - max} more` : `Volumes: ${shown}`;
}

/** A failed import in words: what happened and what to do. */
export function importFailure(
	v: { code?: string; message: string },
	name: string,
	containerless = false
): string {
	switch (v.code) {
		case 'stack_name_taken':
			return `Docker Manager already manages a stack named ${name} here. Nothing was changed.`;
		case 'stack_directory_exists':
			return `Docker Manager already has a folder named ${name}. Nothing was overwritten: move that folder away, then import again.`;
		case 'stack_not_adoptable':
		case 'stack_not_copyable':
			return `${name} can't be imported: ${v.message}`;
		case 'agent_unsupported':
			return containerless
				? 'The agent of this environment cannot import projects without containers by copy yet. Update the agent, then import again.'
				: 'The agent of this environment cannot import projects by copy yet. Update the agent, then import again.';
	}
	return v.message;
}
