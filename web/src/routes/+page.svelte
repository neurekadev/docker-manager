<script lang="ts">
	// Minimal shell (#11): one typed API call through the generated client and
	// Svelte Query. Visual design comes from the mockup in #22.
	import { createQuery } from '@tanstack/svelte-query';
	import { healthQuery } from '$lib/api/queries';

	const health = createQuery(() => healthQuery());
</script>

<main>
	<h1>DockYard</h1>
	{#if health.data}
		<p>
			Manager {health.data.status} &middot; version {health.data.version} ({health.data
				.commit})
		</p>
	{:else if health.isError}
		<p role="alert">Manager unreachable: {health.error.message}</p>
	{:else if health.fetchStatus === 'paused'}
		<p>Waiting for the network&hellip;</p>
	{:else}
		<p>Checking manager&hellip;</p>
	{/if}
</main>
