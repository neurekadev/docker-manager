<script lang="ts">
	// Waiting mode first (docs/internal/architecture/manager-move.md): a new
	// manager that waits for a move answers every route but GET
	// /api/v1/move/status (and health) with 503 manager_move_waiting, so the
	// app reads that route before any sign-in or setup routing (the root
	// layout wraps every page in this gate) and sends every page to the
	// status page (routes.moveStatus) while it waits. Any other manager
	// answers phase none (or complete after a move, when sign-in works as
	// usual); an answer that does not come (offline, an older manager) lets
	// the app start as always. `onready` runs once the app may start its
	// background work (the root layout's live stream): never while the
	// manager waits, where it would only collect 503s.
	import { untrack, type Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { routes } from '$lib/routes';
	import BootScreen from '$lib/shell/BootScreen.svelte';
	import { isWaitingPhase } from './model';
	import { moveStatusQuery } from './queries';

	let { children, onready }: { children: Snippet; onready?: () => void } = $props();

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

	// Not waiting (any more): the app's background work may start, before
	// the pages render (they declare their live scopes when they mount).
	let started = false;
	$effect.pre(() => {
		if (!known || waiting || started) return;
		started = true;
		untrack(() => onready?.());
	});
</script>

{#if onStatusPage || (known && !waiting)}
	{@render children()}
{:else}
	<BootScreen />
{/if}
