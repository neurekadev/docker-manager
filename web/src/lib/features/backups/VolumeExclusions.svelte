<script lang="ts">
	import { createQuery } from '@tanstack/svelte-query';
	import { containersQuery, stacksQuery, volumesQuery } from '$lib/features/common/data';
	import { Checkbox, Skeleton } from '$lib/ui';
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

<p>{environmentName}</p>
{#if volumes.isPending || stacks.isPending || containers.isPending}<Skeleton
		lines={2}
		height="20px"
	/>
{:else if volumes.isError || stacks.isError || containers.isError}<p>
		Volumes could not be loaded. Saved exclusions are retained.
	</p>
{:else if !standalone.length}<p>No standalone volumes.</p>
{:else}
	{#each standalone as volume (volume.name)}
		<Checkbox
			label="Exclude {volume.name}"
			checked={excluded.includes(all ? `${environmentId}/${volume.name}` : volume.name)}
			onchange={(e) => toggle(volume.name, e.currentTarget.checked)}
		/>
	{/each}
{/if}
