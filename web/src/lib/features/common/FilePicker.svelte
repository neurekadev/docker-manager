<script lang="ts">
	// The shared file picker, like a desktop "Open" dialog: the places on
	// the left (hidden when there is one), the folder's path and a filter on
	// top, its entries below, listed when the folder opens; never above a
	// place. One file (default): a click opens a folder or selects a file,
	// a double click or Enter chooses it, arrows move, Backspace goes up.
	// `multiple`: files and folders get tri-state ticks (a ticked folder is
	// chosen whole, filePicker.ts), the header ticks the open folder and
	// each place shows how many items it holds. The source lists folders
	// (filePicker.ts, PickerSource).
	import { tick } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import CornerLeftUp from '@lucide/svelte/icons/corner-left-up';
	import {
		Badge,
		Button,
		Checkbox,
		Dialog,
		IconButton,
		Skeleton,
		TextField,
		formatBytes,
		formatDateTime
	} from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import { entryIcon } from '$lib/features/files/icons';
	import {
		foldersBetween,
		normalize,
		parentOf,
		pickerCrumbs,
		pickerEntries,
		pickerStart,
		placeOf,
		selectedIn,
		selectionText,
		tickState,
		toggle,
		within,
		type PickerEntry,
		type PickerListing,
		type PickerPlace,
		type PickerSource
	} from './filePicker';

	type Props = {
		open?: boolean;
		title: string;
		description?: string;
		places: PickerPlace[];
		source: PickerSource;
		/** Several files and folders instead of one file. */
		multiple?: boolean;
		/** Chosen so far: the picker opens on the first one's folder (and ticks them). */
		value?: string[];
		/** One file: what can be chosen (default: regular files). */
		choosable?: (e: PickerEntry) => boolean;
		/** One file: why an entry can't be chosen (its tooltip). */
		unchoosableReason?: (e: PickerEntry) => string | undefined;
		/** The confirm button ("Choose File", "Review Restore"). */
		confirmLabel: string;
		/** Shown below a folder that lists only its first entries. */
		truncatedHint?: string;
		onpick: (paths: string[]) => void;
	};

	let {
		open = $bindable(false),
		title,
		description,
		places,
		source,
		multiple = false,
		value = [],
		choosable = (e) => e.type === 'file',
		unchoosableReason,
		confirmLabel,
		truncatedHint = 'Only the first entries are listed.',
		onpick
	}: Props = $props();

	const qc = useQueryClient();
	const uid = $props.id();
	// Empty until the picker first opens and knows where to start.
	let dir = $state('');
	let selection = $state<string[]>([]);
	let active = $state('');
	let filter = $state('');
	let list = $state<HTMLElement | null>(null);

	// Every open starts on the chosen path's folder (or the first place).
	// One file: the chosen path is selected only once its folder's listing
	// shows it can be chosen (a pasted path may be a folder or missing).
	let wasOpen = false;
	let preselect = $state('');
	$effect(() => {
		if (open && !wasOpen) {
			const chosen = value.map((v) => v.trim()).filter(Boolean);
			dir = pickerStart(places, chosen);
			selection = multiple ? normalize(chosen) : [];
			preselect = multiple ? '' : (chosen[0] ?? '');
			active = preselect;
			filter = '';
		}
		wasOpen = open;
	});

	const place = $derived(placeOf(places, dir) ?? places[0]);
	const crumbs = $derived(
		place ? pickerCrumbs(place, dir) : [{ name: dir || '/', path: dir || '/' }]
	);
	const here = $derived(crumbs[crumbs.length - 1].name);
	const atTop = $derived(!place || dir === place.path);

	const listingOptions = (d: string) => ({
		...source.query(d),
		staleTime: 5 * 60_000,
		retry: false
	});
	const contents = createQuery(() => ({ ...listingOptions(dir), enabled: open && !!dir }));
	const entries = $derived(pickerEntries(contents.data?.entries ?? [], filter));
	// Decided from the cached listing of the preset's own folder: right
	// after a reopen the query may still hold the previous folder's.
	$effect(() => {
		void contents.data;
		if (!preselect) return;
		if (dir !== parentOf(preselect)) {
			preselect = '';
			return;
		}
		const l = qc.getQueryData<PickerListing>(source.query(dir).queryKey);
		if (!l) return;
		const n = l.entries.find((e) => e.path === preselect);
		if (n && canChoose(n)) selection = [n.path];
		preselect = '';
	});
	const activeIndex = $derived(entries.findIndex((e) => e.path === active));
	const picked = $derived(selection[0] ?? '');

	function canChoose(n: PickerEntry): boolean {
		return n.type !== 'dir' && choosable(n);
	}

	function go(path: string) {
		dir = path;
		filter = '';
		active = '';
		refused = '';
	}

	function up() {
		if (atTop) return;
		const from = dir;
		go(parentOf(dir));
		active = from;
	}

	function confirm(paths: string[]) {
		open = false;
		onpick(paths);
	}

	// Several items: tick or untick, splitting a ticked folder by its
	// listings (loaded first when the cache dropped them); a folder listed
	// only in part is never split (its unlisted entries would leave the
	// selection).
	function children(d: string): string[] | undefined {
		const l = qc.getQueryData<PickerListing>(source.query(d).queryKey);
		return l && !l.truncated ? l.entries.map((e) => e.path) : undefined;
	}

	// Why the last untick changed nothing (its folder can't be split). The
	// clicked box already cleared itself (Checkbox binds `checked`), so the
	// boxes are drawn again from the selection and the clicked one keeps
	// focus.
	let refused = $state('');
	let redraw = $state(0);

	async function flip(path: string, focusId?: string) {
		const ancestor = selection.find((s) => s !== path && within(path, s));
		const dirs = ancestor ? foldersBetween(ancestor, path) : [];
		if (dirs.length)
			await Promise.allSettled(dirs.map((d) => qc.ensureQueryData(listingOptions(d))));
		const before = selection;
		const next = toggle(before, path, children);
		const unchanged = next.length === before.length && next.every((p, i) => p === before[i]);
		const partial = dirs.some(
			(d) => qc.getQueryData<PickerListing>(source.query(d).queryKey)?.truncated
		);
		refused = !(unchanged && ancestor)
			? ''
			: partial
				? 'This folder is listed only in part, so it is chosen whole. Untick the folder to leave it out.'
				: "This folder's contents could not be listed, so it is chosen whole. Untick the folder to leave it out.";
		selection = next;
		if (refused && focusId) {
			redraw++;
			await tick();
			document.getElementById(focusId)?.focus();
		}
	}

	function boxId(path: string): string {
		return `${uid}-tick-${encodeURIComponent(path)}`;
	}

	// One file: the list is a listbox; moving onto a file selects it.
	function focusOn(n: PickerEntry | undefined) {
		if (!n) return;
		active = n.path;
		if (canChoose(n)) selection = [n.path];
		document.getElementById(optionId(n.path))?.scrollIntoView({ block: 'nearest' });
	}

	function activate(n: PickerEntry) {
		if (n.type === 'dir') go(n.path);
		else if (canChoose(n)) confirm([n.path]);
	}

	function onclick(n: PickerEntry) {
		if (n.type === 'dir') go(n.path);
		else focusOn(n);
		list?.focus();
	}

	function onkeydown(e: KeyboardEvent) {
		const i = activeIndex;
		switch (e.key) {
			case 'ArrowDown':
				focusOn(entries[Math.min(i + 1, entries.length - 1)]);
				break;
			case 'ArrowUp':
				focusOn(entries[Math.max(i - 1, 0)]);
				break;
			case 'Home':
				focusOn(entries[0]);
				break;
			case 'End':
				focusOn(entries[entries.length - 1]);
				break;
			case 'Enter':
				if (entries[i]) activate(entries[i]);
				else if (picked) confirm([picked]);
				break;
			case 'Backspace':
				up();
				break;
			default:
				return;
		}
		e.preventDefault();
	}

	// The filter hands ArrowDown to the list; Enter opens a single match.
	function onfilterkey(e: KeyboardEvent) {
		if (e.key !== 'ArrowDown' && e.key !== 'Enter') return;
		e.preventDefault();
		if (e.key === 'Enter' && entries.length === 1) {
			if (multiple && entries[0].type !== 'dir') void flip(entries[0].path);
			else activate(entries[0]);
		} else if (!multiple) {
			list?.focus();
			if (activeIndex < 0) focusOn(entries[0]);
		}
	}

	function optionId(path: string): string {
		return `${uid}-${encodeURIComponent(path)}`;
	}

	const confirmDisabled = $derived(multiple ? selection.length === 0 : !picked);
</script>

{#snippet cells(n: PickerEntry)}
	{@const ic = entryIcon({ name: n.name, type: n.type })}
	<ic.icon size={16} aria-hidden="true" class="entry-icon {ic.tone}" />
	{#if multiple && n.type === 'dir'}
		<button type="button" class="name open" onclick={() => go(n.path)}>{n.name}</button>
	{:else if multiple}
		<button type="button" class="name mono" tabindex="-1" onclick={() => void flip(n.path)}
			>{n.name}</button
		>
	{:else}
		<span class="name" class:mono={n.type !== 'dir'}>{n.name}</span>
	{/if}
	<span class="size num"
		>{n.type === 'dir' || n.size === undefined ? '' : formatBytes(n.size)}</span
	>
	<span class="time num">{n.mtime ? formatDateTime(n.mtime) : ''}</span>
{/snippet}

{#snippet status()}
	{#if contents.isPending}
		<div class="status"><Skeleton lines={6} height="20px" /></div>
	{:else if contents.isError}
		<p class="status error" role="alert">{actionError(contents.error)}</p>
	{:else if !entries.length}
		<p class="status">
			{filter.trim() ? 'Nothing here matches the filter.' : 'This folder is empty.'}
		</p>
	{/if}
{/snippet}

<Dialog bind:open size={places.length > 1 ? 'xl' : 'lg'} {title} {description}>
	<div class="picker" class:places-shown={places.length > 1}>
		{#if places.length > 1}
			<nav class="places" aria-label="Places">
				{#each places as p (p.path)}
					{@const n = multiple ? selectedIn(selection, p.path) : 0}
					<button
						type="button"
						class="place"
						aria-current={p.path === place?.path ? 'location' : undefined}
						title={p.path}
						onclick={() => go(p.path)}
					>
						<p.icon size={16} aria-hidden="true" />
						<span class="label">{p.label}</span>
						{#if n}<Badge tone="accent">{n}</Badge>{/if}
					</button>
				{/each}
			</nav>
		{/if}
		<div class="main">
			<div class="toolbar">
				<IconButton
					label="Up One Folder"
					icon={CornerLeftUp}
					size="sm"
					disabled={atTop}
					onclick={up}
				/>
				<nav class="crumbs" aria-label="Folder">
					{#each crumbs as c, i (c.path)}
						{#if i > 0}<span class="sep" aria-hidden="true">/</span>{/if}
						<button
							type="button"
							class="crumb"
							class:mono={i > 0}
							aria-current={c.path === dir ? 'location' : undefined}
							onclick={() => go(c.path)}>{c.name}</button
						>
					{/each}
				</nav>
				<div class="filter">
					<TextField
						label="Filter This Folder"
						hideLabel
						type="search"
						placeholder="Filter"
						bind:value={filter}
						onkeydown={onfilterkey}
					/>
				</div>
			</div>
			<div class="head" class:ticks={multiple}>
				{#if multiple}
					{@const all = tickState(selection, dir)}
					{#key redraw}
						<Checkbox
							id={boxId(dir)}
							label="All of {here}"
							hideLabel
							checked={all === 'checked'}
							indeterminate={all === 'mixed'}
							disabled={!dir}
							onchange={() => void flip(dir, boxId(dir))}
						/>
					{/key}
				{/if}
				<span></span><span aria-hidden="true">Name</span><span class="r" aria-hidden="true"
					>Size</span
				><span class="r time" aria-hidden="true">Modified</span>
			</div>
			{@render status()}
			{#if multiple}
				<ul class="list" aria-label="Contents of {here}" aria-busy={contents.isPending}>
					{#if contents.isSuccess}
						{#each entries as n (n.path)}
							{@const state = tickState(selection, n.path)}
							<li class="row ticks" class:checked={state !== 'unchecked'}>
								{#key redraw}
									<Checkbox
										id={boxId(n.path)}
										label={n.name}
										hideLabel
										checked={state === 'checked'}
										indeterminate={state === 'mixed'}
										onchange={() => void flip(n.path, boxId(n.path))}
									/>
								{/key}
								{@render cells(n)}
							</li>
						{/each}
					{/if}
				</ul>
			{:else}
				<div
					bind:this={list}
					class="list"
					role="listbox"
					tabindex="0"
					aria-label="Contents of {here}"
					aria-busy={contents.isPending}
					aria-activedescendant={activeIndex >= 0 ? optionId(active) : undefined}
					{onkeydown}
				>
					{#if contents.isSuccess}
						{#each entries as n (n.path)}
							{@const off = n.type !== 'dir' && !canChoose(n)}
							<!-- svelte-ignore a11y_click_events_have_key_events -->
							<div
								id={optionId(n.path)}
								class="row"
								class:active={n.path === active}
								class:disabled={off}
								role="option"
								tabindex="-1"
								aria-selected={n.path === picked}
								aria-disabled={off ? true : undefined}
								title={off ? unchoosableReason?.(n) : undefined}
								onclick={() => onclick(n)}
								ondblclick={() => activate(n)}
							>
								{@render cells(n)}
							</div>
						{/each}
					{/if}
				</div>
			{/if}
			{#if contents.data?.truncated}
				<p class="status">{truncatedHint}</p>
			{/if}
			{#if refused}
				<p class="status warn" role="status">{refused}</p>
			{/if}
		</div>
	</div>
	{#snippet footer()}
		<span class="summary" aria-live="polite">
			{#if multiple}
				{selectionText(selection.length)}
			{:else if picked}
				<span class="mono" title={picked}>{picked}</span>
			{:else}
				No file selected
			{/if}
		</span>
		{#if multiple && selection.length}
			<Button variant="ghost" onclick={() => (selection = [])}>Clear Selection</Button>
		{/if}
		<Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
		<Button
			variant="primary"
			disabled={confirmDisabled}
			onclick={() => confirm(multiple ? [...selection] : [picked])}>{confirmLabel}</Button
		>
	{/snippet}
</Dialog>

<style>
	.picker {
		display: grid;
		gap: var(--space-3);
		min-height: min(60vh, 560px);
	}

	.picker.places-shown {
		grid-template-columns: 220px minmax(0, 1fr);
	}

	.places {
		display: grid;
		align-content: start;
		gap: 2px;
		padding-right: var(--space-3);
		border-right: 1px solid var(--border-subtle);
	}

	.place {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 32px;
		padding: 0 var(--space-2);
		border: 0;
		border-radius: var(--radius-md);
		background: transparent;
		color: var(--text-default);
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	.place .label {
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.place :global(svg) {
		flex: none;
		color: var(--text-muted);
	}

	.place:hover {
		background: var(--surface-hover);
	}

	.place[aria-current='location'] {
		background: var(--surface-hover);
		color: var(--text-strong);
		font-weight: 600;
	}

	.main {
		display: flex;
		flex-direction: column;
		min-width: 0;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border-bottom: 1px solid var(--border-subtle);
	}

	.crumbs {
		display: flex;
		flex: 1;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1);
		min-width: 0;
	}

	.crumb {
		max-width: 240px;
		overflow: hidden;
		padding: 2px var(--space-1);
		border: 0;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--accent-text);
		font: inherit;
		text-overflow: ellipsis;
		white-space: nowrap;
		cursor: pointer;
	}

	.crumb[aria-current='location'] {
		color: var(--text-strong);
	}

	.crumb:hover {
		background: var(--surface-hover);
	}

	.sep {
		color: var(--text-faint);
	}

	.filter {
		width: 200px;
	}

	.head,
	.row {
		display: grid;
		grid-template-columns: 16px minmax(0, 1fr) 84px 150px;
		align-items: center;
		gap: var(--space-2);
		padding: 0 var(--space-3);
	}

	.head.ticks,
	.row.ticks {
		grid-template-columns: auto 16px minmax(0, 1fr) 84px 150px;
	}

	.head {
		min-height: 32px;
		border-bottom: 1px solid var(--border-subtle);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.r {
		text-align: right;
	}

	.list {
		display: grid;
		flex: 1 1 auto;
		align-content: start;
		min-height: 0;
		max-height: min(52vh, 480px);
		margin: 0;
		overflow: auto;
		padding: var(--space-1);
		list-style: none;
		outline: none;
	}

	.list:focus-visible {
		outline: var(--focus-ring);
		outline-offset: -2px;
	}

	.row {
		min-height: 32px;
		border-radius: var(--radius-md);
		color: var(--text-default);
	}

	div.row {
		cursor: pointer;
		user-select: none;
	}

	.row:hover {
		background: var(--surface-hover);
	}

	.list:focus-visible .row.active {
		outline: 1px solid var(--accent-text);
		outline-offset: -1px;
	}

	.row[aria-selected='true'],
	.row.checked {
		background: var(--accent-soft);
		color: var(--text-strong);
	}

	.row.disabled {
		color: var(--text-faint);
		cursor: not-allowed;
	}

	.row :global(.entry-icon) {
		color: var(--text-muted);
	}

	.row :global(.entry-icon.folder) {
		color: var(--tile-blue-fg);
		fill: color-mix(in srgb, var(--tile-blue-fg) 30%, transparent);
	}

	.row :global(.entry-icon.json) {
		color: var(--warn);
	}

	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	button.name {
		padding: 0;
		border: 0;
		background: transparent;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	button.name.open:hover {
		color: var(--accent-text);
	}

	.size,
	.time {
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-align: right;
		white-space: nowrap;
	}

	.status {
		padding: var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.status.error {
		color: var(--danger);
	}

	.status.warn {
		color: var(--warn);
	}

	.summary {
		min-width: 0;
		margin-right: auto;
		overflow: hidden;
		color: var(--text-muted);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.summary .mono {
		color: var(--text-strong);
	}

	@media (max-width: 767px) {
		.picker.places-shown {
			grid-template-columns: minmax(0, 1fr);
		}

		.places {
			display: flex;
			gap: var(--space-1);
			overflow-x: auto;
			padding: 0 0 var(--space-2);
			border-right: 0;
			border-bottom: 1px solid var(--border-subtle);
		}

		.place {
			flex: none;
		}

		.filter {
			width: 100%;
		}

		.head,
		.row {
			grid-template-columns: 16px minmax(0, 1fr) 72px;
		}

		.head.ticks,
		.row.ticks {
			grid-template-columns: auto 16px minmax(0, 1fr) 72px;
		}

		.time {
			display: none;
		}
	}
</style>
