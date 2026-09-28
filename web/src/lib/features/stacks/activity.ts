// The stack's Activity tab (#22): which jobs to list first and one audit
// row per job. Pure; tested in activity.spec.ts.
import type { Job } from '$lib/api/client';
import { jobKindLabel as jobsKindLabel } from '$lib/features/jobs/labels';
import { auditActionLabel } from './model';
import type { AuditEvent } from './queries';

/** Job kinds that run on a schedule and would bury the deploys. */
export const ROUTINE_JOB_KINDS = ['update.check'];

/** The jobs to list: routine checks left out unless asked for. */
export function visibleJobs(
	jobs: readonly Job[],
	hideRoutine: boolean
): { shown: Job[]; hidden: number } {
	if (!hideRoutine) return { shown: [...jobs], hidden: 0 };
	const shown = jobs.filter((j) => !ROUTINE_JOB_KINDS.includes(j.kind));
	return { shown, hidden: jobs.length - shown.length };
}

/** One audit row: a request, or everything recorded for one job. */
export interface AuditRow {
	id: string;
	/** What happened, in words ("Deploy", "Opened the definition"). */
	label: string;
	/** succeeded, failed, partial, or running for a job that has not finished. */
	status: string;
	/** Denied requests say so. */
	denied: boolean;
	/** The newest record's time. */
	at: string;
	/** Who asked: the first record's actor (a job's queued record). */
	actor: AuditEvent['actor'];
	jobId?: string;
}

const OUTCOME: Record<string, string> = {
	success: 'succeeded',
	partial: 'partial',
	failure: 'failed',
	denied: 'failed',
	error: 'failed'
};

/**
 * Audit records (newest first) as rows: the records of one job (the
 * request that started it, queued, started, finished, cancellation) fold
 * into one row named by the job's kind, with the outcome of its finish
 * ("running" until then) and the actor who asked for it.
 */
export function auditRows(events: readonly AuditEvent[]): AuditRow[] {
	const rows: AuditRow[] = [];
	const byJob = new Map<string, { row: AuditRow; finished: boolean; kind: boolean }>();
	for (const e of events) {
		const kind = typeof e.details?.kind === 'string' ? e.details.kind : undefined;
		const lifecycle = e.action.startsWith('job.');
		if (!e.jobId) {
			rows.push({
				id: e.id,
				label: auditActionLabel(e.action),
				status: OUTCOME[e.outcome] ?? e.outcome,
				denied: e.outcome === 'denied',
				at: e.at,
				actor: e.actor
			});
			continue;
		}
		let g = byJob.get(e.jobId);
		if (!g) {
			g = {
				row: {
					id: e.id,
					label: kind ? jobsKindLabel(kind) : auditActionLabel(e.action),
					status: 'running',
					denied: false,
					at: e.at,
					actor: e.actor,
					jobId: e.jobId
				},
				finished: false,
				kind: !!kind
			};
			byJob.set(e.jobId, g);
			rows.push(g.row);
		} else if (kind && !g.kind) {
			g.row.label = jobsKindLabel(kind);
			g.kind = true;
		}
		// Older records come later: the last one seen is who asked.
		g.row.actor = e.actor;
		if (e.action === 'job.finished') {
			g.row.status = OUTCOME[e.outcome] ?? e.outcome;
			g.finished = true;
		} else if (!lifecycle && e.outcome !== 'success' && !g.finished) {
			// The request itself was refused: no job ran.
			g.row.status = OUTCOME[e.outcome] ?? e.outcome;
			g.row.denied = e.outcome === 'denied';
		}
	}
	return rows;
}
