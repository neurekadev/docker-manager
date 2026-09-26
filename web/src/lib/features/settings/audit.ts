// Audit log viewer (#30): presentation helpers. Pure (settings.spec.ts).
import type { BadgeTone } from '$lib/ui/Badge.svelte';
import type { AuditEvent } from './queries';

export const OUTCOME: Record<AuditEvent['outcome'], { tone: BadgeTone; label: string }> = {
	success: { tone: 'ok', label: 'Succeeded' },
	partial: { tone: 'warn', label: 'Partly failed' },
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
	api_token: 'API token',
	service: 'Docker Manager (scheduled)',
	agent: 'Agent',
	anonymous: 'Not signed in'
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
