<script lang="ts">
	// Icon tile (#22): the mockup's rounded square with a translucent tint and
	// a saturated icon. Colour is the resource type's category (TileColor,
	// RESOURCE_ICONS in $lib/features/common/resourceIcons). Decorative: the
	// adjacent text names the thing. Sizes live in CSS classes (not inline
	// styles) so a container can shrink a tile, e.g. the compact KPI card.
	// `xs` is the row icon before a name in lists (IconCell).
	import { tileStyle, type TileColor } from '$lib/design/hue';
	import type { IconComponent } from '$lib/design/icons';

	interface Props {
		icon: IconComponent;
		color?: TileColor;
		size?: 'xs' | 'sm' | 'md' | 'lg';
	}

	let { icon: Icon, color = 'blue', size = 'md' }: Props = $props();
	const iconPx = $derived({ xs: 14, sm: 18, md: 20, lg: 24 }[size]);
</script>

<span class="tile {size}" style={tileStyle(color)} data-color={color} aria-hidden="true">
	<Icon size={iconPx} strokeWidth={1.75} />
</span>

<style>
	.tile {
		display: inline-grid;
		place-items: center;
		flex-shrink: 0;
		border-radius: var(--radius-md);
		background: var(--tile-bg);
		color: var(--tile-fg);
		box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--tile-fg) 14%, transparent);
	}

	.xs {
		width: 24px;
		height: 24px;
		border-radius: var(--radius-sm);
	}

	.sm {
		width: 32px;
		height: 32px;
	}

	.md {
		width: 40px;
		height: 40px;
	}

	.lg {
		width: 48px;
		height: 48px;
	}
</style>
