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

/** What a policy backs up: "the manager state, 2 stacks and 1 volume". */
export function scopeText(p: Pick<BackupPolicy, 'includeManagerState' | 'scope'>): string {
	const scope = p.scope === 'all' ? 'all environments' : 'one environment';
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
