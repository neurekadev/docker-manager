<script lang="ts">
	// Standalone named volumes of one environment for a backup policy (#10):
	// volumes that belong to no stack. Docker Manager's own volumes are never
	// offered (#32); stack volumes are chosen with their stack.
	import { createQuery } from '@tanstack/svelte-query';
	import { Checkbox } from '$lib/ui';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	import { volumesQuery } from '$lib/features/common/data';
	import type { VolumeSelection } from './model';

	interface Props {
		environmentId: string;
		environmentName: string;
		selected: VolumeSelection[];
		onchange: (selected: VolumeSelection[]) => void;
	}

	let { environmentId, environmentName, selected, onchange }: Props = $props();

	const volumes = createQuery(() => volumesQuery(environmentId));
	const standalone = $derived(
		(volumes.data ?? []).filter(
			(v) => !v.stack && !v.protection && !v.labels?.['com.docker.volume.anonymous']
		)
	);

	function isOn(name: string) {
		return selected.some((s) => s.environmentId === environmentId && s.volume === name);
	}

	function toggle(name: string, on: boolean) {
		const rest = selected.filter(
			(s) => !(s.environmentId === environmentId && s.volume === name)
		);
		onchange(on ? [...rest, { environmentId, volume: name }] : rest);
	}
</script>

{#if volumes.isError}
	<p class="muted">
		{environmentName}: the volumes can't be listed right now (the environment may be offline).
	</p>
{:else if standalone.length}
	<ChoiceGrid min="220px">
		{#each standalone as v (v.name)}
			<Checkbox
				label={v.name}
				description={environmentName}
				checked={isOn(v.name)}
				onchange={(e) => toggle(v.name, e.currentTarget.checked)}
			/>
		{/each}
	</ChoiceGrid>
{:else if !volumes.isPending}
	<p class="muted">{environmentName}: no standalone volumes.</p>
{/if}
