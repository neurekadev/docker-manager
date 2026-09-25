// Reads of the resources the files, logs and terminal views hang off (a
// stack, its service containers, a container, a volume), keyed with the live
// conventions (#23) so their events refresh them. The stack and container
// detail pages (#22 B2/B4) key the same resources identically, so the cache
// is shared.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';

export type Stack = Schema<'Stack'>;
export type StackServices = Schema<'StackServices'>;
export type Container = Schema<'Container'>;
export type Volume = Schema<'Volume'>;

export function stackQuery(stackId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.item('stacks', stackId),
		queryFn: ({ signal }): Promise<Stack> =>
			unwrap(
				client.GET('/api/v1/stacks/{stackId}', { params: { path: { stackId } }, signal })
			),
		staleTime: 10_000
	});
}

/** The stack's services and their containers (live Engine state). */
export function stackServicesQuery(stackId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.stackServices(stackId),
		queryFn: ({ signal }): Promise<StackServices> =>
			unwrap(
				client.GET('/api/v1/stacks/{stackId}/services', {
					params: { path: { stackId } },
					signal
				})
			),
		staleTime: 10_000
	});
}

export function containerQuery(
	environmentId: string,
	containerId: string,
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: liveKeys.item('containers', environmentId, containerId),
		queryFn: ({ signal }): Promise<Container> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/containers/{containerId}', {
					params: { path: { environmentId, containerId } },
					signal
				})
			),
		staleTime: 10_000
	});
}

export function volumeQuery(environmentId: string, volumeId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.item('volumes', environmentId, volumeId),
		queryFn: ({ signal }): Promise<Volume> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/volumes/{volumeId}', {
					params: { path: { environmentId, volumeId } },
					signal
				})
			),
		staleTime: 10_000
	});
}
