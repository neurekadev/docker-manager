<script lang="ts">
	// Pop-out windows (#22: the log viewer in its own window). Signed in like
	// the app (same guard and session), without the app shell: the window is
	// all work surface.
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { sessionQuery, setupStatusQuery } from '$lib/api/queries';
	import { appDestination } from '$lib/auth/guard';
	import BootScreen from '$lib/shell/BootScreen.svelte';

	let { children } = $props();
	const session = createQuery(() => sessionQuery());
	const setup = createQuery(() => ({ ...setupStatusQuery(), enabled: session.data === null }));

	$effect(() => {
		if (session.isPending || session.data === undefined) return;
		const s = session.data;
		if (s?.state === 'authenticated') return;
		if (s === null && setup.isPending) return;
		const to = appDestination(
			page.url.pathname + page.url.search,
			s,
			setup.data?.setupComplete
		);
		if (to) void goto(to, { replaceState: true });
	});
</script>

{#if session.data?.state === 'authenticated'}
	<main id="main" class="popout">{@render children()}</main>
{:else}
	<BootScreen
		error={session.isError ? session.error : null}
		waiting={session.fetchStatus === 'paused'}
		onretry={() => session.refetch()}
	/>
{/if}

<style>
	.popout {
		display: flex;
		flex-direction: column;
		height: 100dvh;
		padding: var(--space-3);
		background: var(--surface-canvas);
	}
</style>
