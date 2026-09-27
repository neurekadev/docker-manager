import { readFileSync } from 'node:fs';
import { describe, expect, it, vi } from 'vitest';
import type { Job } from '$lib/api/client';
import { createApiClient } from '$lib/api/client';
import { fetchJobsPage } from '$lib/api/queries';
import {
	JOB_KIND_LABELS,
	STATE_FILTERS,
	blockedText,
	jobActive,
	jobDuration,
	jobKindLabel,
	jobTargetLabel,
	jobTitle,
	policyPage,
	stackNames,
	targetName
} from './labels';

describe('job labels (#26 catalog)', () => {
	it('names every job kind of the lock matrix', () => {
		const doc = readFileSync(
			new URL('../../../../../docs/internal/architecture/job-engine.md', import.meta.url),
			'utf8'
		);
		const kinds = [...doc.matchAll(/^\| `([a-z_]+\.[a-z_]+)` \| (agent|manager) \|/gm)].map(
			(m) => m[1]
		);
		expect(kinds.length).toBeGreaterThan(30);
		for (const k of kinds) expect(JOB_KIND_LABELS[k], k).toBeTruthy();
		expect(jobKindLabel('stack.deploy')).toBe('Deploy stack');
		expect(jobKindLabel('future.thing_done')).toBe('Future thing done');
		expect(jobKindLabel(undefined)).toBe('Job');
	});

	it('names targets by name, never by an opaque ID', () => {
		const names = stackNames([
			{ id: '0190a6e0-0000-7000-8000-000000000001', name: 'silo', displayName: 'Silo' }
		]);
		const stack = { type: 'stack' as const, id: '0190a6e0-0000-7000-8000-000000000001' };
		const policy = {
			type: 'maintenance_policy' as const,
			id: '0190a6e0-0000-7000-8000-00000000000f'
		};
		const ctr = { type: 'container' as const, id: 'homeassistant' };
		expect(jobTargetLabel({ targets: [stack] }, names)).toBe('Silo');
		expect(jobTargetLabel({ targets: [stack] })).toBe('stack');
		expect(jobTargetLabel({ targets: [policy] })).toBe('prune policy');
		expect(jobTargetLabel({ targets: [ctr, policy] })).toBe('homeassistant and 1 more');
		expect(jobTargetLabel({ targets: [] })).toBe('');
		expect(targetName('volume', 'pgdata')).toBe('pgdata');
		expect(jobTitle({ kind: 'stack.deploy', targets: [stack] }, names)).toBe(
			'Deploy stack Silo'
		);
		expect(jobTitle({ kind: 'prune.run', targets: [policy] })).toBe('Prune Docker objects');
		expect(jobTitle({ kind: 'container.restart', targets: [ctr] })).toBe(
			'Restart container homeassistant'
		);
	});

	it('measures durations and says why a job waits', () => {
		const now = new Date('2026-09-25T12:10:00Z');
		expect(
			jobDuration(
				{ createdAt: '2026-09-25T12:00:00Z', finishedAt: '2026-09-25T12:00:45Z' },
				now
			)
		).toBe('45 s');
		expect(
			jobDuration(
				{ createdAt: '2026-09-25T12:00:00Z', startedAt: '2026-09-25T12:07:00Z' },
				now
			)
		).toBe('3 min');
		expect(jobDuration({ createdAt: 'nope' }, now)).toBe('');
		expect(blockedText({ reason: 'agent_offline' })).toMatch(/online/);
		expect(blockedText({ reason: 'lock', jobId: 'j1' })).toMatch(/same resources/);
		expect(jobActive('blocked')).toBe(true);
		expect(jobActive('partial')).toBe(false);
		expect(policyPage('prune.run').href).toBe('/maintenance');
		expect(policyPage('update.check').href).toBe('/updates');
		expect(policyPage('backup.run').href).toBe('/backups/policies');
	});

	it('offers state groups that cover every job state', () => {
		const all = new Set(STATE_FILTERS.flatMap((f) => f.states));
		for (const s of [
			'queued',
			'blocked',
			'dispatched',
			'running',
			'cancelling',
			'succeeded',
			'failed',
			'partial',
			'cancelled',
			'interrupted'
		])
			expect(all.has(s as Job['state']), s).toBe(true);
	});
});

describe('GET /jobs wire format', () => {
	it('sends states comma-separated and origins repeated (the API contract)', async () => {
		const fetch = vi.fn<(req: Request) => Promise<Response>>(
			async () =>
				new Response(JSON.stringify({ items: [] }), {
					status: 200,
					headers: { 'Content-Type': 'application/json' }
				})
		);
		const client = createApiClient(
			fetch as unknown as typeof globalThis.fetch,
			'http://dy.test'
		);
		await fetchJobsPage(
			{
				states: ['failed', 'partial'],
				origins: ['manual', 'scheduled'],
				kind: 'prune.run',
				environmentId: 'e1'
			},
			'c1',
			50,
			undefined,
			client
		);
		const url = new URL((fetch.mock.calls[0][0] as Request).url);
		expect(url.searchParams.getAll('state')).toEqual(['failed,partial']);
		expect(url.searchParams.getAll('origin')).toEqual(['manual', 'scheduled']);
		expect(url.searchParams.get('kind')).toBe('prune.run');
		expect(url.searchParams.get('environmentId')).toBe('e1');
		expect(url.searchParams.get('cursor')).toBe('c1');
		await fetchJobsPage({}, undefined, 20, undefined, client);
		const bare = new URL((fetch.mock.calls[1][0] as Request).url);
		expect([...bare.searchParams.keys()]).toEqual(['limit']);
	});
});
