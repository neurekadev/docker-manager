<script lang="ts">
	// Test harness: a CronField inside a QueryClientProvider, showing the
	// expression it writes (bind:cron).
	import { untrack } from 'svelte';
	import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
	import CronField from '$lib/ui/CronField.svelte';

	let { initial = '0 3 * * *' }: { initial?: string } = $props();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	let cron = $state(untrack(() => initial));
	let timeZone = $state('UTC');
</script>

<QueryClientProvider {client}>
	<CronField label="Check Schedule" bind:cron bind:timeZone debounce={0} timeZones={['UTC']} />
	<p data-testid="cron">{cron}</p>
</QueryClientProvider>
