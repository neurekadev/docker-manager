<script lang="ts">
	// Test harness: a Table over plain rows (default cell rendering).
	import Table from '$lib/ui/Table.svelte';
	import type { Column } from '$lib/ui/table';

	interface Row {
		name: string;
		cpu: number | null;
		image: string;
	}

	let {
		rows,
		layout = 'table',
		maxHeight,
		selectable = false,
		image = {}
	}: {
		rows: Row[];
		layout?: 'table' | 'stacked';
		maxHeight?: string;
		selectable?: boolean;
		/** Extra options of the Image column (maxWidth, truncate, pin, …). */
		image?: Partial<Column<Row>>;
	} = $props();
	let selected = $state<string[]>([]);

	const columns = $derived<Column<Row>[]>([
		{ id: 'name', header: 'Name', sortValue: (r) => r.name, stack: 'title' },
		{ id: 'cpu', header: 'CPU', sortValue: (r) => r.cpu, numeric: true },
		{ id: 'image', header: 'Image', mono: true, ...image }
	]);
</script>

<Table
	label="Services"
	{rows}
	{columns}
	rowKey={(r) => r.name}
	{layout}
	{maxHeight}
	{selectable}
	bind:selected
	rowLabel={(r) => `Select ${r.name}`}
/>
<p data-testid="selected">{selected.join(',')}</p>
