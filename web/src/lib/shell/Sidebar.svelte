<script lang="ts">
	// Sidebar (#22): logo lockup, environment switcher, permission-filtered
	// navigation grouped by spacing (a divider before administration). In
	// rail mode (1024–1279 px or collapsed) only icons show, with tooltips.
	import type { Snippet } from 'svelte';
	import Tooltip from '$lib/ui/Tooltip.svelte';
	import Logo from './Logo.svelte';
	import type { NavItem } from './nav';

	interface Props {
		items: NavItem[];
		activeId?: string;
		rail?: boolean;
		switcher: Snippet;
		onnavigate?: () => void;
	}

	let { items, activeId, rail = false, switcher, onnavigate }: Props = $props();
	const groups = $derived(
		(['overview', 'resources', 'operations', 'admin'] as const)
			.map((g) => ({ group: g, items: items.filter((i) => i.group === g) }))
			.filter((g) => g.items.length)
	);
</script>

<div class="sidebar" class:rail>
	<div class="brand"><Logo mark={rail} /></div>
	<div class="switcher">{@render switcher()}</div>
	<nav aria-label="Main">
		{#each groups as g (g.group)}
			<ul role="list" class="group" class:admin={g.group === 'admin'}>
				{#each g.items as item (item.id)}
					{@const Icon = item.icon}
					<li>
						{#if rail}
							<Tooltip text={item.label} side="right">
								{#snippet trigger(props)}
									<a
										{...props}
										href={item.href}
										class="item"
										aria-label={item.label}
										aria-current={item.id === activeId ? 'page' : undefined}
										onclick={onnavigate}
									>
										<Icon size={18} strokeWidth={1.75} aria-hidden="true" />
									</a>
								{/snippet}
							</Tooltip>
						{:else}
							<a
								href={item.href}
								class="item"
								aria-current={item.id === activeId ? 'page' : undefined}
								onclick={onnavigate}
							>
								<Icon size={18} strokeWidth={1.75} aria-hidden="true" />
								<span>{item.label}</span>
							</a>
						{/if}
					</li>
				{/each}
			</ul>
		{/each}
	</nav>
</div>

<style>
	.sidebar {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		height: 100%;
		padding: var(--space-4) var(--space-3);
		overflow-y: auto;
	}

	.brand {
		padding: 0 var(--space-1);
	}

	nav {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.group {
		display: flex;
		flex-direction: column;
		gap: 2px;
		margin: 0;
	}

	.group.admin {
		padding-top: var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}

	.item {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		height: 40px;
		padding: 0 var(--space-3);
		border-radius: var(--radius-md);
		color: var(--text-default);
		font-size: var(--text-control);
		font-weight: var(--weight-medium);
		text-decoration: none;
		transition:
			background-color var(--duration-fast) var(--ease-out),
			color var(--duration-fast) var(--ease-out);
	}

	.item :global(svg) {
		color: var(--text-muted);
	}

	.item:hover {
		background: var(--surface-hover);
		color: var(--text-strong);
		text-decoration: none;
	}

	.item[aria-current='page'] {
		background: var(--surface-selected);
		color: var(--accent-text);
	}

	.item[aria-current='page'] :global(svg) {
		color: var(--accent-text);
	}

	.rail {
		align-items: center;
		padding: var(--space-4) var(--space-2);
	}

	.rail .brand {
		padding: 0;
	}

	.rail .item {
		justify-content: center;
		width: 44px;
		padding: 0;
	}

	.rail nav {
		align-items: center;
	}
</style>
