<script lang="ts">
	// A category of the resource tree (Stacks, Containers, …) of one
	// environment or of the instance: lists its resources when opened (or
	// while searching), plus resources named by rules that no longer list.
	import { createQuery } from '@tanstack/svelte-query';
	import { SvelteSet } from 'svelte/reactivity';
	import TreeNodeButton from './TreeNodeButton.svelte';
	import { nodesFromRules, type Category } from './tree';
	import type { Rule, ScopeNode } from './permissions';

	interface Props {
		category: Category;
		environmentId?: string;
		depth: number;
		search: string;
		rules: Rule[];
		counts: Map<string, number>;
		selectedKey: string;
		onselect: (node: ScopeNode) => void;
	}

	let { category, environmentId, depth, search, rules, counts, selectedKey, onselect }: Props =
		$props();

	let open = $state(false);
	const openStacks = new SvelteSet<string>();
	const q = $derived(search.trim().toLowerCase());
	const active = $derived(open || !!q);
	const nodes = createQuery(() => ({ ...category.nodes(environmentId ?? ''), enabled: active }));

	const all = $derived.by(() => {
		const listed = nodes.data ?? [];
		const known = new Set(listed.map((n) => n.key));
		return [...listed, ...nodesFromRules(rules, category.type, environmentId, known)];
	});
	const matches = (n: ScopeNode) =>
		!q || n.label.toLowerCase().includes(q) || (n.detail ?? '').toLowerCase().includes(q);
	const shown = $derived(all.filter((n) => matches(n) || (n.children ?? []).some(matches)));
	const ruleCount = $derived(
		rules.filter(
			(r) =>
				r.scope.kind === 'resource' &&
				(r.scope.resourceType === category.type ||
					(category.type === 'stack' && r.scope.resourceType === 'service')) &&
				(r.scope.environmentId || undefined) === environmentId
		).length
	);
	const header = $derived<ScopeNode>({
		key: `cat:${environmentId ?? ''}:${category.type}`,
		label: category.label,
		scope: { kind: 'instance' },
		type: 'category'
	});
</script>

{#if !q || shown.length}
	<li>
		<div class="cat" style:--depth={depth}>
			<button
				type="button"
				class="cat-btn"
				aria-expanded={active}
				onclick={() => (open = !open)}
			>
				<span>{header.label}</span>
				{#if ruleCount}<span class="count num">{ruleCount}</span>{/if}
			</button>
		</div>
		{#if active}
			{#if nodes.isPending}
				<p class="note" style:--depth={depth + 1}>Loading…</p>
			{:else if nodes.isError && all.length === 0}
				<p class="note" style:--depth={depth + 1}>Can't be listed right now.</p>
			{:else if shown.length === 0}
				<p class="note" style:--depth={depth + 1}>None.</p>
			{/if}
			<ul role="list">
				{#each shown as n (n.key)}
					<li>
						<TreeNodeButton
							node={n}
							depth={depth + 1}
							selected={selectedKey === n.key}
							count={counts.get(n.key) ?? 0}
							{onselect}
							expanded={openStacks.has(n.key) ||
								(!!q && (n.children ?? []).some(matches))}
							ontoggle={n.children?.length
								? () =>
										openStacks.has(n.key)
											? openStacks.delete(n.key)
											: openStacks.add(n.key)
								: undefined}
						/>
						{#if n.children?.length && (openStacks.has(n.key) || (!!q && n.children.some(matches)))}
							<ul role="list">
								{#each n.children.filter((c) => !q || matches(c) || matches(n)) as c (c.key)}
									<li>
										<TreeNodeButton
											node={c}
											depth={depth + 2}
											selected={selectedKey === c.key}
											count={counts.get(c.key) ?? 0}
											{onselect}
										/>
									</li>
								{/each}
							</ul>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</li>
{/if}

<style>
	.cat,
	.note {
		padding-left: calc(var(--depth) * 14px + 24px);
	}

	.cat-btn {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		width: 100%;
		min-height: 28px;
		padding: 2px var(--space-2);
		border: 0;
		border-radius: var(--radius-md);
		background: transparent;
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
		text-align: left;
		cursor: pointer;
	}

	.cat-btn:hover {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.count {
		margin-left: auto;
		color: var(--accent-text);
	}

	.note {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: 24px;
	}

	@media (pointer: coarse) {
		.cat-btn {
			min-height: var(--touch-target);
		}
	}
</style>
