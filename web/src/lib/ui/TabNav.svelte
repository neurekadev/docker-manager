<script lang="ts" module>
	export interface TabLink {
		href: string;
		label: string;
		/** A chip after the tab (e.g. "Undeployed changes" beside the tabs). */
		count?: number;
	}
</script>

<script lang="ts">
	// Route tabs (#22 stack detail: Overview · Files · Logs · Terminal ·
	// Revisions · Policies · Activity). Links with aria-current="page"; the
	// URL is the state, so reloads and deep links keep the tab.
	import type { Snippet } from 'svelte';

	interface Props {
		items: TabLink[];
		/** The current path (page.url.pathname). */
		current: string;
		label: string;
		/** Content after the tabs (e.g. an "Undeployed changes" chip). */
		after?: Snippet;
	}

	let { items, current, label, after }: Props = $props();

	function active(href: string): boolean {
		// The first tab is the base path; others match by prefix.
		return href === items[0]?.href ? current === href : current.startsWith(href);
	}
</script>

<div class="tabnav">
	<nav aria-label={label}>
		<ul role="list">
			{#each items as item (item.href)}
				<li>
					<a
						href={item.href}
						class="tab"
						aria-current={active(item.href) ? 'page' : undefined}
					>
						{item.label}
						{#if item.count !== undefined}<span class="count num">{item.count}</span
							>{/if}
					</a>
				</li>
			{/each}
		</ul>
	</nav>
	{#if after}<div class="after">{@render after()}</div>{/if}
</div>

<style>
	.tabnav {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		border-bottom: 1px solid var(--border-subtle);
	}

	nav {
		min-width: 0;
		overflow-x: auto;
		scrollbar-width: none;
	}

	ul {
		display: flex;
		gap: var(--space-1);
		margin: 0;
	}

	.tab {
		position: relative;
		display: inline-flex;
		align-items: center;
		gap: 6px;
		height: 40px;
		padding: 0 var(--space-3);
		border-radius: var(--radius-sm) var(--radius-sm) 0 0;
		color: var(--text-muted);
		font-size: var(--text-control);
		font-weight: var(--weight-medium);
		white-space: nowrap;
	}

	.tab:hover {
		color: var(--text-strong);
		text-decoration: none;
	}

	.tab[aria-current='page'] {
		color: var(--text-strong);
		background: var(--surface-selected);
	}

	.tab[aria-current='page']::after {
		content: '';
		position: absolute;
		left: 0;
		right: 0;
		bottom: -1px;
		height: 2px;
		background: var(--accent);
	}

	.count {
		min-width: 18px;
		padding: 0 5px;
		border-radius: var(--radius-full);
		background: var(--surface-raised);
		font-size: 11px;
		line-height: 18px;
		text-align: center;
	}

	.after {
		flex-shrink: 0;
	}
</style>
