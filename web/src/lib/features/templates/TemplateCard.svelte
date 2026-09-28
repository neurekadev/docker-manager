<script lang="ts">
	// One template in a discovery grid (template registry): icon, name,
	// where it comes from, description, tags and the newest version. The
	// whole card is one link (href) or, in a picker, one button (onselect:
	// the create-from-template dialog chooses the template without leaving
	// the dialog). Tags are Chips; with onTag they are buttons above the
	// card that filter the list. Used for this instance's templates, those
	// of added sources and the public registry page.
	import { Badge, Chip } from '$lib/ui';
	import { versionTitle } from './model';
	import TemplateIcon from './TemplateIcon.svelte';

	interface Props {
		/** The template's page (the public registry page has none). */
		href?: string;
		/** Picker mode: the whole card is a button that chooses the template. */
		onselect?: () => void;
		name: string;
		description?: string;
		tags?: string[];
		iconUrl?: string | null;
		/** private or public (own templates only). */
		visibility?: string;
		/** The newest version's label; absent while only a draft exists. */
		latest?: string;
		/** Where the template comes from ("This instance", a source's name). */
		source?: string;
		onTag?: (tag: string) => void;
	}

	let {
		href,
		onselect,
		name,
		description,
		tags = [],
		iconUrl,
		visibility,
		latest,
		source,
		onTag
	}: Props = $props();

	// Tag buttons would sit inside the picker's button; picker tags stay static.
	const tagAction = $derived(onselect ? undefined : onTag);
</script>

<article class="card" class:interactive={!!href || !!onselect}>
	{#if onselect}
		<button type="button" class="cover" aria-label="Use template {name}" onclick={onselect}
		></button>
	{:else if href}
		<a class="cover" {href} aria-label="Open template {name}"></a>
	{/if}
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
		{#if tags.length}
			<ul class="tags" class:above={!!tagAction} aria-label="Tags">
				{#each tags.slice(0, 5) as tag (tag)}
					<li>
						{#if tagAction}
							<Chip
								size="sm"
								label={tag}
								title="Show templates tagged {tag}"
								onclick={() => tagAction(tag)}
							/>
						{:else}
							<Chip size="sm" label={tag} />
						{/if}
					</li>
				{/each}
				{#if tags.length > 5}<li><Chip size="sm" label="+{tags.length - 5}" /></li>{/if}
			</ul>
		{/if}
		<span class="version">{versionTitle(latest)}</span>
	</footer>
</article>

<style>
	.card {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		min-width: 0;
		height: 100%;
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		transition: border-color var(--duration-fast) var(--ease-out);
	}

	.card.interactive:hover,
	.card.interactive:focus-within {
		border-color: var(--border-strong);
	}

	/* The whole card is the link or button; tag chips sit above it. */
	.cover {
		position: absolute;
		inset: 0;
		z-index: 0;
		width: 100%;
		padding: 0;
		border: 0;
		border-radius: inherit;
		background: transparent;
		cursor: pointer;
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
		margin: 0;
		padding: 0;
		list-style: none;
	}

	/* Clickable tags stay above the cover link. */
	.tags.above li {
		position: relative;
		z-index: 1;
	}

	.version {
		flex-shrink: 0;
		margin-left: auto;
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-variant-numeric: tabular-nums;
	}
</style>
