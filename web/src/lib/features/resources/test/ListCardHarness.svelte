<script lang="ts">
	// Test harness: a ListCard over plain rows with a static and a row-built
	// select and a switch, the rows it lets through and the no-match state.
	import Box from '@lucide/svelte/icons/box';
	import ListCard from '../ListCard.svelte';
	import NoMatches from '../NoMatches.svelte';
	import {
		applyListFilters,
		distinctOptions,
		isFiltering,
		listSummary,
		switchFilter,
		type ListFilter
	} from '../filters';
	import { ListFilters } from '../list-filters.svelte';

	interface Row {
		name: string;
		state: string;
		driver: string;
		labels?: Record<string, string>;
	}

	let {
		rows,
		storage
	}: { rows: Row[]; storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> } = $props();

	// svelte-ignore state_referenced_locally
	const store = new ListFilters('things', storage);
	const defs = $derived<ListFilter<Row>[]>([
		{
			id: 'status',
			label: 'Status',
			all: 'All statuses',
			options: [
				{ value: 'running', label: 'Running' },
				{ value: 'exited', label: 'Exited' }
			],
			match: (r, v) => r.state === v
		},
		{
			id: 'driver',
			label: 'Driver',
			all: 'All drivers',
			dynamic: true,
			options: distinctOptions(rows.map((r) => r.driver)),
			match: (r, v) => r.driver === v
		},
		switchFilter<Row>('stopped', 'Stopped', 'Only stopped things', (r) => r.state === 'exited')
	]);
	const shown = $derived(applyListFilters(rows, defs, store.state, (r) => [r.name]));
	const filtered = $derived(isFiltering(defs, store.state));
</script>

<ListCard
	title="All things"
	id="things"
	summary={listSummary(shown.length, rows.length, filtered, 'thing', 'things')}
	label="Filter things"
	searchLabel="Search things"
	placeholder="Search by name"
	filters={defs}
	{store}
>
	{#if shown.length}
		<ul aria-label="Things">
			{#each shown as r (r.name)}<li>{r.name}</li>{/each}
		</ul>
	{:else}
		<NoMatches what="things" icon={Box} onclear={() => store.clear()} />
	{/if}
</ListCard>
