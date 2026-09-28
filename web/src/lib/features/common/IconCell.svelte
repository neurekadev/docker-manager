<script lang="ts">
	// A list row's name with its type icon before it (#22): a small tile in
	// the resource type's colour (resourceIcons.ts) in a fixed 24 px slot,
	// then the name cell's content. `media` replaces the tile with an
	// object's own icon (a stack's StackIcon size="xs"). The icon is
	// decorative: the name stays the row's accessible label and link.
	import type { Snippet } from 'svelte';
	import type { TileColor } from '$lib/design/hue';
	import { IconTile } from '$lib/ui';
	import { resourceIcon, type ResourceIcon, type ResourceKind } from './resourceIcons';

	interface Props {
		/** The resource type (its icon and colour), or an explicit icon and colour. */
		icon?: ResourceKind | ResourceIcon;
		/** Overrides the type's colour (an offline environment's slate). */
		color?: TileColor;
		/** The object's own icon instead of its type's. */
		media?: Snippet;
		children: Snippet;
	}

	let { icon, color, media, children }: Props = $props();
	const tile = $derived(typeof icon === 'string' ? resourceIcon(icon) : icon);
</script>

<div class="icon-cell">
	<span class="slot">
		{#if media}{@render media()}{:else if tile}<IconTile
				icon={tile.icon}
				color={color ?? tile.color}
				size="xs"
			/>{/if}
	</span>
	<div class="body">{@render children()}</div>
</div>

<style>
	.icon-cell {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
	}

	.slot {
		display: inline-grid;
		place-items: center;
		flex: 0 0 24px;
		width: 24px;
		height: 24px;
	}

	.body {
		flex: 1 1 auto;
		min-width: 0;
	}
</style>
