// Settings (#16, #13, #30, #34) for Svelte Query (live topic 'settings').
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import { ifMatch } from '$lib/features/common/data';

export type SecuritySettings = Schema<'SecuritySettings'>;
export type InstanceSettings = Schema<'InstanceSettings'>;
export type AuditEvent = Schema<'AuditEvent'>;
export type Passkey = Schema<'Passkey'>;

export const settingsKeys = {
	// Live topic 'settings', resource 'instance' (PATCH /settings announces it).
	instance: liveKeys.item('settings', 'instance'),
	security: liveKeys.item('settings', 'security'),
	passkeys: liveKeys.item('permissions', 'me', 'passkeys'),
	recoveryCodes: liveKeys.item('permissions', 'me', 'recovery-codes'),
	audit: (filter: AuditFilter) => liveKeys.list('settings', 'audit', filter)
};

/** The instance settings (settings.read): display name and deployment summary. */
export function instanceSettingsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: settingsKeys.instance,
		queryFn: ({ signal }): Promise<InstanceSettings> =>
			unwrap(client.GET('/api/v1/settings', { signal })),
		retry: false
	});
}

/** Renames the instance (settings.manage) at the revision the form loaded. */
export function saveInstanceName(
	current: InstanceSettings,
	name: string,
	client: ApiClient = api
): Promise<InstanceSettings> {
	return unwrap(
		client.PATCH('/api/v1/settings', {
			params: { header: { 'If-Match': ifMatch(current.revision) } },
			body: { name }
		})
	);
}

export function securitySettingsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: settingsKeys.security,
		queryFn: ({ signal }): Promise<SecuritySettings> =>
			unwrap(client.GET('/api/v1/settings/security', { signal })),
		retry: false
	});
}

export function passkeysQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: settingsKeys.passkeys,
		queryFn: async ({ signal }): Promise<Passkey[]> =>
			(await unwrap(client.GET('/api/v1/me/passkeys', { signal }))).items
	});
}

export function recoveryCodesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: settingsKeys.recoveryCodes,
		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/me/recovery-codes', { signal }))
	});
}

export interface AuditFilter {
	actorKind?: AuditEvent['actor']['kind'][];
	actorId?: string;
	action?: string[];
	category?: AuditEvent['category'][];
	outcome?: AuditEvent['outcome'][];
	environmentId?: string;
	resource?: string;
	since?: string;
	until?: string;
}

/** Only the filter fields that are set (stable query keys and URLs). */
export function cleanFilter(f: AuditFilter): AuditFilter {
	const out: Record<string, unknown> = {};
	for (const [k, v] of Object.entries(f)) {
		if (v === undefined || v === '' || (Array.isArray(v) && v.length === 0)) continue;
		out[k] = v;
	}
	return out as AuditFilter;
}

/** Query string of the export link (repeated parameters for lists). */
export function auditExportHref(f: AuditFilter, format: 'ndjson' | 'csv'): string {
	const q = new URLSearchParams({ format });
	for (const [k, v] of Object.entries(cleanFilter(f))) {
		if (Array.isArray(v)) for (const x of v) q.append(k, String(x));
		else q.set(k, String(v));
	}
	return `/api/v1/audit/exports?${q.toString()}`;
}

/** One page of audit records (newest first). */
export async function auditPage(
	f: AuditFilter,
	cursor: string | undefined,
	signal?: AbortSignal,
	client: ApiClient = api
) {
	return unwrap(
		client.GET('/api/v1/audit', {
			params: { query: { ...cleanFilter(f), cursor, limit: 50 } },
			signal
		})
	);
}
