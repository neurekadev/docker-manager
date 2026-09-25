<script lang="ts">
	// The searchable resource tree of the permission editor (#17):
	// All resources > environment > stacks (> services), containers,
	// volumes, … and the resources shared by every environment. Each node
	// shows how many rules target it.
	import { createQuery } from '@tanstack/svelte-query';
	import { SvelteSet } from 'svelte/reactivity';
	import Globe from '@lucide/svelte/icons/globe';
	import Server from '@lucide/svelte/icons/server';
	import { environmentsQuery } from '$lib/api/queries';
	import { TextField } from '$lib/ui';
	import TreeCategory from './TreeCategory.svelte';
	import TreeNodeButton from './TreeNodeButton.svelte';
	import { ruleCounts, type Rule, type ScopeNode } from './permissions';
	import { CATEGORIES, environmentNode, instanceNode } from './tree';

	interface Props {
		rules: Rule[];
		selectedKey: string;
		onselect: (node: ScopeNode) => void;
	}

	let { rules, selectedKey, onselect }: Props = $props();

	const envs = createQuery(() => environmentsQuery());
	const active = $derived((envs.data ?? []).filter((e) => e.status !== 'archived'));
	const counts = $derived(ruleCounts(rules));
	const openEnvs = new SvelteSet<string>();
	let search = $state('');
	const q = $derived(search.trim().toLowerCase());
	const root = instanceNode();
</script>

<div class="tree">
	<TextField
		label="Find a resource"
		hideLabel
		placeholder="Find a resource"
		bind:value={search}
		type="search"
	/>
	<nav aria-label="Resources">
		<ul role="list">
			<li>
				<TreeNodeButton
					node={root}
					icon={Globe}
					selected={selectedKey === root.key}
					count={counts.get('instance') ?? 0}
					{onselect}
				/>
			</li>
			{#each active as e (e.id)}
				{@const n = environmentNode(e)}
				{@const expanded = openEnvs.has(e.id) || !!q}
				<li>
					<TreeNodeButton
						node={{ ...n, detail: e.online ? undefined : 'offline' }}
						icon={Server}
						selected={selectedKey === n.key}
						count={counts.get(`env-all:${e.id}`) ?? 0}
						{expanded}
						ontoggle={() =>
							openEnvs.has(e.id) ? openEnvs.delete(e.id) : openEnvs.add(e.id)}
						{onselect}
					/>
					{#if expanded}
						<ul role="list">
							{#each CATEGORIES.filter((c) => c.perEnvironment) as c (c.type)}
								<TreeCategory
									category={c}
									environmentId={e.id}
									depth={1}
									{search}
									{rules}
									{counts}
									{selectedKey}
									{onselect}
								/>
							{/each}
						</ul>
					{/if}
				</li>
			{/each}
			<li class="shared">
				<p class="shared-label">Shared by every environment</p>
				<ul role="list">
					{#each CATEGORIES.filter((c) => !c.perEnvironment) as c (c.type)}
						<TreeCategory
							category={c}
							depth={0}
							{search}
							{rules}
							{counts}
							{selectedKey}
							{onselect}
						/>
					{/each}
				</ul>
			</li>
		</ul>
	</nav>
</div>

<style>
	.tree {
		display: grid;
		gap: var(--space-3);
		align-content: start;
		min-width: 0;
	}

	nav {
		max-height: 620px;
		overflow: auto;
	}

	.shared {
		margin-top: var(--space-3);
		padding-top: var(--space-2);
		border-top: 1px solid var(--border-subtle);
	}

	.shared-label {
		padding: 0 var(--space-2) var(--space-1) 24px;
		color: var(--text-muted);
		font-size: var(--text-caption);
	}
</style>
