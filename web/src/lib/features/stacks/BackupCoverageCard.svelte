<script lang="ts">
	// The stack's backup coverage (#10, #246): whether the backup settings
	// cover the stack or leave it (or its environment) out, and when they
	// run. Edited on the Backups page.
	import { createQuery } from '@tanstack/svelte-query';
	import { routes } from '$lib/routes';
	import { Badge, Button, Card, ErrorState, Skeleton } from '$lib/ui';
	import { scheduleWords, settingsCover } from '$lib/features/backups/model';
	import { backupSettingsQuery } from '$lib/features/backups/queries';
	import type { Stack } from './queries';

	let { stack }: { stack: Stack } = $props();
	const settings = createQuery(() => backupSettingsQuery());
	const covered = $derived(
		!!settings.data &&
			settingsCover(settings.data, { environmentId: stack.environmentId, stackId: stack.id })
	);
</script>

<Card title="Backups" id="backups" info="Backups of the stack come from the backup settings.">
	{#snippet actions()}<Button size="sm" href={routes.backups()}>Open Backups</Button>{/snippet}
	{#if settings.isPending}
		<Skeleton lines={2} />
	{:else if settings.isError}
		<ErrorState
			error={settings.error}
			title="The backup settings could not be loaded."
			onretry={() => settings.refetch()}
			compact
		/>
	{:else if settings.data}
		{@const st = settings.data}
		<div class="coverage">
			{#if !covered}
				<Badge tone="neutral">Excluded</Badge>
			{:else if !st.primaryRepositoryId}
				<Badge tone="warn" dot>No Primary Repository</Badge>
			{:else}
				<Badge tone="ok" dot>Covered</Badge>
			{/if}
			<span class="muted"
				>{st.enabled
					? `Backed up ${scheduleWords(st.schedule.cron, st.schedule.timeZone)}`
					: 'Backups run only when started'}</span
			>
		</div>
		{#if !covered}
			<p class="muted">Edit the backup settings to include this stack again.</p>
		{/if}
	{/if}
</Card>

<style>
	.coverage {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-bottom: var(--space-1);
	}
</style>
