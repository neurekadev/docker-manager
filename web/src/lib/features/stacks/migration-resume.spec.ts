import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { matchJob } from '$lib/features/jobs/active';
import { migrationMatch, resumedMigration } from './migration-resume';

const envs = [{ id: 'e1' }, { id: 'e2' }, { id: 'e3' }];

describe('migrationMatch', () => {
	it('matches the stack’s migrations only', () => {
		const m = migrationMatch('st-1');
		const job = (kind: string, id = 'st-1') => ({
			kind,
			environmentId: 'e1',
			targets: [{ type: 'stack' as const, id }]
		});
		expect(matchJob(job('stack.migrate'), m)).toBe(true);
		expect(matchJob(job('stack.migrate', 'st-2'), m)).toBe(false);
		expect(matchJob(job('stack.deploy'), m)).toBe(false);
	});
});

describe('resumedMigration', () => {
	it('reads the destination from the copied volumes’ destination targets', () => {
		const job: Pick<Job, 'environmentId' | 'targets'> = {
			environmentId: 'e1',
			targets: [
				{ type: 'stack', id: 'st-1' },
				{ type: 'volume', id: 'silo_data' },
				{ type: 'volume', id: 'silo_data', environmentId: 'e3' }
			]
		};
		expect(resumedMigration(job, { environmentId: 'e1' }, envs)).toEqual({
			source: 'e1',
			target: 'e3',
			volumes: ['silo_data']
		});
	});

	it('without copied volumes: where the stack lives after the cut-over', () => {
		const job = { environmentId: 'e1', targets: [{ type: 'stack' as const, id: 'st-1' }] };
		expect(resumedMigration(job, { environmentId: 'e2' }, envs)).toEqual({
			source: 'e1',
			target: 'e2',
			volumes: []
		});
	});

	it('without copied volumes: the only other environment, else unknown', () => {
		const job = { environmentId: 'e1', targets: [{ type: 'stack' as const, id: 'st-1' }] };
		expect(resumedMigration(job, { environmentId: 'e1' }, envs.slice(0, 2)).target).toBe('e2');
		expect(resumedMigration(job, { environmentId: 'e1' }, envs).target).toBe('');
		expect(resumedMigration(job, { environmentId: 'e1' }, undefined).target).toBe('');
	});
});
