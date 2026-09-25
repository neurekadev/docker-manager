<script lang="ts" module>
	export interface Fact {
		label: string;
		/** Empty values render as a muted dash. */
		value?: string | number | null;
		mono?: boolean;
		href?: string;
		/** Full value on hover (long digests, paths). */
		title?: string;
		/** Secondary text after the value. */
		note?: string;
	}
</script>

<script lang="ts">
	// Label/value pairs of a detail card (container configuration, image
	// config, volume details): a definition list, two columns wide on
	// desktop and stacked on narrow screens.
	interface Props {
		items: Fact[];
		label?: string;
	}

	let { items, label }: Props = $props();
</script>

<dl class="facts" aria-label={label}>
	{#each items as f (f.label)}
		<div class="row">
			<dt>{f.label}</dt>
			<dd class:mono={f.mono} title={f.title}>
				{#if f.value === undefined || f.value === null || f.value === ''}
					<span class="muted">—</span>
				{:else if f.href}
					<a href={f.href}>{f.value}</a>
				{:else}
					{f.value}
				{/if}
				{#if f.note}<span class="note">{f.note}</span>{/if}
			</dd>
		</div>
	{/each}
</dl>

<style>
	.facts {
		display: grid;
		margin: 0;
	}

	.row {
		display: grid;
		grid-template-columns: minmax(120px, 34%) 1fr;
		gap: var(--space-3);
		padding: 7px 0;
		border-top: 1px solid var(--border-subtle);
	}

	.row:first-child {
		border-top: 0;
		padding-top: 0;
	}

	dt {
		min-width: 0;
		color: var(--text-muted);
		overflow-wrap: anywhere;
	}

	dd {
		margin: 0;
		min-width: 0;
		color: var(--text-default);
		overflow-wrap: anywhere;
	}

	dd.mono {
		font-size: 12.5px;
	}

	a {
		color: var(--accent-text);
		text-decoration: none;
	}

	a:hover {
		text-decoration: underline;
	}

	.note {
		margin-left: var(--space-2);
		color: var(--text-muted);
		font-family: var(--font-sans);
		font-size: var(--text-caption);
	}

	@media (max-width: 767px) {
		.row {
			grid-template-columns: 1fr;
			gap: 2px;
		}
	}
</style>
