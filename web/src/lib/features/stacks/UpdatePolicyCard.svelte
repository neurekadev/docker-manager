<script lang="ts">
	// The stack's update coverage (#20): the environment update policy whose
	// scope includes the stack, and whether it excludes it.
	import { createQuery } from '@tanstack/svelte-query';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import { routes } from '$lib/routes';
	import { Badge, Button, Card, EmptyState, ErrorState, Skeleton } from '$lib/ui';
	import { environmentUpdatePoliciesQuery } from '$lib/features/updates/queries';
	import type { Stack } from './queries';

	let { stack }: { stack: Stack } = $props();
	const policies = createQuery(() => environmentUpdatePoliciesQuery());
	const covering = $derived(
		(policies.data ?? []).find(
			(p) => p.scope === 'all' || p.environmentId === stack.environmentId
		)
	);
	const excluded = $derived(!!covering?.excludeStacks.includes(stack.id));
</script>

<Card
	title="Updates"
	id="updates"
	info="Image checks and automatic updates come from the environment's update policy."
>
	{#snippet actions()}<Button size="sm" href={routes.updates()}>Open Update Policies</Button
		>{/snippet}
	{#if policies.isPending}
		<Skeleton lines={2} />
	{:else if policies.isError}
		<ErrorState
			error={policies.error}
			title="Update policies could not be loaded."
			onretry={() => policies.refetch()}
			compact
		/>
	{:else if covering}
		<div class="policy">
			<a class="name" href={routes.updatePolicy(covering.id)}>{covering.name}</a>
			{#if excluded}
				<Badge tone="neutral">Excluded</Badge>
			{:else}
				<Badge tone="ok" dot>Covered</Badge>
			{/if}
		</div>
		{#if excluded}
			<p class="muted">Edit the policy to include this stack again.</p>
		{/if}
	{:else}
		<EmptyState
			icon={PackageCheck}
			color="violet"
			title="No update policy covers this environment."
			description="Create an update policy for this environment or all environments."
			level={3}
			compact
		/>
	{/if}
</Card>

<style>
	.policy {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-bottom: var(--space-1);
	}

	.name {
		font-weight: var(--weight-medium);
	}
</style>
