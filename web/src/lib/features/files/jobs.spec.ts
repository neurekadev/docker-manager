// The file jobs of a root (#15, docs/internal/web.md "Job progress after
// reload"): the running file jobs of a stack, volume or template root are
// found again by the root target the manager sets, and named by the folder
// they act in.
import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { matchJob } from '$lib/features/jobs/active';
import type { FileScope } from './api';
import { fileJobDir, fileJobMatch, fileJobTitle, timedFileJob } from './jobs';

type J = Pick<Job, 'kind' | 'targets' | 'environmentId' | 'policyId'>;

const stack: FileScope = { kind: 'stack', stackId: 'st-1', environmentId: 'e1' };
const volume: FileScope = { kind: 'volume', environmentId: 'e1', volume: 'pgdata' };
const template: FileScope = { kind: 'template', templateId: 'tp-1' };
// The generated target type does not list template targets yet.
const templateTarget = (id: string) =>
	({ type: 'template', id }) as unknown as NonNullable<J['targets']>[number];

function stackJob(kind: string, dir = 'config', p: Partial<J> = {}): J {
	return {
		kind,
		environmentId: 'e1',
		targets: [
			{ type: 'stack', id: 'st-1' },
			{ type: 'path', id: dir ? `/stack/st-1/${dir}` : '/stack/st-1' },
			{ type: 'destination_path', id: '/stack/st-1/backup' }
		],
		...p
	};
}

describe('fileJobMatch', () => {
	it("matches a stack root's file jobs, the root folder's too, and nothing else", () => {
		const m = fileJobMatch(stack);
		expect(matchJob(stackJob('files.copy'), m)).toBe(true);
		// A job on the root itself has the path target "/stack/st-1".
		expect(matchJob(stackJob('files.delete', ''), m)).toBe(true);
		expect(matchJob(stackJob('stack.deploy'), m)).toBe(false);
		expect(
			matchJob(
				stackJob('files.copy', 'config', { targets: [{ type: 'stack', id: 'st-2' }] }),
				m
			)
		).toBe(false);
		expect(matchJob(stackJob('files.copy', 'config', { environmentId: 'e2' }), m)).toBe(false);
	});

	it("matches a volume root's file jobs in its environment only", () => {
		const m = fileJobMatch(volume);
		const job = (env: string, name = 'pgdata'): J => ({
			kind: 'files.extract',
			environmentId: env,
			targets: [
				{ type: 'volume', id: name },
				{ type: 'path', id: `/volume/${name}/dump` }
			]
		});
		expect(matchJob(job('e1'), m)).toBe(true);
		// Same volume name on another server.
		expect(matchJob(job('e2'), m)).toBe(false);
		expect(matchJob(job('e1', 'redis'), m)).toBe(false);
	});

	it("matches a template's file jobs (no environment) but not its other jobs", () => {
		const m = fileJobMatch(template);
		const job = (kind: string, id = 'tp-1'): J => ({
			kind,
			targets: [templateTarget(id)]
		});
		expect(matchJob(job('template.files.move'), m)).toBe(true);
		expect(matchJob(job('template.publish'), m)).toBe(false);
		expect(matchJob(job('template.files.move', 'tp-2'), m)).toBe(false);
		expect(matchJob(job('files.move'), m)).toBe(false);
	});
});

describe('fileJobTitle', () => {
	it('names the folder a job acts in, or the root', () => {
		expect(fileJobDir(stackJob('files.copy', 'config/nginx'), stack)).toBe('config/nginx');
		expect(fileJobTitle(stackJob('files.copy'), stack, 'silo')).toBe('Copy Files in config');
		expect(fileJobTitle(stackJob('files.delete', ''), stack, 'silo')).toBe(
			'Delete Files in silo'
		);
		expect(
			fileJobTitle(
				{ kind: 'template.files.archive', targets: [templateTarget('tp-1')] },
				template,
				'Web app'
			)
		).toBe('Archive Template Files in Web app');
		// A path of another root is not ours to name.
		expect(fileJobDir(stackJob('files.copy'), volume)).toBe('');
	});
});

describe('timedFileJob', () => {
	it('is the archive and extraction jobs of every root', () => {
		for (const kind of [
			'files.archive',
			'files.extract',
			'template.files.archive',
			'template.files.extract'
		])
			expect(timedFileJob(kind)).toBe(true);
		for (const kind of ['files.copy', 'files.delete', 'template.files.move', undefined])
			expect(timedFileJob(kind)).toBe(false);
	});
});
