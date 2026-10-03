import { describe, expect, it } from 'vitest';
import type { EnvironmentMetrics, Schema } from '$lib/api/client';
import { METRIC_COLORS } from '$lib/design/hue';
import { enrollmentIntent, enrollmentStateStatus } from './enrollment';
import {
	agentContact,
	agentLabel,
	connectionSummary,
	dependentNoun,
	diskMounts,
	environmentStatus,
	hasValues,
	memoryLines,
	mountLabel,
	rangeSeconds,
	removalConsequences,
	seriesValues,
	swapTotal
} from './model';

describe('environment model', () => {
	it('words status, archived first', () => {
		expect(environmentStatus({ online: true, status: 'active' })).toBe('online');
		expect(environmentStatus({ online: false, status: 'active' })).toBe('offline');
		expect(environmentStatus({ online: false, status: 'archived' })).toBe('archived');
	});

	it('reads series by key and mount and keeps gaps as null', () => {
		const m = {
			timestamps: ['a', 'b'],
			series: [
				{ key: 'cpu.percent', unit: 'percent', values: [1, null] },
				{ key: 'disk.used_bytes', unit: 'bytes', mount: 'docker', values: [5, 6] },
				{ key: 'disk.used_bytes', unit: 'bytes', mount: 'stacks', values: [1, 1] },
				{ key: 'disk.total_bytes', unit: 'bytes', mount: 'docker', values: [9, 9] }
			]
		} as unknown as EnvironmentMetrics;
		expect(seriesValues(m, 'cpu.percent')).toEqual([1, null]);
		expect(seriesValues(m, 'disk.used_bytes', 'stacks')).toEqual([1, 1]);
		expect(seriesValues(m, 'memory.used_bytes')).toEqual([]);
		expect(seriesValues(undefined, 'cpu.percent')).toEqual([]);
		expect(diskMounts(m)).toEqual(['docker', 'stacks']);
		expect(mountLabel('docker')).toBe('Docker Data');
		expect(mountLabel('bind-2')).toBe('Bind Mount 2');
		expect(rangeSeconds('24h')).toBe(86400);
		expect(rangeSeconds('bogus')).toBe(3600);
	});

	it('stacks memory like Beszel: used, then the ZFS ARC and the cache when the range has them', () => {
		const metrics = (series: { key: string; values: (number | null)[] }[]) =>
			({ timestamps: ['a', 'b'], series }) as unknown as EnvironmentMetrics;
		const used = { key: 'memory.used_bytes', values: [1, 2] };
		// An older agent: no ARC, no cache, no swap.
		const old = metrics([used, { key: 'memory.cache_bytes', values: [null, null] }]);
		expect(memoryLines(old).map((l) => l.name)).toEqual(['Used']);
		expect(hasValues(old, 'memory.cache_bytes')).toBe(false);
		expect(swapTotal(old)).toBeNull();
		const zfs = metrics([
			used,
			{ key: 'memory.zfs_arc_bytes', values: [null, 5] },
			{ key: 'memory.cache_bytes', values: [3, 4] },
			{ key: 'swap.total_bytes', values: [8, 0] }
		]);
		expect(memoryLines(zfs)).toEqual([
			{ name: 'Used', values: [1, 2], color: METRIC_COLORS.memoryUsed },
			{ name: 'ZFS ARC', values: [null, 5], color: METRIC_COLORS.memoryZfsArc, fill: 0.5 },
			{ name: 'Cache / Buffers', values: [3, 4], color: METRIC_COLORS.memoryCache }
		]);
		// Swap that was turned off: no Swap chart.
		expect(swapTotal(zfs)).toBe(0);
	});

	it('lists what archiving does to every dependent kind with records (#34)', () => {
		const preview = {
			environmentId: 'e1',
			environmentName: 'homelab',
			status: 'active',
			revision: 3,
			action: 'archive',
			description: '',
			hostUntouched: true,
			backupSnapshots: 0,
			reattach: '',
			migration: { stacks: 2, description: '' },
			dependents: [
				{ kind: 'stack', onArchive: 'kept', count: 2, items: [] },
				{ kind: 'backup_policy', onArchive: 'paused', count: 1, items: [] },
				{ kind: 'permission_rule', onArchive: 'removed', count: 3, items: [] },
				{ kind: 'job', onArchive: 'interrupted', count: 0, items: [] }
			]
		} as unknown as Schema<'EnvironmentRemovalPreview'>;
		expect(removalConsequences(preview)).toEqual([
			'Hides homelab from every operation. Its history and backups are kept.',
			'2 stacks: kept, and back after a re-attach.',
			'1 backup policy: kept; scheduled runs pause until a re-attach.',
			'3 permission rules: removed (audited).',
			'Nothing on the host changes: containers, volumes and files keep running as they are.'
		]);
		expect(dependentNoun('backup_repository', 2)).toBe('backup repositories');
	});

	it('words enrollments and agents', () => {
		expect(enrollmentIntent('new')).toBe('New environment');
		expect(enrollmentIntent('reattach:e1')).toBe('Re-attaches an archived environment');
		expect(enrollmentIntent('replace:a1')).toMatch(/Replaces/);
		expect(enrollmentStateStatus('pending')).toBe('queued');
		expect(enrollmentStateStatus('used')).toBe('succeeded');
		expect(enrollmentStateStatus('expired')).toBe('expired');
		expect(agentLabel({ id: '0190a6e0-aaaa', hostname: 'nas' })).toBe('nas');
		expect(agentLabel({ id: '0190a6e0-aaaa', label: 'rack 2', hostname: 'nas' })).toBe(
			'rack 2'
		);
		expect(agentLabel({ id: '0190a6e0-aaaa' })).toBe('0190a6e0');
	});
});

describe('connection wording (#22 polish)', () => {
	const NOW = Date.parse('2026-09-25T12:00:00Z');

	it('says how long an online environment has been connected', () => {
		expect(
			connectionSummary(
				{ online: true, status: 'active', connectionChangedAt: '2026-09-25T08:48:00Z' },
				NOW
			)
		).toBe('Online for 3 h 12 min.');
		expect(connectionSummary({ online: true, status: 'active' }, NOW)).toBe('Online.');
		// Offline and archived environments show a notice instead.
		expect(connectionSummary({ online: false, status: 'active' }, NOW)).toBeUndefined();
		expect(connectionSummary({ online: true, status: 'archived' }, NOW)).toBeUndefined();
	});

	it('shows a connected agent as connected now and a disconnected one when last seen', () => {
		const now = new Date(NOW);
		const connected = agentContact(
			{
				status: 'active',
				connected: true,
				lastConnectedAt: '2026-09-25T11:10:00Z',
				lastSeenAt: '2026-09-25T11:10:00Z'
			},
			now
		);
		expect(connected.text).toBe('Connected now');
		expect(connected.title).toMatch(/^Connected since /);
		// The last-seen time lags a live connection by up to a minute.
		expect(
			agentContact(
				{
					status: 'active',
					connected: true,
					lastConnectedAt: '2026-09-25T11:10:00Z',
					lastSeenAt: '2026-09-25T11:59:20Z'
				},
				now
			).text
		).toBe('Connected now');
		const gone = agentContact(
			{ status: 'active', connected: false, lastSeenAt: '2026-09-25T10:00:00Z' },
			now
		);
		expect(gone.text).toBe('2 hours ago');
		expect(gone.at).toBe('2026-09-25T10:00:00Z');
		expect(
			agentContact(
				{ status: 'revoked', connected: false, revokedAt: '2026-09-23T12:00:00Z' },
				now
			).text
		).toBe('Removed 2 days ago');
		expect(agentContact({ status: 'active', connected: false }, now).text).toBe('—');
	});
});
