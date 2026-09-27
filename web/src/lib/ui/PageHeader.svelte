<script lang="ts" module>
	import type { IconComponent } from '$lib/design/icons';

	export interface MetaItem {
		icon?: IconComponent;
		label: string;
		/** Full value on hover (e.g. the real host path of a stack). */
		title?: string;
		mono?: boolean;
		/** A value to copy with a button after the item (e.g. the host path). */
		copy?: { value: string; what: string };
	}
</script>

<script lang="ts">
	// Page header (#22, the mockup's stack header): optional icon tile, the
	// page title (h1, 28/600), a status snippet beside it, a description,
	// an icon-led meta row with thin dividers, and the page actions.
	import type { Snippet } from 'svelte';
	import type { TileColor } from '$lib/design/hue';
	import CopyButton from './CopyButton.svelte';
	import IconTile from './IconTile.svelte';

	interface Props {
		title: string;
		description?: string;
		icon?: IconComponent;
		color?: TileColor;
		meta?: MetaItem[];
		status?: Snippet;
		actions?: Snippet;
		/** Replaces the icon tile (e.g. a template's image icon). */
		media?: Snippet;
	}

	let {
		title,
		description,
		icon,
		color = 'blue',
		meta = [],
		status,
		actions,
		media
	}: Props = $props();
</script>

<header class="page-header">
	{#if media}{@render media()}{:else if icon}<IconTile {icon} {color} size="lg" />{/if}
	<div class="main">
		<div class="title-row">
			<h1>{title}</h1>
			{#if status}{@render status()}{/if}
		</div>
		{#if description}<p class="desc">{description}</p>{/if}
		{#if meta.length}
			<ul class="meta" role="list">
				{#each meta as m (m.label)}
					{@const Icon = m.icon}
					<li title={m.title}>
						{#if Icon}<Icon size={16} strokeWidth={1.75} aria-hidden="true" />{/if}
						<span class:mono={m.mono}>{m.label}</span>
						{#if m.copy}<CopyButton value={m.copy.value} what={m.copy.what} />{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</div>
	{#if actions}<div class="actions">{@render actions()}</div>{/if}
</header>

<style>
	.page-header {
		display: flex;
		align-items: flex-start;
		gap: var(--space-4);
		flex-wrap: wrap;
	}

	.main {
		flex: 1 1 320px;
		min-width: 0;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		flex-wrap: wrap;
	}

	h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.desc {
		margin-top: 2px;
		color: var(--text-muted);
		font-size: var(--text-control);
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		margin: var(--space-3) 0 0;
		color: var(--text-muted);
	}

	.meta li {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 0 var(--space-4);
		border-left: 1px solid var(--border-strong);
	}

	.meta li:first-child {
		padding-left: 0;
		border-left: 0;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	@media (max-width: 767px) {
		h1 {
			font-size: 22px;
			line-height: 28px;
		}

		.meta {
			gap: var(--space-1) 0;
		}
	}
</style>
