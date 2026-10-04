// Alerts (#159) for Svelte Query: the list with its filters (the Alerts
// page, the environment page), the active alerts (the dashboard), those
// the In App channel shows (the bell: active, and resolved in the last
// BELL_WINDOW_MS), the dismissals and the alert thresholds (Settings →
// Notifications, owner only). Keys are liveKeys.alerts(filter): the
// manager publishes the topic `alerts` whenever an alert is raised,
// changes, is dismissed or resolved, and the live client refreshes every
// alerts list by prefix. Mutations invalidate alertKeys.all and never
// retry. The thresholds have no live event: saving them sets the query's
// data from the response.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { allPages } from '$lib/api/multi-env';
import { ifMatch } from '$lib/features/common/data';
import { liveKeys } from '$lib/live/keys';
import type { Alert, AlertKind } from './model';
import type { AlertSettings, AlertSettingsBody } from './thresholds';

export type AlertDismissals = Schema<'AlertDismissals'>;

/** The server-side filters of GET /alerts (absent: every alert). */
export interface AlertFilter {
	state?: 'active' | 'dismissed' | 'firing' | 'resolved';
	kind?: AlertKind;
	environmentId?: string;
	/** Only what the In App channel shows (the bell). */
	inApp?: boolean;
}

/** The filter without empty values (one key per distinct filter). */
export function normalizeFilter(f: AlertFilter): AlertFilter {
	const out: AlertFilter = {};
	if (f.state) out.state = f.state;
	if (f.kind) out.kind = f.kind;
	if (f.environmentId) out.environmentId = f.environmentId;
	if (f.inApp) out.inApp = true;
	return out;
}

export const alertKeys = {
	/** Every alerts query (the prefix mutations invalidate). */
	all: ['alerts'] as const,
	list: (f: AlertFilter = {}) => liveKeys.alerts(normalizeFilter(f))
};

/** Most alerts one "Dismiss all" request takes (the manager's limit). */
export const MAX_DISMISSALS = 500;

/** Every alert matching the filter, newest first (all pages). */
export function alertsQuery(f: AlertFilter = {}, client: ApiClient = api) {
	const filter = normalizeFilter(f);
	return queryOptions({
		queryKey: alertKeys.list(filter),
		queryFn: ({ signal }): Promise<Alert[]> =>
			allPages((cursor) =>
				unwrap(
					client.GET('/api/v1/alerts', {
						params: { query: { ...filter, limit: 200, cursor } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

/** Firing alerts nobody dismissed: "Needs attention". */
export function activeAlertsQuery(client: ApiClient = api) {
	return alertsQuery({ state: 'active' }, client);
}

/** How far back the bell lists resolved alerts and finished runs. */
export const BELL_WINDOW_MS = 7 * 24 * 60 * 60 * 1000;
/** The most resolved alerts, and finished runs, the bell lists (the newest). */
export const BELL_LIMIT = 50;

/** Firing alerts nobody dismissed that the In App channel shows: the bell. */
export function inAppAlertsQuery(client: ApiClient = api) {
	return alertsQuery({ state: 'active', inApp: true }, client);
}

/** Alerts resolved in the last BELL_WINDOW_MS that the In App channel shows (the bell). */
export function inAppResolvedQuery(client: ApiClient = api, now: () => number = Date.now) {
	return queryOptions({
		queryKey: liveKeys.alerts('in-app', 'resolved'),
		queryFn: async ({ signal }): Promise<Alert[]> => {
			const resolvedSince = new Date(now() - BELL_WINDOW_MS).toISOString();
			const page = await unwrap(
				client.GET('/api/v1/alerts', {
					params: {
						query: { state: 'resolved', inApp: true, resolvedSince, limit: BELL_LIMIT }
					},
					signal
				})
			);
			return page.items;
		},
		staleTime: 15_000
	});
}

/** Dismisses one firing alert for everyone. */
export function dismissAlert(id: string, client: ApiClient = api): Promise<Alert> {
	return unwrap(
		client.POST('/api/v1/alerts/{alertId}/dismissals', { params: { path: { alertId: id } } })
	);
}

/**
 * Dismisses the listed alerts the caller may dismiss (the manager leaves
 * the others alone), in requests of at most MAX_DISMISSALS. Returns how
 * many are dismissed now.
 */
export async function dismissAlerts(
	ids: readonly string[],
	client: ApiClient = api
): Promise<number> {
	let dismissed = 0;
	for (let i = 0; i < ids.length; i += MAX_DISMISSALS) {
		const r = await unwrap(
			client.POST('/api/v1/alerts/dismissals', {
				body: { alertIds: ids.slice(i, i + MAX_DISMISSALS) }
			})
		);
		dismissed += r.dismissed;
	}
	return dismissed;
}

/** The key of the alert thresholds (no live event refreshes it). */
export const alertSettingsKey = liveKeys.item('settings', 'alert-settings');

/** The alert thresholds and every environment's override (owner only). */
export function alertSettingsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: alertSettingsKey,
		queryFn: ({ signal }): Promise<AlertSettings> =>
			unwrap(client.GET('/api/v1/alert-settings', { signal })),
		staleTime: 30_000
	});
}

/** Replaces the thresholds and every override (If-Match: the revision it was edited from). */
export function saveAlertSettings(
	current: Pick<AlertSettings, 'revision'>,
	body: AlertSettingsBody,
	client: ApiClient = api
): Promise<AlertSettings> {
	return unwrap(
		client.PUT('/api/v1/alert-settings', {
			params: { header: { 'If-Match': ifMatch(current.revision) } },
			body
		})
	);
}
