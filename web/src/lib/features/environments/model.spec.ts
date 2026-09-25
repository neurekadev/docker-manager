import { describe, expect, it } from 'vitest';
import type { EnvironmentMetrics, Schema } from '$lib/api/client';
import { enrollmentIntent, enrollmentStateStatus } from './enrollment';
import {
	agentLabel,
	dependentNoun,
	diskMounts,
	environmentStatus,
	mountLabel,
	rangeSeconds,
	removalConsequences,
	seriesValues
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
		expect(mountLabel('docker')).toBe('Docker data');
		expect(mountLabel('bind-2')).toBe('Bind mount 2');
		expect(rangeSeconds('24h')).toBe(86400);
		expect(rangeSeconds('bogus')).toBe(3600);
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
				{ kind: 'update_policy', onArchive: 'paused', count: 1, items: [] },
				{ kind: 'permission_rule', onArchive: 'removed', count: 3, items: [] },
				{ kind: 'job', onArchive: 'interrupted', count: 0, items: [] }
			]
		} as unknown as Schema<'EnvironmentRemovalPreview'>;
		expect(removalConsequences(preview)).toEqual([
			'Hides homelab from every operation. Its history and backups are kept.',
			'2 stacks: kept, and back after a re-attach.',
			'1 update policy: kept; scheduled runs pause until a re-attach.',
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
