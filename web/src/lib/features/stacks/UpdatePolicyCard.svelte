<script lang="ts">
	// The stack's update coverage (#20, #240): whether the update settings
	// cover the stack or leave it (or its environment) out, and whether
	// they check and update it automatically. Hidden from callers who may
	// not read the update settings (they need update_policy.read on all
	// environments).
	import { createQuery } from '@tanstack/svelte-query';
	import { routes } from '$lib/routes';
	import { Badge, Button, Card, ErrorState, Skeleton } from '$lib/ui';
	import { isDenied } from '$lib/features/common/access';
	import { policySchedulesText } from '$lib/features/updates/model';
	import { updateSettingsQuery } from '$lib/features/updates/queries';
	import type { Stack } from './queries';

	let { stack }: { stack: Stack } = $props();
	const settings = createQuery(() => ({ ...updateSettingsQuery(), retry: false }));
	const excluded = $derived(
		!!settings.data &&
			(settings.data.excludeStacks.includes(stack.id) ||
				settings.data.excludeEnvironments.includes(stack.environmentId))
	);
</script>

{#if !(settings.isError && isDenied(settings.error))}
	<Card
		title="Updates"
		id="updates"
		info="Image checks and automatic updates come from the update settings."
	>
		{#snippet actions()}<Button size="sm" href={routes.updates()}>Open Updates</Button
			>{/snippet}
		{#if settings.isPending}
			<Skeleton lines={2} />
		{:else if settings.isError}
			<ErrorState
				error={settings.error}
				title="The update settings could not be loaded."
				onretry={() => settings.refetch()}
				compact
			/>
		{:else if settings.data}
			<div class="policy">
				{#if excluded}
					<Badge tone="neutral">Excluded</Badge>
				{:else}
					<Badge tone="ok" dot>Covered</Badge>
				{/if}
				<span class="muted"
					>{policySchedulesText(
						settings.data.checkSchedule,
						settings.data.runSchedule
					)}</span
				>
			</div>
			{#if excluded}
				<p class="muted">Edit the update settings to include this stack again.</p>
			{/if}
		{/if}
	</Card>
{/if}

<style>
	.policy {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-bottom: var(--space-1);
	}
</style>
