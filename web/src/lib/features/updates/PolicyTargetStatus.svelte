<script lang="ts">
	// The state of an environment update policy's targets in one line (the
	// Updates list): what needs attention first, then the target count. It
	// counts what the Updates KPIs and the policy page count: covered
	// (active) targets, updates as "6 images in 5 stacks".
	import { createQuery } from '@tanstack/svelte-query';
	import { Badge, Skeleton } from '$lib/ui';
	import { summarizeTargets, targetsUpdateText } from './model';
	import { environmentUpdateTargetsQuery } from './queries';

	let { policyId, environmentId = null }: { policyId: string; environmentId?: string | null } =
		$props();
	const targets = createQuery(() => environmentUpdateTargetsQuery(policyId));
	const active = $derived(
		(targets.data ?? []).filter(
			(t) => !t.inactive && (!environmentId || t.environmentId === environmentId)
		)
	);
	const s = $derived(summarizeTargets(active.map((t) => t.candidateSummary)));
</script>

{#if targets.isPending}
	<Skeleton lines={1} height="18px" />
{:else if targets.isError}
	<span class="muted">Unknown</span>
{:else}
	<span class="status">
		{#if s.failing}<Badge tone="danger" dot>{s.failing} failing</Badge>{/if}
		{#if s.withUpdates}<Badge tone="warn" dot>{targetsUpdateText(active)}</Badge>{/if}
		{#if !s.failing && !s.withUpdates}
			{#if active.length && s.unchecked === active.length}<Badge dot>Not Checked Yet</Badge
				>{:else if active.length}<Badge tone="ok" dot>Up to Date</Badge>{/if}
		{/if}
		<span class="muted num">{active.length} covered</span>
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
