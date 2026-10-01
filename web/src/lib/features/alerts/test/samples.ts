// Sample alerts for the alerts unit tests (#159).
import type { Alert } from '../model';

export function sampleAlert(o: Partial<Alert> & Pick<Alert, 'id'>): Alert {
	return {
		kind: 'disk_health',
		severity: 'warning',
		state: 'firing',
		environmentId: 'e1',
		resourceType: 'disk',
		resourceId: '/dev/sda',
		title: `Alert ${o.id}`,
		facts: {},
		fields: [],
		link: '/environments/e1?tab=system',
		startedAt: '2026-09-25T12:00:00Z',
		updatedAt: '2026-09-25T12:00:00Z',
		dismissed: false,
		escalation: 0,
		revision: 1,
		actions: [],
		...o
	};
}

/** A failing disk on homelab. */
export const failingDisk = sampleAlert({
	id: 'a1',
	severity: 'critical',
	title: 'Disk /dev/sda on homelab is failing',
	detail: 'SMART self-assessment failed, 8 reallocated sectors.',
	facts: { device: '/dev/sda', deviceType: 'sat', state: 'failing' },
	actions: ['alert.dismiss']
});

/** A degraded md array on homelab. */
export const degradedArray = sampleAlert({
	id: 'a2',
	kind: 'raid',
	resourceType: 'raid_array',
	resourceId: 'md0',
	title: 'RAID md0 on homelab is degraded',
	facts: { array: 'md0', arrayKind: 'md', level: 'raid1', state: 'degraded' },
	startedAt: '2026-09-26T08:00:00Z'
});

/** edge is offline (another environment). */
export const offlineEdge = sampleAlert({
	id: 'a3',
	kind: 'environment_offline',
	environmentId: 'e2',
	resourceType: 'environment',
	resourceId: 'e2',
	title: 'edge is offline',
	link: '/environments/e2',
	facts: { since: '2026-09-27T10:00:00Z' },
	startedAt: '2026-09-27T10:00:00Z',
	actions: ['alert.dismiss']
});
