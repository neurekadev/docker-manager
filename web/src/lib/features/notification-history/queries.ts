// Notifications (finished backups, restores, prunes and update runs)
// for Svelte Query: GET /notifications page by page (the Notifications
// tab's "Load more"), and those of the last BELL_WINDOW_MS the In App
// channel shows (the bell). Keys are liveKeys.notifications(...): the manager
// publishes the topic `alerts` with the kind `notification` whenever it
// records one, and the live client refreshes every notifications list.
// Notifications are a history: nothing changes them, so there are no
// mutations.
import { infiniteQueryOptions, queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import { BELL_LIMIT, BELL_WINDOW_MS } from '$lib/features/alerts/queries';
import { liveKeys } from '$lib/live/keys';
import type { Notification, NotificationKind, NotificationOutcome } from './model';

/** The server-side filters of GET /notifications (absent: every notification). */
export interface NotificationFilter {
	kind?: NotificationKind;
	outcome?: NotificationOutcome;
	environmentId?: string;
}

/** The filter without empty values (one key per distinct filter). */
export function normalizeNotificationFilter(f: NotificationFilter): NotificationFilter {
	const out: NotificationFilter = {};
	if (f.kind) out.kind = f.kind;
	if (f.outcome) out.outcome = f.outcome;
	if (f.environmentId) out.environmentId = f.environmentId;
	return out;
}

export const notificationKeys = {
	/** Every notifications query (what a new notification refreshes). */
	all: liveKeys.notifications(),
	pages: (f: NotificationFilter = {}) =>
		liveKeys.notifications('pages', normalizeNotificationFilter(f)),
	inApp: liveKeys.notifications('in-app')
};

/** The newest finished runs of the last BELL_WINDOW_MS that the In App channel shows (the bell). */
export function inAppRunsQuery(client: ApiClient = api, now: () => number = Date.now) {
	return queryOptions({
		queryKey: notificationKeys.inApp,
		queryFn: async ({ signal }): Promise<Notification[]> => {
			const since = new Date(now() - BELL_WINDOW_MS).toISOString();
			const page = await unwrap(
				client.GET('/api/v1/notifications', {
					params: { query: { inApp: true, since, limit: BELL_LIMIT } },
					signal
				})
			);
			return page.items;
		},
		staleTime: 15_000
	});
}

/** Notifications matching the filter, newest first, a page at a time. */
export function notificationsInfiniteQuery(
	f: NotificationFilter = {},
	limit = 50,
	client: ApiClient = api
) {
	const filter = normalizeNotificationFilter(f);
	return infiniteQueryOptions({
		queryKey: notificationKeys.pages(filter),
		queryFn: ({ signal, pageParam }) =>
			unwrap(
				client.GET('/api/v1/notifications', {
					params: { query: { ...filter, limit, cursor: pageParam } },
					signal
				})
			),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: (last) => last.nextCursor || undefined,
		staleTime: 15_000
	});
}
