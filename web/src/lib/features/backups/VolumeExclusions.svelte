<script lang="ts">
	// Standalone named volumes of one environment a backup policy (#10)
	// leaves out: volumes that belong to no stack. DockYard's own volumes
	// are never offered (#32); stack volumes follow their stack.
	import { createQuery } from '@tanstack/svelte-query';
	import { containersQuery, stacksQuery, volumesQuery } from '$lib/features/common/data';
	import { Checkbox, Skeleton } from '$lib/ui';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	let {
		environmentId,
		environmentName,
		all,
		excluded,
		onchange
	}: {
		environmentId: string;
		environmentName: string;
		all: boolean;
		excluded: string[];
		onchange: (values: string[]) => void;
	} = $props();
	const volumes = createQuery(() => volumesQuery(environmentId));
	const stacks = createQuery(() => stacksQuery(environmentId));
	const containers = createQuery(() => containersQuery(environmentId));
	const stackNames = $derived(new Set((stacks.data ?? []).map((s) => s.name)));
	const stackContainerIds = $derived(
		new Set(
			(containers.data ?? [])
				.filter((c) => stackNames.has(c.labels?.['com.docker.compose.project'] ?? ''))
				.map((c) => c.id)
		)
	);
	const standalone = $derived(
		(volumes.data ?? []).filter(
			(v) =>
				!v.stack &&
				!v.protection &&
				!stackNames.has(v.labels?.['com.docker.compose.project'] ?? '') &&
				!(v.usedBy ?? []).some((c) => stackContainerIds.has(c.id))
		)
	);
	function toggle(name: string, on: boolean) {
		const key = all ? `${environmentId}/${name}` : name;
		onchange(on ? [...new Set([...excluded, key])] : excluded.filter((v) => v !== key));
	}
</script>

<p class="env">{environmentName}</p>
{#if volumes.isPending || stacks.isPending || containers.isPending}
	<Skeleton lines={2} height="20px" />
{:else if volumes.isError || stacks.isError || containers.isError}
	<p class="muted">
		The volumes can't be listed right now (the environment may be offline). Saved exclusions are
		kept.
	</p>
{:else if !standalone.length}
	<p class="muted">No standalone volumes.</p>
{:else}
	<ChoiceGrid min="220px">
		{#each standalone as volume (volume.name)}
			<Checkbox
				label={volume.name}
				checked={excluded.includes(all ? `${environmentId}/${volume.name}` : volume.name)}
				onchange={(e) => toggle(volume.name, e.currentTarget.checked)}
			/>
		{/each}
	</ChoiceGrid>
{/if}

<style>
	.env {
		color: var(--text-muted);
		font-size: var(--text-caption);
		margin-top: var(--space-1);
	}
</style>
