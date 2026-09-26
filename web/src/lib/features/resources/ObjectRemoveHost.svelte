<script lang="ts">
	// Removes a volume or a network (#6) after the server's removal preview
	// (in use, managed stack, predefined network, Docker Manager's own: #32). A
	// removal that is accepted runs as a job; its outcome is a toast.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, type Schema } from '$lib/api/client';
	import { networkQuery, queryKeys, volumeQuery } from '$lib/api/queries';
	import RemovalDialog from './RemovalDialog.svelte';
	import { idempotencyKey, resourceKey, trackJob } from './jobs.svelte';
	import { refusal, RefusalError, type Protection } from './refusals';

	interface Target {
		kind: 'volume' | 'network';
		environmentId: string;
		name: string;
		protection?: Protection;
		usedBy?: { name: string; state?: string }[];
	}

	interface Props {
		environmentName?: (env: string) => string | undefined;
		onremoved?: (t: Target) => void;
	}

	let { environmentName, onremoved }: Props = $props();
	const queryClient = useQueryClient();

	let target = $state<Target | null>(null);
	let removal = $state<Schema<'Removal'> | undefined>(undefined);
	let open = $state(false);

	export async function request(t: Target) {
		target = t;
		removal = undefined;
		try {
			const full =
				t.kind === 'volume'
					? await queryClient.fetchQuery(volumeQuery(t.environmentId, t.name))
					: await queryClient.fetchQuery(networkQuery(t.environmentId, t.name));
			removal = full.removal;
			const users =
				'usedBy' in full ? full.usedBy : 'containers' in full ? full.containers : undefined;
			target = { ...t, usedBy: users ?? t.usedBy };
		} catch {
			// The server still decides when the removal is requested.
		}
		open = true;
	}

	async function remove() {
		const t = target!;
		const ctx = {
			kind: t.kind,
			name: t.name,
			verb: 'remove' as const,
			protection: t.protection,
			environmentName: environmentName?.(t.environmentId)
		};
		const header = { 'Idempotency-Key': idempotencyKey() };
		try {
			const job =
				t.kind === 'volume'
					? await unwrap(
							api.DELETE('/api/v1/environments/{environmentId}/volumes/{volumeId}', {
								params: {
									path: { environmentId: t.environmentId, volumeId: t.name },
									header
								}
							})
						)
					: await unwrap(
							api.DELETE(
								'/api/v1/environments/{environmentId}/networks/{networkId}',
								{
									params: {
										path: { environmentId: t.environmentId, networkId: t.name },
										header
									}
								}
							)
						);
			trackJob(job, {
				ctx,
				key: resourceKey(t.kind, t.environmentId, t.name),
				queryClient,
				invalidate: [t.kind === 'volume' ? queryKeys.volumes.all : queryKeys.networks.all],
				onfinish: (j) => {
					if (j.state === 'succeeded') onremoved?.(t);
				}
			});
		} catch (e) {
			throw new RefusalError(refusal(e, ctx));
		}
	}
</script>

{#if target}
	<RemovalDialog
		bind:open
		kind={target.kind}
		name={target.name}
		{removal}
		affected={[
			{ label: target.name, detail: target.kind },
			...(target.usedBy ?? []).map((c) => ({ label: c.name, detail: c.state ?? 'container' }))
		]}
		confirmLabel={target.kind === 'volume' ? 'Remove volume and its data' : 'Remove network'}
		onconfirm={remove}
	/>
{/if}
