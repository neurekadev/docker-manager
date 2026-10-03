// Schedules view model (#13): run times in the policy's own time zone,
// DST annotations, run outcomes, where each policy is managed and where it
// applies.
import type { Schedule } from '$lib/api/client';
import { routes } from '$lib/routes';

type RunTime = NonNullable<Schedule['nextRun']>;
type Run = Schedule['recentRuns'][number];

/** "Sun, Sep 27, 02:30" in the schedule's time zone (the zone is shown next to it). */
export function formatRunTime(at: string, timeZone: string): string {
	try {
		return new Intl.DateTimeFormat('en', {
			weekday: 'short',
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
			hourCycle: 'h23',
			timeZone
		}).format(new Date(at));
	} catch {
		return at;
	}
}

/** A short DST label for a run time, or null for an ordinary time. */
export function dstLabel(r: Pick<RunTime, 'dst'>): string | null {
	if (r.dst === 'gap') return 'Clocks Move Forward';
	if (r.dst === 'repeated') return 'Clocks Move Back';
	return null;
}

export const OUTCOME_STATUS: Record<Run['outcome'], { status: string; label: string }> = {
	pending: { status: 'queued', label: 'Pending' },
	enqueued: { status: 'running', label: 'Started' },
	missed: { status: 'skipped', label: 'Missed' },
	skipped: { status: 'skipped', label: 'Skipped' },
	rejected: { status: 'blocked', label: 'Refused' },
	failed: { status: 'failed', label: 'Failed' }
};

/** The status of a run for a badge: the (worst) job result once known. */
export function runStatus(r: Run): { status: string; label: string; kind: 'job' | 'resource' } {
	if (r.outcome === 'enqueued' && r.result && r.result !== 'enqueued')
		return { status: r.result, label: '', kind: 'job' };
	const o = OUTCOME_STATUS[r.outcome] ?? { status: r.outcome, label: r.outcome };
	return { ...o, kind: 'resource' };
}

const REASONS: Record<string, string> = {
	previous_run_active: 'The previous run was still active.',
	skipped_overlap: 'The previous run was still active.',
	nothing_to_run: 'There was nothing to do.',
	policy_disabled: 'The policy was disabled.',
	policy_not_found: 'The policy was deleted.',
	target_not_found: 'What the policy covers no longer exists.',
	environment_archived: 'The environment is archived.',
	no_rules_enabled: 'Every rule of the policy is off.',
	outside_update_window: 'It was outside the update window.'
};

/** Why a run did not start, in words (reason, else errorClass; never a raw class). */
export function runReason(r: Run): string {
	const reason = r.reason?.trim();
	if (reason && !/^[a-z]+(_[a-z]+)+$/.test(reason)) return reason;
	const cls = r.errorClass || reason || '';
	if (cls === 'missed')
		return r.missedCount && r.missedCount > 1
			? `Docker Manager was not running at ${r.missedCount} scheduled times.`
			: 'Docker Manager was not running at the scheduled time.';
	if (REASONS[cls]) return REASONS[cls];
	return cls ? `${cls[0].toUpperCase()}${cls.slice(1).replaceAll('_', ' ')}.` : '';
}

/** Where a policy of this schedule kind is edited: the policy itself when its ID is known. */
export function policyHref(kind: string, policyId?: string): string {
	switch (kind) {
		case 'backup':
			return policyId ? routes.backupPolicy(policyId) : routes.backupPolicies();
		case 'backup_verification':
			return policyId ? routes.backupRepository(policyId) : routes.backupRepositories();
		case 'update_check':
		case 'update_run':
			return policyId ? routes.updatePolicy(policyId) : routes.updates();
		case 'prune':
			return routes.maintenance();
		default:
			return routes.schedules();
	}
}

/** Kinds whose policies cover environments (the rest run on the manager). */
const ENVIRONMENT_KINDS = new Set(['update_check', 'update_run', 'prune']);

/**
 * Where a schedule applies: its environment, "All Environments" for
 * update policies over every environment and maintenance, else "Manager" (backups
 * and repository verification run on Docker Manager itself).
 */
export function scheduleScope(
	s: Pick<Schedule, 'kind' | 'environmentId'>,
	envName: (id: string) => string | undefined
): string {
	if (s.environmentId) return envName(s.environmentId) ?? 'Unknown Environment';
	return ENVIRONMENT_KINDS.has(s.kind) ? 'All Environments' : 'Manager';
}

/** Policy names that two different policies share (they need their scope to tell apart). */
export function sharedNames(
	rows: readonly Pick<Schedule, 'policyId' | 'policyName'>[]
): Set<string> {
	const ids = new Map<string, Set<string>>();
	for (const r of rows)
		ids.set(r.policyName, (ids.get(r.policyName) ?? new Set()).add(r.policyId));
	return new Set([...ids].filter(([, v]) => v.size > 1).map(([k]) => k));
}

/** Enabled, disabled or broken (an invalid saved expression never runs). */
export function scheduleState(s: Pick<Schedule, 'enabled' | 'invalidReason'>): {
	status: string;
	label: string;
} {
	if (s.invalidReason) return { status: 'failed', label: 'Invalid' };
	return s.enabled
		? { status: 'running', label: 'Enabled' }
		: { status: 'stopped', label: 'Disabled' };
}
