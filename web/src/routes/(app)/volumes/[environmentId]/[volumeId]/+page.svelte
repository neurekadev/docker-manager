<script lang="ts">
	// Volume overview (#6): driver and options, what Docker Manager can do
	// with its files, the containers using it, its stack and labels (system
	// labels folded). What removing it would do is shown by the removal
	// dialog (the server's preview), not here.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { volumeQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { Card, StatusBadge, formatDateTime } from '$lib/ui';
	import Columns from '$lib/features/common/Columns.svelte';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import LabelsCard from '$lib/features/resources/LabelsCard.svelte';
	import StackBadge from '$lib/features/resources/StackBadge.svelte';
	import { volumeAccess } from '$lib/features/resources/model';

	const env = $derived(page.params.environmentId ?? '');
	const name = $derived(page.params.volumeId ?? '');
	const q = createQuery(() => volumeQuery(env, name));
	const v = $derived(q.data);

	const facts = $derived<Fact[]>(
		v
			? [
					{ label: 'Driver', value: v.driver, mono: true },
					{ label: 'Scope', value: v.scope },
					{
						label: 'Files',
						value: volumeAccess(v).local
							? 'You can browse, watch and back up its files.'
							: volumeAccess(v).reason
					},
					{
						label: 'Created',
						value: v.createdAt ? formatDateTime(v.createdAt) : undefined
					},
					...Object.entries(v.options ?? {}).map(([k, val]) => ({
						label: `Option ${k}`,
						value: val,
						mono: true
					}))
				]
			: []
	);
</script>

{#if v}
	<Columns ratio="equal">
		<Card title="Details">
			{#if v.view === 'full'}<Facts items={facts} label="Details of {v.name}" />
			{:else}<p class="muted">Its details need the volume read permission.</p>{/if}
		</Card>
		<Card title="Used by">
			{#if v.usedBy?.length}
				<ul class="list" role="list">
					{#each [...v.usedBy].sort((a, b) => a.name.localeCompare(b.name)) as c (c.id)}
						<li>
							<a href={routes.container(env, c.name)}>{c.name}</a>
							{#if c.state}<StatusBadge status={c.state} />{/if}
						</li>
					{/each}
				</ul>
			{:else if v.inUse}<p class="muted">Containers you can't see use it.</p>
			{:else}<p class="muted">No container mounts this volume.</p>{/if}
			{#if v.stack}
				<p class="stack">Part of <StackBadge stack={v.stack} /></p>
			{/if}
		</Card>
	</Columns>
	<LabelsCard labels={v.labels} composeLabels={v.composeLabels} label="Labels of {v.name}" />
{/if}

<style>
	.list {
		display: grid;
		gap: var(--space-2);
	}

	.list li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}

	a {
		color: var(--accent-text);
		text-decoration: none;
	}

	.stack {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin-top: var(--space-4);
		color: var(--text-muted);
	}
</style>
