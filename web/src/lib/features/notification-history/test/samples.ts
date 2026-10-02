// Sample notifications for the notification history unit tests.
import type { Notification } from '../model';

/** A local time of a day of September 2026 (time-zone independent days). */
export const sep = (day: number, hour = 12, minute = 0) =>
	new Date(2026, 8, day, hour, minute).toISOString();

export function sampleNotification(
	o: Partial<Notification> & Pick<Notification, 'id'>
): Notification {
	return {
		kind: 'backup',
		outcome: 'success',
		environmentId: 'e1',
		jobId: `job-${o.id}`,
		jobKind: 'backup.run',
		title: `Notification ${o.id}`,
		fields: [],
		facts: {},
		link: `/jobs/job-${o.id}`,
		createdAt: sep(30, 9),
		...o
	};
}

/** Nightly backed up homelab this morning. */
export const nightlyBackup = sampleNotification({
	id: 'n1',
	title: 'Backup Nightly on homelab succeeded',
	detail: '5 of 5 items backed up (12.4 GiB read) in 3 min 12 s.',
	fields: [
		{ name: 'Environment', value: 'homelab', inline: true },
		{ name: 'Policy', value: 'Nightly', inline: true },
		{ name: 'Items', value: '5 of 5', inline: true },
		{ name: 'Duration', value: '3 min 12 s', inline: true }
	],
	createdAt: sep(30, 9)
});

/** A prune on edge yesterday, with what it removed per kind of object. */
export const weeklyPrune = sampleNotification({
	id: 'n2',
	kind: 'prune',
	jobKind: 'prune.run',
	environmentId: 'e2',
	title: 'Prune Weekly on edge reclaimed 4.2 GiB',
	fields: [
		{ name: 'Environment', value: 'edge', inline: true },
		{ name: 'Reclaimed', value: '4.2 GiB', inline: true },
		{ name: 'Containers', value: '3 containers · 12 MB', inline: true },
		{ name: 'Images', value: '7 images · 4.1 GiB', inline: true },
		{ name: 'Volumes', value: '1 volume', inline: true },
		{ name: 'Networks', value: '2 networks', inline: true },
		{ name: 'Build Cache', value: '14 entries · 96 MB', inline: true }
	],
	createdAt: sep(29, 23, 30)
});

/** An update run on homelab that failed for one service, three days ago. */
export const failedUpdate = sampleNotification({
	id: 'n3',
	kind: 'updates',
	outcome: 'failure',
	jobKind: 'update.run',
	title: 'Update of Silo on homelab failed',
	detail: '1 of 2 services could not be updated.',
	fields: [
		{ name: 'Target', value: 'Silo', inline: true },
		{ name: 'Updated', value: 'web' },
		{ name: 'Failed', value: 'worker' },
		{ name: 'What to Do', value: 'Open the job to see what went wrong, then try again.' }
	],
	createdAt: sep(27, 4)
});
