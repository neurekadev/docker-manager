<script lang="ts">
	import { createQuery } from '@tanstack/svelte-query';
	import { routes } from '$lib/routes';
	import { Button, Card, ErrorState, Skeleton } from '$lib/ui';
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
	subtitle="Image checks and automatic updates are configured by environment policy."
>
	{#snippet actions()}<Button size="sm" href={routes.updates()}>Open update policies</Button
		>{/snippet}
	{#if policies.isPending}<Skeleton lines={2} />
	{:else if policies.isError}<ErrorState
			error={policies.error}
			title="Update policies could not be loaded."
			onretry={() => policies.refetch()}
			compact
		/>
	{:else if covering}
		<p>
			{excluded ? 'This stack is excluded from' : 'This stack is covered by'}
			<a href={routes.updatePolicy(covering.id)}>{covering.name}</a>.
		</p>
	{:else}<p>No update policy covers this environment.</p>{/if}
</Card>
