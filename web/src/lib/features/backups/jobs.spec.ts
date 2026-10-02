// Running jobs of backup views (docs/internal/web.md, "Job progress after
// reload"): verifications of a repository location and restores of a
// backup's subject, matched against real job targets.
import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { matchJob } from '$lib/features/jobs/active';
import { restoreMatch, verifyMatch, verifyTitle } from './jobs';

function job(p: Partial<Job>): Job {
	return { id: 'j', kind: 'backup.verify', state: 'running', targets: [], ...p } as Job;
}

const repo = [{ type: 'repository' as const, id: 'repo-1' }];

describe('verifyMatch', () => {
	it("matches a location's verification by repository and environment", () => {
		const m = verifyMatch('repo-1', 'env:e1');
		expect(matchJob(job({ environmentId: 'e1', targets: repo }), m)).toBe(true);
		expect(matchJob(job({ environmentId: 'e2', targets: repo }), m)).toBe(false);
		expect(
			matchJob(
				job({ environmentId: 'e1', targets: [{ type: 'repository', id: 'repo-2' }] }),
				m
			)
		).toBe(false);
		expect(matchJob(job({ kind: 'manager.verify', targets: repo }), m)).toBe(false);
	});

	it("matches the manager location's verification", () => {
		const m = verifyMatch('repo-1', 'manager');
		expect(matchJob(job({ kind: 'manager.verify', targets: repo }), m)).toBe(true);
		expect(matchJob(job({ environmentId: 'e1', targets: repo }), m)).toBe(false);
	});

	it('matches every verification of the repository without a location', () => {
		const m = verifyMatch('repo-1');
		expect(matchJob(job({ environmentId: 'e1', targets: repo }), m)).toBe(true);
		expect(matchJob(job({ kind: 'manager.verify', targets: repo }), m)).toBe(true);
		expect(matchJob(job({ kind: 'backup.run', environmentId: 'e1', targets: repo }), m)).toBe(
			false
		);
	});

	it('names the location a verification checks', () => {
		const name = (id: string) => (id === 'e1' ? 'Silo' : id);
		expect(verifyTitle({ kind: 'backup.verify', environmentId: 'e1' }, 'NAS', name)).toBe(
			'Verify NAS for Silo'
		);
		expect(verifyTitle({ kind: 'manager.verify' }, 'NAS', name)).toBe(
			'Verify NAS (Manager State)'
		);
	});
});

describe('restoreMatch', () => {
	const restore = (targets: Job['targets'], environmentId = 'e1') =>
		job({ kind: 'restore.run', environmentId, targets });

	it("matches a restore of a stack backup's stack or volumes", () => {
		const m = restoreMatch({
			kind: 'stack',
			stackId: 'st-1',
			environmentId: 'e1',
			volumes: ['shop_db']
		});
		expect(m).not.toBeNull();
		expect(matchJob(restore([{ type: 'stack', id: 'st-1' }]), m!)).toBe(true);
		expect(
			matchJob(restore([{ type: 'volume', id: 'shop_db', environmentId: 'e1' }]), m!)
		).toBe(true);
		expect(
			matchJob(restore([{ type: 'volume', id: 'shop_db', environmentId: 'e2' }], 'e2'), m!)
		).toBe(false);
		expect(matchJob(restore([{ type: 'stack', id: 'st-2' }]), m!)).toBe(false);
		expect(
			matchJob(job({ kind: 'backup.run', targets: [{ type: 'stack', id: 'st-1' }] }), m!)
		).toBe(false);
	});

	it("matches only the page's volume on a volume's page", () => {
		const m = restoreMatch(
			{ kind: 'stack', stackId: 'st-1', environmentId: 'e1', volumes: ['a', 'b'] },
			'b'
		);
		expect(matchJob(restore([{ type: 'volume', id: 'b', environmentId: 'e1' }]), m!)).toBe(
			true
		);
		expect(matchJob(restore([{ type: 'stack', id: 'st-1' }]), m!)).toBe(false);
	});

	it('matches a volume backup by its volume, and nothing for manager state', () => {
		const m = restoreMatch({ kind: 'volume', volume: 'data', environmentId: 'e1' });
		expect(matchJob(restore([{ type: 'volume', id: 'data', environmentId: 'e1' }]), m!)).toBe(
			true
		);
		expect(restoreMatch({ kind: 'manager_state' })).toBeNull();
	});
});
