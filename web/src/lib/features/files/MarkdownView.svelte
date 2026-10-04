<script lang="ts">
	// Rendered Markdown preview (README.md, #22), styled like GitHub: the
	// tree from markdown.ts as Svelte elements (no HTML from the file is
	// ever injected). Alerts carry GitHub's icon and colour; `#heading`
	// links scroll to the heading inside the preview; fenced code is
	// highlighted like the editor (HighlightedCode).
	import ImageIcon from '@lucide/svelte/icons/image';
	import Info from '@lucide/svelte/icons/info';
	import Lightbulb from '@lucide/svelte/icons/lightbulb';
	import MessageSquareWarning from '@lucide/svelte/icons/message-square-warning';
	import OctagonAlert from '@lucide/svelte/icons/octagon-alert';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { lexMarkdown } from '$lib/lazy';
	import { Skeleton } from '$lib/ui';
	import HighlightedCode from './HighlightedCode.svelte';
	import { ALERT_TITLES, toBlocks, type AlertKind, type Block, type Inline } from './markdown';

	let { source, label }: { source: string; label: string } = $props();

	let blocks = $state<Block[] | null>(null);
	let failed = $state(false);
	$effect(() => {
		const src = source;
		let current = true;
		lexMarkdown(src)
			.then((tokens) => {
				if (current) blocks = toBlocks(tokens);
			})
			.catch(() => current && (failed = true));
		return () => (current = false);
	});

	const ALERT_ICONS: Record<AlertKind, typeof Info> = {
		note: Info,
		tip: Lightbulb,
		important: MessageSquareWarning,
		warning: TriangleAlert,
		caution: OctagonAlert
	};

	let article = $state<HTMLElement>();
	function jump(e: MouseEvent, target: string) {
		const heading = article?.querySelector(`[data-slug="${CSS.escape(target)}"]`);
		if (!heading) return;
		e.preventDefault();
		heading.scrollIntoView({ block: 'start' });
	}
</script>

{#snippet inline(nodes: Inline[])}
	{#each nodes as n, i (i)}
		{#if n.type === 'text'}{n.text}{:else if n.type === 'code'}<code>{n.text}</code
			>{:else if n.type === 'strong'}<strong>{@render inline(n.children)}</strong
			>{:else if n.type === 'em'}<em>{@render inline(n.children)}</em
			>{:else if n.type === 'del'}<del>{@render inline(n.children)}</del
			>{:else if n.type === 'link'}<a href={n.href} target="_blank" rel="noopener noreferrer"
				>{@render inline(n.children)}</a
			>{:else if n.type === 'anchor'}<a href="#{n.target}" onclick={(e) => jump(e, n.target)}
				>{@render inline(n.children)}</a
			>{:else if n.type === 'image'}{#if n.href}<a
					class="image"
					href={n.href}
					target="_blank"
					rel="noopener noreferrer"
					title="Image: {n.href}"
					><ImageIcon size={14} strokeWidth={1.75} aria-hidden="true" />{n.alt ||
						'Image'}</a
				>{:else}<span class="image"
					><ImageIcon size={14} strokeWidth={1.75} aria-hidden="true" />{n.alt ||
						'Image'}</span
				>{/if}{:else if n.type === 'break'}<br />{/if}
	{/each}
{/snippet}

{#snippet blockList(list: Block[])}
	{#each list as b, i (i)}
		{#if b.type === 'heading'}
			<svelte:element
				this={`h${Math.min(6, b.level + 1)}`}
				class="h{b.level}"
				data-slug={b.slug}>{@render inline(b.children)}</svelte:element
			>
		{:else if b.type === 'paragraph'}
			<p>{@render inline(b.children)}</p>
		{:else if b.type === 'text'}
			{@render inline(b.children)}
		{:else if b.type === 'list'}
			<svelte:element
				this={b.ordered ? 'ol' : 'ul'}
				start={b.ordered && b.start !== 1 ? b.start : undefined}
				class:tasks={b.items.some((it) => it.checked !== null)}
			>
				{#each b.items as item, j (j)}
					<li class:task={item.checked !== null}>
						{#if item.checked !== null}<input
								type="checkbox"
								checked={item.checked}
								disabled
								aria-label={item.checked ? 'Done' : 'Not done'}
							/>{/if}{@render blockList(item.blocks)}
					</li>
				{/each}
			</svelte:element>
		{:else if b.type === 'code'}
			<HighlightedCode text={b.text} language={b.language} />
		{:else if b.type === 'quote'}
			<blockquote>{@render blockList(b.blocks)}</blockquote>
		{:else if b.type === 'alert'}
			{@const Icon = ALERT_ICONS[b.kind]}
			<div class="alert {b.kind}" role="note">
				<p class="alert-title">
					<Icon size={16} strokeWidth={1.75} aria-hidden="true" />{ALERT_TITLES[b.kind]}
				</p>
				{@render blockList(b.blocks)}
			</div>
		{:else if b.type === 'table'}
			<div class="table">
				<table>
					<thead>
						<tr>
							{#each b.header as cell, j (j)}
								<th style:text-align={b.align[j]}>{@render inline(cell)}</th>
							{/each}
						</tr>
					</thead>
					<tbody>
						{#each b.rows as row, r (r)}
							<tr>
								{#each row as cell, j (j)}
									<td style:text-align={b.align[j]}>{@render inline(cell)}</td>
								{/each}
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{:else}
			<hr />
		{/if}
	{/each}
{/snippet}

<article class="markdown" aria-label={label} bind:this={article}>
	{#if failed}
		<p class="muted">The preview could not be loaded. Reload the page to try again.</p>
	{:else if !blocks}
		<Skeleton lines={6} />
	{:else if blocks.length === 0}
		<p class="muted">This file is empty.</p>
	{:else}
		{@render blockList(blocks)}
	{/if}
</article>

<style>
	.markdown {
		max-width: 88ch;
		padding: var(--space-5) var(--space-6);
		color: var(--text-default);
		font-size: var(--text-control);
		line-height: 1.65;
		overflow-wrap: break-word;
	}

	.markdown :global(:where(h2, h3, h4, h5, h6)) {
		margin: var(--space-6) 0 var(--space-3);
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
		line-height: 1.3;
		scroll-margin-top: var(--space-4);
	}

	.markdown > :global(:first-child) {
		margin-top: 0;
	}

	.markdown :global(.h1),
	.markdown :global(.h2) {
		padding-bottom: var(--space-2);
		border-bottom: 1px solid var(--border-subtle);
	}

	.markdown :global(.h1) {
		font-size: 24px;
	}

	.markdown :global(.h2) {
		font-size: 19px;
	}

	.markdown :global(.h3) {
		font-size: var(--text-section);
	}

	.markdown :global(.h4),
	.markdown :global(.h5) {
		font-size: var(--text-control);
	}

	.markdown :global(.h6) {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.markdown :global(:where(p, ul, ol, blockquote, pre, .table, .alert)) {
		margin: 0 0 var(--space-4);
	}

	.markdown :global(:where(ul, ol)) {
		padding-left: 2em;
	}

	.markdown :global(li > :where(ul, ol)) {
		margin-bottom: 0;
	}

	.markdown :global(li + li) {
		margin-top: 4px;
	}

	.markdown :global(li > p) {
		margin-bottom: var(--space-2);
	}

	.markdown :global(ul.tasks) {
		padding-left: 1.25em;
	}

	.markdown :global(li.task) {
		list-style: none;
	}

	.markdown :global(li.task > input) {
		margin: 0 0.4em 0.2em -1.25em;
		vertical-align: middle;
		accent-color: var(--accent);
	}

	.markdown :global(code) {
		padding: 1px 5px;
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
		color: var(--code-string);
		font-family: var(--font-mono);
		font-size: 12.5px;
	}

	.markdown :global(pre) {
		padding: var(--space-3) var(--space-4);
		overflow-x: auto;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
		line-height: 20px;
	}

	.markdown :global(pre code) {
		padding: 0;
		background: none;
		color: var(--text-default);
	}

	/* Fenced code, coloured like the editor (codemirror-theme.ts). */
	.markdown :global(:where(.tok-propertyName, .tok-attributeName)) {
		color: var(--code-key);
	}
	.markdown :global(:where(.tok-string, .tok-string2)) {
		color: var(--code-string);
	}
	.markdown :global(:where(.tok-number, .tok-bool, .tok-atom, .tok-literal)) {
		color: var(--code-number);
	}
	.markdown :global(:where(.tok-url, .tok-link, .tok-invalid)) {
		color: var(--code-url);
	}
	.markdown :global(.tok-comment) {
		color: var(--code-comment);
		font-style: italic;
	}
	.markdown :global(:where(.tok-punctuation, .tok-operator, .tok-meta)) {
		color: var(--code-punctuation);
	}
	.markdown :global(:where(.tok-keyword, .tok-typeName, .tok-labelName)) {
		color: var(--tile-violet-fg);
	}
	.markdown :global(.tok-inserted) {
		color: var(--ok);
	}
	.markdown :global(.tok-deleted) {
		color: var(--danger);
	}

	.markdown :global(blockquote) {
		padding: 0 var(--space-4);
		border-left: 3px solid var(--border-strong);
		color: var(--text-muted);
	}

	.markdown :global(blockquote > :last-child),
	.markdown :global(.alert > :last-child) {
		margin-bottom: 0;
	}

	.markdown :global(.alert) {
		--alert: var(--info);
		padding: var(--space-2) var(--space-4);
		border-left: 3px solid var(--alert);
	}

	.markdown :global(.alert.tip) {
		--alert: var(--ok);
	}
	.markdown :global(.alert.important) {
		--alert: var(--tile-violet-fg);
	}
	.markdown :global(.alert.warning) {
		--alert: var(--warn);
	}
	.markdown :global(.alert.caution) {
		--alert: var(--danger);
	}

	.markdown :global(.alert-title) {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		margin-bottom: var(--space-1);
		color: var(--alert);
		font-weight: var(--weight-medium);
	}

	.markdown :global(.table) {
		overflow-x: auto;
	}

	.markdown :global(table) {
		border-collapse: collapse;
	}

	.markdown :global(:where(th, td)) {
		padding: 6px 13px;
		border: 1px solid var(--border-strong);
	}

	.markdown :global(th) {
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
	}

	.markdown :global(tbody tr:nth-child(2n)) {
		background: var(--surface-raised);
	}

	.markdown :global(hr) {
		height: 3px;
		margin: var(--space-5) 0;
		border: 0;
		background: var(--border-subtle);
	}

	.markdown :global(a) {
		color: var(--accent-text);
	}

	.markdown :global(.image) {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		color: var(--text-muted);
		vertical-align: middle;
	}

	.markdown :global(a.image) {
		color: var(--accent-text);
	}

	.muted {
		color: var(--text-muted);
	}
</style>
