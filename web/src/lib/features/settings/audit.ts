// Audit log viewer (#30): presentation helpers. Pure (settings.spec.ts).
import type { BadgeTone } from '$lib/ui/Badge.svelte';
import { titleCase } from '$lib/ui/format';
import type { AuditEvent, AuditFilter } from './queries';

export const OUTCOME: Record<AuditEvent['outcome'], { tone: BadgeTone; label: string }> = {
	success: { tone: 'ok', label: 'Succeeded' },
	partial: { tone: 'warn', label: 'Partly Failed' },
	failure: { tone: 'danger', label: 'Failed' },
	denied: { tone: 'danger', label: 'Denied' },
	error: { tone: 'danger', label: 'Error' }
};

export const CATEGORY_LABELS: Record<AuditEvent['category'], string> = {
	identity: 'Identity',
	authorization: 'Authorization',
	credentials: 'Credentials',
	operations: 'Operations',
	system: 'System'
};

export const ACTOR_LABELS: Record<AuditEvent['actor']['kind'], string> = {
	user: 'User',
	api_token: 'API Token',
	service: 'Docker Manager (Scheduled)',
	agent: 'Agent',
	anonymous: 'Not Signed In'
};

export interface DiffRow {
	field: string;
	before?: string;
	after?: string;
	/** For lists (rules, names): what was added and removed. */
	added?: string[];
	removed?: string[];
}

function text(v: unknown): string | undefined {
	if (v === undefined || v === null) return undefined;
	if (typeof v === 'string') return v;
	if (typeof v === 'number' || typeof v === 'boolean') return String(v);
	return JSON.stringify(v);
}

/**
 * The before/after of an edit (details.diff, audit.SetDiff) as rows: one
 * per changed field; lists (e.g. permission rules) as added/removed items.
 */
export function diffRows(details: Record<string, unknown> | undefined): DiffRow[] {
	const diff = details?.diff as { before?: unknown; after?: unknown } | undefined;
	if (!diff) return [];
	const b = (diff.before && typeof diff.before === 'object' ? diff.before : {}) as Record<
		string,
		unknown
	>;
	const a = (diff.after && typeof diff.after === 'object' ? diff.after : {}) as Record<
		string,
		unknown
	>;
	const keys = [...new Set([...Object.keys(b), ...Object.keys(a)])].sort();
	const rows: DiffRow[] = [];
	for (const k of keys) {
		const x = b[k];
		const y = a[k];
		if (Array.isArray(x) || Array.isArray(y)) {
			const xs = (Array.isArray(x) ? x : []).map((v) => text(v) ?? '');
			const ys = (Array.isArray(y) ? y : []).map((v) => text(v) ?? '');
			const added = ys.filter((v) => !xs.includes(v));
			const removed = xs.filter((v) => !ys.includes(v));
			if (added.length || removed.length) rows.push({ field: k, added, removed });
			continue;
		}
		if (text(x) === text(y)) continue;
		rows.push({ field: k, before: text(x), after: text(y) });
	}
	return rows;
}

/** Details other than the diff, as label/value pairs. */
export function detailPairs(details: Record<string, unknown> | undefined): [string, string][] {
	return Object.entries(details ?? {})
		.filter(([k]) => k !== 'diff')
		.map(([k, v]) => [k, text(v) ?? '—'] as [string, string]);
}

/** A local datetime-input value → RFC 3339 (empty stays empty). */
export function localToRFC3339(v: string): string | undefined {
	if (!v) return undefined;
	const d = new Date(v);
	return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

// Readable records: action labels, target names, filters and grouping.

/**
 * Actions that are not catalog capabilities (sign-ins, job lifecycle,
 * credential use, operations guarded by the owner or any signed-in user),
 * in words. Capability keys use the catalog's label; anything else falls
 * back to its key in words.
 */
export const AUDIT_ACTION_LABELS: Record<string, string> = {
	'job.queued': 'Queued a job',
	'job.started': 'Job started',
	'job.cancel_requested': 'Asked to cancel a job',
	'job.finished': 'Job finished',
	'audit.purge': 'Removed old audit records',
	'auth.sign_in': 'Signed in',
	'auth.sign_out': 'Signed out',
	'auth.step_up': 'Confirmed their identity',
	'auth.lockout': 'Locked out after failed sign-ins',
	'auth.totp_enable': 'Set up an authenticator app',
	'auth.totp_disable': 'Turned off the authenticator app',
	'auth.passkey_add': 'Added a passkey',
	'auth.passkey_remove': 'Removed a passkey',
	'auth.password_change': 'Changed their password',
	'auth.password_reset_redeem': 'Used a password reset link',
	'auth.recovery_code_use': 'Used a recovery code',
	'auth.recovery_codes_rotate': 'Generated new recovery codes',
	'auth.enrollment_complete': 'Finished setting up sign-in',
	'setup.owner_create': 'Created the owner account',
	'owner.recovery_issue': 'Started owner recovery',
	'invitation.create': 'Invited a user',
	'invitation.redeem': 'Joined with an invitation',
	'invitation.revoke': 'Revoked an invitation',
	'user.update': 'Changed an account',
	'user.group_change': 'Changed the groups of a user',
	'user.delete': 'Deleted an account',
	'user.factor_reset': 'Reset sign-in factors',
	'user.password_reset_issue': 'Created a password reset link',
	'user.session_revoke': 'Signed a user out everywhere',
	'group_permissions.replace': 'Changed group permissions',
	'group_order.replace': 'Reordered the groups',
	'settings.security_update': 'Changed the sign-in policy',
	'api_token.create': 'Created an API token',
	'api_token.rename': 'Renamed an API token',
	'api_token.revoke': 'Revoked an API token',
	'api_token.revoke_all': 'Revoked all API tokens',
	'agent.enroll': 'Connected an agent',
	'registry.use': 'Used a registry connection',
	'registry.create': 'Added a registry connection',
	'registry.update': 'Changed a registry connection',
	'registry.delete': 'Removed a registry connection',
	'registry.match': 'Looked up the registry of an image',
	'git_credential.use': 'Used a Git credential',
	'git_credential.create': 'Added a Git credential',
	'git_credential.update': 'Changed a Git credential',
	'git_credential.delete': 'Removed a Git credential',
	'container.exec.end': 'Closed a terminal',
	'notification_channel.create': 'Added a notification channel',
	'notification_channel.update': 'Changed a notification channel',
	'notification_channel.delete': 'Deleted a notification channel',
	'notification_channel.reveal': 'Viewed the address of a notification channel',
	'notification_channel.test': 'Sent a test message',
	'stack.export.preview': 'Check Stack Export',
	'stack.import_archive.preview': 'Check Stack From Archive'
};

interface LabelCatalog {
	capabilities: { key: string; label: string; resourceType: string }[];
	resourceTypes: { key: string; label: string }[];
}

/** An audit action in words ("Signed in", "Deploy Stacks", "Stack migrate preview"). */
export function auditActionLabel(action: string, catalog?: LabelCatalog): string {
	const known = AUDIT_ACTION_LABELS[action];
	if (known) return known;
	// Catalog labels name what they act on ("Restart Containers", #281).
	const c = catalog?.capabilities.find((x) => x.key === action);
	return c ? c.label : sentence(action);
}

/** "stack.migrate.preview" → "Stack migrate preview". */
function sentence(key: string): string {
	const words = key.replace(/[._]+/g, ' ').trim();
	return words ? words[0].toUpperCase() + words.slice(1) : key;
}

/** Resource types of audit targets, in words. */
export const TARGET_TYPES: Record<string, string> = {
	stack: 'Stack',
	service: 'Stack Service',
	container: 'Container',
	image: 'Image',
	volume: 'Volume',
	network: 'Network',
	environment: 'Environment',
	agent: 'Agent',
	job: 'Job',
	user: 'User',
	group: 'Group',
	api_token: 'API Token',
	passkey: 'Passkey',
	invitation: 'Invitation',
	registry: 'Registry Connection',
	git_credential: 'Git Credential',
	backup: 'Backup',
	backup_repository: 'Backup Repository',
	backup_policy: 'Backups',
	update_policy: 'Updates',
	maintenance_policy: 'Maintenance',
	build_definition: 'Build Definition',
	template: 'Stack Template',
	schedule: 'Schedule',
	settings: 'Settings',
	notification_channel: 'Notification Channel'
};

/** An ID nobody reads: a UUID, a long hex string or a digest. */
export function isOpaqueId(id: string): boolean {
	return (
		/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id) ||
		/^(sha256:)?[0-9a-f]{12,}$/i.test(id)
	);
}

/**
 * A target as people know it: its name when `nameOf` knows it (users,
 * stacks, registries, …), a readable ID (container and volume names),
 * else only its type. Opaque IDs never become the name.
 */
export function targetText(
	t: { type: string; id: string },
	nameOf: (type: string, id: string) => string | undefined = () => undefined
): { name: string; type: string } {
	const type = TARGET_TYPES[t.type] ?? titleCase(t.type.replace(/[._]+/g, ' ').trim());
	const name = nameOf(t.type, t.id) ?? (!t.id || isOpaqueId(t.id) ? undefined : t.id);
	return { name: name ?? type, type };
}

type ActorKind = AuditEvent['actor']['kind'];

/**
 * The "Who" filter's value: "kind:<actor kind>" or, for the owner who can
 * list users, "user:<id>" (which also matches that user's API tokens).
 */
export function parseWho(value: string): Pick<AuditFilter, 'actorKind' | 'actorId'> {
	const at = value.indexOf(':');
	if (at < 0) return {};
	const kind = value.slice(0, at);
	const id = value.slice(at + 1);
	if (kind === 'user' && id) return { actorId: id };
	if (kind === 'kind' && id in ACTOR_LABELS) return { actorKind: [id as ActorKind] };
	return {};
}

/** The time ranges of the "When" filter. */
export const RANGES: { value: string; label: string; hours: number }[] = [
	{ value: '1h', label: 'Last Hour', hours: 1 },
	{ value: '24h', label: 'Last 24 Hours', hours: 24 },
	{ value: '7d', label: 'Last 7 Days', hours: 24 * 7 },
	{ value: '30d', label: 'Last 30 Days', hours: 24 * 30 }
];

/** The start of a range as RFC 3339 (undefined: any time). */
export function rangeSince(value: string, now: number): string | undefined {
	const r = RANGES.find((x) => x.value === value);
	return r ? new Date(now - r.hours * 3600_000).toISOString() : undefined;
}

/** Records in a row that say the same thing (same action, actor, targets and outcome). */
export interface AuditRow {
	key: string;
	/** Newest first, like the list. */
	events: AuditEvent[];
}

function sameness(e: AuditEvent): string {
	return [
		e.action,
		e.actor.kind,
		e.actor.userId ?? '',
		e.actor.tokenId ?? '',
		e.actor.agentId ?? '',
		e.outcome,
		e.environmentId ?? '',
		e.targets.map((t) => `${t.type}:${t.id}`).join(',')
	].join('|');
}

/**
 * Groups runs of identical records that follow each other (dozens of
 * "Used a registry connection" in a row become one row, "24 times").
 */
export function groupAuditRows(events: readonly AuditEvent[]): AuditRow[] {
	const out: AuditRow[] = [];
	let last = '';
	for (const e of events) {
		const k = sameness(e);
		const prev = out.at(-1);
		if (prev && k === last) prev.events.push(e);
		else out.push({ key: e.id, events: [e] });
		last = k;
	}
	return out;
}
