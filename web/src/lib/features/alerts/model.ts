// Alerts (#159) in words: what an alert is about, how bad it is, its state
// in the list (active, dismissed or resolved), where it leads, and the
// helpers that find the alert of a disk or RAID array on the System tab and
// count them for the dashboard and the environment page. The manager
// raises, dismisses and resolves alerts and filters them by the caller's
// permissions; this module only reads them. Pure (model.spec.ts).
import type { Schema } from '$lib/api/client';
import type { AlertTally } from '$lib/features/dashboard/totals';
import type { IconComponent } from '$lib/design/icons';
import { EVENT_KINDS, eventKind } from '$lib/features/notifications/model';
import { routes } from '$lib/routes';
import { formatRelative } from '$lib/ui/format';

export type Alert = Schema<'Alert'>;
export type AlertKind = Alert['kind'];
export type AlertSeverity = Alert['severity'];
/** An alert as the list shows it: firing and not dismissed, dismissed, or resolved. */
export type AlertView = 'active' | 'dismissed' | 'resolved';

/** The action key of dismissing an alert (in `actions` while it fires and the caller may). */
export const DISMISS = 'alert.dismiss';

const KIND_LABELS: Record<AlertKind, string> = {
	disk_health: 'Disk health',
	raid: 'RAID',
	temperature: 'Temperature',
	disk_space: 'Disk space',
	memory: 'Memory',
	environment_offline: 'Environment offline',
	updates: 'Updates available',
	job_failed: 'Failed job'
};

/**
 * The kinds alerts are raised with, in the notification channels' order
 * (backups, restores and prunes are notifications, never alerts), with
 * their labels.
 */
export const ALERT_KINDS: { kind: AlertKind; label: string }[] = EVENT_KINDS.flatMap((k) =>
	k.kind in KIND_LABELS
		? [{ kind: k.kind as AlertKind, label: KIND_LABELS[k.kind as AlertKind] }]
		: []
);

export function kindLabel(kind: string): string {
	return KIND_LABELS[kind as AlertKind] ?? kind;
}

/** The icon of an alert's kind (a thermometer for temperatures; undefined for an unknown kind). */
export function kindIcon(kind: string): IconComponent | undefined {
	return eventKind(kind)?.icon;
}

/** Field names a row already shows elsewhere (its environment and severity columns). */
const SHOWN_ELSEWHERE = new Set(['Environment', 'Severity']);

/**
 * The short labelled values of an alert for its row ("Sensor coretemp",
 * "Highest 92 °C"): the inline fields, without the environment and the
 * severity the row shows itself.
 */
export function alertFacts(a: Pick<Alert, 'fields'>): Alert['fields'] {
	return (a.fields ?? []).filter((f) => f.inline && !SHOWN_ELSEWHERE.has(f.name));
}

const SEVERITY: Record<AlertSeverity, { label: string; tone: 'danger' | 'warn' | 'info' }> = {
	critical: { label: 'Critical', tone: 'danger' },
	warning: { label: 'Warning', tone: 'warn' },
	info: { label: 'Info', tone: 'info' }
};
const RANK: Record<string, number> = { critical: 0, warning: 1, info: 2 };

/** "Critical", "Warning", "Info" (the StatusBadge vocabulary's words). */
export function severityLabel(s: string): string {
	return SEVERITY[s as AlertSeverity]?.label ?? 'Info';
}

/** The tone of a severity: danger, warn or info. */
export function severityTone(s: string): 'danger' | 'warn' | 'info' {
	return SEVERITY[s as AlertSeverity]?.tone ?? 'info';
}

/** Sort order of severities: critical first. */
export function severityRank(s: string): number {
	return RANK[s] ?? 3;
}

/** The worst severity of some alerts (undefined for none). */
export function worstSeverity(
	alerts: readonly Pick<Alert, 'severity'>[]
): AlertSeverity | undefined {
	let worst: AlertSeverity | undefined;
	for (const a of alerts)
		if (!worst || severityRank(a.severity) < severityRank(worst)) worst = a.severity;
	return worst;
}

/** Critical first, then the newest. */
export function sortAlerts<T extends Pick<Alert, 'severity' | 'startedAt'>>(
	alerts: readonly T[]
): T[] {
	return [...alerts].sort(
		(a, b) =>
			severityRank(a.severity) - severityRank(b.severity) ||
			b.startedAt.localeCompare(a.startedAt)
	);
}

/** Where the alert stands in the list. */
export function alertView(a: Pick<Alert, 'state' | 'dismissed'>): AlertView {
	if (a.state === 'resolved') return 'resolved';
	return a.dismissed ? 'dismissed' : 'active';
}

/** Firing and not dismissed. */
export function isActive(a: Pick<Alert, 'state' | 'dismissed'>): boolean {
	return alertView(a) === 'active';
}

/** The caller may dismiss it (for everyone) now. */
export function canDismiss(a: Pick<Alert, 'state' | 'dismissed' | 'actions'>): boolean {
	return isActive(a) && a.actions.includes(DISMISS);
}

/** The page the alert is about (an app path; anything else opens Alerts). */
export function alertHref(a: Pick<Alert, 'link'>): string {
	const l = a.link;
	return l && l.startsWith('/') && !l.startsWith('//') && !l.startsWith('/\\')
		? l
		: routes.alerts();
}

const RESOLUTION_LABELS: Record<NonNullable<Alert['resolution']>, string> = {
	resolved: 'Resolved',
	removed: 'Removed',
	expired: 'Expired',
	archived: 'Environment archived'
};

/** Why a resolved alert stopped: "Resolved", "Removed", "Expired", "Environment archived". */
export function resolutionLabel(a: Pick<Alert, 'resolution'>): string {
	return RESOLUTION_LABELS[a.resolution ?? 'resolved'] ?? 'Resolved';
}

/** What the tooltip of a resolution says. */
export function resolutionHint(a: Pick<Alert, 'resolution'>): string {
	switch (a.resolution) {
		case 'removed':
			return 'The disk, array or policy it was about is gone.';
		case 'expired':
			return 'The job did not run again for 7 days.';
		case 'archived':
			return 'Its environment was archived.';
		default:
			return 'The problem is gone.';
	}
}

/** "Dismissed by Alex 3 hours ago" (without a name: "Dismissed 3 hours ago"). */
export function dismissedText(
	a: Pick<Alert, 'dismissedAt' | 'dismissedBy'>,
	now: Date = new Date()
): string {
	const by = a.dismissedBy?.name ? ` by ${a.dismissedBy.name}` : '';
	const when = a.dismissedAt ? ` ${formatRelative(a.dismissedAt, now)}` : '';
	return `Dismissed${by}${when}`;
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** "1 alert", "3 alerts". */
export function alertCount(n: number): string {
	return plural(n, 'alert', 'alerts');
}

// Disks and RAID arrays (the environment's System tab).

/** Disk health and RAID alerts that fire (active or dismissed) in one environment. */
export function healthAlerts<T extends Pick<Alert, 'kind' | 'state' | 'environmentId'>>(
	alerts: readonly T[],
	environmentId: string
): T[] {
	return alerts.filter(
		(a) =>
			a.state === 'firing' &&
			a.environmentId === environmentId &&
			(a.kind === 'disk_health' || a.kind === 'raid')
	);
}

/** The key of a disk row: its path and smartctl type (diskKey in diskHealth.ts). */
export function diskAlertKey(device: string, type: string): string {
	return `${device}|${type}`;
}

/** The key of an array row: md or zfs, and its name (the RAID card's row key). */
export function arrayAlertKey(kind: string, name: string): string {
	return `${kind}/${name}`;
}

/** Keeps the worse (then the newer) of two alerts of one row. */
function keepWorse<T extends Pick<Alert, 'severity' | 'startedAt'>>(
	m: Map<string, T>,
	k: string,
	a: T
) {
	const had = m.get(k);
	if (!had || sortAlerts([a, had])[0] === a) m.set(k, a);
}

/** Firing disk alerts by disk (diskAlertKey of `device` and `deviceType`). */
export function alertsByDisk<T extends Alert>(alerts: readonly T[]): Map<string, T> {
	const out = new Map<string, T>();
	for (const a of alerts) {
		if (a.kind !== 'disk_health' || a.state !== 'firing') continue;
		const device = a.facts.device ?? a.resourceId;
		keepWorse(out, diskAlertKey(device, a.facts.deviceType ?? ''), a);
	}
	return out;
}

/** Firing RAID alerts by array (arrayAlertKey of `arrayKind` and `array` or `pool`). */
export function alertsByArray<T extends Alert>(alerts: readonly T[]): Map<string, T> {
	const out = new Map<string, T>();
	for (const a of alerts) {
		if (a.kind !== 'raid' || a.state !== 'firing') continue;
		const kind = a.facts.arrayKind ?? (a.resourceType === 'zfs_pool' ? 'zfs' : 'md');
		const name = a.facts.array ?? a.facts.pool ?? a.resourceId;
		keepWorse(out, arrayAlertKey(kind, name), a);
	}
	return out;
}

/** The disk's or array's name an alert is about ("/dev/sda", "md0", "tank"). */
export function healthSubject(a: Pick<Alert, 'facts' | 'resourceId'>): string {
	return a.facts.device ?? a.facts.array ?? a.facts.pool ?? a.resourceId;
}

/** "/dev/sda", "/dev/sda and md0", "/dev/sda, /dev/sdb and 2 more". */
function namesText(names: string[]): string {
	const unique = [...new Set(names)];
	if (unique.length <= 3) {
		return unique.length > 1
			? `${unique.slice(0, -1).join(', ')} and ${unique.at(-1)}`
			: (unique[0] ?? '');
	}
	return `${unique.slice(0, 2).join(', ')} and ${unique.length - 2} more`;
}

/** The environment page's notice about its active disk and RAID alerts. */
export interface HealthNotice {
	tone: 'danger' | 'warn';
	title: string;
	body?: string;
}

/**
 * The notice for active disk and RAID alerts of an environment: one alert
 * by its title and detail ("Disk /dev/sda on homelab is failing"),
 * several by count ("3 disk and RAID alerts") and the names. Danger when
 * any is critical. Null without active ones.
 */
export function healthNotice(
	alerts: readonly Pick<
		Alert,
		'kind' | 'severity' | 'title' | 'detail' | 'facts' | 'resourceId' | 'startedAt'
	>[]
): HealthNotice | null {
	if (alerts.length === 0) return null;
	const tone = worstSeverity(alerts) === 'critical' ? 'danger' : 'warn';
	if (alerts.length === 1) return { tone, title: alerts[0].title, body: alerts[0].detail };
	const disks = alerts.filter((a) => a.kind === 'disk_health').length;
	const what =
		disks === alerts.length
			? 'disk alerts'
			: disks === 0
				? 'RAID alerts'
				: 'disk and RAID alerts';
	const names = sortAlerts(alerts).map(healthSubject);
	return { tone, title: `${alerts.length} ${what}`, body: `${namesText(names)}.` };
}

/** Active disk health and RAID alerts, counted for the dashboard's "Needs attention". */
export function healthTallies(
	alerts: readonly Pick<Alert, 'kind' | 'severity' | 'state' | 'dismissed'>[]
): { disks: AlertTally; raid: AlertTally } {
	const tally = (kind: AlertKind): AlertTally => {
		const of = alerts.filter((a) => a.kind === kind && isActive(a));
		return { count: of.length, critical: of.some((a) => a.severity === 'critical') };
	};
	return { disks: tally('disk_health'), raid: tally('raid') };
}
