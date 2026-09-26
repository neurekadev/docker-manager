import { describe, expect, it } from 'vitest';
import { destinationReady, emptyDestination, isAbsolutePath } from './destination';
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
	hasRetentionRules,
	incompleteMembers,
	looksLikeRecoveryKey,
	modeText,
	normalizeRecoveryKey,
	parentPath,
	pathCrumbs,
	recentSets,
	repositoryLocation,
	retentionText,
	scopeText,
	type BackupPolicy,
	type BackupRepository
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
	it('describes the rules and the recovery floor', () => {
		expect(retentionText(undefined)).toBe('Keep every backup');
		expect(retentionText({ minKeep: 3 })).toBe('Keep every backup');
		expect(retentionText({ daily: 7, weekly: 4, minKeep: 3 })).toBe(
			'Keep 7 daily, 4 weekly; always keeps the newest 3 of each'
		);
		expect(retentionText({ last: 2, withinDays: 10 })).toBe(
			'Keep last 2, everything from the last 10 days'
		);
		expect(hasRetentionRules({ minKeep: 3, afterBackup: true })).toBe(false);
		expect(hasRetentionRules({ monthly: 1 })).toBe(true);
	});
});

describe('backup sets', () => {
	const p = (id: string, sets: BackupPolicy['recentSets']) =>
		({ id, name: id, recentSets: sets }) as BackupPolicy;

	it('merges the recent sets of every policy, newest first, keeping partial sets partial', () => {
		const sets = recentSets([
			p('a', [
				{
					id: 's1',
					startedAt: '2026-09-24T02:00:00Z',
					origin: 'scheduled',
					state: 'complete',
					members: []
				}
			]),
			p('b', [
				{
					id: 's2',
					startedAt: '2026-09-25T02:00:00Z',
					origin: 'manual',
					state: 'partial',
					members: [
						{
							item: 'silo',
							kind: 'stack',
							scope: 'env:e1',
							state: 'failed',
							errorClass: 'restic_failed'
						},
						{
							item: 'manager',
							kind: 'manager_state',
							scope: 'manager',
							state: 'complete'
						}
					]
				}
			])
		]);
		expect(sets.map((s) => [s.id, s.policyName, s.state])).toEqual([
			['s2', 'b', 'partial'],
			['s1', 'a', 'complete']
		]);
		expect(incompleteMembers(sets[0]).map((m) => m.item)).toEqual(['silo']);
	});

	it('describes what a policy backs up', () => {
		expect(scopeText({ includeManagerState: true, scope: 'all' })).toBe(
			'all environments and the manager state'
		);
		expect(scopeText({ includeManagerState: false, scope: 'environment' })).toBe(
			'one environment'
		);
	});
});

describe('repositories', () => {
	it('shows where a repository lives without credentials', () => {
		const s3 = {
			kind: 's3',
			bucket: 'b',
			prefix: '/dy',
			endpoint: 'https://minio:9000'
		} as BackupRepository;
		expect(repositoryLocation(s3)).toBe('s3://b/dy on minio:9000');
		const local = { kind: 'local', executor: 'e1', path: '/backups' } as BackupRepository;
		expect(repositoryLocation(local, () => 'homelab')).toBe('homelab:/backups');
		expect(
			repositoryLocation({
				kind: 'local',
				executor: 'manager',
				path: '/b'
			} as BackupRepository)
		).toBe('manager:/b');
	});

	it('checks a destination before it is sent', () => {
		const d = emptyDestination();
		expect(destinationReady(d)).toBe(false);
		d.path = '/backups';
		expect(destinationReady(d)).toBe(true);
		const s3 = {
			...emptyDestination(),
			kind: 's3' as const,
			endpoint: 'https://s3',
			bucket: 'b'
		};
		expect(destinationReady(s3)).toBe(false);
		expect(destinationReady(s3, false)).toBe(true);
		expect(destinationReady({ ...s3, accessKeyId: 'id', secretAccessKey: 'x' })).toBe(true);
		expect(isAbsolutePath('relative/path')).toBe(false);
		expect(isAbsolutePath('C:/backups')).toBe(true);
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
	it('builds the request with only the fields of the destination kind', () => {
		const local = importSource(
			{ ...emptyDestination(), path: ' /mnt/b ' },
			KEY.toLowerCase(),
			''
		);
		expect(local).toEqual({ kind: 'local', path: '/mnt/b', recoveryKey: KEY });
		const s3 = importSource(
			{
				...emptyDestination(),
				kind: 's3',
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
			kind: 's3',
			bucket: 'b',
			accessKeyId: 'AK',
			secretAccessKey: 'SK',
			setId: 'set1'
		});
		expect(s3.previousRecoveryKey).toBe(KEY);
		expect('path' in s3).toBe(false);
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
		expect(locationState({ ...loc, key: 'previous' }).label).toMatch(/previous key/);
		expect(locationState({ ...loc, reachable: false }).label).toBe('Not reachable from here');
		expect(locationState({ ...loc, found: false }).label).toBe('Not found');
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
