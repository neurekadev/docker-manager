// The stack page's tray after a reload (docs/internal/web.md, "Job progress
// after reload"): which running jobs are the stack's and what the tray
// calls a job it did not start itself.
import { describe, expect, it } from 'vitest';
import { matchJob } from '$lib/features/jobs/active';
import { stackJobCopy, stackTrayMatch } from './adopt';

const on = (kind: string, id = 's1') => ({
	kind,
	environmentId: 'e1',
	targets: [{ type: 'stack', id }]
});

describe('stackTrayMatch', () => {
	it('takes every job acting on the stack but update checks and backups', () => {
		const m = stackTrayMatch('s1');
		for (const kind of ['stack.deploy', 'stack.rename', 'stack.migrate', 'restore.run'])
			expect(matchJob(on(kind), m)).toBe(true);
		expect(matchJob(on('update.check'), m)).toBe(false);
		expect(matchJob(on('backup.run'), m)).toBe(false);
		expect(matchJob(on('backup.run'), stackTrayMatch('s1', { wizard: true }))).toBe(false);
		expect(matchJob(on('stack.deploy', 's2'), m)).toBe(false);
	});

	it('leaves file jobs to the Files tab while it is open', () => {
		const files = stackTrayMatch('s1', { files: true });
		for (const kind of ['files.archive', 'files.extract', 'files.copy'])
			expect(matchJob(on(kind), files)).toBe(false);
		expect(matchJob(on('stack.deploy'), files)).toBe(true);
		expect(matchJob(on('files.extract'), stackTrayMatch('s1'))).toBe(true);
	});

	it('leaves migrations to the migration wizard while it is open', () => {
		expect(matchJob(on('stack.migrate'), stackTrayMatch('s1', { wizard: true }))).toBe(false);
		expect(matchJob(on('stack.deploy'), stackTrayMatch('s1', { wizard: true }))).toBe(true);
	});
});

describe('stackJobCopy', () => {
	it('uses the words of the action that started the job', () => {
		expect(stackJobCopy('stack.deploy', 'Silo')).toEqual({
			title: 'Deploy Silo',
			success: 'Deployed Silo',
			failure: 'Silo was not deployed'
		});
		expect(stackJobCopy('stack.migrate', 'Silo').title).toBe('Migrate Silo');
		expect(stackJobCopy('stack.export', 'Silo')).toEqual({
			title: 'Export Silo as an Archive',
			success: 'Exported Silo as an archive',
			failure: 'Silo was not exported'
		});
		expect(stackJobCopy('stack.rename', 'Silo').failure).toBe('Silo was not renamed');
	});

	it('names other kinds by their label', () => {
		expect(stackJobCopy('stack.import', 'Silo')).toEqual({
			title: 'Import Project: Silo',
			success: 'Import Project: Silo finished',
			failure: 'Import Project: Silo did not succeed'
		});
	});
});
