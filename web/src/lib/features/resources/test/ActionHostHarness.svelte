<script lang="ts">
	// Test harness: the container action host inside a QueryClient, with a
	// button that requests one action.
	import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
	import type { Container } from '$lib/api/queries';
	import Toaster from '$lib/ui/Toaster.svelte';
	import ContainerActionHost from '../ContainerActionHost.svelte';
	import type { ContainerVerb } from '../container-actions';

	let { container, verb }: { container: Container; verb: ContainerVerb } = $props();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	let host = $state<ContainerActionHost>();
</script>

<QueryClientProvider {client}>
	<ContainerActionHost bind:this={host} environmentName={() => 'homelab'} />
	<button type="button" onclick={() => host?.request(container, verb)}>Request {verb}</button>
	<Toaster />
</QueryClientProvider>
