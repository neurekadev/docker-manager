// Notifications (finished backups, restores, prunes and update runs)
// for Svelte Query: GET /notifications page by page (the Notifications
// tab's "Load more"). Keys are liveKeys.notifications(...): the manager
// publishes the topic `alerts` with the kind `notification` whenever it
// records one, and the live client refreshes every notifications list.
// Notifications are a history: nothing changes them, so there are no
// mutations.
import { infiniteQueryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import type { NotificationKind, NotificationOutcome } from './model';

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
		liveKeys.notifications('pages', normalizeNotificationFilter(f))
};

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
