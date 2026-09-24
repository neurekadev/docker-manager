<script lang="ts">
	// App shell plumbing (#11): Svelte Query, service worker, connection and
	// update notices. No layout or visual design here; that is #22.
	import { onMount } from 'svelte';
	import { onlineManager, QueryClientProvider } from '@tanstack/svelte-query';
	import favicon from '$lib/assets/favicon.svg';
	import { createQueryClient } from '$lib/api/queries';
	import { connectivity } from '$lib/pwa/connectivity.svelte';
	import { startServiceWorker } from '$lib/pwa/register.svelte';
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
		return () => {
			stopWatching();
			stopServiceWorker();
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
