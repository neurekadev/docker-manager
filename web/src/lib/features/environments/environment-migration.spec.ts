import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { matchJob } from '$lib/features/jobs/active';
import {
	andList,
	chosenStacks,
	destinationOptions,
	environmentCheckHeadline,
	environmentMigrationBody,
	environmentMigrationMatch,
	everyStackMoved,
	finishToast,
	jobStackIds,
	migrationEnded,
	migrationOutcome,
	moveOrder,
	moveState,
	oldCopies,
	oldCopiesNotice,
	oldCopyRemovalMatch,
	oldCopyState,
	ownStackIds,
	pendingCopies,
	restoredMigration,
	resultNotice,
	selectionKey,
	skippedRows,
	stackChoices,
	stackMoveSummary,
	stacksLeft,
	withOwnFromCheck,
	type EnvironmentMigration,
	type EnvironmentMigrationPreview,
	type StackMove
} from './environment-migration';
import { removeOldCopies, startOldCopyRemovals } from './migration-actions';

type Preview = EnvironmentMigrationPreview['stacks'][number]['preview'];

const stackPreview = (over: Partial<Preview> = {}): Preview => ({
	access: { changes: [], complete: true, othersAffected: 0 },
	allowed: true,
	blockers: [],
	data: {
		destinationStacksFree: 1000,
		destinationVolumesFree: 1000,
		imageBytes: 0,
		projectBytes: 10,
		totalBytes: 110,
		volumeBytes: 100
	},
	downtime: { basis: 'measured', estimatedSeconds: 30 },
	excluded: [],
	kind: 'stack',
	leftovers: [],
	services: [],
	sourceEnvironmentId: 'e1',
	targetEnvironmentId: 'e2',
	transport: {
		bandwidthLimitBytesPerSecond: 0,
		destinationPlainHttp: false,
		sourcePlainHttp: false
	},
	volumes: [],
	warnings: [],
	...over
});

const move = (stackId: string, name: string, over: Partial<StackMove> = {}): StackMove => ({
	stackId,
	name,
	group: 0,
	dependsOn: [],
	preview: stackPreview(),
	...over
});

const preview = (over: Partial<EnvironmentMigrationPreview> = {}): EnvironmentMigrationPreview => ({
	sourceEnvironmentId: 'e1',
	targetEnvironmentId: 'e2',
	allowed: true,
	blockers: [],
	warnings: [],
	stacks: [],
	groups: [],
	networks: [],
	skipped: [],
	data: stackPreview().data,
	downtime: { basis: 'b', estimatedSeconds: 60 },
	...over
});

const record = (stacks: EnvironmentMigration['stacks']): EnvironmentMigration => ({
	id: 'job-e',
	sourceEnvironmentId: 'e1',
	targetEnvironmentId: 'e2',
	state: 'failed',
	groups: [],
	stacks,
	networks: [],
	createdAt: '2026-09-29T10:00:00Z',
	updatedAt: '2026-09-29T10:00:00Z'
});

interface ListedStack {
	id: string;
	name: string;
	displayName?: string;
	environmentId: string;
	actions: string[];
}

const stack = (id: string, name: string, over: Partial<ListedStack> = {}): ListedStack => ({
	id,
	name,
	environmentId: 'e1',
	actions: ['stack.migrate'],
	...over
});

const ended = (id: string, state: Job['state']) => ({ id, state }) as unknown as Job;

describe('environmentMigrationMatch', () => {
	it('matches environment migrations away from the environment only', () => {
		const m = environmentMigrationMatch('e1');
		const job = (kind: string, environmentId = 'e1') => ({
			kind,
			environmentId,
			targets: [{ type: 'stack' as const, id: 'st-1' }]
		});
		expect(matchJob(job('environment.migrate'), m)).toBe(true);
		expect(matchJob(job('environment.migrate', 'e2'), m)).toBe(false);
		expect(matchJob(job('stack.migrate'), m)).toBe(false);
	});
});

describe('stack choices', () => {
	it('finds Docker Manager’s own stacks from its protected containers and the check', () => {
		const own = ownStackIds([
			{ protection: { role: 'manager' }, stack: { stackId: 'st-dm' } },
			{ protection: { role: 'manager' }, stack: { stackId: 'st-dm' } },
			{ stack: { stackId: 'st-1' } },
			{ protection: { role: 'agent' } }
		]);
		expect(own).toEqual(['st-dm']);
		expect(ownStackIds(undefined)).toEqual([]);
		const skipped = [
			{ stackId: 'st-x', name: 'x', reason: 'docker_manager' as const },
			{ stackId: 'st-2', name: 'b', reason: 'not_selected' as const }
		];
		expect(withOwnFromCheck(own, { skipped })).toEqual(['st-dm', 'st-x']);
	});

	it('lists the source’s stacks by title, Docker Manager’s own and denied ones off', () => {
		const choices = stackChoices(
			[
				stack('st-2', 'web', { displayName: 'Website' }),
				stack('st-dm', 'docker-manager'),
				stack('st-3', 'db', { actions: [] }),
				stack('st-4', 'other', { environmentId: 'e2' }),
				stack('st-1', 'app')
			],
			'e1',
			['st-dm']
		);
		expect(choices).toEqual([
			{ id: 'st-1', title: 'app' },
			{ id: 'st-3', title: 'db', blocked: 'not_permitted' },
			{ id: 'st-dm', title: 'docker-manager', blocked: 'docker_manager' },
			{ id: 'st-2', title: 'Website' }
		]);
		expect(chosenStacks(choices, ['st-2'])).toEqual(['st-1']);
	});

	it('names the stacks only when some were unticked', () => {
		const choices = stackChoices(
			[stack('st-1', 'app'), stack('st-2', 'web'), stack('st-dm', 'dm')],
			'e1',
			['st-dm']
		);
		expect(environmentMigrationBody({ target: 'e2', deselected: [] }, choices)).toEqual({
			targetEnvironmentId: 'e2',
			stacks: undefined
		});
		// Unticking a stack that cannot move changes nothing.
		expect(
			environmentMigrationBody({ target: 'e2', deselected: ['st-dm'] }, choices).stacks
		).toBe(undefined);
		expect(environmentMigrationBody({ target: 'e2', deselected: ['st-2'] }, choices)).toEqual({
			targetEnvironmentId: 'e2',
			stacks: ['st-1']
		});
		expect(selectionKey({ target: 'e2', deselected: ['b', 'a'] })).toBe(
			selectionKey({ target: 'e2', deselected: ['a', 'b', 'a'] })
		);
	});

	it('offers offline destinations turned off, saying why', () => {
		expect(
			destinationOptions([
				{ id: 'e2', name: 'nas', online: true },
				{ id: 'e3', name: 'lab', online: false }
			])
		).toEqual([
			{ value: 'e2', label: 'nas', disabled: false },
			{ value: 'e3', label: 'lab (offline)', disabled: true }
		]);
	});
});

describe('the check', () => {
	it('counts the problems and warnings of the migration and every stack', () => {
		const warn = { code: 'image_rebuild', message: 'm' };
		const block = { code: 'port_conflict', message: 'm' };
		const ready = preview({ stacks: [move('st-1', 'app'), move('st-2', 'web')] });
		expect(environmentCheckHeadline(ready, 'NAS')).toEqual({
			tone: 'info',
			title: 'Ready to migrate 2 stacks to NAS'
		});
		expect(
			environmentCheckHeadline(
				preview({
					warnings: [warn],
					stacks: [move('st-1', 'app', { preview: stackPreview({ warnings: [warn] }) })]
				}),
				'NAS'
			)
		).toEqual({ tone: 'warn', title: 'Ready to migrate 1 stack to NAS, with 2 warnings' });
		expect(
			environmentCheckHeadline(
				preview({
					blockers: [{ code: 'insufficient_space', message: 'm' }],
					stacks: [move('st-1', 'app', { preview: stackPreview({ blockers: [block] }) })]
				}),
				'NAS'
			)
		).toEqual({ tone: 'danger', title: 'Fix 2 problems before migrating' });
	});

	it('lists the groups in move order with what each stack waits for', () => {
		const p = preview({
			groups: [['st-p', 'st-a', 'st-b'], ['st-c']],
			stacks: [
				move('st-p', 'proxy'),
				move('st-a', 'app', { dependsOn: ['st-p'] }),
				move('st-b', 'blog', { dependsOn: ['st-p', 'st-a'] }),
				move('st-c', 'cloud', { group: 1 })
			]
		});
		const titles: Record<string, string> = { 'st-a': 'App' };
		const order = moveOrder(p, (id, name) => titles[id] ?? name);
		expect(order.map((g) => g.stacks)).toEqual([
			[
				{ stackId: 'st-p', title: 'proxy', after: [] },
				{ stackId: 'st-a', title: 'App', after: ['proxy'] },
				{ stackId: 'st-b', title: 'blog', after: ['proxy', 'App'] }
			],
			[{ stackId: 'st-c', title: 'cloud', after: [] }]
		]);
		expect(new Set(order.map((g) => g.key)).size).toBe(2);
		expect(andList(['proxy'])).toBe('proxy');
		expect(andList(['proxy', 'App', 'db'])).toBe('proxy, App and db');
	});

	it('says why stacks are not moved; Docker Manager’s own stays its own', () => {
		const p = preview({
			skipped: [
				{ stackId: 'st-dm', name: 'dm', reason: 'not_selected' },
				{ stackId: 'st-3', name: 'db', reason: 'not_permitted' },
				{ stackId: 'st-4', name: 'x', reason: 'not_selected' }
			]
		});
		expect(skippedRows(p, ['st-dm']).map((r) => r.reason)).toEqual([
			"Docker Manager's own stack. It moves when you move Docker Manager.",
			'You may not migrate this stack.',
			'Not selected.'
		]);
	});

	it('summarises a stack’s detail', () => {
		const vol = { source: 'v', target: 'v', bytes: 1, entries: 1 };
		expect(
			stackMoveSummary(
				move('st-1', 'app', {
					preview: stackPreview({
						volumes: [
							{ ...vol, action: 'copy' },
							{ ...vol, action: 'skip' }
						],
						warnings: [{ code: 'image_rebuild', message: 'm' }]
					})
				})
			)
		).toBe('1 volume copied, 1 warning');
		expect(stackMoveSummary(move('st-1', 'app'))).toBe('no volume data');
	});
});

describe('the outcome', () => {
	const r = record([
		{ stackId: 'st-1', name: 'app', state: 'moved', migrationId: 'm-1' },
		{ stackId: 'st-2', name: 'web', state: 'failed', migrationId: 'm-2' },
		{ stackId: 'st-3', name: 'db', state: 'pending' }
	]);

	it('sorts the stacks by outcome and lists the old copies to remove', () => {
		const o = migrationOutcome(r);
		expect(o.moved.map((s) => s.stackId)).toEqual(['st-1']);
		expect(o.failed.map((s) => s.stackId)).toEqual(['st-2']);
		expect(o.left.map((s) => s.stackId)).toEqual(['st-2', 'st-3']);
		expect(oldCopies(r, (_id, name) => name.toUpperCase())).toEqual([
			{ stackId: 'st-1', migrationId: 'm-1', title: 'APP' }
		]);
		expect(
			oldCopies(
				record([
					{
						stackId: 'st-1',
						name: 'app',
						state: 'moved',
						migrationId: 'm-1',
						sourceRemoved: true
					}
				])
			)
		).toEqual([]);
		expect(everyStackMoved(r)).toBe(false);
		expect(everyStackMoved(record([r.stacks[0]]))).toBe(true);
		expect(everyStackMoved(record([]))).toBe(false);
		expect(moveState('failed')).toEqual({ label: 'Did Not Move', tone: 'danger' });
		expect(moveState('pending').label).toBe('Not Started');
	});

	it('names the stack that did not move and opens its migration', () => {
		const names = { source: 'homelab', destination: 'NAS' };
		expect(finishToast(r, { id: 'job-e', state: 'failed' }, names)).toEqual({
			tone: 'error',
			title: 'web did not move to NAS',
			body: '1 stack moved to NAS and stays there. The stacks that did not move run on homelab again; migrate again to move them.',
			jobId: 'm-2'
		});
		expect(
			finishToast(record([r.stacks[0]]), { id: 'job-e', state: 'succeeded' }, names)
		).toEqual({ tone: 'success', title: 'Migrated 1 stack to NAS' });
		expect(finishToast(undefined, { id: 'job-e', state: 'failed' }, names)).toEqual({
			tone: 'error',
			title: 'The migration of homelab stopped',
			body: 'Nothing moved: the stacks run on homelab again.',
			jobId: 'job-e'
		});
		expect(finishToast(r, { id: 'job-e', state: 'cancelled' }, names).tone).toBe('warn');
	});
});

describe('removeOldCopies', () => {
	const copies = [
		{ stackId: 'st-1', migrationId: 'm-1', title: 'app' },
		{ stackId: 'st-2', migrationId: 'm-2', title: 'web' },
		{ stackId: 'st-3', migrationId: 'm-3', title: 'db' }
	];

	it('sends one removal per copy and resolves once every job ended', async () => {
		const sent: string[] = [];
		const outcome = await removeOldCopies(copies, {
			send: async (c) => {
				sent.push(`${c.stackId}/${c.migrationId}`);
				if (c.stackId === 'st-3') throw new Error('refused');
				return { id: `job-${c.stackId}` };
			},
			watch: (id, onfinish) =>
				queueMicrotask(() =>
					onfinish(ended(id, id === 'job-st-2' ? 'failed' : 'succeeded'))
				)
		});
		expect(sent).toEqual(['st-1/m-1', 'st-2/m-2', 'st-3/m-3']);
		expect(outcome.succeeded).toEqual(['app']);
		expect([...outcome.failed].sort()).toEqual(['db', 'web']);
	});

	it('resolves at once without copies', async () => {
		expect((await removeOldCopies([])).succeeded).toEqual([]);
	});
});

describe('an ended migration to act on', () => {
	type Moved = EnvironmentMigration['stacks'][number];
	const run = (
		id: string,
		state: EnvironmentMigration['state'],
		stacks: Moved[],
		targetEnvironmentId = 'e2'
	): EnvironmentMigration => ({ ...record(stacks), id, state, targetEnvironmentId });
	const moved = (stackId: string, name: string, sourceRemoved = false): Moved => ({
		stackId,
		name,
		state: 'moved',
		migrationId: `m-${stackId}`,
		sourceRemoved: sourceRemoved || undefined
	});
	const failed = (stackId: string, name: string): Moved => ({
		stackId,
		name,
		state: 'failed',
		migrationId: `m-${stackId}`
	});
	const nowhere = () => false;

	it('tells ended runs from running ones and words an old copy', () => {
		expect(migrationEnded({ state: 'running' })).toBe(false);
		for (const st of ['completed', 'failed', 'cancelled', 'interrupted'] as const)
			expect(migrationEnded({ state: st })).toBe(true);
		expect(oldCopyState(moved('st-1', 'app'))).toBe('Old Copy Kept');
		expect(oldCopyState(moved('st-1', 'app', true))).toBe('Old Copy Removed');
		expect(oldCopyState(failed('st-1', 'app'))).toBeUndefined();
	});

	it("collects every ended run's old copies, newest first, one per stack", () => {
		const records = [
			run('j3', 'running', [moved('st-9', 'live')]),
			run('j2', 'completed', [moved('st-2', 'web'), moved('st-1', 'app')], 'e3'),
			run('j1', 'failed', [moved('st-1', 'app'), moved('st-3', 'db', true)])
		];
		expect(pendingCopies(records, (_id, name) => name.toUpperCase())).toEqual([
			{ stackId: 'st-2', migrationId: 'm-st-2', title: 'WEB', destinationId: 'e3' },
			{ stackId: 'st-1', migrationId: 'm-st-1', title: 'APP', destinationId: 'e3' }
		]);
	});

	it('lists the stacks of a run still on the source', () => {
		const r = run('j1', 'failed', [
			moved('st-1', 'app'),
			failed('st-2', 'web'),
			failed('st-3', 'db')
		]);
		expect(stacksLeft(r, (id) => id !== 'st-3').map((s) => s.stackId)).toEqual(['st-2']);
	});

	it('restores the latest run once it ended, while something is left to do', () => {
		const copy = run('j1', 'completed', [moved('st-1', 'app')]);
		const left = run('j2', 'failed', [failed('st-2', 'web')]);
		const done = run('j3', 'completed', [moved('st-3', 'db', true)]);
		expect(restoredMigration([], nowhere)).toBeNull();
		expect(restoredMigration([run('j4', 'running', [])], nowhere)).toBeNull();
		expect(restoredMigration([copy], nowhere)).toBe(copy);
		expect(restoredMigration([left], (id) => id === 'st-2')).toBe(left);
		// web moved since (the stack's own migration): nothing left of j2.
		expect(restoredMigration([left], nowhere)).toBeNull();
		// An earlier run's old copy keeps the latest run's result open.
		expect(restoredMigration([done, copy], nowhere)).toBe(done);
		expect(restoredMigration([done], nowhere)).toBeNull();
	});

	it("words the result's notice for what moved and what is left to remove", () => {
		const names = { source: 'homelab', destination: 'NAS' };
		expect(resultNotice(2, 2, names)).toEqual({
			title: '2 stacks run on NAS now.',
			body: 'Their old copies on homelab are stopped and kept. Remove them once you are sure.'
		});
		expect(resultNotice(1, 1, names)).toEqual({
			title: '1 stack runs on NAS now.',
			body: 'Its old copy on homelab is stopped and kept. Remove it once you are sure.'
		});
		expect(resultNotice(2, 3, names)?.body).toBe(
			'Old copies of 3 stacks are stopped and kept on homelab. Remove them once you are sure.'
		);
		expect(resultNotice(2, 0, names)?.body).toBe('Their old copies were removed from homelab.');
		expect(resultNotice(0, 1, names)).toEqual({
			title: '1 old copy still on homelab',
			body: 'It is stopped and kept from an earlier migration. Remove it once you are sure.'
		});
		expect(resultNotice(0, 0, names)).toBeNull();
	});

	it("words the environment page's notice", () => {
		const names: Record<string, string> = { e2: 'NAS', e3: 'lab' };
		const envName = (id: string) => names[id] ?? id;
		expect(
			oldCopiesNotice(
				[run('j1', 'completed', [moved('st-1', 'app'), moved('st-2', 'web')])],
				envName
			)
		).toEqual({
			title: '2 stacks moved to NAS',
			body: 'Their old copies are still on this server.',
			count: 2
		});
		expect(oldCopiesNotice([run('j1', 'failed', [moved('st-1', 'app')])], envName)).toEqual({
			title: '1 stack moved to NAS',
			body: 'Its old copy is still on this server.',
			count: 1
		});
		expect(
			oldCopiesNotice(
				[
					run('j2', 'completed', [moved('st-2', 'web')], 'e3'),
					run('j1', 'completed', [moved('st-1', 'app')])
				],
				envName
			)?.title
		).toBe('2 stacks moved to other environments');
		expect(
			oldCopiesNotice([run('j1', 'completed', [moved('st-1', 'app', true)])], envName)
		).toBeNull();
		expect(oldCopiesNotice([run('j1', 'running', [moved('st-1', 'app')])], envName)).toBeNull();
	});

	it('matches the removals of old copies on the environment', () => {
		const m = oldCopyRemovalMatch('e1');
		const job = {
			kind: 'stack.remove_source',
			environmentId: 'e1',
			targets: [{ type: 'stack', id: 'st-1' }]
		};
		expect(matchJob(job, m)).toBe(true);
		expect(matchJob({ ...job, environmentId: 'e2' }, m)).toBe(false);
		expect(matchJob({ ...job, kind: 'stack.migrate' }, m)).toBe(false);
		expect(
			jobStackIds({
				targets: [
					{ type: 'stack', id: 'st-1' },
					{ type: 'volume', id: 'data' }
				]
			} as Pick<Job, 'targets'>)
		).toEqual(['st-1']);
		expect(jobStackIds(undefined)).toEqual([]);
	});
});

describe('startOldCopyRemovals', () => {
	it('starts one removal per copy and reports the refused ones', async () => {
		const copies = [
			{ stackId: 'st-1', migrationId: 'm-1', title: 'app' },
			{ stackId: 'st-2', migrationId: 'm-2', title: 'web' }
		];
		const out = await startOldCopyRemovals(copies, async (c) => {
			if (c.stackId === 'st-2') throw new Error('refused');
			return { id: `job-${c.stackId}` } as Job;
		});
		expect(out.started.map((s) => [s.copy.title, s.job.id])).toEqual([['app', 'job-st-1']]);
		expect(out.refused.map((c) => c.title)).toEqual(['web']);
		expect(await startOldCopyRemovals([])).toEqual({ started: [], refused: [] });
	});
});
