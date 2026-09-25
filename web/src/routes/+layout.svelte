<script lang="ts">
	// App shell plumbing (#11, #23): Svelte Query, service worker, live
	// synchronization, connection and update notices. No layout or visual design here; that is #22.
	import { onMount } from 'svelte';
	import { onlineManager, QueryClientProvider } from '@tanstack/svelte-query';
	import favicon from '$lib/assets/favicon.svg';
	import { createQueryClient } from '$lib/api/queries';
	import { connectivity } from '$lib/pwa/connectivity.svelte';
	import { startServiceWorker } from '$lib/pwa/register.svelte';
	import { startLive } from '$lib/live';
	import ConnectionStatus from '$lib/pwa/ConnectionStatus.svelte';
	import UpdatePrompt from '$lib/pwa/UpdatePrompt.svelte';

	let { children } = $props();

	// Svelte Query assumes "online" until the first online/offline event; seed
	// it from the browser so an offline start pauses queries instead of
	// failing them (the offline shell then says "Waiting for the network").
	onlineManager.setOnline(navigator.onLine);
	const queryClient = createQueryClient((outcome) => connectivity.observe(outcome));

	onMount(() => {
		const stopWatching = connectivity.watch(window, navigator.onLine);
		const stopServiceWorker = startServiceWorker();
		// Live synchronization (#23): one stream per tab invalidates the
		// queries of changed resources.
		const stopLive = startLive(queryClient);
		return () => {
			stopWatching();
			stopServiceWorker();
			stopLive();
		};
	});
</script>

<svelte:head>
	<link rel="icon" href={favicon} type="image/svg+xml" />
</svelte:head>

<QueryClientProvider client={queryClient}>
	<ConnectionStatus />
	<UpdatePrompt />
	{@render children()}
</QueryClientProvider>
