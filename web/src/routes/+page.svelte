<script lang="ts">
	// Minimal shell (#11). Visual design comes from the mockup in #22.
	import { onMount } from 'svelte';
	import { getHealth, type Health } from '$lib/api/client';

	let health = $state<Health | null>(null);
	let error = $state<string | null>(null);

	onMount(async () => {
		const result = await getHealth();
		if (result.ok) {
			health = result.data;
		} else {
			error = result.error;
		}
	});
</script>

<main>
	<h1>DockYard</h1>
	{#if health}
		<p>Manager {health.status} &middot; version {health.version} ({health.commit})</p>
	{:else if error}
		<p role="alert">Manager unreachable: {error}</p>
	{:else}
		<p>Checking manager&hellip;</p>
	{/if}
</main>
