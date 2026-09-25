// $lib/live: live synchronization for the UI (#23). See docs/web.md
// ("Live data") for the query-key conventions views must follow.
import type { QueryClient } from '@tanstack/svelte-query';
import { api, unwrap } from '$lib/api/client';
import { LiveClient } from './client';
import { liveKeys } from './keys';

export { LiveClient, LIVE_URL, type LiveScopes } from './client';
export { liveKeys, type FileScopeRef, type Topic } from './keys';
export { liveStatus, LiveStatus, type LiveState } from './status.svelte';
export { criticalWork, CriticalWork, type CriticalKind } from './critical.svelte';

let current: LiveClient | null = null;

/** The tab's live client (null before startLive). */
export function liveClient(): LiveClient | null {
	return current;
}

/**
 * Starts the tab's live client (called once from the root layout) and
 * returns its cleanup. Views declare open file browsers with
 * `liveClient()?.setScopes({ stackIds, volumes })`.
 */
export function startLive(queryClient: QueryClient): () => void {
	const client = new LiveClient({
		queryClient,
		onPermissionsChanged: () =>
			queryClient.prefetchQuery({
				queryKey: liveKeys.myPermissions,
				queryFn: ({ signal }) => unwrap(api.GET('/api/v1/me/permissions', { signal }))
			})
	});
	current = client;
	client.start();
	const online = () => client.reconnectNow();
	window.addEventListener('online', online);
	return () => {
		window.removeEventListener('online', online);
		client.stop();
		if (current === client) current = null;
	};
}
