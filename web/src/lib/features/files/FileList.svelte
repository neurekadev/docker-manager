<script lang="ts" module>
	/** Drag data of entries dragged inside the file manager. */
	export const DRAG_TYPE = 'application/x-docker-manager-files';
	/** Row key of the ".." parent row. */
	export const PARENT_KEY = '..';
</script>

<script lang="ts">
	// The file list (#15): one directory as an ARIA grid with desktop
	// selection (click, Ctrl/Cmd-click, Shift-click, checkboxes for touch;
	// a click that opens a file does not select it),
	// keyboard commands while it has focus (keyboard.ts), a ".." row below
	// the root that opens the parent and accepts drops, drag and drop of
	// entries onto folders (move; Ctrl/Alt copies) and of files from the
	// operating system (upload), and windowed rendering for large
	// directories (virtualWindow from the Table, fixed row height).
	// Permissions and owners are columns of the `details` view only.
	import type { Snippet } from 'svelte';
	import { MediaQuery } from 'svelte/reactivity';
	import ArrowDown from '@lucide/svelte/icons/arrow-down';
	import ArrowUp from '@lucide/svelte/icons/arrow-up';
	import CornerLeftUp from '@lucide/svelte/icons/corner-left-up';
	import Scissors from '@lucide/svelte/icons/scissors';
	import { virtualWindow } from '$lib/ui/table';
	import { formatBytes, formatDateTime, formatRelative } from '$lib/ui';
	import type { FileEntry, ListSort } from './api';
	import { commandFor, isTypingTarget, type FileCommand } from './keyboard';
	import { entryIcon, entryKind, modeString, ownerText, ownerTitle } from './icons';
	import { parent as parentOf, within } from './paths';
	import { hasOsFiles } from './dropped';
	import * as sel from './selection';
	import { nextSort, sortColumn, sortDirection, type SortColumn } from './sort';

	interface Props {
		rows: FileEntry[];
		dir: string;
		showParent: boolean;
		selection: sel.Selection;
		cut?: ReadonlySet<string>;
		sort: ListSort;
		onsort: (s: ListSort) => void;
		/** Accessible name, e.g. "Files in config". */
		label: string;
		hasMore?: boolean;
		onloadmore?: () => void;
		onopen: (entry: FileEntry, via: 'pointer' | 'touch' | 'keyboard') => void;
		onparent: () => void;
		/** Commands the list does not handle itself (copy, cut, paste, rename, delete, escape). */
		oncommand: (c: FileCommand) => void;
		/** scopeKey() of the root (drag data). */
		dragScope: string;
		canMove?: boolean;
		canCopy?: boolean;
		ondropentries?: (paths: string[], targetDir: string, copy: boolean) => void;
		canUpload?: boolean;
		ondropfiles?: (dt: DataTransfer, targetDir: string) => void;
		/** Right-click (or long-press) on a row (null: the empty area). */
		oncontext?: (entry: FileEntry | null) => void;
		/** Props of the ContextMenu trigger, spread on the grid. */
		triggerProps?: Record<string, unknown>;
		rowActions?: Snippet<[FileEntry]>;
		empty?: Snippet;
		/** The grid element (focus after dialogs). */
		ref?: HTMLElement | null;
		/** Show the Permissions and Owner columns (off: name, size, modified). */
		details?: boolean;
		/** The path open in the editor (marked, not selected). */
		active?: string | null;
	}

	let {
		rows,
		dir,
		showParent,
		selection = $bindable(),
		cut = new Set(),
		sort,
		onsort,
		label,
		hasMore = false,
		onloadmore,
		onopen,
		onparent,
		oncommand,
		dragScope,
		canMove = false,
		canCopy = false,
		ondropentries,
		canUpload = false,
		ondropfiles,
		oncontext,
		triggerProps = {},
		rowActions,
		empty,
		ref = $bindable(null),
		details = false,
		active = null
	}: Props = $props();

	const uid = $props.id();
	const coarse = new MediaQuery('pointer: coarse');
	const rowHeight = $derived(coarse.current ? 44 : 32);

	type Row = { key: string; entry: FileEntry | null };
	const display = $derived<Row[]>([
		...(showParent ? [{ key: PARENT_KEY, entry: null }] : []),
		...rows.map((e) => ({ key: e.path, entry: e }))
	]);
	/** Keys the cursor moves over (the ".." row included). */
	const navKeys = $derived(display.map((r) => r.key));
	/** Selectable keys (entries only). */
	const keys = $derived(rows.map((e) => e.path));
	const selectedSet = $derived(new Set(selection.selected));
	const headerState = $derived(sel.targets(selection, keys).length);

	let scroller = $state<HTMLElement>();
	let scrollTop = $state(0);
	let viewport = $state(480);
	const win = $derived(virtualWindow(scrollTop, viewport, rowHeight, display.length));
	const visible = $derived(display.slice(win.start, win.end));

	function onScroll() {
		if (!scroller) return;
		scrollTop = scroller.scrollTop;
		viewport = scroller.clientHeight;
		if (hasMore && scrollTop + viewport > display.length * rowHeight - rowHeight * 20)
			onloadmore?.();
	}

	$effect(() => {
		if (!scroller) return;
		viewport = scroller.clientHeight;
		const ro = new ResizeObserver(() => scroller && (viewport = scroller.clientHeight));
		ro.observe(scroller);
		return () => ro.disconnect();
	});

	// Another folder starts at the top.
	$effect(() => {
		void dir;
		if (scroller) {
			scroller.scrollTop = 0;
			scrollTop = 0;
		}
	});

	// A short directory that still has more pages: load them.
	$effect(() => {
		if (hasMore && display.length * rowHeight < viewport) onloadmore?.();
	});

	function rowId(key: string): string {
		return `${uid}-r${navKeys.indexOf(key)}`;
	}

	function strip(s: sel.Selection): sel.Selection {
		return s.selected.includes(PARENT_KEY)
			? { ...s, selected: s.selected.filter((k) => k !== PARENT_KEY) }
			: s;
	}

	function ensureVisible(key: string | null) {
		if (!key || !scroller) return;
		const i = navKeys.indexOf(key);
		if (i < 0) return;
		const top = i * rowHeight;
		if (top < scroller.scrollTop) scroller.scrollTop = top;
		else if (top + rowHeight > scroller.scrollTop + scroller.clientHeight)
			scroller.scrollTop = top + rowHeight - scroller.clientHeight;
		scrollTop = scroller.scrollTop;
	}

	const entryOf = (key: string) => rows.find((e) => e.path === key) ?? null;

	function openContextMenuAtCursor() {
		const key = selection.cursor;
		const el = (key && ref?.querySelector<HTMLElement>(`#${CSS.escape(rowId(key))}`)) || ref;
		if (!el) return;
		const r = el.getBoundingClientRect();
		el.dispatchEvent(
			new MouseEvent('contextmenu', {
				bubbles: true,
				cancelable: true,
				clientX: r.left + Math.min(48, r.width / 2),
				clientY: el === ref ? r.top + 48 : r.bottom - 4
			})
		);
	}

	function onKeydown(e: KeyboardEvent) {
		if (e.target !== e.currentTarget && isTypingTarget(e.target)) return;
		// Keys typed on a row's own controls (checkbox, actions menu) stay theirs.
		if ((e.target as HTMLElement).closest?.('.row-actions')) return;
		// Shift+F10 / the Menu key: the context menu at the cursor row (the
		// same menu as a right-click; the selection is what it acts on).
		if ((e.key === 'F10' && e.shiftKey) || e.key === 'ContextMenu') {
			e.preventDefault();
			openContextMenuAtCursor();
			return;
		}
		const cmd = commandFor(e);
		if (!cmd) return;
		e.preventDefault();
		switch (cmd.kind) {
			case 'move': {
				const page = Math.max(1, Math.floor(viewport / rowHeight) - 1);
				const delta =
					cmd.delta === 'pageDown' ? page : cmd.delta === 'pageUp' ? -page : cmd.delta;
				selection = strip(sel.move(selection, navKeys, delta, cmd.mode));
				ensureVisible(selection.cursor);
				return;
			}
			case 'toggle':
				if (selection.cursor && selection.cursor !== PARENT_KEY)
					selection = sel.toggleCursor(selection);
				return;
			case 'open': {
				if (selection.cursor === PARENT_KEY) return onparent();
				const entry = selection.cursor ? entryOf(selection.cursor) : null;
				if (entry) onopen(entry, 'keyboard');
				return;
			}
			case 'parent':
				if (showParent) onparent();
				return;
			case 'selectAll':
				selection = sel.selectAll(selection, keys);
				return;
			default:
				oncommand(cmd);
		}
	}

	let pointerType = 'mouse';

	function onRowClick(e: MouseEvent, row: Row) {
		if ((e.target as HTMLElement).closest('.row-actions, .row-check')) return;
		const touch = pointerType === 'touch';
		if (row.key === PARENT_KEY) {
			selection = { ...selection, cursor: PARENT_KEY };
			if (touch) onparent();
			return;
		}
		const mods = { toggle: e.ctrlKey || e.metaKey, range: e.shiftKey };
		const entry = row.entry;
		// Files open on click (and Enter) without being selected; folders
		// are selected by a click and open on double-click, or a tap.
		const opens = !!entry && !mods.toggle && !mods.range && (entry.type !== 'dir' || touch);
		selection = opens
			? sel.focus(selection, keys, row.key)
			: sel.click(selection, keys, row.key, mods);
		if (opens && entry) onopen(entry, touch ? 'touch' : 'pointer');
	}

	function onRowDblClick(e: MouseEvent, row: Row) {
		if ((e.target as HTMLElement).closest('.row-actions, .row-check')) return;
		if (row.key === PARENT_KEY) return onparent();
		if (row.entry?.type === 'dir') onopen(row.entry, 'pointer');
	}

	function onRowContext(row: Row) {
		if (row.key === PARENT_KEY) {
			oncontext?.(null);
			return;
		}
		if (!selectedSet.has(row.key)) selection = sel.click(selection, keys, row.key);
		oncontext?.(row.entry);
	}

	// Drag and drop ---------------------------------------------------------
	let dragging = $state<string[]>([]);
	let dropTarget = $state<string | null>(null);

	function targetDirOf(row: Row | null): string | null {
		if (!row) return dir;
		if (row.key === PARENT_KEY) return parentOf(dir);
		return row.entry?.type === 'dir' ? row.entry.path : null;
	}

	function onDragStart(e: DragEvent, row: Row) {
		if (!e.dataTransfer || row.key === PARENT_KEY || !(canMove || canCopy)) {
			e.preventDefault();
			return;
		}
		if (!selectedSet.has(row.key)) selection = sel.click(selection, keys, row.key);
		const paths = sel.targets(selection, keys);
		dragging = paths;
		e.dataTransfer.setData(DRAG_TYPE, JSON.stringify({ scope: dragScope, paths }));
		e.dataTransfer.setData('text/plain', paths.join('\n'));
		e.dataTransfer.effectAllowed = canMove && canCopy ? 'copyMove' : canMove ? 'move' : 'copy';
	}

	function dropAllowed(e: DragEvent, row: Row | null): string | null {
		const dt = e.dataTransfer;
		if (!dt) return null;
		const target = targetDirOf(row);
		if (target === null) return null;
		if (Array.from(dt.types).includes(DRAG_TYPE)) {
			if (!dragging.length) return null;
			// Never into itself, below itself, or where it already is.
			if (dragging.some((p) => within(target, p))) return null;
			if (dragging.every((p) => parentOf(p) === target)) return null;
			return target;
		}
		if (hasOsFiles(dt) && canUpload) return target;
		return null;
	}

	function copyGesture(e: DragEvent) {
		return e.ctrlKey || e.altKey || !canMove;
	}

	function onDragOver(e: DragEvent, row: Row | null) {
		const target = dropAllowed(e, row);
		if (target === null) {
			if (row) return; // let the list's own handler decide (drop into this folder)
			dropTarget = null;
			return;
		}
		e.preventDefault();
		e.stopPropagation();
		if (e.dataTransfer)
			e.dataTransfer.dropEffect = hasOsFiles(e.dataTransfer)
				? 'copy'
				: copyGesture(e)
					? 'copy'
					: 'move';
		dropTarget = row ? row.key : '.';
	}

	function onDrop(e: DragEvent, row: Row | null) {
		const target = dropAllowed(e, row);
		dropTarget = null;
		if (target === null || !e.dataTransfer) return;
		e.preventDefault();
		e.stopPropagation();
		const dt = e.dataTransfer;
		if (Array.from(dt.types).includes(DRAG_TYPE)) {
			let data: { scope?: string; paths?: string[] };
			try {
				data = JSON.parse(dt.getData(DRAG_TYPE));
			} catch {
				return;
			}
			if (data.scope !== dragScope || !data.paths?.length) return;
			ondropentries?.(data.paths, target, copyGesture(e));
		} else ondropfiles?.(dt, target);
		dragging = [];
	}

	function onDragEnd() {
		dragging = [];
		dropTarget = null;
	}

	function headerSort(col: SortColumn) {
		onsort(nextSort(sort, col));
	}

	function ariaSort(col: SortColumn) {
		return sortColumn(sort) === col ? sortDirection(sort) : 'none';
	}

	function toggleAll() {
		selection =
			headerState === keys.length && keys.length > 0
				? sel.clear(selection)
				: sel.selectAll(selection, keys);
	}

	const now = new Date();
</script>

{#snippet sortHeader(col: SortColumn, text: string)}
	<button type="button" class="sort" onclick={() => headerSort(col)}>
		{text}
		{#if sortColumn(sort) === col}
			{#if sortDirection(sort) === 'ascending'}
				<ArrowUp size={13} strokeWidth={1.75} aria-hidden="true" />
			{:else}
				<ArrowDown size={13} strokeWidth={1.75} aria-hidden="true" />
			{/if}
		{/if}
	</button>
{/snippet}

<div
	class="files"
	class:coarse={coarse.current}
	class:details
	class:selecting={selection.selected.length > 0}
>
	<div
		{...triggerProps}
		bind:this={ref}
		class="grid"
		role="grid"
		tabindex="0"
		aria-label={label}
		aria-multiselectable="true"
		aria-rowcount={display.length + 1}
		aria-activedescendant={selection.cursor && navKeys.includes(selection.cursor)
			? rowId(selection.cursor)
			: undefined}
		onkeydown={onKeydown}
		onpointerdown={(e) => (pointerType = e.pointerType || 'mouse')}
		ondragover={(e) => onDragOver(e, null)}
		ondragleave={(e) => {
			if (e.currentTarget === e.target) dropTarget = null;
		}}
		ondrop={(e) => onDrop(e, null)}
		data-drop={dropTarget === '.' || undefined}
	>
		<div class="row head" role="row" aria-rowindex={1}>
			<div class="cell check" role="columnheader">
				<input
					type="checkbox"
					class="row-check"
					tabindex="-1"
					aria-label="Select all entries"
					checked={keys.length > 0 && headerState === keys.length}
					indeterminate={headerState > 0 && headerState < keys.length}
					onchange={toggleAll}
				/>
			</div>
			<div class="cell name" role="columnheader" aria-sort={ariaSort('name')}>
				{@render sortHeader('name', 'Name')}
			</div>
			<div class="cell size" role="columnheader" aria-sort={ariaSort('size')}>
				{@render sortHeader('size', 'Size')}
			</div>
			<div class="cell modified" role="columnheader" aria-sort={ariaSort('modified')}>
				{@render sortHeader('modified', 'Modified')}
			</div>
			{#if details}
				<div class="cell mode" role="columnheader">Permissions</div>
				<div class="cell owner" role="columnheader">Owner</div>
			{/if}
			<div class="cell actions" role="columnheader"><span class="sr-only">Actions</span></div>
		</div>
		<div class="body" bind:this={scroller} onscroll={onScroll}>
			{#if display.length === 0 && empty}
				<div class="empty" role="row">
					<div role="gridcell">{@render empty()}</div>
				</div>
			{:else}
				<div style="height: {win.padTop}px" aria-hidden="true"></div>
				{#each visible as row, i (row.key)}
					{@const entry = row.entry}
					{@const index = win.start + i}
					{@const selected = selectedSet.has(row.key)}
					<!-- Keyboard: the grid owns focus and moves aria-activedescendant
					     over the rows (onKeydown), so rows are not tab stops. -->
					<!-- svelte-ignore a11y_interactive_supports_focus, a11y_click_events_have_key_events -->
					<div
						id={rowId(row.key)}
						class="row"
						class:selected
						class:cursor={selection.cursor === row.key}
						class:cut={cut.has(row.key)}
						class:drop={dropTarget === row.key}
						class:parent={row.key === PARENT_KEY}
						class:active={!!active && active === row.key}
						role="row"
						aria-rowindex={index + 2}
						aria-selected={row.key === PARENT_KEY ? undefined : selected}
						style="height: {rowHeight}px"
						draggable={row.key !== PARENT_KEY && (canMove || canCopy)}
						onclick={(e) => onRowClick(e, row)}
						ondblclick={(e) => onRowDblClick(e, row)}
						oncontextmenu={() => onRowContext(row)}
						ondragstart={(e) => onDragStart(e, row)}
						ondragend={onDragEnd}
						ondragover={(e) => onDragOver(e, row)}
						ondragleave={() => dropTarget === row.key && (dropTarget = null)}
						ondrop={(e) => onDrop(e, row)}
						data-path={row.key}
					>
						<div class="cell check" role="gridcell">
							{#if entry}
								<input
									type="checkbox"
									class="row-check"
									tabindex="-1"
									aria-label="Select {entry.name}"
									checked={selected}
									onchange={() => (selection = sel.toggle(selection, row.key))}
								/>
							{/if}
						</div>
						<div class="cell name" role="gridcell">
							{#if entry}
								{@const ic = entryIcon(entry)}
								<ic.icon
									size={16}
									strokeWidth={1.75}
									class="entry-icon {ic.tone}"
									aria-hidden="true"
								/>
								<span class="label" title={entry.name}>{entry.name}</span>
								{#if entry.type === 'symlink'}
									<span class="link mono" title={entry.linkTarget}
										>→ {entry.linkTarget}</span
									>
								{/if}
								{#if cut.has(row.key)}
									<Scissors
										size={13}
										strokeWidth={1.75}
										class="cut-mark"
										aria-hidden="true"
									/>
								{/if}
								<span class="sr-only"
									>, {entryKind(entry)}{cut.has(row.key) ? ', cut' : ''}</span
								>
							{:else}
								<CornerLeftUp
									size={16}
									strokeWidth={1.75}
									class="entry-icon folder"
									aria-hidden="true"
								/>
								<span class="label" aria-label="Parent folder">..</span>
							{/if}
						</div>
						<div class="cell size num" role="gridcell">
							{entry && entry.type === 'file' ? formatBytes(entry.size) : ''}
						</div>
						<div class="cell modified" role="gridcell">
							{#if entry}
								<span title={formatDateTime(entry.modifiedAt)}
									>{formatRelative(entry.modifiedAt, now)}</span
								>
							{/if}
						</div>
						{#if details}
							<div class="cell mode mono" role="gridcell">
								{#if entry}<span title={entry.mode}>{modeString(entry.mode)}</span
									>{/if}
							</div>
							<div class="cell owner mono num" role="gridcell">
								{#if entry}<span title={ownerTitle(entry.uid, entry.gid)}
										>{ownerText(entry.uid, entry.gid)}</span
									>{/if}
							</div>
						{/if}
						<div class="cell actions" role="gridcell">
							{#if entry && rowActions}
								<div class="row-actions">{@render rowActions(entry)}</div>
							{/if}
						</div>
					</div>
				{/each}
				<div style="height: {win.padBottom}px" aria-hidden="true"></div>
				<!-- The empty space below the rows: right-click here acts on the folder. -->
				<div
					class="filler"
					aria-hidden="true"
					oncontextmenu={() => {
						selection = sel.clear(selection);
						oncontext?.(null);
					}}
					onclick={() => (selection = sel.clear(selection))}
				></div>
			{/if}
		</div>
	</div>
</div>

<style>
	.files {
		display: flex;
		flex-direction: column;
		min-height: 0;
		height: 100%;
		container-type: inline-size;
	}

	.grid {
		display: flex;
		flex-direction: column;
		flex: 1;
		min-height: 0;
		outline: none;
		font-size: var(--text-body);
		border-radius: var(--radius-md);
	}

	.grid:focus-visible {
		box-shadow: inset 0 0 0 2px var(--accent-text);
	}

	.grid[data-drop] {
		box-shadow: inset 0 0 0 2px var(--accent);
		background: var(--accent-soft);
	}

	.body {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
	}

	.row {
		display: grid;
		grid-template-columns: 32px minmax(0, 1fr) 84px 132px 40px;
		align-items: center;
		flex: none;
		border-radius: var(--radius-md);
		color: var(--text-default);
		cursor: default;
		user-select: none;
	}

	.details .row {
		grid-template-columns: 32px minmax(0, 1fr) 84px 132px 96px 104px 40px;
	}

	.row.head {
		height: 34px;
		border-bottom: 1px solid var(--border-subtle);
		border-radius: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.body .row:hover {
		background: var(--surface-hover);
	}

	.body .row.active {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.body .row.selected {
		background: var(--surface-selected-strong);
		color: var(--text-strong);
	}

	.grid:focus-visible .row.cursor {
		box-shadow: inset 0 0 0 1px var(--accent-text);
	}

	.row.cut .name .label,
	.row.cut :global(.entry-icon) {
		opacity: 0.5;
	}

	.row.drop {
		background: var(--accent-soft);
		box-shadow: inset 0 0 0 2px var(--accent);
	}

	.cell {
		min-width: 0;
		padding: 0 var(--space-2);
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}

	.cell.check {
		display: flex;
		justify-content: center;
		padding: 0;
	}

	.row-check {
		width: 14px;
		height: 14px;
		accent-color: var(--accent);
		opacity: 0;
	}

	/* Checkboxes stay out of the way until a row is pointed at or anything
	   is selected; always shown on touch screens (their way to multi-select). */
	.row:hover .row-check,
	.row.selected .row-check,
	.head .row-check,
	.selecting .row-check,
	.coarse .row-check {
		opacity: 1;
	}

	.head .row-check:not(:checked):not(:indeterminate) {
		opacity: 0.55;
	}

	.coarse .row-check {
		width: 18px;
		height: 18px;
	}

	.cell.name {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	.label {
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.link {
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
	}

	.cell :global(.entry-icon) {
		flex: none;
		color: var(--text-muted);
	}

	.cell :global(.entry-icon.folder) {
		color: var(--tile-blue-fg);
		fill: color-mix(in srgb, var(--tile-blue-fg) 30%, transparent);
	}

	.cell :global(.entry-icon.json) {
		color: var(--warn);
	}

	.cell :global(.cut-mark) {
		flex: none;
		color: var(--text-muted);
	}

	.cell.size,
	.cell.owner {
		text-align: right;
	}

	.cell.modified,
	.cell.mode,
	.cell.owner,
	.cell.size {
		color: var(--text-muted);
	}

	.row.selected .cell {
		color: inherit;
	}

	.cell.mode {
		font-size: 12px;
	}

	/* Owners are short ("root", "1000"): never cut. */
	.cell.owner {
		text-overflow: clip;
	}

	.cell.actions {
		display: flex;
		justify-content: center;
		padding: 0;
	}

	.row-actions {
		opacity: 0;
	}

	.row:hover .row-actions,
	.row.selected .row-actions,
	.row-actions:focus-within,
	.coarse .row-actions {
		opacity: 1;
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

	.row.parent .label {
		color: var(--text-muted);
		letter-spacing: 1px;
	}

	.filler {
		flex: 1;
		min-height: 48px;
	}

	.empty {
		padding: var(--space-6) var(--space-4);
	}

	/* Narrow panes (the list beside the editor): fewer columns. */
	@container (max-width: 640px) {
		.row,
		.details .row {
			grid-template-columns: 32px minmax(0, 1fr) 76px 110px 40px;
		}
		.cell.mode,
		.cell.owner {
			display: none;
		}
	}

	@container (max-width: 420px) {
		.row,
		.details .row {
			grid-template-columns: 32px minmax(0, 1fr) 72px 40px;
		}
		.cell.modified {
			display: none;
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.row {
			transition: none;
		}
	}
</style>
