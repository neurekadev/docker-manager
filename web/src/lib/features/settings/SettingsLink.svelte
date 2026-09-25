<script lang="ts">
	// A settings area on the overview: icon tile, title and its current
	// state, the whole card a link.
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import type { TileColor } from '$lib/design/hue';
	import type { IconComponent } from '$lib/design/icons';
	import { IconTile } from '$lib/ui';

	let {
		href,
		icon,
		color = 'blue',
		title,
		text
	}: {
		href: string;
		icon: IconComponent;
		color?: TileColor;
		title: string;
		text: string;
	} = $props();
</script>

<a class="link" {href}>
	<IconTile {icon} {color} />
	<span class="body">
		<span class="title">{title}</span>
		<span class="text">{text}</span>
	</span>
	<ChevronRight size={16} aria-hidden="true" class="chev" />
</a>

<style>
	.link {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		color: var(--text-default);
		text-decoration: none;
	}

	.link:hover {
		border-color: var(--border-strong);
		background: var(--surface-raised);
	}

	.body {
		display: grid;
		flex: 1;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
	}

	.text {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.link :global(.chev) {
		color: var(--text-muted);
	}
</style>
