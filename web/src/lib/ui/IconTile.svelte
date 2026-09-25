<script lang="ts">
	// Icon tile (#22): the mockup's rounded square with a translucent tint and
	// a saturated icon. Colour is a category (TileColor) or a service hue
	// (serviceIdentity / serviceHue in $lib/design/hue). Decorative: the
	// adjacent text names the thing.
	import { tileStyle, type TileColor } from '$lib/design/hue';
	import type { IconComponent } from '$lib/design/icons';

	interface Props {
		icon: IconComponent;
		color?: TileColor;
		size?: 'sm' | 'md' | 'lg';
	}

	let { icon: Icon, color = 'blue', size = 'md' }: Props = $props();
	const px = $derived({ sm: 32, md: 40, lg: 48 }[size]);
	const iconPx = $derived({ sm: 18, md: 20, lg: 24 }[size]);
</script>

<span
	class="tile"
	style="{tileStyle(color)} width: {px}px; height: {px}px;"
	data-color={color}
	aria-hidden="true"
>
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
</style>
