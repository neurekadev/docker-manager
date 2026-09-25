<script lang="ts">
	// What of one stack a backup policy takes (#10): the project directory
	// with its relative bind sources (always, minus excluded paths), each
	// named volume (included unless excluded), anonymous volumes only when
	// turned on (default off), and bind sources outside the project only
	// by explicit opt-in (plus the agent's allowlist).
	import { createQuery } from '@tanstack/svelte-query';
	import { Checkbox, Switch, TextArea } from '$lib/ui';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import { volumesQuery, type Stack } from '$lib/features/common/data';
	import { linesToList, listToLines } from '$lib/features/common/text';
	import type { StackSelection } from './model';

	let {
		value = $bindable(),
		stack,
		onchange
	}: { value: StackSelection; stack: Stack; onchange?: () => void } = $props();

	const volumes = createQuery(() => volumesQuery(stack.environmentId));
	const named = $derived(
		(volumes.data ?? []).filter(
			(v) =>
				(v.stack?.stackId === stack.id || v.stack?.project === stack.name) &&
				!v.labels?.['com.docker.volume.anonymous']
		)
	);

	function set(patch: Partial<StackSelection>) {
		value = { ...value, ...patch };
		onchange?.();
	}

	function toggleVolume(name: string, on: boolean) {
		const ex = value.volumeExclude ?? [];
		set({ volumeExclude: on ? ex.filter((v) => v !== name) : [...ex, name] });
	}
</script>

<div class="stack-scope">
	<FieldGroup
		legend="Volumes of {stack.displayName || stack.name}"
		hint="Named volumes are backed up unless you clear them here."
	>
		{#if volumes.isError}
			<p class="muted">
				The volumes could not be listed (the environment may be offline). Every named volume
				is included.
			</p>
		{:else if named.length}
			<ChoiceGrid min="220px">
				{#each named as v (v.name)}
					<Checkbox
						label={v.name}
						checked={!(value.volumeExclude ?? []).includes(v.name)}
						onchange={(e) => toggleVolume(v.name, e.currentTarget.checked)}
					/>
				{/each}
			</ChoiceGrid>
		{:else if !volumes.isPending}
			<p class="muted">This stack has no named volumes.</p>
		{/if}
		<Switch
			label="Also back up anonymous volumes"
			description="Off by default: anonymous volumes usually hold caches and temporary data."
			checked={!!value.anonymousVolumes}
			onchange={(v) => set({ anonymousVolumes: v })}
		/>
	</FieldGroup>
	<TextArea
		label="Paths not backed up"
		description="Optional. Relative to the project directory, one per line (e.g. data/cache). Relative bind sources inside it are included otherwise."
		mono
		rows={2}
		value={listToLines(value.pathExcludes)}
		onchange={(e) => set({ pathExcludes: linesToList(e.currentTarget.value) })}
	/>
	<TextArea
		label="Bind sources outside the project directory"
		description="Optional, explicit opt-in: absolute paths, one per line. Each must also be below the agent's DOCKYARD_BACKUP_EXTERNAL_ALLOWLIST; nothing outside the project is ever included implicitly."
		mono
		rows={2}
		value={listToLines(value.externalPaths)}
		onchange={(e) => set({ externalPaths: linesToList(e.currentTarget.value) })}
	/>
</div>

<style>
	.stack-scope {
		display: grid;
		gap: var(--space-3);
	}
</style>
