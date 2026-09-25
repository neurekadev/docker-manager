<script lang="ts">
	// Empty state (#22): an invitation to act, e.g. "No stacks on homelab
	// yet." + "Create a stack or import an existing Compose project." with
	// the matching buttons in `actions`.
	import type { Snippet } from 'svelte';
	import type { IconComponent } from '$lib/design/icons';
	import type { TileColor } from '$lib/design/hue';
	import IconTile from './IconTile.svelte';

	interface Props {
		title: string;
		description?: string;
		icon?: IconComponent;
		color?: TileColor;
		actions?: Snippet;
		/** Heading level of the title within the page. */
		level?: 1 | 2 | 3;
		compact?: boolean;
	}

	let {
		title,
		description,
		icon,
		color = 'slate',
		actions,
		level = 2,
		compact = false
	}: Props = $props();
</script>

<div class="empty" class:compact>
	{#if icon}<IconTile {icon} {color} size={compact ? 'md' : 'lg'} />{/if}
	<svelte:element this={`h${level}`} class="title">{title}</svelte:element>
	{#if description}<p class="desc">{description}</p>{/if}
	{#if actions}<div class="actions">{@render actions()}</div>{/if}
</div>

<style>
	.empty {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-3);
		max-width: 460px;
		margin: 0 auto;
		padding: var(--space-12) var(--space-4);
		text-align: center;
	}

	.compact {
		padding: var(--space-6) var(--space-4);
	}

	.title {
		margin-top: var(--space-1);
		font-size: var(--text-section);
		line-height: var(--leading-section);
	}

	.desc {
		color: var(--text-muted);
		font-size: var(--text-control);
		line-height: 22px;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: var(--space-2);
		margin-top: var(--space-2);
	}
</style>
