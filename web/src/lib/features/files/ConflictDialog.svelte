<script lang="ts">
	// Conflict resolution (#15): one conflict at a time with Replace, Keep
	// Both or Skip; "Apply to All Remaining Conflicts" is off by default.
	// `single` asks once for the whole operation (an extraction or an
	// archive is one request with one policy): the conflicting names are
	// listed and the choice covers all of them.
	import { Button, Checkbox, Dialog, formatBytes, formatDateTime } from '$lib/ui';
	import { ConflictQueue, type ConflictChoice, type ConflictItem } from './conflicts';
	import { basename, keepBothName, parent } from './paths';

	interface Props {
		open?: boolean;
		/** What is happening, e.g. "Paste 3 items into config". */
		title: string;
		items: ConflictItem[];
		/** More conflicts exist than the preview listed (1000+). */
		truncated?: boolean;
		single?: boolean;
		allowSkip?: boolean;
		/** Name of the root folder (for destinations at the root). */
		rootLabel?: string;
		/** Called once: the decisions, or null when cancelled. */
		onresolve: (decisions: Map<string, ConflictChoice> | null) => void;
	}

	let {
		open = $bindable(false),
		title,
		items,
		truncated = false,
		single = false,
		rootLabel = 'the root folder',
		allowSkip = true,
		onresolve
	}: Props = $props();

	let queue = new ConflictQueue([]);
	let applyAll = $state(false);
	let settled = false;
	// The queue is a plain object: mirror what the dialog shows.
	let current = $state<ConflictItem | null>(null);
	let position = $state(0);
	let remaining = $state(0);

	function sync() {
		current = queue.current;
		position = queue.position;
		remaining = queue.remaining;
	}

	$effect(() => {
		if (open) {
			queue = new ConflictQueue(items);
			applyAll = false;
			settled = false;
			sync();
		}
	});

	function finish(result: Map<string, ConflictChoice> | null) {
		if (settled) return;
		settled = true;
		open = false;
		onresolve(result);
	}

	function decide(choice: ConflictChoice) {
		queue.decide(choice, single || applyAll);
		sync();
		if (queue.done) finish(new Map(queue.decisions));
	}

	function onclose() {
		if (!settled) finish(null);
	}
</script>

<Dialog bind:open {title} size="md" alert {onclose}>
	{#if single}
		<p class="lead">
			{items.length}{truncated ? '+' : ''}
			{items.length === 1 ? 'entry already exists' : 'entries already exist'} at the destination.
		</p>
		<ul class="names mono" role="list">
			{#each items.slice(0, 50) as c (c.source)}<li>{c.destination}</li>{/each}
			{#if items.length > 50}<li class="more">and {items.length - 50} more</li>{/if}
		</ul>
	{:else if current}
		<p class="count num">Conflict {position} of {items.length}</p>
		<p class="lead">
			<span class="mono strong">{basename(current.destination)}</span> already exists in
			<span class="mono"
				>{parent(current.destination) === '.'
					? rootLabel
					: parent(current.destination)}</span
			>.
		</p>
		{#if current.self}
			<p class="count">It is being pasted into its own folder: keep both makes a copy.</p>
		{/if}
		<dl class="facts">
			<div>
				<dt>Existing</dt>
				<dd>
					{current.existing.type === 'dir'
						? 'Folder'
						: formatBytes(current.existing.size)}, modified
					{formatDateTime(current.existing.modifiedAt)}
				</dd>
			</div>
			<div>
				<dt>Incoming</dt>
				<dd class="mono">{current.source}</dd>
			</div>
		</dl>
		{#if remaining > 1}
			<div class="all">
				<Checkbox
					bind:checked={applyAll}
					label="Apply to All {remaining} Remaining Conflicts"
				/>
			</div>
		{/if}
	{/if}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => finish(null)}>Cancel</Button>
		{#if allowSkip}
			<Button variant="secondary" onclick={() => decide('skip')}
				>{single ? 'Skip Existing' : 'Skip'}</Button
			>
		{/if}
		<Button variant="secondary" onclick={() => decide('keep_both')}
			>{single || !current
				? 'Keep Both'
				: `Keep Both (${keepBothName(basename(current.destination))})`}</Button
		>
		<!-- An entry pasted into its own folder can only be duplicated. -->
		{#if single || !current?.self}
			<Button variant="danger" onclick={() => decide('overwrite')}
				>{single ? 'Replace All' : 'Replace'}</Button
			>
		{/if}
	{/snippet}
</Dialog>

<style>
	.count {
		margin-bottom: var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.lead {
		color: var(--text-default);
		font-size: var(--text-control);
		line-height: 22px;
	}

	.strong {
		color: var(--text-strong);
	}

	.facts {
		display: grid;
		gap: var(--space-2);
		margin: var(--space-3) 0 0;
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-canvas);
	}

	.facts div {
		display: grid;
		grid-template-columns: 80px minmax(0, 1fr);
		gap: var(--space-2);
	}

	dt {
		color: var(--text-muted);
	}

	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}

	.all {
		margin-top: var(--space-3);
	}

	.names {
		max-height: 180px;
		margin-top: var(--space-3);
		overflow-y: auto;
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-canvas);
		font-size: 12.5px;
	}

	.more {
		color: var(--text-muted);
		font-family: var(--font-sans);
	}
</style>
