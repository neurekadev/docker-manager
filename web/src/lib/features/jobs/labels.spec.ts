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
	jobAgain,
	jobDuration,
	jobErrorHeadline,
	jobHeadline,
	jobKindLabel,
	jobKindPhrase,
	jobRetry,
	jobTargetLabel,
	jobTitle,
	policyPage,
	stackNames,
	targetHref,
	targetName,
	timelineTime
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
		expect(jobKindLabel('stack.deploy')).toBe('Deploy Stack');
		expect(jobKindLabel('container.unpause')).toBe('Unpause Container');
		expect(jobKindLabel('future.thing_done')).toBe('Future Thing Done');
		expect(jobKindLabel(undefined)).toBe('Job');
		expect(jobKindPhrase('update.check')).toBe('Check for updates');
		expect(jobKindPhrase('manager.backup')).toBe('Back up Docker Manager');
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
		expect(jobTargetLabel({ targets: [policy] })).toBe('maintenance');
		expect(jobTargetLabel({ targets: [ctr, policy] })).toBe('homeassistant and 1 more');
		expect(jobTargetLabel({ targets: [] })).toBe('');
		// A stack operation on some services is named by its stack (#280).
		const web = { type: 'service' as const, id: `${stack.id}/web` };
		expect(jobTargetLabel({ targets: [stack, web] }, names)).toBe('Silo');
		expect(jobTitle({ kind: 'stack.stop', targets: [stack, web] }, names)).toBe(
			'Stop Services Silo'
		);
		expect(targetName('volume', 'pgdata')).toBe('pgdata');
		expect(jobTitle({ kind: 'stack.deploy', targets: [stack] }, names)).toBe(
			'Deploy Stack Silo'
		);
		expect(jobTitle({ kind: 'prune.run', targets: [policy] })).toBe('Prune Docker Objects');
		expect(jobTitle({ kind: 'container.restart', targets: [ctr] })).toBe(
			'Restart Container homeassistant'
		);
	});

	it('leads job headlines with the target’s name', () => {
		const id = '0190a6e0-0000-7000-8000-000000000001';
		const names = stackNames([{ id, name: 'zerobyte' }]);
		expect(
			jobHeadline(
				{ kind: 'update.check', targets: [{ type: 'stack', id }] },
				{ nameOf: names }
			)
		).toEqual({ title: 'zerobyte', subtitle: 'Check for Updates' });
		// An unresolved stack ID never shows: the kind leads.
		expect(jobHeadline({ kind: 'stack.deploy', targets: [{ type: 'stack', id }] })).toEqual({
			title: 'Deploy Stack',
			subtitle: ''
		});
		expect(
			jobHeadline(
				{ kind: 'prune.run', targets: [{ type: 'maintenance_policy', id }] },
				{ fallback: 'homelab' }
			)
		).toEqual({ title: 'Prune Docker Objects', subtitle: 'homelab' });
		expect(
			jobHeadline({
				kind: 'container.restart',
				targets: [
					{ type: 'container', id: 'homeassistant' },
					{ type: 'container', id: 'mqtt' }
				]
			})
		).toEqual({ title: 'homeassistant and 1 more', subtitle: 'Restart Container' });
		expect(
			jobHeadline({
				kind: 'files.delete',
				targets: [{ type: 'path', id: '/srv/stacks/silo/compose.yaml' }]
			})
		).toEqual({ title: 'compose.yaml', subtitle: 'Delete Files' });
		expect(jobHeadline({ kind: 'manager.backup', targets: [] })).toEqual({
			title: 'Back Up Docker Manager',
			subtitle: ''
		});
		// An environment migration names the environment, not its first stack.
		const migrate: Pick<Job, 'kind' | 'targets'> = {
			kind: 'environment.migrate',
			targets: [
				{ type: 'stack', id },
				{ type: 'stack', id: 'st-2' }
			]
		};
		expect(jobHeadline(migrate, { nameOf: names, fallback: 'homelab' })).toEqual({
			title: 'homelab',
			subtitle: 'Migrate Environment'
		});
		expect(jobHeadline(migrate, { nameOf: names })).toEqual({
			title: 'Migrate Environment',
			subtitle: ''
		});
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
		expect(policyPage('backup.run').href).toBe('/backups');
		expect(policyPage('prune.run', 'mp-1').href).toBe('/maintenance');
		expect(policyPage('update.check', 'up-1').href).toBe('/updates');
		expect(policyPage('backup.run', 'bs-1').href).toBe('/backups');
		expect(policyPage('backup.verify', 'br-1').href).toBe('/backups/repositories/br-1');
	});

	it('times the timeline to the second and links targets', () => {
		expect(timelineTime('2026-09-27T16:54:03Z', 'UTC')).toBe('Sep 27, 2026, 16:54:03');
		expect(timelineTime(undefined)).toBe('—');
		expect(targetHref({ type: 'stack', id: 's1' }, 'e1')).toBe('/stacks/s1');
		expect(targetHref({ type: 'container', id: 'web' }, 'e1')).toBe('/containers/e1/web');
		expect(targetHref({ type: 'volume', id: 'data', environmentId: 'e2' }, 'e1')).toBe(
			'/volumes/e2/data'
		);
		expect(targetHref({ type: 'path', id: '/srv' }, 'e1')).toBeUndefined();
	});

	it('heads failures in words and says where to try again', () => {
		expect(jobErrorHeadline({ class: 'step_failed' }, 'failed')).toBe('A step failed');
		expect(jobErrorHeadline({ class: 'something_new' }, 'failed')).toBe('The job failed');
		expect(jobErrorHeadline(undefined, 'partial')).toBe('It partly failed');
		const base = { id: 'j1', environmentId: 'e1', targets: [] as Job['targets'] };
		expect(jobAgain({ ...base, kind: 'update.check', policyId: 'up-1' })?.href).toBe(
			'/updates'
		);
		expect(jobAgain({ ...base, kind: 'image.build' })?.href).toBe('/builds/e1/j1');
		expect(
			jobAgain({ ...base, kind: 'stack.deploy', targets: [{ type: 'stack', id: 's1' }] })
				?.href
		).toBe('/stacks/s1');
		expect(
			jobAgain({
				...base,
				kind: 'container.restart',
				targets: [{ type: 'container', id: 'web' }]
			})?.href
		).toBe('/containers/e1/web');
		expect(
			jobAgain({ ...base, kind: 'stack.export', targets: [{ type: 'stack', id: 's1' }] })
		).toEqual({ href: '/stacks/s1?export=1', label: 'Export the Archive Again' });
		// The stack a failed import created is gone: start over from the list.
		expect(
			jobAgain({
				...base,
				kind: 'stack.import_archive',
				targets: [{ type: 'stack', id: 's9' }]
			})?.href
		).toBe('/stacks?fromArchive=1&environment=e1');
		expect(jobAgain({ ...base, kind: 'files.copy' })).toBeNull();
	});

	it('retries through the server when it can, else links to where to try again', () => {
		const base = {
			id: 'j1',
			environmentId: 'e1',
			kind: 'stack.deploy',
			targets: [{ type: 'stack' as const, id: 's1' }]
		};
		expect(jobRetry({ ...base, state: 'failed', retryable: true })).toBe('retry');
		// Cancelled jobs are retried only by the server.
		expect(jobRetry({ ...base, state: 'cancelled', retryable: true })).toBe('retry');
		expect(jobRetry({ ...base, state: 'cancelled', retryable: false })).toBeNull();
		expect(jobRetry({ ...base, state: 'failed', retryable: false })).toEqual({
			href: '/stacks/s1',
			label: 'Open the Stack to Try Again'
		});
		expect(jobRetry({ ...base, state: 'partial' })).toEqual(
			expect.objectContaining({ href: '/stacks/s1' })
		);
		expect(jobRetry({ ...base, state: 'succeeded', retryable: false })).toBeNull();
		expect(jobRetry({ ...base, state: 'running', retryable: false })).toBeNull();
		expect(
			jobRetry({
				...base,
				kind: 'files.copy',
				targets: [],
				state: 'failed',
				retryable: false
			})
		).toBeNull();
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

	it('filters by policy and passes the total through', async () => {
		const fetch = vi.fn<(req: Request) => Promise<Response>>(
			async () =>
				new Response(JSON.stringify({ items: [], nextCursor: 'c2', total: 1234 }), {
					status: 200,
					headers: { 'Content-Type': 'application/json' }
				})
		);
		const client = createApiClient(
			fetch as unknown as typeof globalThis.fetch,
			'http://dy.test'
		);
		const page = await fetchJobsPage({ policyId: 'up-1' }, undefined, 50, undefined, client);
		const url = new URL((fetch.mock.calls[0][0] as Request).url);
		expect(url.searchParams.get('policyId')).toBe('up-1');
		expect(page).toEqual({ items: [], nextCursor: 'c2', total: 1234 });
		await fetchJobsPage({ policyId: '' }, undefined, 20, undefined, client);
		const bare = new URL((fetch.mock.calls[1][0] as Request).url);
		expect(bare.searchParams.has('policyId')).toBe(false);
	});
});
