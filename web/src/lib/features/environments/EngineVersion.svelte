<script lang="ts">
	// The Engine version of one environment (GET …/system, cached per
	// environment and refreshed by inventory events). A table cell.
	import { createQuery } from '@tanstack/svelte-query';
	import { environmentSystemQuery } from '$lib/api/queries';

	let { id, enabled }: { id: string; enabled: boolean } = $props();
	const system = createQuery(() => ({ ...environmentSystemQuery(id), enabled }));
	const engine = $derived(system.data?.engine);
</script>

{#if engine}
	<span class="num" title="Engine API {engine.apiVersion}, {engine.os}/{engine.arch}"
		>Docker {engine.version}</span
	>
{:else}<span class="muted">—</span>{/if}
