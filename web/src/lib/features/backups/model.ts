// Backups (#10, #24): presentation helpers. Pure functions (model.spec.ts).
import type { Schema } from '$lib/api/client';
import type { BadgeTone } from '$lib/ui/Badge.svelte';
import { describeCron } from '$lib/ui/cron';
import { formatBytes, formatNumber, formatRelative } from '$lib/ui/format';

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
export type ResticSnapshot = Schema<'ResticSnapshot'>;
export type ResticLocation = Schema<'ResticLocationSnapshots'>;

/**
 * The safety statement of the Recovery Key (#10, verbatim meaning): the
 * UI must say that a restore needs this key and that losing both copies
 * loses the data.
 */
export const RECOVERY_KEY_WARNING =
	'A restore on a new Docker Manager needs this Recovery Key. If the manager key store and your copy are both lost, the data can’t be restored: nobody, including Docker Manager, can decrypt the backups without it.';

export const RECOVERY_KEY_SCOPE =
	'One Recovery Key opens every backup repository of this Docker Manager.';

interface Presentation {
	tone: BadgeTone;
	label: string;
}

const SET_STATE: Record<BackupSet['state'], Presentation> = {
	pending: { tone: 'info', label: 'Running' },
	complete: { tone: 'ok', label: 'Complete' },
	partial: { tone: 'warn', label: 'Partial' },
	failed: { tone: 'danger', label: 'Failed' },
	// Every item was removed before its turn: nothing to back up, nothing failed.
	skipped: { tone: 'neutral', label: 'Skipped' }
};

export function setState(s: BackupSet['state']): Presentation {
	return SET_STATE[s] ?? { tone: 'neutral', label: s };
}

const MEMBER_STATE: Record<SetMember['state'], Presentation> = {
	pending: { tone: 'info', label: 'Running' },
	complete: { tone: 'ok', label: 'Complete' },
	partial: { tone: 'warn', label: 'Some Files Unreadable' },
	failed: { tone: 'danger', label: 'Failed' },
	missing: { tone: 'danger', label: 'Missing' },
	// Removed before its turn (a temporary volume, a deleted stack): not a failure.
	skipped: { tone: 'neutral', label: 'Skipped' }
};

export function memberState(s: SetMember['state']): Presentation {
	return MEMBER_STATE[s] ?? { tone: 'neutral', label: s };
}

/**
 * Why a member has no (complete) backup, in words: an error for failed
 * members, a plain note for skipped ones (removed before their turn).
 */
export function memberReason(
	m: Pick<SetMember, 'state' | 'errorClass'>
): { text: string; error: boolean } | undefined {
	if (m.state === 'skipped')
		return {
			text:
				m.errorClass && m.errorClass !== 'item_gone'
					? sentenceCase(m.errorClass.replaceAll('_', ' '))
					: 'Removed before its turn',
			error: false
		};
	if (!m.errorClass) return undefined;
	return { text: sentenceCase(m.errorClass.replaceAll('_', ' ')), error: true };
}

export const KIND_LABEL: Record<NonNullable<Backup['kind']>, string> = {
	manager_state: 'Manager State',
	stack: 'Stack',
	volume: 'Volume'
};

export const CONSISTENCY_LABEL: Record<NonNullable<Backup['consistency']>, string> = {
	live: 'Live (Crash-Consistent)',
	shutdown: 'Containers Stopped',
	snapshot: 'Consistent Database Snapshot'
};

/** A member's or backup's name: stack name, volume or "Manager State". */
export function itemName(m: {
	kind?: SetMember['kind'];
	stackName?: string;
	volume?: string;
	item?: string;
}): string {
	if (m.kind === 'manager_state') return 'Manager State';
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

function retentionParts(r: BackupRetention): string[] {
	const parts: string[] = [];
	if (r.last) parts.push(`last ${r.last}`);
	if (r.hourly) parts.push(`${r.hourly} hourly`);
	if (r.daily) parts.push(`${r.daily} daily`);
	if (r.weekly) parts.push(`${r.weekly} weekly`);
	if (r.monthly) parts.push(`${r.monthly} monthly`);
	if (r.yearly) parts.push(`${r.yearly} yearly`);
	if (r.withinDays) parts.push(`everything from the last ${r.withinDays} days`);
	return parts;
}

/** Plain-language retention, e.g. "Keep 7 daily, 4 weekly". */
export function retentionText(r: BackupRetention | undefined): string {
	const expiry = r?.expireDeletedDays
		? `; backups of deleted stacks and volumes go after ${r.expireDeletedDays} ${r.expireDeletedDays === 1 ? 'day' : 'days'}`
		: '';
	const parts = r ? retentionParts(r) : [];
	if (!parts.length) return `Keep every backup${expiry}`;
	return `Keep ${parts.join(', ')}${expiry}`;
}

/** Retention in a few words for tables and KPIs: "7 daily, 4 weekly", "Keep everything". */
export function retentionShort(r: BackupRetention | undefined): string {
	const parts = r ? retentionParts(r) : [];
	return parts.length ? sentenceCase(parts.join(', ')) : 'Keep everything';
}

const NO_RULES = {
	last: 0,
	hourly: 0,
	daily: 0,
	weekly: 0,
	monthly: 0,
	yearly: 0,
	withinDays: 0
} as const;
const RULE_KEYS = Object.keys(NO_RULES) as (keyof typeof NO_RULES)[];

/** The retention choices of the policy form; Custom reveals every rule. */
export type RetentionPreset = 'recommended' | 'last30' | 'everything' | 'custom';

export const RETENTION_PRESETS: {
	value: RetentionPreset;
	label: string;
	description?: string;
}[] = [
	{ value: 'recommended', label: '7 Daily, 4 Weekly, 12 Monthly (Recommended)' },
	{ value: 'last30', label: 'Keep the Last 30', description: 'Per stack and volume.' },
	{
		value: 'everything',
		label: 'Keep Everything',
		description: 'The repository keeps growing.'
	},
	{ value: 'custom', label: 'Custom' }
];

const PRESET_RULES: Record<Exclude<RetentionPreset, 'custom'>, Partial<BackupRetention>> = {
	recommended: { daily: 7, weekly: 4, monthly: 12 },
	last30: { last: 30 },
	everything: {}
};

/**
 * The preset a policy's retention matches (editing opens on it): no rules
 * is "Keep everything"; a preset's rules match it; anything else is Custom.
 */
export function retentionPreset(r: BackupRetention | undefined): RetentionPreset {
	if (!r || !hasRetentionRules(r)) return 'everything';
	for (const k of ['recommended', 'last30'] as const) {
		const want: BackupRetention = { ...NO_RULES, ...PRESET_RULES[k] };
		if (RULE_KEYS.every((key) => (r[key] ?? 0) === want[key])) return k;
	}
	return 'custom';
}

/**
 * The retention a preset sets: its rules (every other rule off); "after
 * every backup" and the expiry of deleted items are kept. Custom changes
 * nothing.
 */
export function applyRetentionPreset(preset: RetentionPreset, r: BackupRetention): BackupRetention {
	if (preset === 'custom') return r;
	return { ...r, ...NO_RULES, ...PRESET_RULES[preset] };
}

/** Retention of a new policy: the recommended preset (7 daily, 4 weekly, 12 monthly). */
export const DEFAULT_RETENTION: BackupRetention = {
	...NO_RULES,
	daily: 7,
	weekly: 4,
	monthly: 12
};

/** Days a deleted stack's or volume's backups are kept when the expiry is turned on. */
export const DEFAULT_EXPIRE_DELETED_DAYS = 30;

export function hasRetentionRules(r: BackupRetention | undefined): boolean {
	return (
		!!r &&
		!!(r.last || r.hourly || r.daily || r.weekly || r.monthly || r.yearly || r.withinDays)
	);
}

/**
 * The edits of a saved policy: the form's input without its scope, which
 * is fixed once the policy exists (the update refuses unknown fields).
 */
export function policyEdits(input: PolicyInput): Omit<PolicyInput, 'scope'> {
	const { scope, ...edits } = input;
	void scope;
	return edits;
}

/** Whether retention would remove anything: rules are set or deleted items' backups expire. */
export function retentionActive(r: BackupRetention | undefined): boolean {
	return hasRetentionRules(r) || !!r?.expireDeletedDays;
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

/**
 * Members of a set that did not complete (a retry re-runs only these):
 * skipped members were removed before their turn and are not retried.
 */
export function incompleteMembers(s: BackupSet): SetMember[] {
	return s.members.filter((m) => m.state !== 'complete' && m.state !== 'skipped');
}

export const SOURCE_STATE: Record<string, Presentation> = {
	included: { tone: 'ok', label: 'Included' },
	excluded: { tone: 'neutral', label: 'Excluded' },
	requires_opt_in: { tone: 'warn', label: 'Needs Opt-In' },
	blocked: { tone: 'danger', label: 'Blocked' },
	missing: { tone: 'danger', label: 'Missing' }
};

export function sourceState(s: string): Presentation {
	return SOURCE_STATE[s] ?? { tone: 'neutral', label: s.replaceAll('_', ' ') };
}

type ScopeSourceOf = ScopeItem['sources'][number];

/**
 * An item's sources without repeats (the agent lists a bind two services
 * mount, or several volumes without a path, more than once), the ones that
 * need attention first (needs opt-in, blocked, missing), then included,
 * then left out.
 */
export function scopeSources(sources: ScopeSourceOf[]): ScopeSourceOf[] {
	const seen = new Set<string>();
	const out: ScopeSourceOf[] = [];
	for (const s of sources) {
		const key = JSON.stringify([s.kind, s.path, s.name, s.service, s.state, s.reason]);
		if (seen.has(key)) continue;
		seen.add(key);
		out.push(s);
	}
	const rank = (st: string) => (st === 'included' ? 1 : st === 'excluded' ? 2 : 0);
	return out.sort((a, b) => rank(a.state) - rank(b.state));
}

/** How many of an item's (distinct) sources are in each state. */
export function scopeCounts(sources: ScopeSourceOf[]): { state: string; count: number }[] {
	const counts = new Map<string, number>();
	for (const s of scopeSources(sources)) counts.set(s.state, (counts.get(s.state) ?? 0) + 1);
	return [...counts].map(([state, count]) => ({ state, count }));
}

/** An item whose details should be open: something needs the user. */
export function scopeNeedsAttention(item: ScopeItem): boolean {
	return (
		!!item.error ||
		!!item.conflicts?.length ||
		item.sources.some((s) => s.state !== 'included' && s.state !== 'excluded')
	);
}

/** An item's name: the stack's name (never its ID) or the volume. */
export function scopeItemTitle(
	item: ScopeItem,
	stackName?: (id: string) => string | undefined
): string {
	if (item.kind === 'stack')
		return (item.stackId && stackName?.(item.stackId)) || item.item.replace(/^stack\//, '');
	return item.volume || item.item.replace(/^volume\//, '');
}

type RetentionDecisionOf = RetentionPreview['locations'][number]['decisions'][number];

/** One stack or volume of a retention preview: its backups, newest first. */
export interface RetentionGroup {
	item: string;
	name: string;
	forget: number;
	keep: number;
	decisions: RetentionDecisionOf[];
}

/**
 * A location's retention decisions per stack or volume (named, never by
 * ID), those losing backups first, then by name.
 */
export function retentionGroups(
	decisions: RetentionDecisionOf[],
	stackName?: (id: string) => string | undefined
): RetentionGroup[] {
	const by = new Map<string, RetentionGroup>();
	for (const d of decisions) {
		let g = by.get(d.item);
		if (!g) {
			const [kind, ...rest] = d.item.split('/');
			const id = rest.join('/');
			const name =
				d.item === 'manager' || kind === 'manager'
					? 'Manager State'
					: kind === 'stack'
						? `Stack ${stackName?.(id) ?? id}`
						: kind === 'volume'
							? `Volume ${id}`
							: d.item;
			g = { item: d.item, name, forget: 0, keep: 0, decisions: [] };
			by.set(d.item, g);
		}
		g.decisions.push(d);
		if (d.keep) g.keep++;
		else g.forget++;
	}
	const out = [...by.values()];
	for (const g of out) g.decisions.sort((a, b) => b.time.localeCompare(a.time));
	return out.sort((a, b) => b.forget - a.forget || a.name.localeCompare(b.name));
}

const RETENTION_REASON: Record<string, string> = {
	last: 'the latest backups',
	hourly: 'the hourly rule',
	daily: 'the daily rule',
	weekly: 'the weekly rule',
	monthly: 'the monthly rule',
	yearly: 'the yearly rule',
	within: 'the keep-within rule',
	newest: 'being the newest'
};

/** A retention keep reason in words; unknown reasons stay as they are. */
export function retentionReasonText(reason: string): string {
	return RETENTION_REASON[reason] ?? reason;
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
/** The label that leaves a volume, or the volumes of a container, out of backups. */
export const BACKUP_EXCLUDE_LABEL = 'docker-manager.backup.exclude';
const COMPOSE_PROJECT_LABEL = 'com.docker.compose.project';

/** A buildx builder's state volume (buildx_buildkit_<builder>_state): rebuildable build cache. */
export function isBuildxVolume(name: string): boolean {
	const m = /^buildx_buildkit_(.+)_state$/.exec(name);
	return !!m && m[1] !== '';
}

function backupExcluded(labels: Record<string, string> | undefined): boolean {
	return (labels?.[BACKUP_EXCLUDE_LABEL] ?? '').toLowerCase() === 'true';
}

const HELPER_ASIDE_NAME = /.-docker-manager-(update|rename)-[0-9a-f]{12}$/;
const COMPOSE_TEMP_NAME = /^[0-9a-f]{12}_./;

/**
 * A temporary container of Docker Manager or Compose (the manager's rule,
 * protocol.IsHelperContainer): a container set aside during an image update
 * or a stack rename, Compose's replacement during a recreate (its label
 * stays after the rename, so only the temporary name counts), or the
 * agent's self-update helper. It never counts as a user of a volume.
 */
export function isHelperContainer(
	name: string | undefined,
	labels: Record<string, string> | undefined
): boolean {
	const n = (name ?? '').replace(/^\//, '');
	// Docker Manager's role label, or its key of before 2026-09-28.
	const role = labels?.['docker-manager.role'] ?? labels?.['dev.neureka.docker-manager.role'];
	if (role === 'self-update') return true;
	if (HELPER_ASIDE_NAME.test(n)) return true;
	return labels?.['com.docker.compose.replace'] !== undefined && COMPOSE_TEMP_NAME.test(n);
}

export interface CoveredVolume {
	name: string;
	anonymous: boolean;
	/** A buildx builder's state (left out unless the policy includes them). */
	buildx: boolean;
	/** Left out by the backup exclude label (on the volume or a container using it). */
	labelled: boolean;
	/** Where that label is: the volume, its stack's Compose file, a container using it. */
	labelledBy?: 'volume' | 'compose' | 'container';
	/** Used only by temporary containers of Docker Manager or Compose: left out. */
	temporary: boolean;
	/** The managed stack the volume belongs to; undefined: standalone. */
	stackId?: string;
}

/**
 * An environment's volumes as a backup policy covers them (#10): Docker
 * Manager's own are left out (#32), and so are volumes of Compose projects
 * that are no managed stack (the manager backs up neither); a volume
 * belongs to a managed stack by its membership, its Compose project label
 * or a stack container using it (the manager's rule); anonymous volumes
 * carry the Engine's label. The backup exclude label counts on the
 * volume, in its Compose labels (the agent honors them too) or on a
 * container using it. Temporary containers (isHelperContainer) never
 * count as users: a standalone volume only they use is marked temporary.
 * The manager also leaves out volumes of unfinished environment
 * migrations, which this list cannot tell (it does not know how the
 * migrations ended).
 */
export function coveredVolumes(
	volumes: {
		name: string;
		labels?: Record<string, string>;
		/** Labels the stack's Compose file declares that Docker kept off the volume. */
		composeLabels?: Record<string, string>;
		protection?: unknown;
		stack?: { stackId?: string; project: string };
		usedBy?: { id: string }[];
	}[],
	stacks: { id: string; name: string }[],
	containers: { id: string; name?: string; labels?: Record<string, string> }[]
): CoveredVolume[] {
	const stackByName = new Map(stacks.map((s) => [s.name, s.id]));
	const stackOfContainer = new Map<string, string>();
	const labelledContainers = new Set<string>();
	const helpers = new Set<string>();
	for (const c of containers) {
		if (isHelperContainer(c.name, c.labels)) {
			helpers.add(c.id);
			continue;
		}
		const id = stackByName.get(c.labels?.[COMPOSE_PROJECT_LABEL] ?? '');
		if (id) stackOfContainer.set(c.id, id);
		if (backupExcluded(c.labels)) labelledContainers.add(c.id);
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
		out.push({
			name: v.name,
			anonymous: ANONYMOUS_VOLUME_LABEL in (v.labels ?? {}),
			buildx: !stackId && isBuildxVolume(v.name),
			...labelling(v, labelledContainers),
			temporary:
				!stackId &&
				(v.usedBy ?? []).length > 0 &&
				(v.usedBy ?? []).every((c) => helpers.has(c.id)),
			stackId
		});
	}
	return out.sort((a, b) => a.name.localeCompare(b.name));
}

function labelling(
	v: {
		labels?: Record<string, string>;
		composeLabels?: Record<string, string>;
		usedBy?: { id: string }[];
	},
	labelledContainers: Set<string>
): Pick<CoveredVolume, 'labelled' | 'labelledBy'> {
	const by: CoveredVolume['labelledBy'] =
		v.composeLabels?.[BACKUP_EXCLUDE_LABEL] !== undefined
			? backupExcluded(v.composeLabels)
				? 'compose'
				: undefined
			: backupExcluded(v.labels)
				? 'volume'
				: undefined;
	const labelledBy =
		by ??
		((v.usedBy ?? []).some((c) => labelledContainers.has(c.id)) ? 'container' : undefined);
	return { labelled: !!labelledBy, labelledBy };
}

/** Why a volume the backup exclude label leaves out can't be selected (its (i)). */
export function labelLockReason(by: NonNullable<CoveredVolume['labelledBy']>): string {
	switch (by) {
		case 'compose':
			return `Managed by a volume label in the stack's Compose file: ${BACKUP_EXCLUDE_LABEL}=true leaves it out of backups.`;
		case 'container':
			return `Managed by a container label: a container using it has ${BACKUP_EXCLUDE_LABEL}=true, which leaves it out of backups.`;
		default:
			return `Managed by a volume label: ${BACKUP_EXCLUDE_LABEL}=true leaves it out of backups.`;
	}
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

/** A compression ratio: "2.01x", "2x" ("—" when unknown). */
export function ratioText(ratio: number): string {
	return ratio > 0 && Number.isFinite(ratio) ? `${formatNumber(ratio)}x` : '—';
}

/** "Volume media", "Stack shop", "Manager State". */
export function activityItemName(it: ActivityItem): string {
	if (it.kind === 'manager_state') return 'Manager State';
	if (it.kind === 'stack') return `Stack ${it.stackName || it.stackId || it.item}`;
	return `Volume ${it.volume || it.item}`;
}

/** A retention job (forget, then prune) in the running list. */
export function isRetentionActivity(a: Pick<BackupActivity, 'kind'>): boolean {
	return a.kind === 'backup.retention' || a.kind === 'manager.retention';
}

/**
 * Overall percent of a running backup job (-1 unknown), unrounded: views
 * show it with formatPercent.
 */
export function activityPercent(a: BackupActivity): number {
	const c = a.current;
	if (!c || a.itemCount < 1) return a.percent;
	return ((c.index + c.percent / 100) / a.itemCount) * 100;
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

/**
 * "11 of 12 complete", plus the skipped members (removed before their
 * turn, not counted as backups) and the environments of a multi-host set.
 */
export function setSummary(s: BackupSet): string {
	const skipped = s.members.filter((m) => m.state === 'skipped').length;
	const counted = s.members.filter((m) => m.state !== 'skipped');
	const done = counted.filter((m) => m.state === 'complete').length;
	const envs = new Set(s.members.map((m) => m.environmentId).filter(Boolean)).size;
	const total = counted.length;
	const parts = [
		s.members.length === 0
			? 'Nothing selected'
			: total === 0
				? ''
				: done === total
					? `${total} ${total === 1 ? 'backup' : 'backups'}`
					: `${done} of ${total} complete`,
		skipped ? `${skipped} skipped` : '',
		envs > 1 ? `${envs} environments` : ''
	];
	return parts.filter(Boolean).join(' · ');
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

// --- Raw snapshots (#10): restic's own snapshots, from a repository page ---

export const SNAPSHOT_CLASS: Record<ResticSnapshot['class'], Presentation> = {
	stack: { tone: 'info', label: 'Stack' },
	volume: { tone: 'info', label: 'Volume' },
	manager_state: { tone: 'info', label: 'Manager State' },
	set_manifest: { tone: 'neutral', label: 'Set Manifest' },
	host_manifest: { tone: 'neutral', label: 'Host Manifest' },
	foreign: { tone: 'warn', label: 'Not From Docker Manager' }
};

/** What a snapshot holds: the backup's name, its item, or its class. */
export function snapshotName(s: ResticSnapshot): string {
	if (s.name) return s.name;
	if (s.class === 'manager_state') return 'Manager State';
	if (s.item?.startsWith('volume/')) return s.item.slice('volume/'.length);
	if (s.item?.startsWith('stack/'))
		return `Stack ${s.item.slice('stack/'.length, 'stack/'.length + 8)}`;
	if (s.class === 'set_manifest' || s.class === 'host_manifest') return 'Manifest';
	return s.paths.join(', ') || s.shortId;
}

/** One row of the raw snapshots page: a snapshot with its repository and location. */
export interface SnapshotRow {
	key: string;
	repositoryId: string;
	repositoryName: string;
	scope: string;
	environmentId?: string;
	snapshot: ResticSnapshot;
}

/** A location that could not be listed completely. */
export interface SnapshotProblem {
	repositoryName: string;
	scope: string;
	environmentId?: string;
	errorClass?: string;
	truncated: boolean;
}

/**
 * Flattens the listings of several repositories, newest first, keeping
 * only the environment's locations when environmentId is set (the manager
 * scope belongs to none).
 */
export function snapshotRows(
	listings: { repository: { id: string; name: string }; locations: ResticLocation[] }[],
	environmentId: string | null = null
): { rows: SnapshotRow[]; problems: SnapshotProblem[] } {
	const rows: SnapshotRow[] = [];
	const problems: SnapshotProblem[] = [];
	for (const { repository: r, locations } of listings) {
		for (const l of locations) {
			if (environmentId && l.environmentId !== environmentId) continue;
			if (l.errorClass || l.truncated)
				problems.push({
					repositoryName: r.name,
					scope: l.scope,
					environmentId: l.environmentId,
					errorClass: l.errorClass,
					truncated: l.truncated
				});
			for (const s of l.snapshots)
				rows.push({
					key: `${r.id}/${l.scope}/${s.id}`,
					repositoryId: r.id,
					repositoryName: r.name,
					scope: l.scope,
					environmentId: l.environmentId,
					snapshot: s
				});
		}
	}
	rows.sort((a, b) => b.snapshot.time.localeCompare(a.snapshot.time));
	return { rows, problems };
}

// --- Policies, coverage and runs (#10) ---

/** "Daily at 03:00" → "daily at 03:00" (mid-sentence). */
function lowerFirst(s: string): string {
	return s ? s[0].toLowerCase() + s.slice(1) : s;
}

/** A schedule mid-sentence: "daily at 03:00", or "on its schedule" for shapes without words. */
export function scheduleWords(cron: string, timeZone?: string): string {
	const words = describeCron(cron, timeZone);
	return words === (cron ?? '').trim() ? 'on its schedule' : lowerFirst(words);
}

/**
 * The status sentence of a policy page: what, where and when, then how
 * the last run went. "Backs up all environments to B2 daily at 03:00.
 * Last run completed 17 hours ago."
 */
export function policySentence(
	p: Pick<
		BackupPolicy,
		'includeManagerState' | 'scope' | 'environmentId' | 'schedule' | 'recentSets'
	>,
	o: {
		repository?: string;
		environmentName?: (id: string) => string;
		running?: boolean;
		now?: Date;
	} = {}
): string {
	const when = p.schedule?.enabled
		? scheduleWords(p.schedule.cron, p.schedule.timeZone)
		: 'when you start it';
	const where = o.repository ? ` to ${o.repository}` : '';
	const what = `Backs up ${scopeText(p, o.environmentName)}${where} ${when}.`;
	const s = p.recentSets?.[0];
	if (o.running || s?.state === 'pending') return `${what} A backup is running now.`;
	if (!s) return `${what} It has not run yet.`;
	const ago = formatRelative(s.finishedAt ?? s.startedAt, o.now);
	if (s.state === 'skipped')
		return `${what} Last run ${ago} had nothing to back up: everything was removed before its turn.`;
	const how =
		s.state === 'complete' ? 'completed' : s.state === 'partial' ? 'partly failed' : 'failed';
	return `${what} Last run ${how} ${ago}.`;
}

/** The earliest next run of the given policies' enabled schedules. */
export function nextPolicyRun(policies: Pick<BackupPolicy, 'schedule'>[]): string | undefined {
	let next: string | undefined;
	for (const p of policies) {
		const at = p.schedule?.enabled ? p.schedule.nextRun : undefined;
		if (at && (!next || at < next)) next = at;
	}
	return next;
}

/** What a policy covers in a KPI: the value and one line below it. */
export function coverageSummary(
	p: Pick<
		BackupPolicy,
		| 'scope'
		| 'environmentId'
		| 'stacks'
		| 'volumes'
		| 'excludeStacks'
		| 'excludeVolumes'
		| 'includeManagerState'
	>,
	environmentName: (id: string) => string
): { value: string; secondary: string } {
	const stacks = (p.stacks ?? []).length;
	const volumes = (p.volumes ?? []).length;
	if (stacks || volumes) {
		const n = (k: number, one: string, many: string) => `${k} ${k === 1 ? one : many}`;
		return {
			value: [
				stacks ? n(stacks, 'stack', 'stacks') : '',
				volumes ? n(volumes, 'volume', 'volumes') : ''
			]
				.filter(Boolean)
				.join(', '),
			secondary: p.includeManagerState ? 'Manager state too' : 'Chosen one by one'
		};
	}
	const leftOut = (p.excludeStacks ?? []).length + (p.excludeVolumes ?? []).length;
	const value = p.scope === 'all' ? 'All Environments' : environmentName(p.environmentId ?? '');
	const secondary = p.includeManagerState
		? leftOut
			? `Manager state too; ${leftOut} left out`
			: 'Manager state too'
		: leftOut
			? `${leftOut} left out`
			: 'Every stack and volume';
	return { value, secondary };
}

/** A stack or volume whose backup coverage is asked for. */
export interface CoverageTarget {
	environmentId: string;
	stackId?: string;
	volume?: string;
}

/**
 * Whether a policy backs up the stack or volume: in scope (all
 * environments or its own) and not left out; a policy with an explicit
 * selection covers only what it lists.
 */
export function policyCovers(
	p: Pick<
		BackupPolicy,
		'scope' | 'environmentId' | 'stacks' | 'volumes' | 'excludeStacks' | 'excludeVolumes'
	>,
	t: CoverageTarget
): boolean {
	if (p.scope !== 'all' && p.environmentId !== t.environmentId) return false;
	const explicit = (p.stacks ?? []).length > 0 || (p.volumes ?? []).length > 0;
	if (t.stackId) {
		if (explicit) return (p.stacks ?? []).some((s) => s.stackId === t.stackId);
		return !(p.excludeStacks ?? []).includes(t.stackId);
	}
	if (t.volume) {
		if (explicit)
			return (p.volumes ?? []).some(
				(v) => v.volume === t.volume && v.environmentId === t.environmentId
			);
		return !(p.excludeVolumes ?? []).includes(
			volumeKey(p.scope === 'all', t.environmentId, t.volume)
		);
	}
	return false;
}

/** One run of a policy as it concerns one stack or volume. */
export interface MemberRun {
	setId: string;
	policyId: string;
	policyName: string;
	startedAt: string;
	member: SetMember;
}

/** The recent runs of the policies that included the stack or volume, newest first. */
export function memberRuns(policies: BackupPolicy[], t: CoverageTarget): MemberRun[] {
	const out: MemberRun[] = [];
	for (const p of policies)
		for (const s of p.recentSets ?? [])
			for (const m of s.members) {
				const match = t.stackId
					? m.kind === 'stack' && m.stackId === t.stackId
					: m.kind === 'volume' &&
						m.volume === t.volume &&
						(!m.environmentId || m.environmentId === t.environmentId);
				if (match)
					out.push({
						setId: s.id,
						policyId: p.id,
						policyName: p.name,
						startedAt: s.startedAt,
						member: m
					});
			}
	return out.sort((a, b) => b.startedAt.localeCompare(a.startedAt));
}

/** The backups of one run (a set), or a backup without a set on its own. */
export interface BackupRun {
	key: string;
	setId?: string;
	policyId?: string;
	/** The earliest snapshot time of its backups. */
	time: string;
	state: 'complete' | 'partial';
	/** Sum of the backups' sizes (undefined when none is known). */
	bytes?: number;
	/** By name, A to Z. */
	backups: Backup[];
}

/** Backups grouped by the run that took them, newest run first. */
export function groupBackupsByRun(backups: Backup[]): BackupRun[] {
	const runs = new Map<string, BackupRun>();
	for (const b of backups) {
		const key = b.setId ? `set:${b.setId}` : `backup:${b.id}`;
		let r = runs.get(key);
		if (!r) {
			r = {
				key,
				setId: b.setId,
				policyId: b.policyId,
				time: b.snapshotTime,
				state: 'complete',
				backups: []
			};
			runs.set(key, r);
		}
		r.backups.push(b);
		if (b.snapshotTime < r.time) r.time = b.snapshotTime;
		if (b.state !== 'complete') r.state = 'partial';
		if (b.bytes !== undefined) r.bytes = (r.bytes ?? 0) + b.bytes;
	}
	const out = [...runs.values()];
	for (const r of out) r.backups.sort((a, b) => itemName(a).localeCompare(itemName(b)));
	return out.sort((a, b) => b.time.localeCompare(a.time));
}

// --- Repositories and verification (#10) ---

/** How much a verification reads. */
export const VERIFY_READ_OPTIONS: { value: string; label: string; description: string }[] = [
	{
		value: '',
		label: 'Check Structure Only',
		description: 'Quick: checks the index and every backup.'
	},
	{
		value: '5%',
		label: 'Also Read 5% of the Data',
		description: 'A different part each time.'
	},
	{
		value: '100%',
		label: 'Read All Data',
		description: 'Reads everything stored. Slow, and S3 providers may charge for downloads.'
	}
];

/** The options, plus a saved amount that is none of them (kept as it is). */
export function verifyReadOptions(current: string | undefined): typeof VERIFY_READ_OPTIONS {
	const c = (current ?? '').trim();
	if (VERIFY_READ_OPTIONS.some((o) => o.value === c)) return VERIFY_READ_OPTIONS;
	return [
		...VERIFY_READ_OPTIONS,
		{ value: c, label: `Also Read ${c} of the Data`, description: 'The amount saved earlier.' }
	];
}

/** "checks the structure only", "also reads 5% of the data", "reads all data". */
export function verifyReadText(subset: string | undefined): string {
	const s = (subset ?? '').trim();
	if (!s) return 'checks the structure only';
	if (s === '100%') return 'reads all data';
	return `also reads ${s} of the data`;
}

/** The verification schedule in one sentence. */
export function verificationText(v: Schema<'BackupVerification'> | undefined): string {
	if (!v?.enabled) return 'Verification is off. Run Verify on one of its backups.';
	return `Verifies ${scheduleWords(v.cron, v.timeZone)} and ${verifyReadText(v.readDataSubset)}.`;
}

/** "Healthy · 42 GB · last backup 3 hours ago · verified yesterday". */
export function repositoryStatusLine(
	h: Pick<RepositoryHealth, 'healthy' | 'lastBackupAt' | 'lastVerifiedAt'>,
	storedBytes: number | undefined,
	now: Date = new Date()
): string {
	return [
		h.healthy ? 'Healthy' : 'Needs attention',
		storedBytes ? formatBytes(storedBytes) : undefined,
		h.lastBackupAt ? `last backup ${formatRelative(h.lastBackupAt, now)}` : 'no backup yet',
		h.lastVerifiedAt ? `verified ${formatRelative(h.lastVerifiedAt, now)}` : 'never verified'
	]
		.filter(Boolean)
		.join(' · ');
}

/** "Last checked yesterday: working". */
export function connectionTestText(t: Pick<ConnectionTest, 'at' | 'ok'>, now = new Date()): string {
	return `Last checked ${formatRelative(t.at, now)}: ${t.ok ? 'working' : 'failed'}`;
}

/** What a location holds: "Manager State", "Environment prod". */
export function scopeName(scope: string, environmentName: (id: string) => string): string {
	if (scope === 'manager' || scope === 'docker-manager') return 'Manager State';
	const m = scope.match(/^(?:env:|docker-manager-env-)(.+)$/);
	return m ? `Environment ${environmentName(m[1])}` : scope;
}

/** A restore target by name ("Volume shop_db"); its host path is for the tooltip. */
export function restoreTargetName(t: { kind: string; name?: string; path: string }): string {
	const base = t.path.replace(/\/+$/, '').split('/').pop() || t.path;
	if (t.kind === 'project') return t.name ? `Files of Stack ${t.name}` : 'Stack Files';
	if (t.kind === 'volume') return `Volume ${t.name || base}`;
	if (t.kind === 'file') return `File ${base}`;
	return t.name || base;
}

// --- Repository compression (#10) ---

export type CompressionMode = 'auto' | 'max' | 'off';

/** How a repository compresses what it stores (a choice, in plain words). */
export const COMPRESSION_OPTIONS: { value: CompressionMode; label: string; description: string }[] =
	[
		{
			value: 'auto',
			label: 'Automatic',
			description: 'Recommended.'
		},
		{
			value: 'max',
			label: 'Maximum',
			description: 'Smallest backups, more CPU.'
		},
		{
			value: 'off',
			label: 'Off',
			description: 'For already compressed data, such as videos or archives.'
		}
	];

/** Shown with the choice when editing: what a change affects. */
export const COMPRESSION_CHANGE_NOTE = 'Applies to new backups only.';

/** Shown with the choice when adding a repository. */
export const COMPRESSION_NEW_NOTE = 'You can change it later.';

/** "Automatic", "Maximum", "Off" (a missing mode is Automatic). */
export function compressionText(mode: string | undefined): string {
	return COMPRESSION_OPTIONS.find((o) => o.value === mode)?.label ?? COMPRESSION_OPTIONS[0].label;
}
