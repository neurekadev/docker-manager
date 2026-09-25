<script lang="ts" generics="P extends Record<string, unknown>">
	// Test harness: renders a component inside a QueryClientProvider (for
	// components that read Svelte Query data). Pass a fresh QueryClient per
	// test; stub globalThis.fetch for the typed client's requests.
	import type { Component } from 'svelte';
	import { QueryClientProvider, type QueryClient } from '@tanstack/svelte-query';
	import Toaster from '$lib/ui/Toaster.svelte';

	let {
		client,
		component: C,
		props
	}: { client: QueryClient; component: Component<P>; props: P } = $props();
</script>

<QueryClientProvider {client}>
	<C {...props} />
	<Toaster />
</QueryClientProvider>
