<script lang="ts">
	// Rendered Markdown preview (README.md, #22): the tree from markdown.ts
	// as Svelte elements. No HTML from the file is ever injected.
	import { parseMarkdown, type Inline } from './markdown';

	let { source, label }: { source: string; label: string } = $props();
	const blocks = $derived(parseMarkdown(source));
</script>

{#snippet inline(nodes: Inline[])}
	{#each nodes as n, i (i)}
		{#if n.type === 'text'}{n.text}{:else if n.type === 'code'}<code>{n.text}</code
			>{:else if n.type === 'strong'}<strong>{@render inline(n.children)}</strong
			>{:else if n.type === 'em'}<em>{@render inline(n.children)}</em
			>{:else if n.type === 'link'}<a href={n.href} target="_blank" rel="noopener noreferrer"
				>{@render inline(n.children)}</a
			>{/if}
	{/each}
{/snippet}

<article class="markdown" aria-label={label}>
	{#each blocks as b, i (i)}
		{#if b.type === 'heading'}
			<svelte:element this={`h${Math.min(6, b.level + 1)}`} class="h{b.level}"
				>{@render inline(b.children)}</svelte:element
			>
		{:else if b.type === 'paragraph'}
			<p>{@render inline(b.children)}</p>
		{:else if b.type === 'list'}
			<svelte:element this={b.ordered ? 'ol' : 'ul'}>
				{#each b.items as item, j (j)}<li>{@render inline(item)}</li>{/each}
			</svelte:element>
		{:else if b.type === 'code'}
			<pre><code>{b.text}</code></pre>
		{:else if b.type === 'quote'}
			<blockquote>{@render inline(b.children)}</blockquote>
		{:else}
			<hr />
		{/if}
	{:else}
		<p class="muted">This file is empty.</p>
	{/each}
</article>

<style>
	.markdown {
		max-width: 76ch;
		padding: var(--space-5) var(--space-6);
		color: var(--text-default);
		font-size: var(--text-control);
		line-height: 1.65;
	}

	.markdown :global(:where(h2, h3, h4, h5, h6)) {
		margin: var(--space-5) 0 var(--space-2);
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
	}

	.markdown :global(.h1) {
		margin-top: 0;
		font-size: 22px;
		line-height: 30px;
	}

	.markdown :global(.h2) {
		font-size: var(--text-section);
	}

	.markdown :global(.h3),
	.markdown :global(.h4),
	.markdown :global(.h5),
	.markdown :global(.h6) {
		font-size: var(--text-control);
	}

	p,
	ul,
	ol,
	blockquote,
	pre {
		margin: 0 0 var(--space-3);
	}

	ul,
	ol {
		padding-left: 22px;
	}

	li + li {
		margin-top: 2px;
	}

	code {
		padding: 1px 5px;
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
		color: var(--code-string);
		font-family: var(--font-mono);
		font-size: 12.5px;
	}

	pre {
		padding: var(--space-3);
		overflow-x: auto;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
	}

	pre code {
		padding: 0;
		background: none;
		color: var(--text-default);
	}

	blockquote {
		padding-left: var(--space-3);
		border-left: 2px solid var(--border-strong);
		color: var(--text-muted);
	}

	hr {
		margin: var(--space-4) 0;
		border: 0;
		border-top: 1px solid var(--border-subtle);
	}

	a {
		color: var(--accent-text);
	}
</style>
