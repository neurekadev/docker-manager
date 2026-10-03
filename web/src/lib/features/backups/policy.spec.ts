// Backup policies, retention presets, coverage and repository wording
// (#10): pure helpers of model.ts.
import { describe, expect, it } from 'vitest';
import {
	DEFAULT_RETENTION,
	RETENTION_PRESETS,
	applyRetentionPreset,
	connectionTestText,
	coverageSummary,
	groupBackupsByRun,
	memberRuns,
	nextPolicyRun,
	policyCovers,
	policySentence,
	repositoryStatusLine,
	restoreTargetName,
	retentionActive,
	retentionPreset,
	retentionShort,
	retentionText,
	scopeName,
	verificationText,
	verifyReadOptions,
	verifyReadText,
	type Backup,
	type BackupPolicy,
	type BackupSet
} from './model';

const viewerZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
const now = new Date('2026-09-27T03:00:00Z');
const envName = (id: string) => ({ e1: 'prod', e2: 'edge' })[id] ?? id;

function policy(over: Partial<BackupPolicy> = {}): BackupPolicy {
	return {
		id: 'p1',
		name: 'Daily Backups',
		scope: 'all',
		excludeStacks: [],
		excludeVolumes: [],
		anonymousVolumes: false,
		buildxVolumes: false,
		externalBinds: false,
		enabled: true,
		view: 'full',
		actions: [],
		includeManagerState: false,
		includeMetrics: false,
		stacks: [],
		volumes: [],
		shutdown: false,
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

describe('policy pages', () => {
	it('says what, where, when and how the last run went', () => {
		const p = policy({
			schedule: { cron: '0 3 * * *', timeZone: viewerZone, enabled: true },
			recentSets: [set()]
		});
		expect(policySentence(p, { repository: 'B2', environmentName: envName, now })).toBe(
			'Backs up all environments to B2 daily at 03:00. Last run completed 17 hours ago.'
		);
		expect(
			policySentence(
				{
					...p,
					scope: 'environment',
					environmentId: 'e1',
					includeManagerState: true,
					recentSets: [set({ state: 'partial' })]
				},
				{ environmentName: envName, now }
			)
		).toBe(
			'Backs up prod and the manager state daily at 03:00. Last run partly failed 17 hours ago.'
		);
		expect(
			policySentence(
				{ ...p, recentSets: [set({ state: 'skipped' })] },
				{ repository: 'B2', environmentName: envName, now }
			)
		).toBe(
			'Backs up all environments to B2 daily at 03:00. Last run 17 hours ago had nothing to back up: everything was removed before its turn.'
		);
		expect(
			policySentence({ ...p, schedule: { ...p.schedule!, enabled: false }, recentSets: [] })
		).toBe('Backs up all environments when you start it. It has not run yet.');
		expect(policySentence(p, { running: true })).toBe(
			'Backs up all environments daily at 03:00. A backup is running now.'
		);
		// Shapes without words never show the raw expression.
		expect(
			policySentence({
				...p,
				schedule: { cron: '*/7 2-4 * 1 *', timeZone: viewerZone, enabled: true },
				recentSets: []
			})
		).toBe('Backs up all environments on its schedule. It has not run yet.');
	});

	it('finds the next scheduled run of several policies', () => {
		expect(
			nextPolicyRun([
				{
					schedule: {
						cron: '',
						timeZone: 'UTC',
						enabled: true,
						nextRun: '2026-09-28T03:00:00Z'
					}
				},
				{
					schedule: {
						cron: '',
						timeZone: 'UTC',
						enabled: false,
						nextRun: '2026-09-27T04:00:00Z'
					}
				},
				{
					schedule: {
						cron: '',
						timeZone: 'UTC',
						enabled: true,
						nextRun: '2026-09-27T05:00:00Z'
					}
				},
				{}
			])
		).toBe('2026-09-27T05:00:00Z');
		expect(nextPolicyRun([])).toBeUndefined();
	});

	it('sums up what a policy covers', () => {
		expect(coverageSummary(policy(), envName)).toEqual({
			value: 'All Environments',
			secondary: 'Every stack and volume'
		});
		expect(
			coverageSummary(
				policy({
					scope: 'environment',
					environmentId: 'e2',
					excludeStacks: ['a'],
					excludeVolumes: ['v'],
					includeManagerState: true
				}),
				envName
			)
		).toEqual({ value: 'edge', secondary: 'Manager state too; 2 left out' });
		expect(
			coverageSummary(policy({ stacks: [{ stackId: 'a' }, { stackId: 'b' }] }), envName)
		).toEqual({ value: '2 stacks', secondary: 'Chosen one by one' });
	});
});

describe('coverage of a stack or volume', () => {
	it('covers what is in scope and not left out', () => {
		const all = policy();
		expect(policyCovers(all, { environmentId: 'e1', stackId: 'st1' })).toBe(true);
		expect(
			policyCovers(
				{ ...all, excludeStacks: ['st1'] },
				{ environmentId: 'e1', stackId: 'st1' }
			)
		).toBe(false);
		expect(
			policyCovers(
				{ ...all, scope: 'environment', environmentId: 'e2' },
				{ environmentId: 'e1', stackId: 'st1' }
			)
		).toBe(false);
		// Volumes are left out by environment/name when a policy covers all environments.
		expect(
			policyCovers(
				{ ...all, excludeVolumes: ['e1/data'] },
				{ environmentId: 'e1', volume: 'data' }
			)
		).toBe(false);
		expect(
			policyCovers(
				{ ...all, scope: 'environment', environmentId: 'e1', excludeVolumes: ['data'] },
				{ environmentId: 'e1', volume: 'data' }
			)
		).toBe(false);
		expect(policyCovers(all, { environmentId: 'e1', volume: 'data' })).toBe(true);
	});

	it('covers only what an explicit selection lists', () => {
		const p = policy({
			stacks: [{ stackId: 'st1' }],
			volumes: [{ environmentId: 'e1', volume: 'up' }]
		});
		expect(policyCovers(p, { environmentId: 'e1', stackId: 'st1' })).toBe(true);
		expect(policyCovers(p, { environmentId: 'e1', stackId: 'st2' })).toBe(false);
		expect(policyCovers(p, { environmentId: 'e1', volume: 'up' })).toBe(true);
		expect(policyCovers(p, { environmentId: 'e2', volume: 'up' })).toBe(false);
	});

	it('lists the runs that included the stack, newest first', () => {
		const runs = memberRuns(
			[
				policy({
					recentSets: [
						set({
							id: 'old',
							startedAt: '2026-09-25T03:00:00Z',
							members: [
								{
									item: 'stack/st1',
									kind: 'stack',
									scope: 'env:e1',
									stackId: 'st1',
									state: 'failed'
								}
							]
						}),
						set({
							id: 'new',
							startedAt: '2026-09-26T03:00:00Z',
							members: [
								{
									item: 'stack/st1',
									kind: 'stack',
									scope: 'env:e1',
									stackId: 'st1',
									state: 'complete'
								},
								{
									item: 'stack/st2',
									kind: 'stack',
									scope: 'env:e1',
									stackId: 'st2',
									state: 'complete'
								}
							]
						})
					]
				})
			],
			{ environmentId: 'e1', stackId: 'st1' }
		);
		expect(runs.map((r) => [r.setId, r.member.state, r.policyName])).toEqual([
			['new', 'complete', 'Daily Backups'],
			['old', 'failed', 'Daily Backups']
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
