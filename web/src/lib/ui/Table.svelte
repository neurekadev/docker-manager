<script lang="ts" generics="T">
	// Data table (#22): sortable headers (aria-sort), selectable rows, sticky
	// header, stacked row cards below 768 px, and windowed rendering past
	// `virtualizeAfter` rows. Columns may cap and truncate their content
	// (`maxWidth`, `truncate`, `title`) and pin to the right edge
	// (`pin: 'end'`) while the table scrolls sideways. Without rows and
	// without an `empty` snippet it shows "Nothing here yet.". Columns and
	// helpers: ./table.ts.
	import type { Snippet } from 'svelte';
	import { MediaQuery } from 'svelte/reactivity';
	import ArrowDown from '@lucide/svelte/icons/arrow-down';
	import ArrowUp from '@lucide/svelte/icons/arrow-up';
	import ChevronsUpDown from '@lucide/svelte/icons/chevrons-up-down';
	import Checkbox from './Checkbox.svelte';
	import {
		cellStyle,
		cellTitle,
		nextSort,
		selectionState,
		sortRows,
		toggleAll,
		virtualWindow,
		type Column,
		type SortState
	} from './table';

	interface Props {
		rows: T[];
		columns: Column<T>[];
		rowKey: (row: T) => string;
		/** Accessible name of the table (e.g. "Services of Silo"). */
		label: string;
		sort?: SortState | null;
		/** Rows arrive sorted by the server: only report sort changes. */
		manualSort?: boolean;
		onsort?: (sort: SortState) => void;
		selectable?: boolean;
		selected?: string[];
		/** Name of a row for its selection checkbox ("Select silo-web"). */
		rowLabel?: (row: T) => string;
		/** Keys of rows that just changed (live updates pulse them once). */
		changed?: ReadonlySet<string>;
		/** Scroll container height; enables the sticky header and windowing. */
		maxHeight?: string;
		virtualizeAfter?: number;
		rowHeight?: number;
		/** Force a layout (tests); default follows the viewport. */
		layout?: 'auto' | 'table' | 'stacked';
		empty?: Snippet;
	}

	let {
		rows,
		columns,
		rowKey,
		label,
		sort = $bindable(null),
		manualSort = false,
		onsort,
		selectable = false,
		selected = $bindable([]),
		rowLabel,
		changed,
		maxHeight,
		virtualizeAfter = 500,
		rowHeight = 48,
		layout = 'auto',
		empty
	}: Props = $props();

	const narrow = new MediaQuery('max-width: 767px');
	const stacked = $derived(layout === 'stacked' || (layout === 'auto' && narrow.current));

	const sorted = $derived(manualSort ? rows : sortRows(rows, columns, sort));
	const keys = $derived(sorted.map(rowKey));
	const headerState = $derived(selectionState(keys, selected));
	const colCount = $derived(columns.length + (selectable ? 1 : 0));

	// Windowing: only when the list is long and has its own scroll container.
	const virtual = $derived(!stacked && sorted.length > virtualizeAfter);
	let scrollTop = $state(0);
	let viewport = $state(600);
	const win = $derived(
		virtual
			? virtualWindow(scrollTop, viewport, rowHeight, sorted.length)
			: { start: 0, end: sorted.length, padTop: 0, padBottom: 0 }
	);
	const visibleRows = $derived(sorted.slice(win.start, win.end));

	function onScroll(e: Event) {
		const el = e.currentTarget as HTMLElement;
		scrollTop = el.scrollTop;
		viewport = el.clientHeight;
	}

	function activateSort(col: Column<T>) {
		const s = nextSort(sort, col.id);
		sort = s;
		onsort?.(s);
	}

	function toggleRow(key: string, on: boolean) {
		selected = on ? [...selected, key] : selected.filter((k) => k !== key);
	}

	function ariaSort(col: Column<T>): 'ascending' | 'descending' | 'none' | undefined {
		if (!col.sortValue) return undefined;
		if (sort?.column !== col.id) return 'none';
		return sort.direction === 'asc' ? 'ascending' : 'descending';
	}

	const byRole = (role: NonNullable<Column<T>['stack']>) =>
		columns.filter((c) => (c.stack ?? 'meta') === role);
</script>

{#snippet cellContent(col: Column<T>, row: T)}
	{#if col.cell}
		{@render col.cell(row)}
	{:else}
		{String((row as Record<string, unknown>)[col.id] ?? '')}
	{/if}
{/snippet}

{#snippet boxedContent(col: Column<T>, row: T)}
	{#if col.maxWidth || col.truncate}
		<div class="cell-box" class:truncate={col.truncate} style={cellStyle(col)}>
			{@render cellContent(col, row)}
		</div>
	{:else}
		{@render cellContent(col, row)}
	{/if}
{/snippet}

{#if sorted.length === 0 && empty}
	<div class="empty">{@render empty()}</div>
{:else if stacked}
	<ul class="stacked" role="list" aria-label={label}>
		{#each sorted as row (rowKey(row))}
			{@const key = rowKey(row)}
			<li class="card" data-changed={changed?.has(key) || undefined}>
				<div class="card-head">
					{#if selectable}
						<Checkbox
							label={rowLabel ? rowLabel(row) : `Select Row ${key}`}
							hideLabel
							checked={selected.includes(key)}
							onchange={(e) => toggleRow(key, e.currentTarget.checked)}
						/>
					{/if}
					<div class="card-lead">
						<div class="card-title">
							{#each byRole('title') as col (col.id)}{@render cellContent(
									col,
									row
								)}{/each}
						</div>
						{#each byRole('status') as col (col.id)}<div class="card-status">
								{@render cellContent(col, row)}
							</div>{/each}
					</div>
					{#each byRole('head') as col (col.id)}<div class="card-end">
							{@render cellContent(col, row)}
						</div>{/each}
				</div>
				{#if byRole('meta').length}
					<dl class="card-meta">
						{#each byRole('meta') as col (col.id)}
							<div>
								<dt>{col.header}</dt>
								<dd class:num={col.numeric} class:mono={col.mono}>
									{@render cellContent(col, row)}
								</dd>
							</div>
						{/each}
					</dl>
				{/if}
				{#if byRole('actions').length}
					<div class="card-actions">
						{#each byRole('actions') as col (col.id)}{@render cellContent(
								col,
								row
							)}{/each}
					</div>
				{/if}
			</li>
		{:else}
			<li class="card none">Nothing here yet.</li>
		{/each}
	</ul>
{:else}
	<div
		class="scroll"
		class:bounded={!!maxHeight}
		style={maxHeight ? `max-height: ${maxHeight}` : undefined}
		onscroll={virtual ? onScroll : undefined}
	>
		<table aria-label={label} aria-rowcount={virtual ? sorted.length + 1 : undefined}>
			<thead>
				<tr>
					{#if selectable}
						<th class="select" scope="col">
							<Checkbox
								label="Select All Rows"
								hideLabel
								checked={headerState === true}
								indeterminate={headerState === 'mixed'}
								onchange={() => (selected = toggleAll(keys, selected))}
							/>
						</th>
					{/if}
					{#each columns as col (col.id)}
						<th
							scope="col"
							style={col.width ? `width: ${col.width}` : undefined}
							class:end={col.align === 'end' || col.numeric}
							class:pin={col.pin === 'end'}
							aria-sort={ariaSort(col)}
						>
							{#if col.sortValue}
								<button
									type="button"
									class="sort"
									onclick={() => activateSort(col)}
								>
									<span class:sr-only={col.hideHeader}>{col.header}</span>
									{#if sort?.column === col.id}
										{#if sort.direction === 'asc'}
											<ArrowUp
												size={14}
												strokeWidth={1.75}
												aria-hidden="true"
											/>
										{:else}
											<ArrowDown
												size={14}
												strokeWidth={1.75}
												aria-hidden="true"
											/>
										{/if}
									{:else}
										<ChevronsUpDown
											size={14}
											strokeWidth={1.75}
											aria-hidden="true"
											class="idle"
										/>
									{/if}
								</button>
							{:else}
								<span class:sr-only={col.hideHeader}>{col.header}</span>
							{/if}
						</th>
					{/each}
				</tr>
			</thead>
			<tbody>
				{#if win.padTop > 0}
					<tr class="spacer" aria-hidden="true"
						><td colspan={colCount} style="height: {win.padTop}px"></td></tr
					>
				{/if}
				{#each visibleRows as row, i (rowKey(row))}
					{@const key = rowKey(row)}
					<tr
						class:selected={selectable && selected.includes(key)}
						data-changed={changed?.has(key) || undefined}
						aria-rowindex={virtual ? win.start + i + 2 : undefined}
						style={virtual ? `height: ${rowHeight}px` : undefined}
					>
						{#if selectable}
							<td class="select">
								<Checkbox
									label={rowLabel ? rowLabel(row) : `Select Row ${key}`}
									hideLabel
									checked={selected.includes(key)}
									onchange={(e) => toggleRow(key, e.currentTarget.checked)}
								/>
							</td>
						{/if}
						{#each columns as col (col.id)}
							<td
								class:end={col.align === 'end' || col.numeric}
								class:num={col.numeric}
								class:mono={col.mono}
								class:pin={col.pin === 'end'}
								title={cellTitle(col, row)}
							>
								{@render boxedContent(col, row)}
							</td>
						{/each}
					</tr>
				{:else}
					<tr>
						<td class="none" colspan={colCount}>Nothing here yet.</td>
					</tr>
				{/each}
				{#if win.padBottom > 0}
					<tr class="spacer" aria-hidden="true"
						><td colspan={colCount} style="height: {win.padBottom}px"></td></tr
					>
				{/if}
			</tbody>
		</table>
	</div>
{/if}

<style>
	/* position: relative keeps absolutely positioned content of cells (the
	   .sr-only texts) inside the scroll box, so it cannot widen the page.
	   The bottom radius clips the last row's hover to a card's corners. */
	.scroll {
		position: relative;
		overflow-x: auto;
		border-radius: 0 0 calc(var(--radius-lg) - 1px) calc(var(--radius-lg) - 1px);
	}

	.scroll.bounded {
		overflow-y: auto;
	}

	table {
		width: 100%;
		border-collapse: separate;
		border-spacing: 0;
		font-size: var(--text-body);
	}

	th {
		position: sticky;
		top: 0;
		z-index: var(--z-sticky);
		height: 40px;
		padding: 0 var(--space-4);
		background: var(--surface-raised);
		color: var(--text-muted);
		font-weight: var(--weight-regular);
		text-align: left;
		white-space: nowrap;
		border-bottom: 1px solid var(--border-subtle);
	}

	th.end,
	td.end {
		text-align: right;
	}

	/* A figure and its unit stay on one line ("136.17 MB", not "MB" below). */
	td.num {
		white-space: nowrap;
	}

	th.select,
	td.select {
		width: 44px;
		padding-right: 0;
	}

	.sort {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 0;
		border: 0;
		background: none;
		color: inherit;
		font: inherit;
	}

	.sort:hover {
		color: var(--text-strong);
	}

	.sort :global(.idle) {
		opacity: 0.5;
	}

	td {
		height: 48px;
		padding: 6px var(--space-4);
		border-bottom: 1px solid var(--border-subtle);
		color: var(--text-default);
		vertical-align: middle;
	}

	tbody tr:last-child td {
		border-bottom: 0;
	}

	tbody tr:not(.spacer):hover td,
	tr.selected td {
		background: var(--surface-hover);
	}

	/* Content capped by Column.maxWidth; truncate keeps it to one line. */
	.cell-box {
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.cell-box.truncate {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	/* Column.pin 'end': stays at the right edge while the table scrolls
	   sideways, on the row's own background (hover and selection too). */
	th.pin,
	td.pin {
		position: sticky;
		right: 0;
	}

	td.pin {
		z-index: 1;
		background: var(--surface-panel);
	}

	th.pin {
		z-index: calc(var(--z-sticky) + 1);
	}

	td.none {
		height: auto;
		padding: var(--space-6) var(--space-4);
		color: var(--text-muted);
		text-align: center;
	}

	tbody tr:has(td.none):hover td {
		background: none;
	}

	tr.selected td:first-child {
		box-shadow: inset 2px 0 0 var(--accent);
	}

	.spacer td {
		padding: 0;
		border: 0;
	}

	.empty {
		padding: var(--space-8) var(--space-4);
	}

	/* Stacked row cards (<768 px). */
	.stacked {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		margin: 0;
		padding: var(--space-2);
	}

	.card {
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-panel);
	}

	/* First line: the selection box, the title with its status and the
	   compact 'head' parts (a row menu) at the end. The status stays beside
	   a short title and moves below a long one, so the title keeps the
	   card's width instead of being cut to a few letters. */
	.card-head {
		display: flex;
		align-items: flex-start;
		gap: var(--space-3);
	}

	.card-lead {
		display: flex;
		flex: 1;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-3);
		min-width: 0;
	}

	.card-title {
		flex: 1 1 auto;
		min-width: 0;
		max-width: 100%;
	}

	.card-status,
	.card-end {
		display: flex;
		flex: none;
		align-items: center;
		gap: var(--space-2);
		max-width: 100%;
	}

	.card-meta {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
		gap: var(--space-2) var(--space-4);
		margin: var(--space-3) 0 0;
	}

	.card-meta > div {
		min-width: 0;
	}

	.card-meta dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	/* Long values (image references, paths, summaries) wrap rather than
	   being cut: a phone has no tooltip to show the rest. */
	.card-meta dd {
		margin: 0;
		color: var(--text-default);
		overflow-wrap: anywhere;
	}

	.card.none {
		color: var(--text-muted);
		text-align: center;
	}

	.card-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-1);
		margin-top: var(--space-2);
	}
</style>
