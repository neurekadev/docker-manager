<script lang="ts">
	// Test harness: the command palette inside a QueryClientProvider.
	import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
	import CommandPalette from '$lib/shell/CommandPalette.svelte';
	import type { NavItem } from '$lib/shell/nav';

	let { pages, onnavigate }: { pages: NavItem[]; onnavigate: (href: string) => void } = $props();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	let open = $state(false);
</script>

<QueryClientProvider {client}>
	<button type="button" onclick={() => (open = true)}>Search</button>
	<CommandPalette bind:open {pages} environmentId={null} {onnavigate} debounce={0} />
</QueryClientProvider>
