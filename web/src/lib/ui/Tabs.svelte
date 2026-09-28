<script lang="ts" module>
	export interface TabItem {
		id: string;
		label: string;
		/** A count after the label (e.g. failed items). */
		count?: number;
		disabled?: boolean;
	}
</script>

<script lang="ts">
	// In-page tabs with panels (#22; Bits UI Tabs: roving focus, arrow keys,
	// Home/End). Route-level tabs (stack detail) use TabNav instead.
	import type { Snippet } from 'svelte';
	import { Tabs } from 'bits-ui';

	interface Props {
		items: TabItem[];
		value?: string;
		label: string;
		/** Renders the active panel's content. */
		panel: Snippet<[string]>;
		onchange?: (id: string) => void;
	}

	let { items, value = $bindable(items[0]?.id ?? ''), label, panel, onchange }: Props = $props();
</script>

<Tabs.Root bind:value onValueChange={(v) => onchange?.(v)} class="dy-tabs">
	<Tabs.List class="dy-tab-list" aria-label={label}>
		{#each items as item (item.id)}
			<Tabs.Trigger value={item.id} disabled={item.disabled} class="dy-tab">
				{item.label}
				{#if item.count !== undefined}<span class="dy-tab-count num">{item.count}</span
					>{/if}
			</Tabs.Trigger>
		{/each}
	</Tabs.List>
	{#each items as item (item.id)}
		<Tabs.Content value={item.id} class="dy-tab-panel">
			{#if value === item.id}{@render panel(item.id)}{/if}
		</Tabs.Content>
	{/each}
</Tabs.Root>

<style>
	/* Sideways only: a vertical swipe on the tabs scrolls the page instead of
	   bouncing the row. The baseline is an inset line and the underline sits
	   on it (bottom: 0), so nothing overflows the list vertically. */
	:global(.dy-tab-list) {
		display: flex;
		gap: var(--space-1);
		box-shadow: inset 0 -1px 0 var(--border-subtle);
		overflow-x: auto;
		overflow-y: hidden;
		overscroll-behavior-x: contain;
		scrollbar-width: none;
	}

	:global(.dy-tab) {
		position: relative;
		display: inline-flex;
		align-items: center;
		gap: 6px;
		height: 40px;
		padding: 0 var(--space-3);
		border: 0;
		border-radius: var(--radius-sm) var(--radius-sm) 0 0;
		background: transparent;
		color: var(--text-muted);
		font-size: var(--text-control);
		font-weight: var(--weight-medium);
		white-space: nowrap;
	}

	:global(.dy-tab:hover:not([data-disabled])) {
		color: var(--text-strong);
	}

	:global(.dy-tab[data-state='active']) {
		color: var(--text-strong);
		background: var(--surface-selected);
	}

	:global(.dy-tab[data-state='active'])::after {
		content: '';
		position: absolute;
		left: 0;
		right: 0;
		bottom: 0;
		height: 2px;
		background: var(--accent);
	}

	:global(.dy-tab[data-disabled]) {
		opacity: 0.45;
	}

	:global(.dy-tab-count) {
		min-width: 18px;
		padding: 0 5px;
		border-radius: var(--radius-full);
		background: var(--surface-raised);
		color: var(--text-default);
		font-size: 11px;
		line-height: 18px;
		text-align: center;
	}

	:global(.dy-tab-panel) {
		padding-top: var(--space-4);
		outline: none;
	}
</style>
