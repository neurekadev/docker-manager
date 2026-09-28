// Backups overview (#10): storage totals, running backups and set summaries.
import { describe, expect, it } from 'vitest';
import {
	activityItemName,
	activityPercent,
	membersByEnvironment,
	middleTruncate,
	ratioText,
	setBytes,
	setDuration,
	setSummary,
	snapshotName,
	snapshotRows,
	storageTotals,
	type Backup,
	type BackupActivity,
	type BackupRepository,
	type BackupSet,
	type ResticSnapshot
} from './model';

const loc = (
	scope: string,
	environmentId: string | undefined,
	size: number,
	uncompressed: number,
	progress: number,
	snapshots: number,
	measuredAt: string
) => ({
	scope,
	environmentId,
	sizeBytes: size,
	uncompressedBytes: uncompressed,
	compressionRatio: size ? uncompressed / size : 0,
	compressionProgress: progress,
	snapshots,
	measuredAt
});

const repo = (id: string, locations: ReturnType<typeof loc>[]): BackupRepository =>
	({
		id,
		name: `Repo ${id}`,
		kind: 'local',
		state: 'ready',
		view: 'full',
		actions: [],
		storage: locations.length
			? {
					sizeBytes: 0,
					uncompressedBytes: 0,
					compressionRatio: 0,
					compressionProgress: 0,
					snapshots: 0,
					locations
				}
			: undefined
	}) as BackupRepository;

describe('storage totals', () => {
	const repos = [
		repo('a', [
			loc('manager', undefined, 100, 300, 100, 4, '2026-09-27T02:00:00Z'),
			loc('env:e1', 'e1', 300, 500, 50, 6, '2026-09-27T01:00:00Z')
		]),
		repo('b', [loc('env:e2', 'e2', 50, 100, 100, 2, '2026-09-27T03:00:00Z')]),
		repo('c', [])
	];

	it('sums every measured location', () => {
		const t = storageTotals(repos)!;
		expect(t.sizeBytes).toBe(450);
		expect(t.uncompressedBytes).toBe(900);
		expect(t.freedBytes).toBe(450);
		expect(t.ratio).toBe(2);
		expect(t.snapshots).toBe(12);
		expect(t.compressedPercent).toBeCloseTo((100 * 300 + 50 * 500 + 100 * 100) / 900);
		expect(t.measuredAt).toBe('2026-09-27T01:00:00Z');
		expect(t.repositories.map((r) => r.id)).toEqual(['a', 'b']);
		expect(t.repositories[0]).toMatchObject({
			sizeBytes: 400,
			uncompressedBytes: 800,
			ratio: 2
		});
	});

	it('keeps only the selected environment', () => {
		const t = storageTotals(repos, 'e1')!;
		expect(t).toMatchObject({ sizeBytes: 300, uncompressedBytes: 500, snapshots: 6 });
		expect(t.repositories.map((r) => r.id)).toEqual(['a']);
	});

	it('is undefined before anything was measured', () => {
		expect(storageTotals([repo('c', [])])).toBeUndefined();
		expect(storageTotals(repos, 'e9')).toBeUndefined();
	});

	it('never reports negative savings', () => {
		const t = storageTotals([
			repo('x', [loc('env:e1', 'e1', 120, 100, 0, 1, '2026-09-27T01:00:00Z')])
		])!;
		expect(t.freedBytes).toBe(0);
		expect(ratioText(t.ratio)).toBe('0.83x');
	});

	it('writes ratios like 2.01x', () => {
		expect(ratioText(160 / 79.6)).toBe('2.01x');
		expect(ratioText(0)).toBe('—');
		expect(ratioText(Infinity)).toBe('—');
	});
});

describe('running backups', () => {
	const job = (current?: Partial<BackupActivity['current']>, itemCount = 4): BackupActivity =>
		({
			jobId: 'j1',
			kind: 'backup.run',
			state: 'running',
			setId: 's1',
			percent: 30,
			itemCount,
			current: current && {
				item: 'volume/media',
				kind: 'volume',
				index: 0,
				percent: 0,
				filesDone: 0,
				filesTotal: 0,
				bytesDone: 0,
				bytesTotal: 0,
				reportedAt: '2026-09-27T01:00:00Z',
				...current
			}
		}) as BackupActivity;

	it('spreads progress over the items', () => {
		expect(activityPercent(job({ index: 1, percent: 50 }))).toBe(38);
		expect(activityPercent(job({ index: 3, percent: 100 }))).toBe(100);
		// Before the first report: the job's own progress.
		expect(activityPercent(job())).toBe(30);
	});

	it('names the item', () => {
		expect(activityItemName(job({ volume: 'media' }).current!)).toBe('Volume media');
		expect(activityItemName(job({ kind: 'stack', stackName: 'shop' }).current!)).toBe(
			'Stack shop'
		);
		expect(activityItemName(job({ kind: 'manager_state' }).current!)).toBe('Manager state');
	});

	it('shortens long paths in the middle', () => {
		const p = `media/${'deep/'.repeat(30)}holiday.jpg`;
		const t = middleTruncate(p, 40);
		expect(t.length).toBe(40);
		expect(t.startsWith('media/')).toBe(true);
		expect(t.endsWith('holiday.jpg')).toBe(true);
		expect(t).toContain('…');
		expect(middleTruncate('short.txt', 40)).toBe('short.txt');
	});
});

describe('set summaries', () => {
	const member = (state: string, environmentId?: string) => ({
		item: 'x',
		kind: 'volume',
		scope: 's',
		state,
		environmentId
	});
	const set = (members: ReturnType<typeof member>[], finishedAt?: string) =>
		({
			id: 's1',
			state: 'partial',
			origin: 'scheduled',
			startedAt: '2026-09-27T01:00:00Z',
			finishedAt,
			members
		}) as BackupSet;

	it('counts complete backups and environments', () => {
		expect(setSummary(set([member('complete', 'e1'), member('complete', 'e1')]))).toBe(
			'2 backups'
		);
		expect(
			setSummary(set([member('complete', 'e1'), member('failed', 'e2'), member('complete')]))
		).toBe('2 of 3 complete · 2 environments');
		expect(setSummary(set([]))).toBe('Nothing selected');
	});

	it('counts skipped members apart, not as missing backups', () => {
		expect(setSummary(set([member('complete', 'e1'), member('skipped', 'e1')]))).toBe(
			'1 backup · 1 skipped'
		);
		expect(setSummary(set([member('complete'), member('failed'), member('skipped')]))).toBe(
			'1 of 2 complete · 1 skipped'
		);
		expect(setSummary(set([member('skipped'), member('skipped')]))).toBe('2 skipped');
	});

	it('measures duration once finished', () => {
		expect(setDuration(set([], '2026-09-27T01:02:30Z'))).toBe(150);
		expect(setDuration(set([]))).toBeUndefined();
	});

	it('adds up the sizes of its backups', () => {
		const backups = [
			{ setId: 's1', bytes: 10 },
			{ setId: 's1', bytes: 5 },
			{ setId: 's2', bytes: 99 },
			{ setId: 's1' }
		] as Backup[];
		expect(setBytes(backups, 's1')).toBe(15);
		expect(setBytes(backups, 's3')).toBeUndefined();
	});

	it('groups backups by environment, the manager last', () => {
		const groups = membersByEnvironment(
			set([member('complete'), member('complete', 'e2'), member('failed', 'e1')])
		);
		expect(groups.map(([env, ms]) => [env, ms.length])).toEqual([
			['e1', 1],
			['e2', 1],
			['', 1]
		]);
	});
});

describe('restic snapshots', () => {
	const snap = (
		id: string,
		time: string,
		extra: Partial<ResticSnapshot> = {}
	): ResticSnapshot => ({
		id,
		shortId: id.slice(0, 8),
		time,
		paths: ['/data'],
		tags: [],
		class: 'volume',
		...extra
	});
	const listings = [
		{
			repository: { id: 'r1', name: 'Local' },
			locations: [
				{
					scope: 'manager',
					truncated: false,
					snapshots: [snap('m1', '2026-09-27T01:00:00Z', { class: 'set_manifest' })]
				},
				{
					scope: 'env:e1',
					environmentId: 'e1',
					truncated: true,
					snapshots: [snap('v1', '2026-09-27T03:00:00Z', { item: 'volume/media' })]
				}
			]
		},
		{
			repository: { id: 'r2', name: 'Offsite' },
			locations: [
				{
					scope: 'env:e2',
					environmentId: 'e2',
					errorClass: 'agent_offline',
					truncated: false,
					snapshots: []
				},
				{
					scope: 'env:e1',
					environmentId: 'e1',
					truncated: false,
					snapshots: [snap('v2', '2026-09-27T02:00:00Z', { name: 'db', backupId: 'b1' })]
				}
			]
		}
	];

	it('lists every location newest first and names what could not be read', () => {
		const { rows, problems } = snapshotRows(listings);
		expect(rows.map((r) => r.snapshot.id)).toEqual(['v1', 'v2', 'm1']);
		expect(rows[1]).toMatchObject({
			repositoryName: 'Offsite',
			scope: 'env:e1',
			environmentId: 'e1'
		});
		expect(problems).toEqual([
			{
				repositoryName: 'Local',
				scope: 'env:e1',
				environmentId: 'e1',
				errorClass: undefined,
				truncated: true
			},
			{
				repositoryName: 'Offsite',
				scope: 'env:e2',
				environmentId: 'e2',
				errorClass: 'agent_offline',
				truncated: false
			}
		]);
	});

	it('keeps only the selected environment (the manager scope has none)', () => {
		const { rows, problems } = snapshotRows(listings, 'e1');
		expect(rows.map((r) => r.snapshot.id)).toEqual(['v1', 'v2']);
		expect(problems.map((p) => p.repositoryName)).toEqual(['Local']);
	});

	it('names snapshots by what they hold', () => {
		expect(snapshotName(snap('a', '', { name: 'db' }))).toBe('db');
		expect(snapshotName(snap('a', '', { item: 'volume/media' }))).toBe('media');
		expect(snapshotName(snap('a', '', { class: 'stack', item: 'stack/01a0e0da-c66c' }))).toBe(
			'Stack 01a0e0da'
		);
		expect(snapshotName(snap('a', '', { class: 'manager_state' }))).toBe('Manager state');
		expect(snapshotName(snap('a', '', { class: 'host_manifest' }))).toBe('Manifest');
		expect(snapshotName(snap('abcdef123', '', { class: 'foreign', paths: [] }))).toBe(
			'abcdef12'
		);
		expect(snapshotName(snap('a', '', { class: 'foreign', paths: ['/srv', '/etc'] }))).toBe(
			'/srv, /etc'
		);
	});
});
