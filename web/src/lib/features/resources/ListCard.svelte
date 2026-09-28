<script lang="ts" generics="T">
	// The card of a list page (#22; containers, images, volumes, networks,
	// stacks, jobs, schedules): "All containers" and the count as its
	// header, the search, the select filters and the switches built into
	// the header (wrapping below the title when they do not fit), and
	// "Clear filters" while any of them is set. The state lives in a
	// ListFilters store (kept per list and browser tab); the page filters
	// its rows with the same filters (applyListFilters).
	import X from '@lucide/svelte/icons/x';
	import type { Snippet } from 'svelte';
	import { Button, Card, Select, Switch, TextField } from '$lib/ui';
	import {
		SWITCH_ON,
		activeValue,
		isFiltering,
		selectOptions,
		visibleFilters,
		type ListFilter
	} from './filters';
	import type { ListFilters } from './list-filters.svelte';

	interface Props {
		/** "All containers". */
		title: string;
		/** Card ID (its heading is labelled by it). */
		id: string;
		/** "12 of 40 containers", shown next to the title and announced. */
		summary?: string;
		/** Name of the search region, e.g. "Filter containers". */
		label: string;
		searchLabel: string;
		placeholder: string;
		filters: ListFilter<T>[];
		store: ListFilters;
		children: Snippet;
	}

	let { title, id, summary, label, searchLabel, placeholder, filters, store, children }: Props =
		$props();

	let search = $state<HTMLInputElement | null>(null);
	const shown = $derived(visibleFilters(filters, store.state));
	const selects = $derived(shown.filter((f) => f.kind !== 'switch'));
	const switches = $derived(shown.filter((f) => f.kind === 'switch'));
	const filtering = $derived(isFiltering(filters, store.state));

	function clear() {
		store.clear();
		search?.focus();
	}
</script>

<Card padding="none" {title} {id} subtitle={summary} stretchActions>
	{#snippet actions()}
		<div class="filters" role="search" aria-label={label}>
			<div class="control search">
				<TextField
					label={searchLabel}
					hideLabel
					type="search"
					{placeholder}
					bind:value={() => store.q, (v) => (store.q = v)}
					bind:ref={search}
					autocomplete="off"
				/>
			</div>
			{#each selects as f (f.id)}
				<div class="control">
					{#if f.kind === 'text'}
						<TextField
							label={f.label}
							hideLabel
							mono
							placeholder={f.all}
							title={f.label}
							bind:value={() => store.get(f.id), (v) => store.set(f.id, v)}
							autocomplete="off"
							spellcheck="false"
						/>
					{:else}
						<Select
							label={f.label}
							hideLabel
							title={f.label}
							options={selectOptions(f, store.get(f.id))}
							bind:value={
								() => activeValue(f, store.get(f.id)), (v) => store.set(f.id, v)
							}
						/>
					{/if}
				</div>
			{/each}
			{#each switches as f (f.id)}
				<div class="toggle" title={f.all}>
					<Switch
						label={f.label}
						checked={activeValue(f, store.get(f.id)) === SWITCH_ON}
						onchange={(on) => store.set(f.id, on ? SWITCH_ON : '')}
					/>
				</div>
			{/each}
			{#if filtering}
				<Button variant="ghost" icon={X} onclick={clear}>Clear filters</Button>
			{/if}
		</div>
	{/snippet}
	<p class="sr-only" role="status">{summary ?? ''}</p>
	{@render children()}
</Card>

<style>
	.filters {
		display: flex;
		flex: 1 1 auto;
		flex-wrap: wrap;
		align-items: center;
		justify-content: flex-end;
		gap: var(--space-2);
		min-width: 0;
	}

	.control {
		flex: 0 1 180px;
		min-width: 140px;
	}

	.control.search {
		flex: 0 1 240px;
	}

	.toggle {
		display: inline-flex;
		align-items: center;
		min-height: var(--control-height);
		padding: 0 var(--space-1);
		white-space: nowrap;
	}

	@media (max-width: 767px) {
		.control,
		.control.search {
			flex: 1 1 140px;
		}

		.control.search {
			flex-basis: 100%;
		}
	}
</style>
