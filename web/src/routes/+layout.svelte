<script lang="ts">
	// Root layout: global design styles (#22), Svelte Query with the session
	// expiry hook, service worker (#11), live synchronization (#23),
	// connection and update notices, the toast region and the app-wide
	// tooltips (every title attribute, TooltipLayer). A new server waiting
	// for a move shows only its status page (MoveGate reads the move status
	// before any sign-in or setup routing). Page chrome lives
	// in (app)/+layout.svelte (the shell) and (auth)/+layout.svelte (sign-in
	// and onboarding).
	import '$lib/design/global.css';
	import { onMount } from 'svelte';
	import { onlineManager, QueryClientProvider } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { createQueryClient } from '$lib/api/queries';
	import { handleUnauthenticated } from '$lib/auth/session';
	import { connectivity } from '$lib/pwa/connectivity.svelte';
	import { startServiceWorker } from '$lib/pwa/register.svelte';
	import { startLive } from '$lib/live';
	import MoveGate from '$lib/features/managermove/MoveGate.svelte';
	import ConnectionStatus from '$lib/pwa/ConnectionStatus.svelte';
	import UpdatePrompt from '$lib/pwa/UpdatePrompt.svelte';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { notices } from '$lib/shell/notices.svelte';
	import { preventFocusZoom } from '$lib/shell/viewport';
	import Toaster from '$lib/ui/Toaster.svelte';
	import TooltipLayer from '$lib/ui/TooltipLayer.svelte';

	let { children } = $props();

	// iOS zooms into focused fields with text under 16 px; stop that on iOS
	// only, keeping the fields' size and pinch zoom ($lib/shell/viewport).
	preventFocusZoom(document, navigator);

	// Svelte Query assumes "online" until the first online/offline event; seed
	// it from the browser so an offline start pauses queries instead of
	// failing them (the offline shell then says "Waiting for the network").
	onlineManager.setOnline(navigator.onLine);
	const queryClient = createQueryClient((outcome) => connectivity.observe(outcome), {
		onUnauthenticated: () =>
			handleUnauthenticated({
				queryClient,
				navigate: (url) => goto(url, { replaceState: true }),
				currentPath: () => page.url.pathname + page.url.search,
				resetShell: () => {
					environmentSelection.reset();
					notices.clear();
				}
			})
	});

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

<QueryClientProvider client={queryClient}>
	<MoveGate>{@render children()}</MoveGate>
	<div class="status-stack">
		<ConnectionStatus />
		<UpdatePrompt />
	</div>
	<Toaster />
	<TooltipLayer />
</QueryClientProvider>

<style>
	.status-stack {
		position: fixed;
		left: var(--space-4);
		bottom: var(--space-4);
		z-index: var(--z-toast);
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		max-width: calc(100vw - 32px);
		pointer-events: none;
	}
</style>
