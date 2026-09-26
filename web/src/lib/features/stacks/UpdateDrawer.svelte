<script lang="ts">
	import { createQuery } from '@tanstack/svelte-query';
	import { routes } from '$lib/routes';
	import { Button, Drawer, ErrorState, Skeleton } from '$lib/ui';
	import { environmentUpdatePoliciesQuery } from '$lib/features/updates/queries';
	import type { Stack } from './queries';
	let { open = $bindable(false), stack }: { open: boolean; stack: Stack } = $props();
	const policies = createQuery(() => ({ ...environmentUpdatePoliciesQuery(), enabled: open }));
	const covering = $derived(
		(policies.data ?? []).find(
			(p) => p.scope === 'all' || p.environmentId === stack.environmentId
		)
	);
</script>

<Drawer bind:open title="Update {stack.displayName || stack.name}" size="560px">
	{#if policies.isPending}<Skeleton lines={3} />
	{:else if policies.isError}<ErrorState
			error={policies.error}
			title="Update policies could not be loaded."
			onretry={() => policies.refetch()}
			compact
		/>
	{:else if covering}
		<p>
			{covering.excludeStacks.includes(stack.id)
				? 'This stack is excluded from'
				: 'This stack is covered by'} the environment update policy
			<strong>{covering.name}</strong>.
		</p>
		<p>Checks and update runs apply to every included target in the policy's scope.</p>
		<Button href={routes.updatePolicy(covering.id)}>Open update policy</Button>
	{:else}
		<p>No update policy covers this environment.</p>
		<Button href={routes.updatePolicyNew()}>Create environment update policy</Button>
	{/if}
</Drawer>
