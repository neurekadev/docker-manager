<script lang="ts">
	// One scope in the resource tree: a button that selects it (aria-current
	// marks the selection), its rule count, and an optional expander.
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import type { IconComponent } from '$lib/design/icons';
	import type { ScopeNode } from './permissions';

	interface Props {
		node: ScopeNode;
		selected: boolean;
		count?: number;
		icon?: IconComponent;
		depth?: number;
		expanded?: boolean;
		onselect: (node: ScopeNode) => void;
		ontoggle?: () => void;
	}

	let {
		node,
		selected,
		count = 0,
		icon,
		depth = 0,
		expanded,
		onselect,
		ontoggle
	}: Props = $props();
</script>

<div class="row" style:--depth={depth}>
	{#if ontoggle}
		<button
			type="button"
			class="expander"
			aria-expanded={expanded}
			aria-label="{expanded ? 'Collapse' : 'Expand'} {node.label}"
			onclick={ontoggle}
		>
			<ChevronRight size={14} aria-hidden="true" class={expanded ? 'open' : ''} />
		</button>
	{:else}
		<span class="spacer" aria-hidden="true"></span>
	{/if}
	<button
		type="button"
		class="node"
		class:selected
		aria-current={selected ? 'true' : undefined}
		onclick={() => onselect(node)}
	>
		{#if icon}{@const Icon = icon}<Icon size={15} aria-hidden="true" />{/if}
		<span class="label">{node.label}</span>
		{#if node.detail}<span class="detail">{node.detail}</span>{/if}
		{#if count > 0}<span class="count num" aria-label="{count} {count === 1 ? 'rule' : 'rules'}"
				>{count}</span
			>{/if}
	</button>
</div>

<style>
	.row {
		display: flex;
		align-items: center;
		gap: 2px;
		padding-left: calc(var(--depth) * 14px);
	}

	.expander,
	.spacer {
		display: inline-grid;
		place-items: center;
		flex: 0 0 22px;
		width: 22px;
		height: 22px;
	}

	.expander {
		border: 0;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--text-muted);
		cursor: pointer;
	}

	.expander:hover {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.expander :global(svg) {
		transition: transform var(--duration-fast) var(--ease-out);
	}

	.expander :global(svg.open) {
		transform: rotate(90deg);
	}

	.node {
		display: flex;
		flex: 1;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
		min-height: 30px;
		padding: 2px var(--space-2);
		border: 0;
		border-radius: var(--radius-md);
		background: transparent;
		color: var(--text-default);
		text-align: left;
		cursor: pointer;
	}

	.node:hover {
		background: var(--surface-hover);
	}

	.node.selected {
		background: var(--surface-selected);
		color: var(--accent-text);
	}

	.label {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.detail {
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.count {
		margin-left: auto;
		padding: 0 6px;
		border-radius: var(--radius-full);
		background: var(--accent-soft);
		color: var(--accent-text);
		font-size: var(--text-caption);
		line-height: 18px;
	}

	@media (pointer: coarse) {
		.node {
			min-height: var(--touch-target);
		}

		.expander,
		.spacer {
			flex-basis: var(--touch-target);
			width: var(--touch-target);
			height: var(--touch-target);
		}
	}
</style>
