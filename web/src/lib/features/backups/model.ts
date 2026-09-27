// Backups (#10, #24): presentation helpers. Pure functions (model.spec.ts).
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';

export type BackupRepository = Schema<'BackupRepository'>;
export type RepositoryHealth = Schema<'BackupRepositoryHealth'>;
export type ConnectionTest = Schema<'BackupConnectionTest'>;
export type RecoveryKeyState = Schema<'RecoveryKeyState'>;
export type RecoveryKeyReveal = Schema<'RecoveryKeyReveal'>;
export type BackupPolicy = Schema<'BackupPolicy'>;
export type BackupRetention = Schema<'BackupRetention'>;
export type BackupSet = Schema<'BackupSetSummary'>;
export type SetMember = Schema<'BackupSetMember'>;
export type Backup = Schema<'Backup'>;
export type BackupDetail = Schema<'BackupDetail'>;
export type BackupNode = Schema<'BackupNode'>;
export type StackSelection = Schema<'BackupStackSelection'>;
export type VolumeSelection = Schema<'BackupVolumeSelection'>;
export type ScopePreview = Schema<'ScopePreview'>;
export type ScopeItem = Schema<'ScopePreviewItem'>;
export type RetentionPreview = Schema<'RetentionPreview'>;
export type RestorePreview = Schema<'RestorePreview'>;
export type PolicyInput = Schema<'PolicyInputBody'>;
export type BackupActivity = Schema<'BackupActivity'>;
export type ActivityItem = Schema<'BackupActivityItem'>;
export type BackupStorage = Schema<'BackupStorage'>;

/**
 * The safety statement of the Recovery Key (#10, verbatim meaning): the
 * UI must say that a restore needs this key and that losing both copies
 * loses the data.
 */
export const RECOVERY_KEY_WARNING =
	'A restore on a new Docker Manager needs this Recovery Key. If the manager key store and your copy are both lost, the data can’t be restored: nobody, including Docker Manager, can decrypt the backups without it.';

export const RECOVERY_KEY_SCOPE =
	'One Recovery Key opens every Docker Manager backup repository of this instance: the manager and every environment, local and S3.';

interface Presentation {
	tone: BadgeTone;
	label: string;
}

const SET_STATE: Record<BackupSet['state'], Presentation> = {
	pending: { tone: 'info', label: 'Running' },
	complete: { tone: 'ok', label: 'Complete' },
	partial: { tone: 'warn', label: 'Partial' },
	failed: { tone: 'danger', label: 'Failed' }
};

export function setState(s: BackupSet['state']): Presentation {
	return SET_STATE[s] ?? { tone: 'neutral', label: s };
}

const MEMBER_STATE: Record<SetMember['state'], Presentation> = {
	pending: { tone: 'info', label: 'Running' },
	complete: { tone: 'ok', label: 'Complete' },
	partial: { tone: 'warn', label: 'Some files unreadable' },
	failed: { tone: 'danger', label: 'Failed' },
	missing: { tone: 'danger', label: 'Missing' }
};

export function memberState(s: SetMember['state']): Presentation {
	return MEMBER_STATE[s] ?? { tone: 'neutral', label: s };
}

export const KIND_LABEL: Record<NonNullable<Backup['kind']>, string> = {
	manager_state: 'Manager state',
	stack: 'Stack',
	volume: 'Volume'
};

export const CONSISTENCY_LABEL: Record<NonNullable<Backup['consistency']>, string> = {
	live: 'Live (crash-consistent)',
	shutdown: 'Containers stopped',
	snapshot: 'Consistent database snapshot'
};

/** A member's or backup's name: stack name, volume or "Manager state". */
export function itemName(m: {
	kind?: SetMember['kind'];
	stackName?: string;
	volume?: string;
	item?: string;
}): string {
	if (m.kind === 'manager_state') return 'Manager state';
	return m.stackName || m.volume || m.item || 'Backup';
}

/** Where a repository lives, without credentials. */
export function repositoryLocation(
	r: BackupRepository,
	environmentName?: (id: string) => string
): string {
	if (r.kind === 's3') {
		const where = `s3://${r.bucket ?? ''}${r.prefix ? `/${r.prefix.replace(/^\/+/, '')}` : ''}`;
		return r.endpoint ? `${where} on ${r.endpoint.replace(/^https?:\/\//, '')}` : where;
	}
	const host =
		!r.executor || r.executor === 'manager'
			? 'manager'
			: (environmentName?.(r.executor) ?? r.executor);
	return `${host}:${r.path ?? ''}`;
}

/** Plain-language retention, e.g. "7 daily, 4 weekly; always keeps 3 of each". */
export function retentionText(r: BackupRetention | undefined): string {
	if (!r) return 'Keep every backup';
	const parts: string[] = [];
	if (r.last) parts.push(`last ${r.last}`);
	if (r.hourly) parts.push(`${r.hourly} hourly`);
	if (r.daily) parts.push(`${r.daily} daily`);
	if (r.weekly) parts.push(`${r.weekly} weekly`);
	if (r.monthly) parts.push(`${r.monthly} monthly`);
	if (r.yearly) parts.push(`${r.yearly} yearly`);
	if (r.withinDays) parts.push(`everything from the last ${r.withinDays} days`);
	if (!parts.length) return 'Keep every backup';
	const floor = r.minKeep ? `; always keeps the newest ${r.minKeep} of each` : '';
	return `Keep ${parts.join(', ')}${floor}`;
}

/**
 * Retention of a new policy: the newest 168 snapshots (a week of hourly
 * backups, the default schedule), every other rule off (0), and a recovery
 * floor of 1.
 */
export const DEFAULT_RETENTION: BackupRetention = {
	last: 168,
	hourly: 0,
	daily: 0,
	weekly: 0,
	monthly: 0,
	yearly: 0,
	withinDays: 0,
	minKeep: 1
};

export function hasRetentionRules(r: BackupRetention | undefined): boolean {
	return (
		!!r &&
		!!(r.last || r.hourly || r.daily || r.weekly || r.monthly || r.yearly || r.withinDays)
	);
}

/** Recovery Keys are DYRK- plus 13 groups of four base32 characters. */
export function normalizeRecoveryKey(input: string): string {
	return input.replace(/\s+/g, '').toUpperCase();
}

export function looksLikeRecoveryKey(input: string): boolean {
	return /^DYRK(-[A-Z2-7]{4}){13}$/.test(normalizeRecoveryKey(input));
}

/** Where a policy backs up: "all environments and the manager state", "prod". */
export function scopeText(
	p: Pick<BackupPolicy, 'includeManagerState' | 'scope' | 'environmentId'>,
	environmentName?: (id: string) => string
): string {
	const scope =
		p.scope === 'all'
			? 'all environments'
			: p.environmentId && environmentName
				? environmentName(p.environmentId)
				: 'one environment';
	return p.includeManagerState ? `${scope} and the manager state` : scope;
}

/** Every recent set of the given policies, newest first. */
export function recentSets(
	policies: BackupPolicy[]
): (BackupSet & { policyId: string; policyName: string })[] {
	const out = policies.flatMap((p) =>
		(p.recentSets ?? []).map((s) => ({ ...s, policyId: p.id, policyName: p.name }))
	);
	return out.sort((a, b) => b.startedAt.localeCompare(a.startedAt));
}

/** Members of a set that did not complete (a retry re-runs only these). */
export function incompleteMembers(s: BackupSet): SetMember[] {
	return s.members.filter((m) => m.state !== 'complete');
}

export const SOURCE_STATE: Record<string, Presentation> = {
	included: { tone: 'ok', label: 'Included' },
	excluded: { tone: 'neutral', label: 'Excluded' },
	requires_opt_in: { tone: 'warn', label: 'Needs opt-in' },
	blocked: { tone: 'danger', label: 'Blocked' },
	missing: { tone: 'danger', label: 'Missing' }
};

export function sourceState(s: string): Presentation {
	return SOURCE_STATE[s] ?? { tone: 'neutral', label: s.replaceAll('_', ' ') };
}

/** Server phrases as sentences: first letter upper case. */
export function sentenceCase(s: string): string {
	return s ? s[0].toUpperCase() + s.slice(1) : s;
}

/** Octal permission bits, e.g. 420 → "0644". */
export function modeText(mode: number): string {
	return (mode & 0o7777).toString(8).padStart(4, '0');
}

/** The parent of an absolute snapshot path ("/" stays "/"). */
export function parentPath(path: string): string {
	const p = path.replace(/\/+$/, '');
	const i = p.lastIndexOf('/');
	return i <= 0 ? '/' : p.slice(0, i);
}

/** Path crumbs of a snapshot directory: [{name:'/',path:'/'}, …]. */
export function pathCrumbs(path: string): { name: string; path: string }[] {
	const out = [{ name: '/', path: '/' }];
	let acc = '';
	for (const part of path.split('/').filter(Boolean)) {
		acc += `/${part}`;
		out.push({ name: part, path: acc });
	}
	return out;
}

/** The job that runs a policy ID for a member, for links. */
export function jobHref(id: string | undefined, job: (id: string) => string): string | undefined {
	return id ? job(id) : undefined;
}

/** The Engine's label on volumes it created for anonymous mounts. */
export const ANONYMOUS_VOLUME_LABEL = 'com.docker.volume.anonymous';
const COMPOSE_PROJECT_LABEL = 'com.docker.compose.project';

export interface CoveredVolume {
	name: string;
	anonymous: boolean;
	/** The managed stack the volume belongs to; undefined: standalone. */
	stackId?: string;
}

/**
 * An environment's volumes as a backup policy covers them (#10): Docker
 * Manager's own are left out (#32), and so are volumes of Compose projects
 * that are no managed stack (the manager backs up neither); a volume
 * belongs to a managed stack by its membership, its Compose project label
 * or a stack container using it (the manager's rule); anonymous volumes
 * carry the Engine's label.
 */
export function coveredVolumes(
	volumes: {
		name: string;
		labels?: Record<string, string>;
		protection?: unknown;
		stack?: { stackId?: string; project: string };
		usedBy?: { id: string }[];
	}[],
	stacks: { id: string; name: string }[],
	containers: { id: string; labels?: Record<string, string> }[]
): CoveredVolume[] {
	const stackByName = new Map(stacks.map((s) => [s.name, s.id]));
	const stackOfContainer = new Map<string, string>();
	for (const c of containers) {
		const id = stackByName.get(c.labels?.[COMPOSE_PROJECT_LABEL] ?? '');
		if (id) stackOfContainer.set(c.id, id);
	}
	const out: CoveredVolume[] = [];
	for (const v of volumes) {
		if (v.protection) continue;
		const stackId =
			(v.stack?.stackId && stacks.some((s) => s.id === v.stack?.stackId)
				? v.stack.stackId
				: undefined) ??
			stackByName.get(v.stack?.project ?? v.labels?.[COMPOSE_PROJECT_LABEL] ?? '') ??
			(v.usedBy ?? []).map((c) => stackOfContainer.get(c.id)).find(Boolean);
		if (v.stack && !stackId) continue;
		out.push({ name: v.name, anonymous: ANONYMOUS_VOLUME_LABEL in (v.labels ?? {}), stackId });
	}
	return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** The exclusion key of a volume: environmentID/name for All Environments. */
export function volumeKey(all: boolean, environmentId: string, name: string): string {
	return all ? `${environmentId}/${name}` : name;
}

// --- Overview (#10): storage, running backups, set summaries ---

/** Storage of the given repositories, summed over their measured locations. */
export interface StorageTotals {
	/** Stored at the destinations (compressed, deduplicated). */
	sizeBytes: number;
	/** The same data before compression. */
	uncompressedBytes: number;
	/** What compression saved (uncompressed − stored, never negative). */
	freedBytes: number;
	/** uncompressed ÷ stored (0 when nothing is stored). */
	ratio: number;
	/** Share of the data stored compressed, 0–100. */
	compressedPercent: number;
	/** restic snapshots. */
	snapshots: number;
	/** The oldest measurement. */
	measuredAt?: string;
	repositories: {
		id: string;
		name: string;
		sizeBytes: number;
		uncompressedBytes: number;
		ratio: number;
	}[];
}

/**
 * Sums the measured locations of repos (only the environment's locations
 * when environmentId is set; the manager state belongs to no environment).
 * Undefined when nothing was measured yet.
 */
export function storageTotals(
	repos: BackupRepository[],
	environmentId: string | null = null
): StorageTotals | undefined {
	const t: StorageTotals = {
		sizeBytes: 0,
		uncompressedBytes: 0,
		freedBytes: 0,
		ratio: 0,
		compressedPercent: 0,
		snapshots: 0,
		repositories: []
	};
	let compressed = 0;
	let measured = false;
	for (const r of repos) {
		const locs = (r.storage?.locations ?? []).filter(
			(l) => !environmentId || l.environmentId === environmentId
		);
		if (!locs.length) continue;
		measured = true;
		let size = 0;
		let uncompressed = 0;
		for (const l of locs) {
			size += l.sizeBytes;
			uncompressed += l.uncompressedBytes;
			t.snapshots += l.snapshots;
			compressed += l.compressionProgress * l.uncompressedBytes;
			if (!t.measuredAt || l.measuredAt < t.measuredAt) t.measuredAt = l.measuredAt;
		}
		t.sizeBytes += size;
		t.uncompressedBytes += uncompressed;
		t.repositories.push({
			id: r.id,
			name: r.name,
			sizeBytes: size,
			uncompressedBytes: uncompressed,
			ratio: size > 0 ? uncompressed / size : 0
		});
	}
	if (!measured) return undefined;
	t.freedBytes = Math.max(0, t.uncompressedBytes - t.sizeBytes);
	t.ratio = t.sizeBytes > 0 ? t.uncompressedBytes / t.sizeBytes : 0;
	t.compressedPercent = t.uncompressedBytes > 0 ? compressed / t.uncompressedBytes : 0;
	return t;
}

/** A compression ratio: "2.01x" ("—" when unknown). */
export function ratioText(ratio: number): string {
	return ratio > 0 && Number.isFinite(ratio) ? `${ratio.toFixed(2)}x` : '—';
}

/** "Volume media", "Stack shop", "Manager state". */
export function activityItemName(it: ActivityItem): string {
	if (it.kind === 'manager_state') return 'Manager state';
	if (it.kind === 'stack') return `Stack ${it.stackName || it.stackId || it.item}`;
	return `Volume ${it.volume || it.item}`;
}

/** Overall percent of a running backup job (-1 unknown). */
export function activityPercent(a: BackupActivity): number {
	const c = a.current;
	if (!c || a.itemCount < 1) return a.percent;
	return Math.round(((c.index + c.percent / 100) / a.itemCount) * 100);
}

/**
 * A long path shortened in the middle so both its start and the file
 * name stay readable: "stacks/…/deep/file.txt".
 */
export function middleTruncate(s: string, max = 72): string {
	if (s.length <= max) return s;
	const keep = max - 1;
	const tail = Math.ceil(keep * 0.6);
	return `${s.slice(0, keep - tail)}…${s.slice(s.length - tail)}`;
}

/** "11 of 12 complete", plus the environments of a multi-host set. */
export function setSummary(s: BackupSet): string {
	const done = s.members.filter((m) => m.state === 'complete').length;
	const envs = new Set(s.members.map((m) => m.environmentId).filter(Boolean)).size;
	const total = s.members.length;
	const base =
		total === 0
			? 'Nothing selected'
			: done === total
				? `${total} ${total === 1 ? 'backup' : 'backups'}`
				: `${done} of ${total} complete`;
	return envs > 1 ? `${base} · ${envs} environments` : base;
}

/** Seconds a set ran (undefined while it runs). */
export function setDuration(s: BackupSet): number | undefined {
	if (!s.finishedAt) return undefined;
	return Math.max(0, (Date.parse(s.finishedAt) - Date.parse(s.startedAt)) / 1000);
}

/** Bytes the backups of a set processed (undefined when none is known). */
export function setBytes(backups: Backup[] | undefined, setId: string): number | undefined {
	let sum = 0;
	let found = false;
	for (const b of backups ?? []) {
		if (b.setId === setId && b.bytes !== undefined) {
			sum += b.bytes;
			found = true;
		}
	}
	return found ? sum : undefined;
}

/** A set's members grouped by environment ("" = the manager state). */
export function membersByEnvironment(s: BackupSet): [string, SetMember[]][] {
	const groups = new Map<string, SetMember[]>();
	for (const m of s.members) {
		const k = m.environmentId ?? '';
		groups.set(k, [...(groups.get(k) ?? []), m]);
	}
	return [...groups.entries()].sort(([a], [b]) =>
		a === '' ? 1 : b === '' ? -1 : a.localeCompare(b)
	);
}
