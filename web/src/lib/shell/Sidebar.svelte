<script lang="ts">
	// Sidebar (#22): logo lockup, environment switcher, permission-filtered
	// navigation in groups (NAV_GROUPS: small sentence-case labels in the
	// full sidebar, thin dividers in the rail). In rail mode (1024–1279 px
	// or collapsed) only icons show, with tooltips. Items are 36 px tall so
	// every section fits a 900 px high window; the phone drawer (`drawer`)
	// uses 40 px touch targets.
	import type { Snippet } from 'svelte';
	import Tooltip from '$lib/ui/Tooltip.svelte';
	import Logo from './Logo.svelte';
	import { NAV_GROUPS, type NavItem } from './nav';

	interface Props {
		items: NavItem[];
		activeId?: string;
		rail?: boolean;
		/** Inside the navigation drawer (touch-sized items). */
		drawer?: boolean;
		switcher: Snippet;
		onnavigate?: () => void;
	}

	let { items, activeId, rail = false, drawer = false, switcher, onnavigate }: Props = $props();
	const uid = $props.id();
	const groups = $derived(
		NAV_GROUPS.map((g) => ({ ...g, items: items.filter((i) => i.group === g.id) })).filter(
			(g) => g.items.length
		)
	);
</script>

<div class="sidebar" class:rail class:drawer>
	<div class="brand"><Logo mark={rail} /></div>
	<div class="switcher">{@render switcher()}</div>
	<nav aria-label="Main">
		{#each groups as g (g.id)}
			<div class="group">
				{#if g.label && !rail}
					<p class="group-label" id="{uid}-{g.id}">{g.label}</p>
				{/if}
				<ul
					role="list"
					aria-labelledby={g.label && !rail ? `${uid}-${g.id}` : undefined}
					aria-label={g.label && rail ? g.label : undefined}
				>
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
			</div>
		{/each}
	</nav>
</div>

<style>
	.sidebar {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		height: 100%;
		padding: var(--space-3);
		overflow-x: hidden;
		overflow-y: auto;
	}

	.brand {
		min-width: 0;
	}

	nav {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}

	.group {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
	}

	.group-label {
		padding: 0 var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		font-weight: var(--weight-medium);
	}

	ul {
		display: flex;
		flex-direction: column;
		gap: 2px;
		margin: 0;
	}

	.item {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		height: var(--control-height);
		padding: 0 var(--space-3);
		border-radius: var(--radius-md);
		color: var(--text-default);
		font-size: var(--text-control);
		font-weight: var(--weight-medium);
		text-decoration: none;
		white-space: nowrap;
		transition:
			background-color var(--duration-fast) var(--ease-out),
			color var(--duration-fast) var(--ease-out);
	}

	.drawer .item {
		height: var(--touch-target);
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

	@media (pointer: coarse) {
		.item {
			height: var(--touch-target);
		}
	}

	/* Rail: icons only, the groups divided by thin lines. */
	.rail {
		align-items: center;
		padding: var(--space-3) var(--space-2);
	}

	.rail .item {
		justify-content: center;
		width: 44px;
		padding: 0;
	}

	.rail nav {
		align-items: center;
	}

	.rail .group + .group {
		padding-top: var(--space-3);
		border-top: 1px solid var(--border-subtle);
	}
</style>
