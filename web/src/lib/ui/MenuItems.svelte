<script lang="ts">
	// Renders menu entries inside a Bits UI menu content (Menu, ContextMenu).
	// An item's description shows as a muted line under its label and is
	// its accessible description (the label alone stays its name).
	import { DropdownMenu } from 'bits-ui';
	import { isHeading, isSeparator, type MenuEntry, type MenuItem } from './menu';

	let { items }: { items: MenuEntry[] } = $props();
	const uid = $props.id();

	function described(entry: MenuItem, i: number): Record<string, string> {
		if (!entry.description) return {};
		return {
			'aria-labelledby': `${uid}-${i}-label`,
			'aria-describedby': `${uid}-${i}-description`
		};
	}
</script>

{#snippet content(entry: MenuItem, i: number)}
	{@const Icon = entry.icon}
	{#if Icon}<Icon size={16} strokeWidth={1.75} aria-hidden="true" />{/if}
	{#if entry.description}
		<span class="dy-menu-text">
			<span class="dy-menu-label" id="{uid}-{i}-label">{entry.label}</span>
			<span class="dy-menu-description" id="{uid}-{i}-description">{entry.description}</span>
		</span>
	{:else}
		<span class="dy-menu-label">{entry.label}</span>
	{/if}
	{#if entry.shortcut}<kbd class="dy-menu-shortcut">{entry.shortcut}</kbd>{/if}
{/snippet}

{#each items as entry, i (i)}
	{#if isSeparator(entry)}
		<DropdownMenu.Separator class="dy-menu-separator" />
	{:else if isHeading(entry)}
		<div class="dy-menu-heading" role="presentation">{entry.heading}</div>
	{:else}
		<DropdownMenu.Item
			class="dy-menu-item {entry.tone === 'danger' ? 'danger' : ''} {entry.description
				? 'described'
				: ''}"
			disabled={entry.disabled}
			textValue={entry.label}
			onSelect={() => entry.onSelect?.()}
		>
			{#snippet child({ props })}
				{#if entry.href && !entry.disabled}
					<a {...props} {...described(entry, i)} href={entry.href}>
						{@render content(entry, i)}
					</a>
				{:else}
					<div {...props} {...described(entry, i)}>
						{@render content(entry, i)}
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

	/* A described item: label over its description; when disabled only the
	   label and icon dim, so the reason stays readable. */
	:global(.dy-menu-item.described) {
		align-items: flex-start;
	}
	:global(.dy-menu-item.described > svg) {
		margin-top: 2px;
	}
	:global(.dy-menu-item.described[data-disabled]) {
		opacity: 1;
	}
	:global(.dy-menu-item.described[data-disabled] .dy-menu-label),
	:global(.dy-menu-item.described[data-disabled] > svg) {
		opacity: 0.45;
	}

	:global(.dy-menu-text) {
		display: flex;
		flex: 1;
		flex-direction: column;
		min-width: 0;
	}

	:global(.dy-menu-description) {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		white-space: normal;
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
