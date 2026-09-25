<script lang="ts">
	// Volume overview (#6): driver and options, the containers using it,
	// its stack, labels, and exactly what removing it would do (the
	// server's removal preview: consequences or what blocks it).
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { volumeQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { Card, StatusBadge } from '$lib/ui';
	import { sentence } from '$lib/features/resources/refusals';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
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
							? 'Local: files, watching and backups work'
							: volumeAccess(v).reason
					},
					{
						label: 'Created',
						value: v.createdAt ? new Date(v.createdAt).toLocaleString() : undefined
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
	<div class="grid">
		<Card title="Details">
			{#if v.view === 'full'}<Facts items={facts} label="Details of {v.name}" />
			{:else}<p class="muted">Its details need the volume read permission.</p>{/if}
		</Card>
		<Card title="Used by">
			{#if v.usedBy?.length}
				<ul class="list" role="list">
					{#each v.usedBy as c (c.id)}
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
		{#if v.removal}
			<Card title="If you remove it">
				{#if v.removal.blockers.length}
					<p class="lead">Removal is refused now:</p>
					<ul class="bullets">
						{#each v.removal.blockers as b, i (i)}<li>{sentence(b.message)}</li>{/each}
					</ul>
					<p class="then">Once nothing blocks it, removing it:</p>
				{/if}
				<ul class="bullets">
					{#each v.removal.consequences as c (c)}<li>{sentence(c)}</li>{/each}
				</ul>
			</Card>
		{/if}
		<Card title="Labels">
			{#if v.labels && Object.keys(v.labels).length}
				<Facts
					items={Object.entries(v.labels)
						.sort(([a], [b]) => a.localeCompare(b))
						.map(([k, val]) => ({ label: k, value: val, mono: true }))}
					label="Labels of {v.name}"
				/>
			{:else}<p class="muted">No labels.</p>{/if}
		</Card>
	</div>
{/if}

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-4);
	}

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

	.lead {
		margin-bottom: var(--space-2);
		color: var(--warn);
	}

	.then {
		margin: var(--space-3) 0 var(--space-2);
		color: var(--text-muted);
	}

	.bullets {
		display: grid;
		gap: 4px;
		padding-left: 18px;
		margin-bottom: var(--space-2);
	}

	@media (max-width: 1023px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
