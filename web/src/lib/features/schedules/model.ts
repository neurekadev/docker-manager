// Schedules view model (#13): run times in the policy's own time zone,
// DST annotations, run outcomes and where each policy kind is managed.
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
	if (r.dst === 'gap') return 'Clocks move forward';
	if (r.dst === 'repeated') return 'Clocks move back';
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

/** Why a run did not start, in words (errorClass and reason). */
export function runReason(r: Run): string {
	if (r.reason) return r.reason;
	switch (r.errorClass) {
		case 'missed':
			return r.missedCount && r.missedCount > 1
				? `DockYard was not running at ${r.missedCount} scheduled times.`
				: 'DockYard was not running at the scheduled time.';
		case 'previous_run_active':
			return 'The previous run was still active.';
		case 'nothing_to_run':
			return 'There was nothing to do.';
		case 'policy_disabled':
			return 'The policy was disabled.';
		case 'environment_archived':
			return 'The environment is archived.';
		default:
			return r.errorClass ? r.errorClass.replaceAll('_', ' ') : '';
	}
}

/** Where a policy of this schedule kind is edited. */
export function policyHref(kind: string): string {
	switch (kind) {
		case 'backup':
		case 'backup_verification':
			return routes.backups();
		case 'update_check':
		case 'update_run':
			return routes.updates();
		case 'prune':
			return routes.maintenance();
		default:
			return routes.schedules();
	}
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
