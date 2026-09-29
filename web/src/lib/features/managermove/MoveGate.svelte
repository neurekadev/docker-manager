<script lang="ts">
	// Waiting mode first (docs/internal/architecture/manager-move.md): a new
	// manager that waits for a move answers every route but GET
	// /api/v1/move/status with 503 manager_move_waiting, so the app reads
	// that route before any sign-in or setup routing (the root layout wraps
	// every page in this gate) and sends every page to the status page
	// (routes.moveStatus) while it waits. Any other manager answers phase
	// none (or complete after a move, when sign-in works as usual); an
	// answer that does not come (offline, an older manager) lets the app
	// start as always.
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { routes } from '$lib/routes';
	import BootScreen from '$lib/shell/BootScreen.svelte';
	import { isWaitingPhase } from './model';
	import { moveStatusQuery } from './queries';

	let { children }: { children: Snippet } = $props();

	// Read once per page load; the status page polls the same query.
	const status = createQuery(() => ({
		...moveStatusQuery(),
		staleTime: Infinity,
		refetchOnWindowFocus: false
	}));
	const known = $derived(
		status.data !== undefined || status.isError || status.fetchStatus === 'paused'
	);
	const waiting = $derived(isWaitingPhase(status.data?.phase));
	const onStatusPage = $derived(page.url.pathname === routes.moveStatus());

	$effect(() => {
		if (waiting && !onStatusPage) void goto(routes.moveStatus(), { replaceState: true });
	});
</script>

{#if onStatusPage || (known && !waiting)}
	{@render children()}
{:else}
	<BootScreen />
{/if}
