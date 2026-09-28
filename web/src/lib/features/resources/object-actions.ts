// Removal requests of images, volumes and networks (#6): each answers 202
// with a job. Shared by the single-object dialogs (ImageActionHost,
// ObjectRemoveHost) and the lists' bulk removal.
import { api, unwrap, type ApiClient, type Job } from '$lib/api/client';
import { idempotencyKey } from './jobs.svelte';

/** Removes an image; `force` untags an image with several tags. */
export function removeImage(
	env: string,
	id: string,
	opts: { force?: boolean } = {},
	client: ApiClient = api
): Promise<Job> {
	return unwrap(
		client.DELETE('/api/v1/environments/{environmentId}/images/{imageId}', {
			params: {
				path: { environmentId: env, imageId: id },
				header: { 'Idempotency-Key': idempotencyKey() },
				query: { force: opts.force || undefined }
			}
		})
	);
}

/** Removes a volume and its data. */
export function removeVolume(env: string, name: string, client: ApiClient = api): Promise<Job> {
	return unwrap(
		client.DELETE('/api/v1/environments/{environmentId}/volumes/{volumeId}', {
			params: {
				path: { environmentId: env, volumeId: name },
				header: { 'Idempotency-Key': idempotencyKey() }
			}
		})
	);
}

/** Removes a network. */
export function removeNetwork(env: string, name: string, client: ApiClient = api): Promise<Job> {
	return unwrap(
		client.DELETE('/api/v1/environments/{environmentId}/networks/{networkId}', {
			params: {
				path: { environmentId: env, networkId: name },
				header: { 'Idempotency-Key': idempotencyKey() }
			}
		})
	);
}
