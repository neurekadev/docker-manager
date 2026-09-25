<script lang="ts">
	// Card / panel (#22): --surface-panel, 1 px subtle border, 12 px radius,
	// no shadow. With a title it renders a header row (title + actions).
	import type { Snippet } from 'svelte';

	interface Props {
		title?: string;
		/** Heading level of the title (2 by default: sections of a page). */
		level?: 2 | 3;
		subtitle?: string;
		actions?: Snippet;
		/** none: children touch the edges (tables). */
		padding?: 'none' | 'md';
		children: Snippet;
		id?: string;
	}

	let { title, level = 2, subtitle, actions, padding = 'md', children, id }: Props = $props();
</script>

<section class="card" aria-labelledby={title && id ? `${id}-title` : undefined} {id}>
	{#if title || actions}
		<header class="head">
			<div class="titles">
				{#if title}
					<svelte:element
						this={`h${level}`}
						class="title"
						id={id ? `${id}-title` : undefined}
					>
						{title}
					</svelte:element>
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
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-4) var(--space-5) 0;
		min-height: 52px;
	}

	.titles {
		display: flex;
		align-items: baseline;
		gap: var(--space-3);
		min-width: 0;
	}

	.title {
		font-size: var(--text-section);
		line-height: var(--leading-section);
	}

	.subtitle {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.actions {
		display: flex;
		align-items: center;
		gap: var(--space-2);
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
</style>
