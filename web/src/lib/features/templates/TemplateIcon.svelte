<script lang="ts">
	// A template's icon (template registry): the uploaded image in the icon
	// tile's frame, or the template glyph when there is none (or it fails to
	// load). Icons are only ever rendered with <img>, which runs no scripts;
	// the URL carries the icon's hash, so a changed icon is a new URL.
	// Decorative: the adjacent text names the template.
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import { IconTile } from '$lib/ui';

	interface Props {
		url?: string | null;
		size?: 'sm' | 'md' | 'lg';
	}

	let { url, size = 'md' }: Props = $props();
	let failed = $state<string | null>(null);
	const show = $derived(!!url && failed !== url);
</script>

{#if show}
	<span class="frame {size}" aria-hidden="true">
		<img
			src={url}
			alt=""
			loading="lazy"
			decoding="async"
			onerror={() => (failed = url ?? null)}
		/>
	</span>
{:else}
	<IconTile icon={LayoutTemplate} color="violet" {size} />
{/if}

<style>
	.frame {
		display: inline-grid;
		place-items: center;
		flex-shrink: 0;
		overflow: hidden;
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		box-shadow: inset 0 0 0 1px var(--border-subtle);
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

	img {
		width: 78%;
		height: 78%;
		object-fit: contain;
	}
</style>
