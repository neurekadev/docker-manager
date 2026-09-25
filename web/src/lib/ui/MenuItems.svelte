<script lang="ts">
	// Renders menu entries inside a Bits UI menu content (Menu, ContextMenu).
	import { DropdownMenu } from 'bits-ui';
	import { isHeading, isSeparator, type MenuEntry } from './menu';

	let { items }: { items: MenuEntry[] } = $props();
</script>

{#each items as entry, i (i)}
	{#if isSeparator(entry)}
		<DropdownMenu.Separator class="dy-menu-separator" />
	{:else if isHeading(entry)}
		<div class="dy-menu-heading" role="presentation">{entry.heading}</div>
	{:else}
		{@const Icon = entry.icon}
		<DropdownMenu.Item
			class="dy-menu-item {entry.tone === 'danger' ? 'danger' : ''}"
			disabled={entry.disabled}
			textValue={entry.label}
			onSelect={() => entry.onSelect?.()}
		>
			{#snippet child({ props })}
				{#if entry.href && !entry.disabled}
					<a {...props} href={entry.href}>
						{#if Icon}<Icon size={16} strokeWidth={1.75} aria-hidden="true" />{/if}
						<span class="dy-menu-label">{entry.label}</span>
						{#if entry.shortcut}<kbd class="dy-menu-shortcut">{entry.shortcut}</kbd
							>{/if}
					</a>
				{:else}
					<div {...props}>
						{#if Icon}<Icon size={16} strokeWidth={1.75} aria-hidden="true" />{/if}
						<span class="dy-menu-label">{entry.label}</span>
						{#if entry.shortcut}<kbd class="dy-menu-shortcut">{entry.shortcut}</kbd
							>{/if}
					</div>
				{/if}
			{/snippet}
		</DropdownMenu.Item>
	{/if}
{/each}

<style>
	:global(.dy-menu) {
		z-index: var(--z-menu);
		min-width: 200px;
		max-width: min(360px, calc(100vw - 16px));
		padding: var(--space-1);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
		outline: none;
	}

	:global(.dy-menu[data-state='open']) {
		animation: dy-menu-in var(--duration-open) var(--ease-out);
	}

	@keyframes -global-dy-menu-in {
		from {
			opacity: 0;
			transform: translateY(-4px);
		}
		to {
			opacity: 1;
			transform: none;
		}
	}

	:global(.dy-menu-item) {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 32px;
		padding: 6px var(--space-2);
		border-radius: var(--radius-sm);
		color: var(--text-default);
		font-size: var(--text-control);
		line-height: var(--leading-control);
		text-decoration: none;
		cursor: pointer;
		user-select: none;
		outline: none;
	}

	:global(.dy-menu-item:hover) {
		text-decoration: none;
	}

	:global(.dy-menu-item[data-highlighted]) {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	:global(.dy-menu-item.danger) {
		color: var(--danger);
	}

	:global(.dy-menu-item[data-disabled]) {
		opacity: 0.45;
		cursor: not-allowed;
	}

	:global(.dy-menu-label) {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	:global(.dy-menu-shortcut) {
		color: var(--text-muted);
		font-family: var(--font-sans);
		font-size: var(--text-caption);
	}

	:global(.dy-menu-separator) {
		height: 1px;
		margin: var(--space-1) calc(-1 * var(--space-1));
		background: var(--border-subtle);
	}

	:global(.dy-menu-heading) {
		padding: 6px var(--space-2) 2px;
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
	}

	@media (pointer: coarse) {
		:global(.dy-menu-item) {
			min-height: var(--touch-target);
		}
	}
</style>
