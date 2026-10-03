<script lang="ts">
	// Card / panel (#22): --surface-panel, 1 px subtle border, 12 px radius,
	// no shadow. With a title it renders a header row (title + actions) that
	// wraps the actions below the title when they do not fit. The title is
	// always the section size (16 px); headings inside the body use the
	// global `.subsection-title` class (14 px semibold). `info` puts an (i)
	// beside the title for an explanation needed only now and then; a
	// subtitle stays for facts the card always shows.
	import type { Snippet } from 'svelte';
	import InfoTip from './InfoTip.svelte';

	interface Props {
		title?: string;
		/** Heading level of the title (2 by default: sections of a page). */
		level?: 2 | 3;
		subtitle?: string;
		/** An explanation behind an (i) beside the title (InfoTip). */
		info?: string;
		actions?: Snippet;
		/** none: children touch the edges (tables). */
		padding?: 'none' | 'md';
		children: Snippet;
		id?: string;
		/**
		 * The actions take the free width of the header and wrap below the
		 * title when they do not fit (a list's search and filters).
		 */
		stretchActions?: boolean;
	}

	let {
		title,
		level = 2,
		subtitle,
		info,
		actions,
		padding = 'md',
		children,
		id,
		stretchActions = false
	}: Props = $props();
</script>

<section class="card" aria-labelledby={title && id ? `${id}-title` : undefined} {id}>
	{#if title || actions}
		<header class="head" class:stretch={stretchActions}>
			<div class="titles">
				{#if title}
					<span class="title-row">
						<svelte:element
							this={`h${level}`}
							class="title"
							id={id ? `${id}-title` : undefined}
						>
							{title}
						</svelte:element>
						{#if info}<InfoTip text={info} />{/if}
					</span>
				{/if}
				{#if subtitle}<span class="subtitle">{subtitle}</span>{/if}
			</div>
			{#if actions}<div class="actions">{@render actions()}</div>{/if}
		</header>
	{/if}
	<div class="body" class:flush={padding === 'none'}>
		{@render children()}
	</div>
</section>

<style>
	.card {
		min-width: 0;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2) var(--space-3);
		padding: var(--space-4) var(--space-5) 0;
		min-height: 52px;
	}

	/* The subtitle stays beside the title while both fit and moves below
	   it otherwise, so a narrow card never squeezes the title into a
	   column of single words. */
	.titles {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 2px var(--space-3);
		min-width: 0;
	}

	.title-row {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
	}

	.title {
		min-width: 0;
		font-size: var(--text-section);
		line-height: var(--leading-section);
		overflow-wrap: anywhere;
	}

	.subtitle {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.stretch {
		row-gap: var(--space-3);
	}

	.stretch .actions {
		flex: 1 1 auto;
		flex-wrap: wrap;
		justify-content: flex-end;
		min-width: 0;
	}

	.body {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.body.flush {
		padding: var(--space-3) 0 0;
	}

	.head + .body.flush {
		padding-top: var(--space-3);
	}

	@media (max-width: 767px) {
		.head {
			padding: var(--space-4) var(--space-4) 0;
		}

		.body {
			padding: var(--space-4);
		}

		.body.flush {
			padding: var(--space-3) 0 0;
		}
	}
</style>
