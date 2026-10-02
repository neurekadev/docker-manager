<script lang="ts">
	// Deploy and remove orphaned containers (#7): a deploy with
	// removeOrphans, which also removes the containers of services that are
	// no longer in the Compose file (a plain deploy keeps them). Confirmed
	// first because it removes containers; names the orphans Docker Manager
	// knows (services with containers that the last deploy no longer had).
	// Opened from the Deploy menu and from the overview's drift notice.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { ConfirmDialog } from '$lib/ui';
	import { startDeploy, type RemoveOrphansRequest } from './deploy.svelte';
	import { orphanedServices, stackTitle } from './model';
	import { stackServicesQuery, type Stack } from './queries';
	import type { JobTray } from './tray.svelte';

	interface Props {
		request: RemoveOrphansRequest;
		stack: Stack;
		tray: JobTray;
	}

	let { request, stack, tray }: Props = $props();
	const queryClient = useQueryClient();
	const title = $derived(stackTitle(stack));
	const services = createQuery(() => ({
		...stackServicesQuery(stack.id),
		enabled: request.open && stack.view === 'full'
	}));
	const orphans = $derived(orphanedServices(services.data?.services));

	const consequences = $derived([
		`Deploys ${title}: recreates the services whose settings changed.`,
		orphans.length
			? `Removes the ${orphans.length === 1 ? 'container' : 'containers'} of ${orphans.join(', ')}: ${orphans.length === 1 ? 'it is' : 'they are'} no longer in the Compose file.`
			: 'Removes the containers of every service that is no longer in the Compose file.',
		'Volumes and files are kept.'
	]);
</script>

<ConfirmDialog
	bind:open={request.open}
	title="Deploy {title} and remove orphaned containers?"
	{consequences}
	confirmLabel="Deploy and Remove Orphans"
	tone="danger"
	onconfirm={() => startDeploy(stack, { removeOrphans: true }, tray, queryClient)}
/>
