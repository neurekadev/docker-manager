<script lang="ts">
	// The first cell of a list row: the name (a link to the detail page when
	// href is set) and a muted secondary line, as in the mockup's services
	// table ("silo-web" / "Web frontend"). With `icon` (a resource type) or
	// `media` (the object's own icon) the row's icon sits before it
	// (IconCell).
	import type { Snippet } from 'svelte';
	import type { TileColor } from '$lib/design/hue';
	import IconCell from './IconCell.svelte';
	import type { ResourceIcon, ResourceKind } from './resourceIcons';

	interface Props {
		name: string;
		href?: string;
		sub?: string;
		mono?: boolean;
		subMono?: boolean;
		/** The row's type icon (resourceIcons.ts). */
		icon?: ResourceKind | ResourceIcon;
		/** Overrides the type icon's colour. */
		iconColor?: TileColor;
		/** The object's own icon instead of its type's. */
		media?: Snippet;
		/** Extra content on the secondary line (badges). */
		extra?: Snippet;
	}

	let {
		name,
		href,
		sub,
		mono = false,
		subMono = false,
		icon,
		iconColor,
		media,
		extra
	}: Props = $props();
</script>

{#snippet content()}
	<div class="name-cell">
		{#if href}
			<a class="name" class:mono {href}>{name}</a>
		{:else}
			<span class="name" class:mono>{name}</span>
		{/if}
		{#if sub || extra}
			<div class="sub">
				{#if sub}<span class:mono={subMono}>{sub}</span>{/if}
				{#if extra}{@render extra()}{/if}
			</div>
		{/if}
	</div>
{/snippet}

{#if icon || media}
	<IconCell {icon} color={iconColor} {media}>{@render content()}</IconCell>
{:else}
	{@render content()}
{/if}

<style>
	.name-cell {
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		overflow-wrap: anywhere;
	}

	a.name:hover {
		color: var(--accent-text);
	}

	.sub {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin-top: 2px;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}
</style>
