// Plain-language names for jobs (#22 copy rules, #26 job model): what a job
// does, what it acts on, why it exists and how long it took. Pure; `now`
// is injectable for tests.
import type { Job } from '$lib/api/client';
import { formatDuration } from '$lib/ui/format';
import { routes } from '$lib/routes';

/** Every job kind of the catalog (docs/architecture/job-engine.md). */
export const JOB_KIND_LABELS: Record<string, string> = {
	'backup.import': 'Import backups',
	'backup.retention': 'Apply backup retention',
	'backup.run': 'Back up',
	'backup.verify': 'Verify backup repository',
	'container.create': 'Create container',
	'container.pause': 'Pause container',
	'container.remove': 'Remove container',
	'container.restart': 'Restart container',
	'container.start': 'Start container',
	'container.stop': 'Stop container',
	'container.unpause': 'Resume container',
	'container.update': 'Update container settings',
	'files.archive': 'Archive files',
	'files.copy': 'Copy files',
	'files.delete': 'Delete files',
	'files.extract': 'Extract archive',
	'files.metadata': 'Change file permissions',
	'files.move': 'Move files',
	'image.build': 'Build image',
	'image.pull': 'Pull image',
	'image.remove': 'Remove image',
	'manager.backup': 'Back up DockYard',
	'manager.retention': 'Apply DockYard backup retention',
	'manager.verify': 'Verify DockYard backups',
	'network.create': 'Create network',
	'network.remove': 'Remove network',
	'prune.run': 'Prune Docker objects',
	'restore.run': 'Restore',
	'stack.build': 'Build stack images',
	'stack.deploy': 'Deploy stack',
	'stack.down': 'Take stack down',
	'stack.migrate': 'Migrate stack',
	'stack.remove': 'Delete stack',
	'stack.remove_source': 'Remove migrated stack',
	'stack.restart': 'Restart stack',
	'stack.start': 'Start stack',
	'stack.stop': 'Stop stack',
	'stack.update': 'Update stack images',
	'update.check': 'Check for updates',
	'update.run': 'Apply updates',
	'volume.create': 'Create volume',
	'volume.migrate': 'Migrate volume',
	'volume.remove': 'Remove volume'
};

/** "Deploy stack"; unknown kinds read as their words ("Foo bar"). */
export function jobKindLabel(kind: string | undefined): string {
	if (!kind) return 'Job';
	const known = JOB_KIND_LABELS[kind];
	if (known) return known;
	const s = kind.replaceAll('.', ' ').replaceAll('_', ' ');
	return s[0].toUpperCase() + s.slice(1);
}

export const ORIGIN_LABELS: Record<Job['origin'], string> = {
	manual: 'Manual',
	scheduled: 'Scheduled',
	api_token: 'API token'
};

const TARGET_NOUNS: Record<string, string> = {
	stack: 'stack',
	container: 'container',
	volume: 'volume',
	image: 'image',
	network: 'network',
	repository: 'repository',
	path: 'path',
	destination_path: 'destination',
	build_definition: 'build',
	maintenance_policy: 'prune policy'
};

/** Resolves IDs to names where the page knows them (stacks, policies). */
export type NameOf = (type: string, id: string) => string | undefined;

/** What the job acts on: "homeassistant", "Silo and 2 more", or "". */
export function jobTargetLabel(job: Pick<Job, 'targets'>, nameOf?: NameOf): string {
	const t = job.targets ?? [];
	if (!t.length) return '';
	const first = targetName(t[0].type, t[0].id, nameOf);
	return t.length === 1 ? first : `${first} and ${t.length - 1} more`;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** A target's name: resolved by the page, its ID when it is a name (containers,
 * images, volumes), or its kind ("prune policy") when the ID is an opaque UUID. */
export function targetName(type: string, id: string, nameOf?: NameOf): string {
	const n = nameOf?.(type, id);
	if (n) return n;
	return UUID.test(id) ? (TARGET_NOUNS[type] ?? type.replaceAll('_', ' ')) : id;
}

/** "Restart container homeassistant"; opaque targets (policy IDs) are left out. */
export function jobTitle(job: Pick<Job, 'kind' | 'targets'>, nameOf?: NameOf): string {
	const t = job.targets ?? [];
	const kind = jobKindLabel(job.kind);
	if (t.length !== 1) return kind;
	const n = nameOf?.(t[0].type, t[0].id) ?? (UUID.test(t[0].id) ? '' : t[0].id);
	return n ? `${kind} ${n}` : kind;
}

/** Where the policy that started a job is managed, by job kind. */
export function policyPage(kind: string): { href: string; label: string } {
	if (kind.startsWith('prune.')) return { href: routes.maintenance(), label: 'Prune policy' };
	if (kind.startsWith('update.')) return { href: routes.updates(), label: 'Update policy' };
	if (kind.startsWith('backup.') || kind.startsWith('manager.'))
		return { href: routes.backupPolicies(), label: 'Backup policy' };
	return { href: routes.schedules(), label: 'Policy' };
}

/** "container homeassistant" (the noun helps where kinds are mixed). */
export function targetText(type: string, id: string, nameOf?: NameOf): string {
	return `${TARGET_NOUNS[type] ?? type.replaceAll('_', ' ')} ${nameOf?.(type, id) ?? id}`;
}

/** Resolves stack IDs to their display names (other types stay unresolved). */
export function stackNames(
	stacks: readonly { id: string; name: string; displayName?: string }[] | undefined
): NameOf {
	const m = new Map((stacks ?? []).map((s) => [s.id, s.displayName || s.name]));
	return (type, id) => (type === 'stack' ? m.get(id) : undefined);
}

/** Wall time of the job so far or in total ("45 s", "3 min"), or "". */
export function jobDuration(
	job: Pick<Job, 'createdAt' | 'startedAt' | 'finishedAt'>,
	now: Date = new Date()
): string {
	const start = Date.parse(job.startedAt ?? job.createdAt);
	const end = job.finishedAt ? Date.parse(job.finishedAt) : now.getTime();
	if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return '';
	return formatDuration((end - start) / 1000);
}

/** The job is still going (not in a terminal state). */
export function jobActive(state: Job['state']): boolean {
	return ['queued', 'blocked', 'dispatched', 'running', 'cancelling'].includes(state);
}

/** Groups of states offered by the jobs filter. */
export const STATE_FILTERS: { id: string; label: string; states: Job['state'][] }[] = [
	{ id: '', label: 'All states', states: [] },
	{
		id: 'active',
		label: 'In progress',
		states: ['queued', 'blocked', 'dispatched', 'running', 'cancelling']
	},
	{
		id: 'problems',
		label: 'Failed or partly failed',
		states: ['failed', 'partial', 'interrupted']
	},
	{ id: 'succeeded', label: 'Succeeded', states: ['succeeded'] },
	{ id: 'cancelled', label: 'Cancelled', states: ['cancelled'] },
	{ id: 'blocked', label: 'Blocked', states: ['blocked'] }
];

/** Why a job waits, in the user's words. */
export function blockedText(b: NonNullable<Job['blockedBy']>): string {
	switch (b.reason) {
		case 'agent_offline':
			return 'Waiting for the environment to come back online.';
		case 'lock':
			return 'Waiting for another job on the same resources to finish.';
		default:
			return 'Waiting for a free slot: this environment runs a limited number of pulls and builds at once.';
	}
}
