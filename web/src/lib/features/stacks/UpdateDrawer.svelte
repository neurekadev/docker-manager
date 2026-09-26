<script lang="ts">
	// Stack update entry point (#20): updates run through the environment
	// update policy covering the stack; the drawer points to it.
	import { createQuery } from '@tanstack/svelte-query';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import { routes } from '$lib/routes';
	import { Badge, Button, Drawer, EmptyState, ErrorState, Skeleton } from '$lib/ui';
	import { environmentUpdatePoliciesQuery } from '$lib/features/updates/queries';
	import type { Stack } from './queries';

	let { open = $bindable(false), stack }: { open: boolean; stack: Stack } = $props();
	const policies = createQuery(() => ({ ...environmentUpdatePoliciesQuery(), enabled: open }));
	const covering = $derived(
		(policies.data ?? []).find(
			(p) => p.scope === 'all' || p.environmentId === stack.environmentId
		)
	);
	const excluded = $derived(!!covering?.excludeStacks.includes(stack.id));
</script>

<Drawer bind:open title="Update {stack.displayName || stack.name}" size="560px">
	{#if policies.isPending}
		<Skeleton lines={3} />
	{:else if policies.isError}
		<ErrorState
			error={policies.error}
			title="Update policies could not be loaded."
			onretry={() => policies.refetch()}
			compact
		/>
	{:else if covering}
		<div class="body">
			<div class="policy">
				<strong class="name">{covering.name}</strong>
				{#if excluded}
					<Badge tone="neutral">Excluded</Badge>
				{:else}
					<Badge tone="ok" dot>Covered</Badge>
				{/if}
			</div>
			<p class="muted">
				{excluded
					? 'The environment update policy leaves this stack out. Edit the policy to include it again.'
					: 'Checks and updates of this stack run through its environment update policy. Preview and apply them there.'}
			</p>
			<div>
				<Button variant="primary" href={routes.updatePolicy(covering.id)}
					>Open update policy</Button
				>
			</div>
		</div>
	{:else}
		<EmptyState
			icon={PackageCheck}
			color="violet"
			title="No update policy covers this environment."
			description="Create an update policy to check and apply image updates for this stack."
			level={3}
			compact
		>
			{#snippet actions()}<Button variant="primary" href={routes.updatePolicyNew()}
					>Create update policy</Button
				>{/snippet}
		</EmptyState>
	{/if}
</Drawer>

<style>
	.body {
		display: grid;
		gap: var(--space-3);
	}

	.policy {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}
</style>
