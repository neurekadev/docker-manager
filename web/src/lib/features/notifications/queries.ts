// Notification channels (#142) for Svelte Query (live topic 'settings':
// every change is announced as a notification_channel change). The
// address is never part of a channel; revealAddress reads it (owner, a
// recent step-up, audited) and changes that set one need a step-up too.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, unwrapEmpty, type ApiClient, type Schema } from '$lib/api/client';
import { allPages } from '$lib/api/multi-env';
import { liveKeys } from '$lib/live/keys';
import { ifMatch } from '$lib/features/common/data';
import type { NotificationChannel, NotificationTest } from './model';

export type ChannelInput = Schema<'CreateNotificationChannelInputBody'>;
export type ChannelPatch = Schema<'UpdateNotificationChannelInputBody'>;

export const notificationKeys = {
	all: liveKeys.list('settings', 'notification-channels'),
	item: (id: string) => liveKeys.item('settings', id)
};

/** Every notification channel (owner only). */
export function notificationChannelsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: notificationKeys.all,
		queryFn: ({ signal }): Promise<NotificationChannel[]> =>
			allPages((cursor) =>
				unwrap(
					client.GET('/api/v1/notification-channels', {
						params: { query: { limit: 200, cursor } },
						signal
					})
				)
			),
		staleTime: 15_000
	});
}

export function createChannel(
	body: ChannelInput,
	client: ApiClient = api
): Promise<NotificationChannel> {
	return unwrap(client.POST('/api/v1/notification-channels', { body }));
}

export function updateChannel(
	c: Pick<NotificationChannel, 'id' | 'revision'>,
	body: ChannelPatch,
	client: ApiClient = api
): Promise<NotificationChannel> {
	return unwrap(
		client.PATCH('/api/v1/notification-channels/{channelId}', {
			params: { path: { channelId: c.id }, header: { 'If-Match': ifMatch(c.revision) } },
			body
		})
	);
}

export function deleteChannel(
	c: Pick<NotificationChannel, 'id' | 'revision'>,
	client: ApiClient = api
): Promise<void> {
	return unwrapEmpty(
		client.DELETE('/api/v1/notification-channels/{channelId}', {
			params: { path: { channelId: c.id }, header: { 'If-Match': ifMatch(c.revision) } }
		})
	);
}

/** The channel's stored address (owner, recent step-up; every reveal is audited). */
export async function revealAddress(id: string, client: ApiClient = api): Promise<string> {
	const r = await unwrap(
		client.GET('/api/v1/notification-channels/{channelId}/address', {
			params: { path: { channelId: id } }
		})
	);
	return r.address;
}

/** Sends a test message now (at most one per channel every 5 seconds). */
export function sendTest(id: string, client: ApiClient = api): Promise<NotificationTest> {
	return unwrap(
		client.POST('/api/v1/notification-channels/{channelId}/tests', {
			params: { path: { channelId: id } }
		})
	);
}
