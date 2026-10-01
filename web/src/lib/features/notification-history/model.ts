// Notifications in words: finished backups and restores, prunes and update
// runs as the Notifications page lists them (newest first, grouped by
// day in the viewer's time zone), how each went (Done, Warning, Failed),
// the tile of its kind, its labelled values and where it leads (the run's
// job). The manager records them and filters them by the caller's
// permissions; this module only reads them. Pure (model.spec.ts).
import type { Schema } from '$lib/api/client';
import { resourceIcon, type ResourceIcon } from '$lib/features/common/resourceIcons';
import { eventKind } from '$lib/features/notifications/model';
import { routes } from '$lib/routes';

export type Notification = Schema<'Notification'>;
export type NotificationKind = Notification['kind'];
export type NotificationOutcome = Notification['outcome'];
export type NotificationField = Schema<'NotificationField'>;

/** The kinds of notifications in display order, named as "What to send" names them. */
export const NOTIFICATION_KINDS: { kind: NotificationKind; label: string }[] = (
	['backup', 'prune', 'updates'] as const
).map((kind) => ({ kind, label: eventKind(kind)?.label ?? kind }));

/** "Backups and restores", "Prune", "Image updates". */
export function notificationKindLabel(kind: string): string {
	return NOTIFICATION_KINDS.find((k) => k.kind === kind)?.label ?? kind;
}

const OUTCOMES: Record<NotificationOutcome, { label: string; tone: 'ok' | 'warn' | 'danger' }> = {
	success: { label: 'Done', tone: 'ok' },
	warning: { label: 'Warning', tone: 'warn' },
	failure: { label: 'Failed', tone: 'danger' }
};

/** The outcomes in the filter's order, with their badge labels. */
export const NOTIFICATION_OUTCOMES: { outcome: NotificationOutcome; label: string }[] = (
	['success', 'warning', 'failure'] as const
).map((outcome) => ({ outcome, label: OUTCOMES[outcome].label }));

/** "Done", "Warning", "Failed". */
export function outcomeLabel(o: string): string {
	return OUTCOMES[o as NotificationOutcome]?.label ?? 'Done';
}

/** The badge tone of an outcome: ok, warn or danger. */
export function outcomeTone(o: string): 'ok' | 'warn' | 'danger' {
	return OUTCOMES[o as NotificationOutcome]?.tone ?? 'ok';
}

/** The tile of a kind: the icon of the policy that runs it (backups, maintenance, updates). */
export function notificationTile(kind: string): ResourceIcon {
	switch (kind) {
		case 'backup':
			return resourceIcon('backup');
		case 'prune':
			return resourceIcon('maintenancePolicy');
		case 'updates':
			return resourceIcon('updatePolicy');
		default:
			return resourceIcon('job');
	}
}

/** The page of the run (an app path; anything else opens its job). */
export function notificationHref(n: Pick<Notification, 'link' | 'jobId'>): string {
	const l = n.link;
	return l && l.startsWith('/') && !l.startsWith('//') && !l.startsWith('/\\')
		? l
		: routes.job(n.jobId);
}

/** The row shows the environment itself. */
const SHOWN_ELSEWHERE = new Set(['Environment']);

/** The short values shown side by side ("Reclaimed 4.2 GiB", "Containers 3 containers · 12 MB"). */
export function inlineFields(n: Pick<Notification, 'fields'>): NotificationField[] {
	return (n.fields ?? []).filter((f) => f.inline && !SHOWN_ELSEWHERE.has(f.name));
}

/** The longer values on lines of their own (the services an update run updated, what to do). */
export function blockFields(n: Pick<Notification, 'fields'>): NotificationField[] {
	return (n.fields ?? []).filter((f) => !f.inline && !SHOWN_ELSEWHERE.has(f.name));
}

/** The local calendar day of a time ("2026-09-29"). */
export function dayKey(d: Date): string {
	const pad = (n: number) => String(n).padStart(2, '0');
	return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

const dayFormat = new Intl.DateTimeFormat('en', {
	weekday: 'short',
	month: 'short',
	day: 'numeric'
});
const dayYearFormat = new Intl.DateTimeFormat('en', {
	weekday: 'short',
	month: 'short',
	day: 'numeric',
	year: 'numeric'
});

/**
 * The heading of a day in the viewer's time zone: "Today", "Yesterday",
 * else "Mon, Sep 28" (with the year when it is not this year's).
 */
export function dayLabel(d: Date, now: Date = new Date()): string {
	const key = dayKey(d);
	if (key === dayKey(now)) return 'Today';
	const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
	if (key === dayKey(yesterday)) return 'Yesterday';
	return (d.getFullYear() === now.getFullYear() ? dayFormat : dayYearFormat).format(d);
}

export interface DayGroup<T> {
	/** The day ("2026-09-29"), unique per group. */
	key: string;
	/** "Today", "Yesterday", "Mon, Sep 28". */
	label: string;
	items: T[];
}

/**
 * The items by local day, in the order they come (newest first from the
 * server): one group per day, its items in their order. Items without a
 * valid time are left out.
 */
export function groupByDay<T extends { createdAt: string }>(
	items: readonly T[],
	now: Date = new Date()
): DayGroup<T>[] {
	const groups = new Map<string, DayGroup<T>>();
	for (const item of items) {
		const d = new Date(item.createdAt);
		if (!Number.isFinite(d.getTime())) continue;
		const key = dayKey(d);
		let g = groups.get(key);
		if (!g) {
			g = { key, label: dayLabel(d, now), items: [] };
			groups.set(key, g);
		}
		g.items.push(item);
	}
	return [...groups.values()];
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** "1 notification", "3 notifications". */
export function notificationCount(n: number): string {
	return plural(n, 'notification', 'notifications');
}

/**
 * The count next to the title: every match when the server counts them
 * ("120 notifications"), else the loaded ones ("50 notifications
 * loaded" while more pages wait); "2 of 50 notifications" while the
 * search narrows the loaded ones.
 */
export function notificationsSummary(o: {
	shown: number;
	loaded: number;
	total?: number;
	more: boolean;
}): string {
	if (o.shown !== o.loaded)
		return `${o.shown} of ${notificationCount(o.loaded)}${o.more ? ' loaded' : ''}`;
	if (o.total !== undefined && o.total >= o.loaded) return notificationCount(o.total);
	return o.more ? `${notificationCount(o.loaded)} loaded` : notificationCount(o.loaded);
}
