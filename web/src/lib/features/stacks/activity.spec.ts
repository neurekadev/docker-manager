import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { auditRows, visibleJobs } from './activity';
import type { AuditEvent } from './queries';

const job = (id: string, kind: string) => ({ id, kind }) as Job;

describe('visibleJobs', () => {
	it('leaves routine update checks out unless asked for', () => {
		const jobs = [
			job('1', 'update.check'),
			job('2', 'stack.deploy'),
			job('3', 'update.check'),
			job('4', 'stack.restart')
		];
		expect(visibleJobs(jobs, true)).toEqual({ shown: [jobs[1], jobs[3]], hidden: 2 });
		expect(visibleJobs(jobs, false)).toEqual({ shown: jobs, hidden: 0 });
	});
});

describe('auditRows', () => {
	const user = { kind: 'user', userId: 'u1' } as const;
	const service = { kind: 'service' } as const;
	const ev = (over: Partial<AuditEvent>): AuditEvent =>
		({
			id: over.id ?? 'e',
			action: 'stack.deploy',
			actor: user,
			at: '2026-09-27T10:00:00Z',
			category: 'operations',
			details: {},
			hash: 'h',
			prevHash: 'p',
			outcome: 'success',
			seq: 1,
			targets: [],
			...over
		}) as AuditEvent;

	it("folds a job's records into one row named by its kind, with its outcome and requester", () => {
		const rows = auditRows([
			ev({
				id: 'f',
				action: 'job.finished',
				jobId: 'j1',
				actor: service,
				outcome: 'failure',
				at: '2026-09-27T10:02:00Z',
				details: { kind: 'stack.deploy' }
			}),
			ev({
				id: 's',
				action: 'job.started',
				jobId: 'j1',
				actor: service,
				details: { kind: 'stack.deploy' }
			}),
			ev({ id: 'q', action: 'job.queued', jobId: 'j1', details: { kind: 'stack.deploy' } }),
			ev({ id: 'r', action: 'stack.definition.read', at: '2026-09-27T09:00:00Z' })
		]);
		expect(rows).toEqual([
			{
				id: 'f',
				label: 'Deploy Stack',
				status: 'failed',
				denied: false,
				at: '2026-09-27T10:02:00Z',
				actor: user,
				jobId: 'j1'
			},
			{
				id: 'r',
				label: 'Opened the Definition',
				status: 'succeeded',
				denied: false,
				at: '2026-09-27T09:00:00Z',
				actor: user
			}
		]);
	});

	it('shows a job without its finish as running, and names denied requests', () => {
		const rows = auditRows([
			ev({ id: 'q', action: 'job.queued', jobId: 'j2', details: { kind: 'stack.restart' } }),
			ev({ id: 'd', action: 'stack.remove', outcome: 'denied' })
		]);
		expect(rows.map((r) => [r.label, r.status, r.denied])).toEqual([
			['Restart Stack', 'running', false],
			['Delete', 'failed', true]
		]);
	});
});
