// Plain-language names for jobs (#22 copy rules, #26 job model): what a job
// does, what it acts on, why it exists and how long it took. Pure; `now`
// is injectable for tests.
import type { Job } from '$lib/api/client';
import { ACTIVE_JOB_STATES, isActiveJobState } from '$lib/api/job-states';
import { formatDuration } from '$lib/ui/format';
import { routes } from '$lib/routes';

/** Every job kind of the catalog (docs/internal/architecture/job-engine.md). */
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
	'environment.migrate': 'Migrate environment',
	'files.archive': 'Archive files',
	'files.copy': 'Copy files',
	'files.delete': 'Delete files',
	'files.extract': 'Extract archive',
	'files.metadata': 'Change file permissions',
	'files.move': 'Move files',
	'image.build': 'Build image',
	'image.pull': 'Pull image',
	'image.remove': 'Remove image',
	'manager.backup': 'Back up Docker Manager',
	'manager.move': 'Move every app to the new server',
	'manager.retention': 'Apply Docker Manager backup retention',
	'manager.verify': 'Verify Docker Manager backups',
	'network.create': 'Create network',
	'network.remove': 'Remove network',
	'prune.run': 'Prune Docker objects',
	'restore.run': 'Restore',
	'stack.build': 'Build stack images',
	'stack.deploy': 'Deploy stack',
	'stack.down': 'Take stack down',
	'stack.import': 'Import project',
	'stack.migrate': 'Migrate stack',
	'stack.pull': 'Pull stack images',
	'stack.remove': 'Delete stack',
	'stack.remove_source': 'Remove migrated stack',
	'stack.rename': 'Rename stack',
	'stack.restart': 'Restart stack',
	'stack.start': 'Start stack',
	'stack.stop': 'Stop stack',
	'stack.update': 'Update stack images',
	'template.files.archive': 'Archive template files',
	'template.files.copy': 'Copy template files',
	'template.files.delete': 'Delete template files',
	'template.files.extract': 'Extract archive in a template',
	'template.files.metadata': 'Change template file permissions',
	'template.files.move': 'Move template files',
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

/** A job row's two lines: what it acts on first, then what it does. */
export interface JobHeadline {
	/** The target's human name ("zerobyte", "Silo and 2 more"), else the kind. */
	title: string;
	/** The kind ("Check for updates") when the title is a name, else `fallback` or "". */
	subtitle: string;
}

/** The last segment of a path target ("/srv/app/compose.yaml" → "compose.yaml"). */
function pathName(p: string): string {
	const parts = p.split('/').filter(Boolean);
	return parts.length ? parts[parts.length - 1] : p;
}

/**
 * Kinds that act on a whole environment: their headline leads with the
 * environment's name (`fallback`), not with the first of their targets.
 */
const ENVIRONMENT_KINDS = new Set(['environment.migrate']);

/**
 * The headline of a job row, leading with the target's name:
 * `{ title: 'zerobyte', subtitle: 'Check for updates' }`. Targets are named
 * by `nameOf` (stack IDs through `stackNames(stacks)`, policies by the
 * page's policy list), else by their ID when it is a name (containers,
 * volumes, networks, images; paths by their last segment). Jobs without a
 * nameable target (a prune policy's run) lead with the kind, and the
 * subtitle is `fallback` (e.g. the environment's name). Jobs on a whole
 * environment (an environment migration) lead with `fallback`.
 */
export function jobHeadline(
	job: Pick<Job, 'kind' | 'targets'>,
	options: { nameOf?: NameOf; fallback?: string } = {}
): JobHeadline {
	const kind = jobKindLabel(job.kind);
	if (ENVIRONMENT_KINDS.has(job.kind))
		return options.fallback
			? { title: options.fallback, subtitle: kind }
			: { title: kind, subtitle: '' };
	const t = job.targets ?? [];
	const first = t[0];
	let name = first ? options.nameOf?.(first.type, first.id) : undefined;
	if (!name && first && !UUID.test(first.id))
		name =
			first.type === 'path' || first.type === 'destination_path'
				? pathName(first.id)
				: first.id;
	if (!name) return { title: kind, subtitle: options.fallback ?? '' };
	return { title: t.length > 1 ? `${name} and ${t.length - 1} more` : name, subtitle: kind };
}

/**
 * The policy that started a job: its own page when the job names it
 * (`policyId`), else the section where policies of the kind are managed.
 */
export function policyPage(kind: string, policyId?: string): { href: string; label: string } {
	if (kind.startsWith('prune.'))
		return {
			href: policyId ? routes.maintenancePolicy(policyId) : routes.maintenance(),
			label: 'Maintenance policy'
		};
	if (kind.startsWith('update.'))
		return {
			href: policyId ? routes.updatePolicy(policyId) : routes.updates(),
			label: 'Update policy'
		};
	if (kind === 'backup.verify' || kind === 'manager.verify')
		return {
			href: policyId ? routes.backupRepository(policyId) : routes.backupRepositories(),
			label: 'Backup repository'
		};
	if (kind.startsWith('backup.') || kind.startsWith('manager.'))
		return {
			href: policyId ? routes.backupPolicy(policyId) : routes.backupPolicies(),
			label: 'Backup policy'
		};
	return { href: routes.schedules(), label: 'Policy' };
}

/** A failed job's error class as a headline (the engine's message goes below it). */
const ERROR_HEADLINES: Record<string, string> = {
	agent_offline: 'The environment was offline',
	authorization_revoked: 'The permission to run it was withdrawn',
	step_failed: 'A step failed',
	unknown_outcome: 'It is unknown whether the last step finished',
	journal_lost: 'The environment lost track of the job',
	resume_limit: 'It was interrupted too often',
	rejected: 'The environment refused the job',
	compensation_failed: 'Cleaning up after a failure failed',
	executor_restarted: 'It stopped when its runner restarted',
	cancelled: 'It was cancelled',
	credential_unavailable: 'A credential it needs is gone',
	policy_rejected: 'Its policy refused the run'
};

/** "A step failed"; unknown classes read as "The job failed". */
export function jobErrorHeadline(error: { class: string } | undefined, state: string): string {
	if (state === 'cancelled') return 'It was cancelled';
	return (
		(error && ERROR_HEADLINES[error.class]) ??
		(state === 'partial' ? 'It partly failed' : 'The job failed')
	);
}

/**
 * Where to run a finished job again when the server cannot retry it
 * (`retryable` is false: its kind has no retry, or its input was not
 * kept): the page of the action that started it (the stack, the
 * container, the policy, the build), or null when there is none.
 */
export function jobAgain(
	job: Pick<Job, 'id' | 'kind' | 'targets' | 'environmentId' | 'policyId'>
): { href: string; label: string } | null {
	const t = job.targets?.[0];
	if (job.policyId && /^(update|prune|backup|manager)\./.test(job.kind))
		return {
			href: policyPage(job.kind, job.policyId).href,
			label: 'Open the policy to run it again'
		};
	if (job.kind === 'image.build' && job.environmentId)
		return {
			href: routes.build(job.environmentId, job.id),
			label: 'Open the build to build again'
		};
	if (t?.type === 'stack')
		return { href: routes.stack(t.id), label: 'Open the stack to try again' };
	if (t?.type === 'container' && job.environmentId && !job.kind.endsWith('.remove'))
		return {
			href: routes.container(t.environmentId ?? job.environmentId, t.id),
			label: 'Open the container to try again'
		};
	return null;
}

/**
 * How the job page offers to try a job again: 'retry' when the server can
 * start it again (`retryable`: it ended unsuccessfully, its kind can be
 * retried and the caller may start it; POST /jobs/{jobId}/retries), else
 * the page of the originating action for a failed job (`jobAgain`), else
 * null (it is running, succeeded or was cancelled).
 */
export function jobRetry(
	job: Pick<Job, 'id' | 'kind' | 'state' | 'targets' | 'environmentId' | 'policyId'> & {
		retryable?: boolean;
	}
): 'retry' | { href: string; label: string } | null {
	if (job.retryable) return 'retry';
	if (!['failed', 'partial', 'interrupted'].includes(job.state)) return null;
	return jobAgain(job);
}

/** Why a retry did not start, by API code (other codes: the server's message). */
export const RETRY_ERRORS: Record<string, string> = {
	job_not_retryable:
		'This job can no longer be tried again (it is running, succeeded or its input was not kept). Reload the page to see its current state.',
	environment_archived:
		'Its environment is archived: archived environments keep their history but run no jobs.',
	forbidden: 'You are not allowed to start this action again. Ask an administrator for access.',
	idempotency_key_reused: 'This retry was already sent. Reload the page to see the new job.'
};

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
	return isActiveJobState(state);
}

/** Groups of states offered by the jobs filter. */
export const STATE_FILTERS: { id: string; label: string; states: Job['state'][] }[] = [
	{ id: '', label: 'All states', states: [] },
	{ id: 'active', label: 'In progress', states: [...ACTIVE_JOB_STATES] },
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

/** "Sep 27, 2026, 16:54:03": a job timeline's instants, to the second ("—" if absent). */
export function timelineTime(iso: string | undefined | null, timeZone?: string): string {
	if (!iso) return '—';
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return '—';
	return new Intl.DateTimeFormat('en', {
		month: 'short',
		day: 'numeric',
		year: 'numeric',
		hour: '2-digit',
		minute: '2-digit',
		second: '2-digit',
		hourCycle: 'h23',
		timeZone
	}).format(d);
}

/** A job target's page, where it has one (stacks, containers, volumes, networks). */
export function targetHref(
	t: { type: string; id: string; environmentId?: string },
	environmentId: string | undefined
): string | undefined {
	const env = t.environmentId ?? environmentId;
	switch (t.type) {
		case 'stack':
			return routes.stack(t.id);
		case 'container':
			return env ? routes.container(env, t.id) : undefined;
		case 'volume':
			return env ? routes.volume(env, t.id) : undefined;
		case 'network':
			return env ? routes.network(env, t.id) : undefined;
		case 'maintenance_policy':
			return routes.maintenancePolicy(t.id);
		default:
			return undefined;
	}
}
