<script lang="ts">
	// The volumes of one environment a backup policy (#10) covers: the
	// volumes of the included stacks and the standalone ones, all included
	// until unchecked (unchecking adds the volume to the policy's
	// exclusions). Anonymous and buildx builder volumes appear only when the
	// policy backs them up. Volumes left out by the backup exclude label (on
	// the volume, in its Compose file or on a container using it) are listed
	// unchecked and locked, with an (i) naming the label; standalone volumes
	// only temporary containers of Docker Manager or Compose use are
	// counted, never offered. Docker Manager's own volumes
	// are never offered (#32). Volumes of unfinished environment migrations
	// cannot be told apart here (the manager leaves them out at run time).
	import { createQuery } from '@tanstack/svelte-query';
	import { Skeleton } from '$lib/ui';
	import CoverageList from '$lib/features/common/CoverageList.svelte';
	import { containersQuery, stacksQuery, volumesQuery } from '$lib/features/common/data';
	import { coveredVolumes, labelLockReason, volumeKey } from './model';

	let {
		environmentId,
		environmentName,
		all,
		excluded,
		excludedStacks,
		anonymous,
		buildx,
		onchange
	}: {
		environmentId: string;
		environmentName: string;
		all: boolean;
		excluded: string[];
		excludedStacks: string[];
		anonymous: boolean;
		buildx: boolean;
		onchange: (values: string[]) => void;
	} = $props();

	const volumes = createQuery(() => volumesQuery(environmentId));
	const stacks = createQuery(() => stacksQuery(environmentId));
	const containers = createQuery(() => containersQuery(environmentId));
	const stackTitle = (id: string) => {
		const s = stacks.data?.find((x) => x.id === id);
		return s?.displayName || s?.name || 'Stack';
	};
	const list = $derived(
		coveredVolumes(volumes.data ?? [], stacks.data ?? [], containers.data ?? [])
	);
	const temporary = $derived(list.filter((v) => !v.labelled && v.temporary).length);
	const offered = $derived(list.filter((v) => v.labelled || !v.temporary));
	const hiddenAnonymous = $derived(
		anonymous ? 0 : offered.filter((v) => !v.labelled && v.anonymous).length
	);
	const hiddenBuildx = $derived(
		buildx ? 0 : offered.filter((v) => !v.labelled && v.buildx).length
	);
	const shown = $derived(
		offered.filter((v) => (anonymous || !v.anonymous) && (buildx || !v.buildx))
	);
	const stackVolumes = $derived(
		shown.filter((v) => v.stackId && !excludedStacks.includes(v.stackId))
	);
	const standalone = $derived(shown.filter((v) => !v.stackId));
	const item = (v: (typeof list)[number], withStack: boolean) => ({
		key: volumeKey(all, environmentId, v.name),
		label: v.name,
		description:
			[withStack && v.stackId ? stackTitle(v.stackId) : '', v.anonymous ? 'anonymous' : '']
				.filter(Boolean)
				.join(', ') || undefined,
		locked: v.labelledBy ? labelLockReason(v.labelledBy) : undefined
	});
</script>

<div class="env">
	<p class="env-name">{environmentName}</p>
	{#if volumes.isPending || stacks.isPending || containers.isPending}
		<Skeleton lines={2} height="20px" />
	{:else if volumes.isError || stacks.isError || containers.isError}
		<p class="muted">
			The volumes can't be listed right now (the environment may be offline). Saved exclusions
			are kept.
		</p>
	{:else}
		<div class="lists">
			<div class="list">
				<span class="list-title">Stack Volumes</span>
				{#if stackVolumes.length}
					<CoverageList
						label="Stack Volumes on {environmentName}"
						items={stackVolumes.map((v) => item(v, true))}
						{excluded}
						{onchange}
					/>
				{:else}
					<p class="muted small">No volumes of included stacks.</p>
				{/if}
			</div>
			<div class="list">
				<span class="list-title">Standalone Volumes</span>
				{#if standalone.length}
					<CoverageList
						label="Standalone Volumes on {environmentName}"
						items={standalone.map((v) => item(v, false))}
						{excluded}
						{onchange}
					/>
				{:else}
					<p class="muted small">No standalone volumes.</p>
				{/if}
			</div>
		</div>
		{#if hiddenAnonymous}
			<p class="muted small">
				{hiddenAnonymous}
				{hiddenAnonymous === 1 ? 'anonymous volume is' : 'anonymous volumes are'} not backed up
				(turn on anonymous volumes to include them).
			</p>
		{/if}
		{#if hiddenBuildx}
			<p class="muted small">
				{hiddenBuildx}
				{hiddenBuildx === 1 ? 'buildx builder volume is' : 'buildx builder volumes are'} not backed
				up (turn on buildx builder volumes to include them).
			</p>
		{/if}
		{#if temporary}
			<p class="muted small">
				{temporary}
				{temporary === 1 ? 'volume is' : 'volumes are'} not backed up: only temporary containers
				of Docker Manager or Compose use {temporary === 1 ? 'it' : 'them'}.
			</p>
		{/if}
	{/if}
</div>

<style>
	.env {
		display: grid;
		gap: var(--space-3);
		min-width: 0;
	}

	.env-name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.lists {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-5);
	}

	.list {
		display: grid;
		gap: var(--space-1);
		align-content: start;
		min-width: 0;
	}

	.list-title {
		color: var(--text-default);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
	}

	.small {
		font-size: var(--text-caption);
	}

	@media (max-width: 767px) {
		.lists {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
