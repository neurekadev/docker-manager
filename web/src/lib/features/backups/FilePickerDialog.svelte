<script lang="ts">
	// Choose what to restore from a backup (#10): a lazily listed file tree
	// of the stack's project directory and volumes (or one volume) with
	// tri-state ticks. A ticked folder is restored whole and made identical
	// to the backup; nothing is listed before it is opened.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { SvelteSet } from 'svelte/reactivity';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Layers from '@lucide/svelte/icons/layers';
	import { Button, Checkbox, Dialog, formatDateTime } from '$lib/ui';
	import SnapshotTreeLevel from './SnapshotTreeLevel.svelte';
	import type { Backup, BackupNode } from './model';
	import { backupKeys } from './queries';
	import { pickerRoots, tickState, toggle } from './selection';

	type Props = {
		open?: boolean;
		backup: Backup;
		/** Only this volume of the backup (a volume's page). */
		volume?: string;
		/** The user chose: review the restore of these paths. */
		onnext: (paths: string[]) => void;
	};

	let { open = $bindable(false), backup, volume, onnext }: Props = $props();

	const qc = useQueryClient();
	const roots = $derived(pickerRoots(backup, volume));
	let selection = $state<string[]>([]);
	const expanded = new SvelteSet<string>();

	// A new backup or a fresh open starts with nothing ticked and the
	// roots open.
	let openedFor = '';
	$effect(() => {
		if (!open) return;
		const key = `${backup.id}:${volume ?? ''}`;
		if (key === openedFor) return;
		openedFor = key;
		selection = [];
		expanded.clear();
		for (const r of roots) expanded.add(r.path);
	});

	function children(dir: string): string[] | undefined {
		const data = qc.getQueryData<{ entries: BackupNode[] }>(
			backupKeys.contents(backup.id, dir)
		);
		return data?.entries.map((e) => e.path);
	}

	function flip(path: string) {
		selection = toggle(selection, path, children);
	}

	function expand(path: string) {
		if (expanded.has(path)) expanded.delete(path);
		else expanded.add(path);
	}

	const count = $derived(selection.length);
</script>

<Dialog
	bind:open
	size="xl"
	title="Choose What to Restore"
	description="Backup of {formatDateTime(
		backup.snapshotTime
	)}. Tick files and folders: a ticked folder is restored whole and made identical to the backup. Everything you leave unticked stays as it is."
>
	<div class="tree" role="region" aria-label="Files in the Backup">
		<div class="head" aria-hidden="true">
			<span></span><span></span><span></span><span>Name</span><span class="r">Size</span><span
				class="r time">Modified</span
			>
		</div>
		<ul>
			{#each roots as r (r.path)}
				{@const state = tickState(selection, r.path)}
				{@const isOpen = expanded.has(r.path)}
				<li>
					<div class="row root">
						<button
							type="button"
							class="twisty"
							class:open={isOpen}
							aria-expanded={isOpen}
							aria-label="{isOpen ? 'Close' : 'Open'} {r.label}"
							onclick={() => expand(r.path)}
						>
							<ChevronRight size={14} aria-hidden="true" />
						</button>
						<Checkbox
							label="All of {r.label}"
							hideLabel
							checked={state === 'checked'}
							indeterminate={state === 'mixed'}
							onchange={() => flip(r.path)}
						/>
						{#if r.kind === 'project'}<Layers
								size={16}
								aria-hidden="true"
							/>{:else}<HardDrive size={16} aria-hidden="true" />{/if}
						<button type="button" class="name" onclick={() => expand(r.path)}
							>{r.label}</button
						>
						<span class="path mono" title={r.path}>{r.path}</span>
					</div>
					{#if isOpen}
						<ul role="group" aria-label="Contents of {r.label}">
							<SnapshotTreeLevel
								backupId={backup.id}
								dir={r.path}
								depth={1}
								{selection}
								{expanded}
								ontoggle={flip}
								onexpand={expand}
							/>
						</ul>
					{/if}
				</li>
			{:else}
				<li class="empty">This backup lists no files to choose from.</li>
			{/each}
		</ul>
	</div>
	{#snippet footer()}
		<span class="count" aria-live="polite">
			{count === 0
				? 'Nothing selected'
				: `${count} ${count === 1 ? 'item' : 'items'} selected`}
		</span>
		<Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
		<Button
			variant="primary"
			disabled={count === 0}
			onclick={() => {
				open = false;
				onnext([...selection]);
			}}>Review Restore</Button
		>
	{/snippet}
</Dialog>

<style>
	.tree {
		max-height: min(60vh, 640px);
		overflow: auto;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		padding: var(--space-1) 0;
	}

	ul {
		display: grid;
	}

	.head {
		display: grid;
		grid-template-columns: 20px 16px 16px minmax(0, 1fr) 84px 150px;
		gap: var(--space-2);
		padding: 0 var(--space-3) var(--space-1);
		border-bottom: 1px solid var(--border-subtle);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.r {
		text-align: right;
	}

	.row.root {
		display: grid;
		grid-template-columns: 20px auto 16px auto minmax(0, 1fr);
		align-items: center;
		gap: var(--space-2);
		min-height: 36px;
		padding: 0 var(--space-3);
		color: var(--text-strong);
	}

	.row.root:hover {
		background: var(--surface-hover);
	}

	.row.root :global(svg) {
		color: var(--text-muted);
	}

	.twisty {
		display: inline-grid;
		place-items: center;
		width: 20px;
		height: 20px;
		padding: 0;
		border: 0;
		border-radius: var(--radius-sm);
		background: transparent;
		cursor: pointer;
	}

	.twisty :global(svg) {
		transition: transform var(--duration-fast) ease;
	}

	.twisty.open :global(svg) {
		transform: rotate(90deg);
	}

	.name {
		padding: 0;
		border: 0;
		background: transparent;
		color: inherit;
		font: inherit;
		font-weight: 600;
		text-align: left;
		cursor: pointer;
	}

	.path {
		overflow: hidden;
		color: var(--text-faint);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.empty {
		padding: var(--space-3);
		color: var(--text-muted);
	}

	.count {
		margin-right: auto;
		color: var(--text-muted);
	}

	@media (max-width: 640px) {
		.head {
			grid-template-columns: 20px 16px 16px minmax(0, 1fr) 72px;
		}

		.time,
		.path {
			display: none;
		}
	}
</style>
