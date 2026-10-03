// Environment view model (#3, #5, #34): status words, metric ranges and
// series extraction, compatibility and removal-preview wording. Pure.
import type { Environment, EnvironmentMetrics, Schema } from '$lib/api/client';
import { METRIC_COLORS } from '$lib/design/hue';
import { formatDateTime, formatDuration, formatRelative, secondsSince } from '$lib/ui/format';
import { latestValue, type ChartLine } from '$lib/ui/timeseries';

export type EnvironmentStatus = 'online' | 'offline' | 'archived';

export function environmentStatus(e: Pick<Environment, 'online' | 'status'>): EnvironmentStatus {
	if (e.status === 'archived') return 'archived';
	return e.online ? 'online' : 'offline';
}

/** Chart ranges (the API picks raw 10 s, 1 min or 15 min storage). */
export const METRIC_RANGES = [
	{ id: '1h', label: '1 Hour', seconds: 3600 },
	{ id: '6h', label: '6 Hours', seconds: 6 * 3600 },
	{ id: '24h', label: '24 Hours', seconds: 24 * 3600 },
	{ id: '7d', label: '7 Days', seconds: 7 * 86400 },
	{ id: '30d', label: '30 Days', seconds: 30 * 86400 },
	{ id: '90d', label: '90 Days', seconds: 90 * 86400 }
] as const;

export type MetricRangeId = (typeof METRIC_RANGES)[number]['id'];

export function rangeSeconds(id: string): number {
	return METRIC_RANGES.find((r) => r.id === id)?.seconds ?? 3600;
}

/** The values of one series (by key and, for disks, mount); [] if absent. */
export function seriesValues(
	m: EnvironmentMetrics | undefined,
	key: string,
	mount?: string
): (number | null)[] {
	const s = m?.series.find((x) => x.key === key && (mount === undefined || x.mount === mount));
	return s ? s.values.map((v) => (v === undefined ? null : v)) : [];
}

/** Whether a series has a value in the range (older agents send none of the newer keys). */
export function hasValues(m: EnvironmentMetrics | undefined, key: string): boolean {
	return seriesValues(m, key).some((v) => v !== null);
}

/**
 * The parts of the Memory chart, stacked from the bottom like Beszel's:
 * used, the ZFS ARC (hosts with ZFS) and the buffers and page cache (left
 * out while the range has none: older agents).
 */
export function memoryLines(m: EnvironmentMetrics | undefined): ChartLine[] {
	const parts: ChartLine[] = [
		{
			name: 'Used',
			values: seriesValues(m, 'memory.used_bytes'),
			color: METRIC_COLORS.memoryUsed
		}
	];
	if (hasValues(m, 'memory.zfs_arc_bytes'))
		parts.push({
			name: 'ZFS ARC',
			values: seriesValues(m, 'memory.zfs_arc_bytes'),
			color: METRIC_COLORS.memoryZfsArc,
			fill: 0.5
		});
	if (hasValues(m, 'memory.cache_bytes'))
		parts.push({
			name: 'Cache / Buffers',
			values: seriesValues(m, 'memory.cache_bytes'),
			color: METRIC_COLORS.memoryCache
		});
	return parts;
}

/** The configured swap at the end of the range (0 or null: no Swap chart). */
export function swapTotal(m: EnvironmentMetrics | undefined): number | null {
	return latestValue(seriesValues(m, 'swap.total_bytes'));
}

/** Disk mounts present in a metrics response (docker, stacks, bind-N). */
export function diskMounts(m: EnvironmentMetrics | undefined): string[] {
	const out: string[] = [];
	for (const s of m?.series ?? [])
		if (s.key === 'disk.used_bytes' && s.mount && !out.includes(s.mount)) out.push(s.mount);
	return out;
}

/** Filesystem roles in the user's words (never host paths, #5). */
export function mountLabel(mount: string): string {
	if (mount === 'docker') return 'Docker Data';
	if (mount === 'stacks') return 'Stacks';
	const m = mount.match(/^bind-(\d+)$/);
	return m ? `Bind Mount ${m[1]}` : mount;
}

export const COMPATIBILITY: Record<
	string,
	{ status: string; label: string; tone: 'ok' | 'warn' | 'danger' }
> = {
	current: { status: 'current', label: 'Current', tone: 'ok' },
	outdated: { status: 'outdated', label: 'Upgrade Recommended', tone: 'warn' },
	unsupported: { status: 'unsupported', label: 'Unsupported: Refused', tone: 'danger' }
};

type Dependent = Schema<'RemovalDependentKind'>;

const DEPENDENT_NOUNS: Record<Dependent['kind'], [string, string]> = {
	stack: ['stack', 'stacks'],
	managed_container: ['managed container', 'managed containers'],
	backup_repository: ['backup repository', 'backup repositories'],
	backup_set: ['backup set', 'backup sets'],
	registry_connection: ['registry connection', 'registry connections'],
	build_definition: ['build definition', 'build definitions'],
	permission_rule: ['permission rule', 'permission rules'],
	schedule: ['schedule', 'schedules'],
	job: ['queued or running job', 'queued or running jobs']
};

const ON_ARCHIVE: Record<Dependent['onArchive'], string> = {
	kept: 'kept, and back after a re-attach',
	paused: 'kept; scheduled runs pause until a re-attach',
	removed: 'removed (audited)',
	interrupted: 'stopped'
};

export function dependentNoun(kind: Dependent['kind'], count: number): string {
	const n = DEPENDENT_NOUNS[kind] ?? [kind.replaceAll('_', ' '), kind.replaceAll('_', ' ')];
	return count === 1 ? n[0] : n[1];
}

/** One consequence line per dependent kind with records, e.g. "2 stacks: kept, and back after a re-attach." */
export function removalConsequences(p: Schema<'EnvironmentRemovalPreview'>): string[] {
	const out: string[] = [];
	out.push(`Hides ${p.environmentName} from every operation. Its history and backups are kept.`);
	for (const d of p.dependents) {
		if (!d.count) continue;
		const what = `${d.count} ${dependentNoun(d.kind, d.count)}`;
		out.push(
			`${what[0].toUpperCase()}${what.slice(1)}: ${ON_ARCHIVE[d.onArchive] ?? d.onArchive}.`
		);
	}
	if (p.hostUntouched)
		out.push(
			'Nothing on the host changes: containers, volumes and files keep running as they are.'
		);
	return out;
}

/** "e1a2…" style short agent label for lists. */
export function agentLabel(a: Pick<Schema<'Agent'>, 'label' | 'hostname' | 'id'>): string {
	return a.label || a.hostname || a.id.slice(0, 8);
}

/**
 * The environment's connection as the header's status sentence ("Online
 * for 3 h 12 min."). Offline and archived environments get a notice
 * instead (undefined here).
 */
export function connectionSummary(
	e: Pick<Environment, 'online' | 'status' | 'connectionChangedAt'>,
	nowMs: number
): string | undefined {
	if (e.status === 'archived' || !e.online) return undefined;
	const s = secondsSince(e.connectionChangedAt, nowMs);
	return s === null ? 'Online.' : `Online for ${formatDuration(s)}.`;
}

/**
 * When an agent was last in contact, in words. A connected agent is in
 * contact now ("Connected now", connected since as the tooltip): the
 * manager refreshes its stored last-seen time only about once a minute, so
 * that time lags a live connection. A disconnected agent shows its
 * last-seen time (written on disconnect and at most a minute old after a
 * lost connection or a manager restart). `title` is the absolute time for
 * the tooltip.
 */
export function agentContact(
	a: Pick<
		Schema<'Agent'>,
		'status' | 'connected' | 'lastSeenAt' | 'lastConnectedAt' | 'revokedAt'
	>,
	now?: Date
): { text: string; at?: string; title?: string } {
	if (a.status === 'revoked' || a.revokedAt) {
		return a.revokedAt
			? {
					text: `Removed ${formatRelative(a.revokedAt, now)}`,
					at: a.revokedAt,
					title: formatDateTime(a.revokedAt)
				}
			: { text: 'Removed' };
	}
	if (a.connected)
		return a.lastConnectedAt
			? {
					text: 'Connected now',
					at: a.lastConnectedAt,
					title: `Connected since ${formatDateTime(a.lastConnectedAt)}`
				}
			: { text: 'Connected now' };
	if (a.lastSeenAt)
		return {
			text: formatRelative(a.lastSeenAt, now),
			at: a.lastSeenAt,
			title: formatDateTime(a.lastSeenAt)
		};
	return { text: '—' };
}
