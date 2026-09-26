<script lang="ts">
	// The state of an environment update policy's targets in one line (the
	// Updates list): what needs attention first, then the target count.
	import { createQuery } from '@tanstack/svelte-query';
	import { Badge, Skeleton } from '$lib/ui';
	import { summarizeTargets } from './model';
	import { environmentUpdateTargetsQuery } from './queries';

	let { policyId }: { policyId: string } = $props();
	const targets = createQuery(() => environmentUpdateTargetsQuery(policyId));
	const active = $derived((targets.data ?? []).filter((t) => !t.inactive));
	const s = $derived(summarizeTargets(active.map((t) => t.candidateSummary)));
</script>

{#if targets.isPending}
	<Skeleton lines={1} height="18px" />
{:else if targets.isError}
	<span class="muted">Unknown</span>
{:else}
	<span class="status">
		{#if s.failing}<Badge tone="danger" dot>{s.failing} failing</Badge>{/if}
		{#if s.withUpdates}<Badge tone="warn" dot>{s.withUpdates} with updates</Badge>{/if}
		{#if !s.failing && !s.withUpdates}
			{#if active.length && s.unchecked === active.length}<Badge dot>Not checked yet</Badge
				>{:else if active.length}<Badge tone="ok" dot>Up to date</Badge>{/if}
		{/if}
		<span class="muted num">{active.length} {active.length === 1 ? 'target' : 'targets'}</span>
	</span>
{/if}

<style>
	.status {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
</style>
