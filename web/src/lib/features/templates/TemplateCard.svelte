<script lang="ts">
	// One template in a discovery grid (template registry): icon, name,
	// where it comes from, description, tags and the newest version. The
	// whole card is one link; tags inside are buttons that filter the list
	// (onTag). Used for this instance's templates and, later, for templates
	// of added registries.
	import { Badge } from '$lib/ui';
	import TemplateIcon from './TemplateIcon.svelte';

	interface Props {
		href: string;
		name: string;
		description?: string;
		tags?: string[];
		iconUrl?: string | null;
		/** private or public (own templates only). */
		visibility?: string;
		/** The newest version's label; absent while only a draft exists. */
		latest?: string;
		/** Where the template comes from ("This instance", a registry's name). */
		source?: string;
		onTag?: (tag: string) => void;
	}

	let {
		href,
		name,
		description,
		tags = [],
		iconUrl,
		visibility,
		latest,
		source,
		onTag
	}: Props = $props();
</script>

<article class="card">
	<a class="cover" {href} aria-label="Open template {name}"></a>
	<div class="head">
		<TemplateIcon url={iconUrl} />
		<div class="title">
			<h3>{name}</h3>
			{#if source}<span class="source">{source}</span>{/if}
		</div>
		{#if visibility === 'public'}
			<Badge tone="info">Public</Badge>
		{:else if visibility === 'private'}
			<Badge tone="neutral">Private</Badge>
		{/if}
	</div>
	<p class="desc" class:muted={!description}>{description || 'No description.'}</p>
	<footer>
		<div class="tags">
			{#each tags.slice(0, 5) as tag (tag)}
				{#if onTag}
					<button type="button" class="tag" onclick={() => onTag(tag)}>#{tag}</button>
				{:else}
					<span class="tag">#{tag}</span>
				{/if}
			{/each}
			{#if tags.length > 5}<span class="tag more">+{tags.length - 5}</span>{/if}
		</div>
		<span class="version">{latest ? `v${latest}` : 'Draft only'}</span>
	</footer>
</article>

<style>
	.card {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		min-width: 0;
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		transition: border-color 120ms ease;
	}

	.card:hover,
	.card:focus-within {
		border-color: var(--border-strong);
	}

	/* The whole card is the link; tag buttons sit above it. */
	.cover {
		position: absolute;
		inset: 0;
		border-radius: inherit;
	}

	.cover:focus-visible {
		outline: var(--focus-ring);
		outline-offset: var(--focus-offset);
	}

	.head {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
	}

	.title {
		display: flex;
		flex: 1;
		flex-direction: column;
		min-width: 0;
	}

	h3 {
		overflow: hidden;
		color: var(--text-strong);
		font-size: var(--text-body);
		font-weight: var(--weight-medium);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.source {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.desc {
		display: -webkit-box;
		overflow: hidden;
		min-height: 2.8em;
		color: var(--text-default);
		font-size: var(--text-control);
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 2;
		line-clamp: 2;
	}

	footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
		margin-top: auto;
	}

	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		min-width: 0;
	}

	.tag {
		position: relative;
		z-index: 1;
		padding: 1px 6px;
		border: 0;
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
		color: var(--text-muted);
		font: inherit;
		font-size: var(--text-caption);
		cursor: default;
	}

	button.tag {
		cursor: pointer;
	}

	button.tag:hover {
		color: var(--text-strong);
	}

	.version {
		flex-shrink: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-variant-numeric: tabular-nums;
	}
</style>
