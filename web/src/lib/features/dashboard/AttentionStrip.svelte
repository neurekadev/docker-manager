<script lang="ts" module>
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import type { AttentionItem } from './totals';

	/** Sets the target list's filters (kept per list and tab) before the link is followed. */
	export function presetFilters(item: Pick<AttentionItem, 'filters'>) {
		if (!item.filters) return;
		const f = new ListFilters(item.filters.list);
		f.clear();
		for (const [id, value] of Object.entries(item.filters.values)) f.set(id, value);
	}
</script>

<script lang="ts">
	// "Needs attention" on the dashboard (#22 polish): one line per problem
	// (offline environments, failed jobs, containers not running, undeployed
	// changes, available updates), each a link to the list that shows it,
	// filtered where the list keeps filters. Nothing to report: one calm
	// line instead of an empty card.
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';

	let { items }: { items: AttentionItem[] } = $props();
</script>

{#if items.length}
	<section class="attention" aria-labelledby="attention-title">
		<h2 id="attention-title" class="subsection-title">Needs attention</h2>
		<ul role="list">
			{#each items as item (item.id)}
				<li>
					<a href={item.href} class={item.tone} onclick={() => presetFilters(item)}>
						<span class="dot" aria-hidden="true"></span>
						<span class="label">{item.label}</span>
						<ChevronRight size={14} strokeWidth={1.75} aria-hidden="true" />
					</a>
				</li>
			{/each}
		</ul>
	</section>
{:else}
	<p class="calm" role="status">
		<CircleCheck size={16} strokeWidth={1.75} aria-hidden="true" /> Everything is running.
	</p>
{/if}

<style>
	.attention {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-4);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	ul {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	a {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 32px;
		padding: 0 var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-full);
		background: var(--surface-raised);
		color: var(--text-default);
		text-decoration: none;
		transition: border-color var(--duration-fast) var(--ease-out);
	}

	a:hover {
		border-color: var(--border-strong);
		color: var(--text-strong);
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
	}

	.danger .dot {
		background: var(--danger);
	}

	.warn .dot {
		background: var(--warn);
	}

	.offline .dot {
		background: var(--offline);
	}

	.calm {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--text-muted);
	}

	.calm :global(svg) {
		color: var(--ok);
	}

	@media (pointer: coarse) {
		a {
			min-height: var(--touch-target);
		}
	}
</style>
