<script lang="ts">
	// The signed-in shell's banner while this Docker Manager is read-only
	// because it moves to a new server (not a toast: it stays). Every
	// session reads the lock (GET /auth/session, managerMove: none, moving
	// or moved, with the address); the owner's shell also reads the move
	// itself (GET /manager/move, polled while it changes). The first change
	// the manager refuses with 409 manager_moved (moved.svelte.ts) shows the
	// banner too and reads the session again.
	import { onMount } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Truck from '@lucide/svelte/icons/truck';
	import { queryKeys, sessionQuery } from '$lib/api/queries';
	import { Notice } from '$lib/ui';
	import { isPolled, lockOf, moveBanner, thisAddress } from './model';
	import { managerMoved, watchManagerMoved } from './moved.svelte';
	import { managerMoveKeys, managerMoveQuery } from './queries';

	let { owner }: { owner: boolean } = $props();

	const qc = useQueryClient();
	onMount(() => watchManagerMoved());

	const session = createQuery(() => sessionQuery());
	const move = createQuery(() => ({
		...managerMoveQuery(),
		enabled: owner,
		refetchInterval: (q) => (isPolled(q.state.data?.state) ? 15_000 : false)
	}));

	// A refusal the last reads did not explain: read the lock again.
	$effect(() => {
		if (!managerMoved.refused) return;
		void qc.invalidateQueries({ queryKey: queryKeys.session });
		if (owner) void qc.invalidateQueries({ queryKey: managerMoveKeys.current });
	});

	// The owner's polled move is the freshest (null: no move); else the session's lock.
	const lock = $derived(
		owner && move.data !== undefined
			? move.data
				? lockOf(move.data.state)
				: 'none'
			: session.data?.managerMove?.state
	);
	const banner = $derived(
		moveBanner(
			lock,
			managerMoved.refused,
			thisAddress(session.data?.managerMove?.address, globalThis.location?.origin)
		)
	);
</script>

{#if banner}
	<Notice tone="warn" icon={Truck} title={banner.title} bar live="status">{banner.body}</Notice>
{/if}
