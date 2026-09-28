<script lang="ts">
	// Test harness: the command palette inside a QueryClientProvider.
	import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
	import CommandPalette from '$lib/shell/CommandPalette.svelte';
	import type { Access, NavItem } from '$lib/shell/nav';
	import type { RecentPage } from '$lib/shell/recent.svelte';

	let {
		pages,
		onnavigate,
		access,
		recent = []
	}: {
		pages: NavItem[];
		onnavigate: (href: string) => void;
		access?: Access;
		recent?: RecentPage[];
	} = $props();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	let open = $state(false);
</script>

<QueryClientProvider {client}>
	<button type="button" onclick={() => (open = true)}>Search</button>
	<CommandPalette
		bind:open
		{pages}
		environmentId={null}
		{onnavigate}
		{access}
		{recent}
		currentPath="/"
		debounce={0}
	/>
</QueryClientProvider>
