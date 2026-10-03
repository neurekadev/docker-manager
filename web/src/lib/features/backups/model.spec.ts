import { describe, expect, it } from 'vitest';
import { destinationReady, emptyDestination } from './destination';
import {
	IMPORT_ERRORS,
	bundleText,
	importBlocker,
	importSource,
	locationState,
	type ImportSet
} from './importModel';
import {
	RECOVERY_KEY_WARNING,
	coveredVolumes,
	hasRetentionRules,
	incompleteMembers,
	isBuildxVolume,
	isHelperContainer,
	labelLockReason,
	retentionGroups,
	retentionReasonText,
	scopeCounts,
	scopeItemTitle,
	scopeNeedsAttention,
	scopeSources,
	memberReason,
	memberState,
	looksLikeRecoveryKey,
	modeText,
	normalizeRecoveryKey,
	parentPath,
	pathCrumbs,
	repositoryLocation,
	retentionText,
	roleLabel,
	setState,
	volumeKey,
	type BackupRepository,
	type BackupSet
} from './model';

const KEY = 'DYRK-4RN4-LUDA-QCP3-RQ2S-RV4J-OYB7-3PZF-CXUB-5YG2-MY7Z-O5A2-HZ4D-FGLA';

describe('Recovery Key', () => {
	it('says a restore needs the key and that losing both copies loses the data (#10)', () => {
		expect(RECOVERY_KEY_WARNING).toContain('needs this Recovery Key');
		expect(RECOVERY_KEY_WARNING).toContain('manager key store and your copy are both lost');
		expect(RECOVERY_KEY_WARNING).toContain('can’t be restored');
	});

	it('recognizes the DYRK format, tolerating spaces and lower case', () => {
		expect(looksLikeRecoveryKey(KEY)).toBe(true);
		expect(looksLikeRecoveryKey(` ${KEY.toLowerCase()} `)).toBe(true);
		expect(looksLikeRecoveryKey(KEY.slice(0, -5))).toBe(false);
		expect(looksLikeRecoveryKey('DYRK-0000-' + KEY.slice(10))).toBe(false); // 0 is not base32
		expect(normalizeRecoveryKey('dyrk-abcd\n efgh')).toBe('DYRK-ABCDEFGH');
	});
});

describe('retention', () => {
	it('describes the rules', () => {
		expect(retentionText(undefined)).toBe('Keep every backup');
		expect(retentionText({ afterBackup: true })).toBe('Keep every backup');
		expect(retentionText({ daily: 7, weekly: 4 })).toBe('Keep 7 daily, 4 weekly');
		expect(retentionText({ last: 2, withinDays: 10 })).toBe(
			'Keep last 2, everything from the last 10 days'
		);
		expect(hasRetentionRules({ afterBackup: true })).toBe(false);
		expect(hasRetentionRules({ monthly: 1 })).toBe(true);
	});
});

describe('backup sets', () => {
	it('lists the members a retry re-runs: neither complete nor skipped', () => {
		const s: BackupSet = {
			id: 's2',
			startedAt: '2026-09-25T02:00:00Z',
			origin: 'manual',
			state: 'partial',
			members: [
				{
					item: 'silo',
					kind: 'stack',
					scope: 'env:e1',
					repositoryId: 'r2',
					state: 'failed',
					errorClass: 'restic_failed'
				},
				{
					item: 'silo',
					kind: 'stack',
					scope: 'env:e1',
					repositoryId: 'r1',
					state: 'complete'
				},
				{
					item: 'manager',
					kind: 'manager_state',
					scope: 'manager',
					repositoryId: 'r1',
					state: 'complete'
				}
			]
		};
		expect(incompleteMembers(s).map((m) => [m.item, m.repositoryId])).toEqual([['silo', 'r2']]);
	});

	it('names the role of a repository', () => {
		expect(roleLabel('primary')).toBe('Primary');
		expect(roleLabel('secondary')).toBe('Secondary');
		expect(roleLabel(undefined)).toBeUndefined();
	});

	it('groups volumes into standalone and stack volumes, named or anonymous', () => {
		const anon = { 'com.docker.volume.anonymous': '' };
		const out = coveredVolumes(
			[
				{ name: 'media' },
				{ name: 'shop_db', stack: { project: 'shop', stackId: 's1' } },
				{ name: 'shop_cache', labels: { 'com.docker.compose.project': 'shop' } },
				{ name: '3f2a', labels: anon, usedBy: [{ id: 'c1' }] },
				{ name: '9c1b', labels: anon },
				{ name: 'docker-manager-data', protection: { reason: 'own' } },
				{ name: 'other_data', stack: { project: 'unmanaged' } }
			],
			[{ id: 's1', name: 'shop' }],
			[{ id: 'c1', labels: { 'com.docker.compose.project': 'shop' } }]
		);
		const plain = { buildx: false, labelled: false, temporary: false };
		expect(out).toEqual([
			{ name: '3f2a', anonymous: true, stackId: 's1', ...plain },
			{ name: '9c1b', anonymous: true, stackId: undefined, ...plain },
			{ name: 'media', anonymous: false, stackId: undefined, ...plain },
			{ name: 'shop_cache', anonymous: false, stackId: 's1', ...plain },
			{ name: 'shop_db', anonymous: false, stackId: 's1', ...plain }
		]);
		expect(volumeKey('e1', 'media')).toBe('e1/media');
	});

	it('marks buildx builder volumes and volumes the backup exclude label leaves out', () => {
		const exclude = { 'docker-manager.backup.exclude': 'TRUE' };
		const out = coveredVolumes(
			[
				{ name: 'buildx_buildkit_builder0_state' },
				{ name: 'cache', labels: exclude },
				{ name: 'dumps', usedBy: [{ id: 'c1' }] },
				{ name: 'media' }
			],
			[],
			[{ id: 'c1', labels: exclude }]
		);
		expect(out.map((v) => [v.name, v.buildx, v.labelled])).toEqual([
			['buildx_buildkit_builder0_state', true, false],
			['cache', false, true],
			['dumps', false, true],
			['media', false, false]
		]);
		expect(isBuildxVolume('buildx_buildkit__state')).toBe(false);
	});

	it('says where the backup exclude label is, honoring Compose labels over the volume', () => {
		const exclude = { 'docker-manager.backup.exclude': 'true' };
		const out = coveredVolumes(
			[
				{ name: 'a_compose', composeLabels: exclude },
				{ name: 'b_volume', labels: exclude },
				{
					name: 'c_unset',
					labels: exclude,
					composeLabels: { 'docker-manager.backup.exclude': 'false' }
				},
				{ name: 'd_container', usedBy: [{ id: 'c1' }] }
			],
			[],
			[{ id: 'c1', labels: exclude }]
		);
		expect(out.map((v) => [v.name, v.labelled, v.labelledBy])).toEqual([
			['a_compose', true, 'compose'],
			['b_volume', true, 'volume'],
			['c_unset', false, undefined],
			['d_container', true, 'container']
		]);
		expect(labelLockReason('compose')).toMatch(
			/^Managed by a volume label in the stack's Compose file/
		);
		expect(labelLockReason('volume')).toMatch(/^Managed by a volume label:/);
		expect(labelLockReason('container')).toMatch(/^Managed by a container label/);
	});

	it('recognizes temporary containers of Docker Manager and Compose like the manager', () => {
		const replace = { 'com.docker.compose.replace': 'app-db-1' };
		expect(isHelperContainer('web-docker-manager-update-0123456789ab', {})).toBe(true);
		expect(isHelperContainer('/db-docker-manager-rename-abcdef012345', undefined)).toBe(true);
		expect(isHelperContainer('0123456789ab_app-db-1', replace)).toBe(true);
		expect(isHelperContainer('helper', { 'docker-manager.role': 'self-update' })).toBe(true);
		// A helper started before the label prefix changed.
		expect(
			isHelperContainer('helper', { 'dev.neureka.docker-manager.role': 'self-update' })
		).toBe(true);
		// The current key wins over a legacy one.
		expect(
			isHelperContainer('agent', {
				'docker-manager.role': 'agent',
				'dev.neureka.docker-manager.role': 'self-update'
			})
		).toBe(false);
		// Compose keeps the label on the renamed replacement: not temporary.
		expect(isHelperContainer('app-db-1', replace)).toBe(false);
		expect(isHelperContainer('0123456789ab_app-db-1', {})).toBe(false);
		expect(isHelperContainer('web-docker-manager-update-0123456789AB', {})).toBe(false);
		expect(isHelperContainer('web-docker-manager-update-0123456789a', {})).toBe(false);
		expect(isHelperContainer('-docker-manager-update-0123456789ab', {})).toBe(false);
		expect(isHelperContainer('web', { 'dev.neureka.docker-manager.role': 'agent' })).toBe(
			false
		);
	});

	it('marks standalone volumes only temporary containers use', () => {
		const out = coveredVolumes(
			[
				{ name: 'aside-only', usedBy: [{ id: 'aside' }] },
				{ name: 'media', usedBy: [{ id: 'aside' }, { id: 'web' }] },
				{ name: 'replace-only', usedBy: [{ id: 'tmp' }] },
				{ name: 'unused' }
			],
			[],
			[
				{ id: 'aside', name: 'web-docker-manager-update-0123456789ab' },
				{ id: 'web', name: 'web' },
				{
					id: 'tmp',
					name: '0123456789ab_app-db-1',
					labels: { 'com.docker.compose.replace': 'app-db-1' }
				}
			]
		);
		expect(out.map((v) => [v.name, v.temporary])).toEqual([
			['aside-only', true],
			['media', false],
			['replace-only', true],
			['unused', false]
		]);
	});
});

describe('skipped backups', () => {
	it('shows skipped sets and members as neutral, never as errors', () => {
		expect(setState('skipped')).toEqual({ tone: 'neutral', label: 'Skipped' });
		expect(memberState('skipped')).toEqual({ tone: 'neutral', label: 'Skipped' });
		expect(memberReason({ state: 'skipped', errorClass: 'item_gone' })).toEqual({
			text: 'Removed before its turn',
			error: false
		});
		expect(memberReason({ state: 'failed', errorClass: 'volume_unavailable' })).toEqual({
			text: 'Volume unavailable',
			error: true
		});
		expect(memberReason({ state: 'complete' })).toBeUndefined();
	});

	it('never retries skipped members', () => {
		const set = {
			id: 'set',
			state: 'partial',
			origin: 'manual',
			startedAt: '2026-09-27T01:00:00Z',
			members: [
				{ item: 'a', kind: 'volume', scope: 's', state: 'complete' },
				{
					item: 'gone',
					kind: 'volume',
					scope: 's',
					state: 'skipped',
					errorClass: 'item_gone'
				},
				{ item: 'broken', kind: 'volume', scope: 's', state: 'failed' }
			]
		} as BackupSet;
		expect(incompleteMembers(set).map((m) => m.item)).toEqual(['broken']);
	});
});

describe('repositories', () => {
	it('shows where a repository lives without credentials', () => {
		const s3 = {
			bucket: 'b',
			prefix: '/dy',
			endpoint: 'https://minio:9000'
		} as BackupRepository;
		expect(repositoryLocation(s3)).toBe('s3://b/dy on minio:9000');
		expect(repositoryLocation({ bucket: 'b' } as BackupRepository)).toBe('s3://b');
	});

	it('checks a destination before it is sent', () => {
		expect(destinationReady(emptyDestination())).toBe(false);
		const s3 = { ...emptyDestination(), endpoint: 'https://s3', bucket: 'b' };
		expect(destinationReady(s3)).toBe(false);
		expect(destinationReady(s3, false)).toBe(true);
		expect(destinationReady({ ...s3, accessKeyId: 'id', secretAccessKey: 'x' })).toBe(true);
	});
});

describe('snapshot browsing', () => {
	it('splits paths into crumbs and parents', () => {
		expect(pathCrumbs('/a/b')).toEqual([
			{ name: '/', path: '/' },
			{ name: 'a', path: '/a' },
			{ name: 'b', path: '/a/b' }
		]);
		expect(parentPath('/a/b/')).toBe('/a');
		expect(parentPath('/a')).toBe('/');
		expect(modeText(0o644)).toBe('0644');
		expect(modeText(0o100755)).toBe('0755');
	});
});

describe('fresh-manager import (#24)', () => {
	it('builds the request from the destination', () => {
		const plain = importSource(
			{
				...emptyDestination(),
				endpoint: ' https://s3 ',
				bucket: 'b',
				accessKeyId: 'AK',
				secretAccessKey: 'SK'
			},
			KEY.toLowerCase(),
			''
		);
		expect(plain).toEqual({
			endpoint: 'https://s3',
			bucket: 'b',
			pathStyle: true,
			accessKeyId: 'AK',
			secretAccessKey: 'SK',
			recoveryKey: KEY
		});
		const s3 = importSource(
			{
				...emptyDestination(),
				endpoint: 'https://s3',
				bucket: 'b',
				accessKeyId: 'AK',
				secretAccessKey: 'SK'
			},
			KEY,
			KEY,
			{ setId: 'set1' }
		);
		expect(s3).toMatchObject({
			bucket: 'b',
			accessKeyId: 'AK',
			secretAccessKey: 'SK',
			setId: 'set1'
		});
		expect(s3.previousRecoveryKey).toBe(KEY);
	});

	it('explains why a set cannot be imported', () => {
		const set = (s: Partial<ImportSet>): ImportSet => ({
			setId: 's',
			hostOnly: false,
			importable: true,
			schemaCompatible: true,
			members: [],
			problems: [],
			...s
		});
		expect(importBlocker(set({}))).toBeNull();
		expect(importBlocker(set({ hostOnly: true }))).toMatch(/host repository/);
		expect(importBlocker(set({ schemaCompatible: false, appVersion: '2.0.0' }))).toMatch(
			/2\.0\.0/
		);
		expect(importBlocker(set({ importable: false, problems: ['Damaged manifest.'] }))).toBe(
			'Damaged manifest.'
		);
		expect(bundleText('previous_key')).toMatch(/previous Recovery Key/);
		expect(bundleText(undefined)).toBeNull();
	});

	it('says which key opens each location', () => {
		const loc = { repository: 'r', scope: 'env:e1', found: true, reachable: true };
		expect(locationState({ ...loc, key: 'current' }).tone).toBe('ok');
		expect(locationState({ ...loc, key: 'previous' }).label).toMatch(/Previous Key/);
		expect(locationState({ ...loc, reachable: false }).label).toBe('Not Reachable From Here');
		expect(locationState({ ...loc, found: false }).label).toBe('Not Found');
	});

	it('has recovery copy for every documented import error (#24)', () => {
		for (const code of [
			'backup_import_key_rejected',
			'backup_import_not_found',
			'backup_import_manifest_corrupt',
			'backup_import_schema_incompatible',
			'backup_import_key_rotated',
			'backup_import_state_missing',
			'backup_import_unreachable',
			'backup_import_in_progress'
		])
			expect(IMPORT_ERRORS[code]).toBeTruthy();
		expect(IMPORT_ERRORS.backup_import_key_rejected).toMatch(/cannot be recovered/);
	});
});

describe('scope preview (#10)', () => {
	const src = (state: string, path = '/p', service?: string) => ({
		kind: 'bind',
		path,
		state,
		service
	});
	const item = (sources: ReturnType<typeof src>[], extra = {}) => ({
		item: 'stack/s1',
		kind: 'stack',
		stackId: 's1',
		estimateComplete: true,
		estimatedBytes: 1,
		estimatedFiles: 1,
		sources,
		...extra
	});

	it('lists each source once, what needs attention first', () => {
		const out = scopeSources([
			src('included', '/a'),
			src('excluded', '/b'),
			src('blocked', '/c'),
			src('included', '/a')
		]);
		expect(out.map((s) => s.path)).toEqual(['/c', '/a', '/b']);
		expect(scopeCounts(out)).toEqual([
			{ state: 'blocked', count: 1 },
			{ state: 'included', count: 1 },
			{ state: 'excluded', count: 1 }
		]);
	});

	it('opens items that need the user and names stacks and volumes', () => {
		expect(scopeNeedsAttention(item([src('included')]))).toBe(false);
		expect(scopeNeedsAttention(item([src('requires_opt_in')]))).toBe(true);
		expect(scopeNeedsAttention(item([src('included')], { conflicts: ['x'] }))).toBe(true);
		expect(scopeItemTitle(item([]), (id) => (id === 's1' ? 'media' : undefined))).toBe('media');
		expect(scopeItemTitle(item([]))).toBe('s1');
		expect(
			scopeItemTitle({
				...item([]),
				kind: 'volume',
				stackId: undefined,
				item: 'volume/data',
				volume: 'data'
			})
		).toBe('data');
	});
});

describe('retention preview (#10)', () => {
	const d = (item: string, keep: boolean, time: string, reasons?: string[]) => ({
		snapshotId: `${item}@${time}`,
		item,
		keep,
		time,
		reasons
	});

	it('groups decisions per stack or volume by name, those losing backups first', () => {
		const groups = retentionGroups(
			[
				d('volume/media', true, '2026-09-02T00:00:00Z', ['daily']),
				d('stack/s1', true, '2026-09-01T00:00:00Z', ['last']),
				d('stack/s1', false, '2026-08-01T00:00:00Z'),
				d('stack/s1', true, '2026-09-03T00:00:00Z', ['last']),
				d('manager', true, '2026-09-01T00:00:00Z', ['newest'])
			],
			(id) => (id === 's1' ? 'shop' : undefined)
		);
		expect(groups.map((g) => [g.name, g.forget, g.keep])).toEqual([
			['Stack shop', 1, 2],
			['Manager State', 0, 1],
			['Volume media', 0, 1]
		]);
		expect(groups[0].decisions.map((x) => x.time)).toEqual([
			'2026-09-03T00:00:00Z',
			'2026-09-01T00:00:00Z',
			'2026-08-01T00:00:00Z'
		]);
		expect(retentionReasonText('daily')).toBe('the daily rule');
		expect(retentionReasonText('odd')).toBe('odd');
	});
});
