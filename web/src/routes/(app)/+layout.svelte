<script lang="ts">
	// Signed-in area: the auth guard and the app shell (#16, #22). Anything
	// under (app) renders only with an authenticated session; otherwise the
	// visitor goes to setup, sign-in or factor enrollment (guard.ts). While
	// the session is unknown (loading, or offline at start) a boot screen
	// shows instead of a flash of the wrong page.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { sessionQuery, setupStatusQuery, queryKeys } from '$lib/api/queries';
	import { appDestination } from '$lib/auth/guard';
	import { msUntilExpiryCheck, signOut } from '$lib/auth/session';
	import { routes } from '$lib/routes';
	import AppShell from '$lib/shell/AppShell.svelte';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { notices } from '$lib/shell/notices.svelte';
	import BootScreen from '$lib/shell/BootScreen.svelte';

	let { children } = $props();
	const qc = useQueryClient();
	const session = createQuery(() => sessionQuery());
	const setup = createQuery(() => ({ ...setupStatusQuery(), enabled: session.data === null }));

	let wasSignedIn = false;
	$effect(() => {
		if (session.isPending || session.data === undefined) return;
		const s = session.data;
		if (s?.state === 'authenticated') {
			wasSignedIn = true;
			return;
		}
		if (s === null && setup.isPending) return;
		const path = page.url.pathname + page.url.search;
		let to = appDestination(path, s, setup.data?.setupComplete);
		// The session ended while the page was open: say so on sign-in.
		if (to && wasSignedIn && s === null) to = routes.signIn(path, 'expired');
		if (to) void goto(to, { replaceState: true });
	});

	// Re-check the session when it would expire (idle or absolute).
	$effect(() => {
		const ms = msUntilExpiryCheck(session.data);
		if (ms === null) return;
		const t = setTimeout(
			() => qc.invalidateQueries({ queryKey: queryKeys.session }),
			Math.min(ms, 2 ** 31 - 1)
		);
		return () => clearTimeout(t);
	});

	function onsignout() {
		void signOut({
			queryClient: qc,
			navigate: (url) => goto(url, { replaceState: true }),
			currentPath: () => page.url.pathname,
			resetShell: () => {
				environmentSelection.reset();
				notices.clear();
			}
		});
	}
</script>

{#if session.data?.state === 'authenticated' && session.data.user}
	<AppShell user={session.data.user} {onsignout}>
		{@render children()}
	</AppShell>
{:else}
	<BootScreen
		error={session.isError ? session.error : null}
		waiting={session.fetchStatus === 'paused'}
		onretry={() => session.refetch()}
	/>
{/if}
