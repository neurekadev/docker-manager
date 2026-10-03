// The backup settings, retention presets, coverage and repository wording
// (#10, #246): pure helpers of model.ts.
import { describe, expect, it } from 'vitest';
import {
	DEFAULT_RETENTION,
	RETENTION_PRESETS,
	applyRetentionPreset,
	connectionTestText,
	coverageSummary,
	groupBackupsByRun,
	memberRuns,
	repositoryStatusLine,
	restoreTargetName,
	retentionActive,
	retentionPreset,
	retentionShort,
	retentionText,
	scopeName,
	settingsCover,
	settingsSentence,
	verificationText,
	verifyReadOptions,
	verifyReadText,
	type Backup,
	type BackupSet,
	type BackupSettings
} from './model';

const viewerZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
const now = new Date('2026-09-27T03:00:00Z');
const envName = (id: string) => ({ e1: 'prod', e2: 'edge' })[id] ?? id;

function settings(over: Partial<BackupSettings> = {}): BackupSettings {
	return {
		id: 'bs1',
		enabled: true,
		primaryRepositoryId: 'r1',
		secondaryRepositoryId: '',
		schedule: { cron: '0 3 * * *', timeZone: viewerZone },
		excludeEnvironments: [],
		excludeStacks: [],
		excludeVolumes: [],
		anonymousVolumes: false,
		buildxVolumes: false,
		externalBinds: false,
		includeMetrics: false,
		shutdown: false,
		retention: {},
		recentSets: [],
		actions: [],
		revision: 1,
		updatedAt: '2026-09-26T00:00:00Z',
		...over
	};
}

function set(over: Partial<BackupSet> = {}): BackupSet {
	return {
		id: 's1',
		state: 'complete',
		origin: 'scheduled',
		startedAt: '2026-09-26T09:50:00Z',
		finishedAt: '2026-09-26T10:00:00Z',
		members: [],
		...over
	};
}

describe('retention presets', () => {
	it('offers the recommended rules first and Custom last', () => {
		expect(RETENTION_PRESETS.map((p) => p.value)).toEqual([
			'recommended',
			'last30',
			'everything',
			'custom'
		]);
		expect(RETENTION_PRESETS[0].label).toBe('7 Daily, 4 Weekly, 12 Monthly (Recommended)');
		expect(retentionPreset(DEFAULT_RETENTION)).toBe('recommended');
	});

	it('maps a saved retention to the preset it matches', () => {
		expect(retentionPreset(undefined)).toBe('everything');
		expect(retentionPreset({ afterBackup: true })).toBe('everything');
		expect(retentionPreset({ daily: 7, weekly: 4, monthly: 12 })).toBe('recommended');
		expect(retentionPreset({ last: 30, hourly: 0, afterBackup: true })).toBe('last30');
		// Another rule is Custom.
		expect(retentionPreset({ last: 30, daily: 1 })).toBe('custom');
		expect(retentionPreset({ daily: 7, weekly: 4, monthly: 12, last: 3 })).toBe('custom');
		expect(retentionPreset({ last: 168 })).toBe('custom');
	});

	it('sets a preset’s rules, turns the others off and keeps "after every backup" and the expiry', () => {
		const custom = {
			last: 5,
			hourly: 24,
			afterBackup: true,
			expireDeletedDays: 30
		};
		expect(applyRetentionPreset('recommended', custom)).toEqual({
			last: 0,
			hourly: 0,
			daily: 7,
			weekly: 4,
			monthly: 12,
			yearly: 0,
			withinDays: 0,
			afterBackup: true,
			expireDeletedDays: 30
		});
		expect(applyRetentionPreset('last30', custom)).toMatchObject({ last: 30, hourly: 0 });
		expect(applyRetentionPreset('everything', custom)).toMatchObject({ last: 0, hourly: 0 });
		expect(applyRetentionPreset('custom', custom)).toBe(custom);
	});

	it('says retention in a few words', () => {
		expect(retentionShort(undefined)).toBe('Keep everything');
		expect(retentionShort({ afterBackup: true })).toBe('Keep everything');
		expect(retentionShort(DEFAULT_RETENTION)).toBe('7 daily, 4 weekly, 12 monthly');
		expect(retentionShort({ last: 30 })).toBe('Last 30');
	});

	it('counts the expiry of deleted items as retention', () => {
		expect(retentionActive({ expireDeletedDays: 30 })).toBe(true);
		expect(retentionActive({ afterBackup: true })).toBe(false);
		expect(retentionActive(DEFAULT_RETENTION)).toBe(true);
		expect(retentionText({ expireDeletedDays: 30 })).toBe(
			'Keep every backup; backups of deleted stacks and volumes go after 30 days'
		);
	});
});

describe('the backup settings', () => {
	it('says whether, where and when, and how the last run went', () => {
		const st = settings({ recentSets: [set()] });
		expect(settingsSentence(st, { primary: 'B2', now })).toBe(
			'Backs up every environment to B2 daily at 03:00. Last run completed 17 hours ago.'
		);
		expect(
			settingsSentence(
				{ ...st, excludeEnvironments: ['e2'], recentSets: [set({ state: 'partial' })] },
				{ primary: 'B2', secondary: 'NAS', now }
			)
		).toBe(
			'Backs up the covered environments to B2, then to NAS, daily at 03:00. Last run partly failed 17 hours ago.'
		);
		expect(
			settingsSentence(
				{ ...st, recentSets: [set({ state: 'skipped' })] },
				{ primary: 'B2', now }
			)
		).toBe(
			'Backs up every environment to B2 daily at 03:00. Last run 17 hours ago had nothing to back up: everything was removed before its turn.'
		);
		expect(settingsSentence({ ...st, enabled: false, recentSets: [] }, { primary: 'B2' })).toBe(
			'Backs up every environment to B2 when you start them. Nothing has been backed up yet.'
		);
		expect(settingsSentence(st, { primary: 'B2', running: true })).toBe(
			'Backs up every environment to B2 daily at 03:00. A backup is running now.'
		);
		expect(settingsSentence(st, {})).toBe(
			'Backups are paused: no repository is the Primary one.'
		);
		// Shapes without words never show the raw expression.
		expect(
			settingsSentence(
				{
					...st,
					schedule: { cron: '*/7 2-4 * 1 *', timeZone: viewerZone },
					recentSets: []
				},
				{ primary: 'B2' }
			)
		).toBe('Backs up every environment to B2 on its schedule. Nothing has been backed up yet.');
	});

	it('sums up what the backups cover', () => {
		const envs = [{ id: 'e1' }, { id: 'e2' }, { id: 'old', status: 'archived' }];
		expect(coverageSummary(settings(), envs)).toEqual({
			value: 'All Environments',
			secondary: 'Manager state too'
		});
		expect(
			coverageSummary(
				settings({
					excludeEnvironments: ['e1'],
					excludeStacks: ['a'],
					excludeVolumes: ['e2/v']
				}),
				envs
			)
		).toEqual({ value: '1 of 2 Environments', secondary: 'Manager state too; 2 left out' });
	});
});

describe('coverage of a stack or volume', () => {
	it('covers what is not left out, nor its environment', () => {
		const st = settings();
		expect(settingsCover(st, { environmentId: 'e1', stackId: 'st1' })).toBe(true);
		expect(
			settingsCover(
				{ ...st, excludeStacks: ['st1'] },
				{ environmentId: 'e1', stackId: 'st1' }
			)
		).toBe(false);
		expect(
			settingsCover(
				{ ...st, excludeEnvironments: ['e1'] },
				{ environmentId: 'e1', stackId: 'st1' }
			)
		).toBe(false);
		// Volumes are left out by environment/name.
		expect(
			settingsCover(
				{ ...st, excludeVolumes: ['e1/data'] },
				{ environmentId: 'e1', volume: 'data' }
			)
		).toBe(false);
		expect(
			settingsCover(
				{ ...st, excludeVolumes: ['e2/data'] },
				{ environmentId: 'e1', volume: 'data' }
			)
		).toBe(true);
	});

	it('lists the runs that included the stack, newest first, a copy per repository', () => {
		const member = (repositoryId: string, state: 'complete' | 'failed') => ({
			item: 'stack/st1',
			kind: 'stack' as const,
			scope: 'env:e1',
			repositoryId,
			stackId: 'st1',
			state
		});
		const runs = memberRuns(
			settings({
				recentSets: [
					set({
						id: 'old',
						startedAt: '2026-09-25T03:00:00Z',
						members: [member('r1', 'failed')]
					}),
					set({
						id: 'new',
						startedAt: '2026-09-26T03:00:00Z',
						members: [
							member('r1', 'complete'),
							member('r2', 'complete'),
							{ ...member('r1', 'complete'), item: 'stack/st2', stackId: 'st2' }
						]
					})
				]
			}),
			{ environmentId: 'e1', stackId: 'st1' }
		);
		expect(runs.map((r) => [r.setId, r.member.repositoryId, r.member.state])).toEqual([
			['new', 'r1', 'complete'],
			['new', 'r2', 'complete'],
			['old', 'r1', 'failed']
		]);
	});
});

describe('backups grouped by run', () => {
	const b = (over: Partial<Backup>): Backup => ({
		id: 'b',
		repositoryId: 'r1',
		snapshotTime: '2026-09-26T03:00:00Z',
		state: 'complete',
		view: 'full',
		actions: [],
		...over
	});

	it('puts the backups of one set together, newest run first', () => {
		const runs = groupBackupsByRun([
			b({
				id: '1',
				setId: 's1',
				kind: 'volume',
				volume: 'media',
				bytes: 100,
				snapshotTime: '2026-09-26T03:01:00Z'
			}),
			b({
				id: '2',
				setId: 's1',
				kind: 'stack',
				stackName: 'forgejo',
				bytes: 50,
				state: 'partial'
			}),
			b({
				id: '3',
				setId: 's2',
				kind: 'stack',
				stackName: 'shop',
				snapshotTime: '2026-09-27T03:00:00Z'
			}),
			b({ id: '4', kind: 'manager_state', snapshotTime: '2026-09-20T03:00:00Z' })
		]);
		expect(runs.map((r) => r.key)).toEqual(['set:s2', 'set:s1', 'backup:4']);
		const s1 = runs[1];
		expect(s1.backups.map((x) => x.id)).toEqual(['2', '1']);
		expect(s1.time).toBe('2026-09-26T03:00:00Z');
		expect(s1.state).toBe('partial');
		expect(s1.bytes).toBe(150);
		expect(runs[0].bytes).toBeUndefined();
	});
});

describe('repositories and verification', () => {
	it('offers plain choices of how much a verification reads', () => {
		expect(verifyReadOptions('').map((o) => o.label)).toEqual([
			'Check Structure Only',
			'Also Read 5% of the Data',
			'Read All Data'
		]);
		// A saved amount that is none of them stays choosable.
		expect(verifyReadOptions('1/10').at(-1)).toMatchObject({
			value: '1/10',
			label: 'Also Read 1/10 of the Data'
		});
		expect(verifyReadText('')).toBe('checks the structure only');
		expect(verifyReadText('5%')).toBe('also reads 5% of the data');
		expect(verifyReadText('100%')).toBe('reads all data');
	});

	it('says the verification schedule in one sentence', () => {
		expect(verificationText({ cron: '0 5 * * 0', timeZone: 'UTC', enabled: false })).toBe(
			'Verification is off. Run Verify on one of its backups.'
		);
		expect(verificationText(undefined)).toMatch(/^Verification is off/);
		expect(
			verificationText({
				cron: '0 5 * * 0',
				timeZone: viewerZone,
				enabled: true,
				readDataSubset: '5%'
			})
		).toBe('Verifies weekly on Sunday at 05:00 and also reads 5% of the data.');
	});

	it('leads the repository page with one status line', () => {
		expect(
			repositoryStatusLine(
				{
					healthy: true,
					lastBackupAt: '2026-09-27T00:00:00Z',
					lastVerifiedAt: '2026-09-26T03:00:00Z'
				},
				42 * 1024 ** 3,
				now
			)
		).toBe('Healthy · 42 GB · last backup 3 hours ago · verified yesterday');
		expect(repositoryStatusLine({ healthy: false }, undefined, now)).toBe(
			'Needs attention · no backup yet · never verified'
		);
		expect(connectionTestText({ at: '2026-09-26T03:00:00Z', ok: true }, now)).toBe(
			'Last checked yesterday: working'
		);
	});

	it('names locations and restore targets without internal paths', () => {
		expect(scopeName('docker-manager', envName)).toBe('Manager State');
		expect(scopeName('env:e1', envName)).toBe('Environment prod');
		expect(scopeName('docker-manager-env-e2', envName)).toBe('Environment edge');
		expect(
			restoreTargetName({
				kind: 'volume',
				name: 'shop_db',
				path: '/var/lib/docker/volumes/shop_db/_data'
			})
		).toBe('Volume shop_db');
		expect(restoreTargetName({ kind: 'project', name: 'shop', path: '/srv/stacks/shop' })).toBe(
			'Files of Stack shop'
		);
		expect(restoreTargetName({ kind: 'file', path: '/srv/stacks/shop/compose.yaml' })).toBe(
			'File compose.yaml'
		);
	});
});
