<script lang="ts">
	// One directory of the backup file picker (#10): its entries, listed
	// when the directory is opened (at most 500), each with a tri-state
	// tick and the file manager's icons. Opened subdirectories render this
	// component again one level deeper.
	import { createQuery } from '@tanstack/svelte-query';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import { Checkbox, Skeleton, formatBytes, formatDateTime } from '$lib/ui';
	import { entryIcon } from '$lib/features/files/icons';
	import { actionError } from '$lib/features/common/errors';
	import Self from './SnapshotTreeLevel.svelte';
	import type { BackupNode } from './model';
	import { backupContentsQuery } from './queries';
	import { tickState } from './selection';

	interface Props {
		backupId: string;
		dir: string;
		depth: number;
		selection: readonly string[];
		expanded: ReadonlySet<string>;
		ontoggle: (path: string) => void;
		onexpand: (path: string) => void;
	}

	let { backupId, dir, depth, selection, expanded, ontoggle, onexpand }: Props = $props();

	const contents = createQuery(() => backupContentsQuery(backupId, dir));
	const entries = $derived(
		[...(contents.data?.entries ?? [])].sort(
			(a, b) =>
				Number(b.type === 'dir') - Number(a.type === 'dir') || a.name.localeCompare(b.name)
		)
	);

	function icon(n: BackupNode) {
		return entryIcon({
			name: n.name,
			type: n.type === 'dir' ? 'dir' : n.type === 'symlink' ? 'symlink' : 'file'
		});
	}
</script>

{#if contents.isPending}
	<li class="status" style:--depth={depth}><Skeleton lines={2} height="16px" /></li>
{:else if contents.isError}
	<li class="status error" style:--depth={depth} role="alert">
		{actionError(contents.error)}
	</li>
{:else}
	{#each entries as n (n.path)}
		{@const state = tickState(selection, n.path)}
		{@const open = expanded.has(n.path)}
		{@const ic = icon(n)}
		<li>
			<div class="row" style:--depth={depth}>
				{#if n.type === 'dir'}
					<button
						type="button"
						class="twisty"
						class:open
						aria-expanded={open}
						aria-label="{open ? 'Close' : 'Open'} {n.name}"
						onclick={() => onexpand(n.path)}
					>
						<ChevronRight size={14} aria-hidden="true" />
					</button>
				{:else}
					<span class="twisty" aria-hidden="true"></span>
				{/if}
				<Checkbox
					label={n.name}
					hideLabel
					checked={state === 'checked'}
					indeterminate={state === 'mixed'}
					onchange={() => ontoggle(n.path)}
				/>
				<ic.icon size={16} aria-hidden="true" class="entry-icon {ic.tone}" />
				{#if n.type === 'dir'}
					<button type="button" class="name dir" onclick={() => onexpand(n.path)}
						>{n.name}</button
					>
				{:else}
					<span class="name">{n.name}</span>
				{/if}
				<span class="size num">{n.type === 'dir' ? '' : formatBytes(n.size)}</span>
				<span class="time num">{n.mtime ? formatDateTime(n.mtime) : ''}</span>
			</div>
			{#if n.type === 'dir' && open}
				<ul role="group" aria-label="Contents of {n.name}">
					<Self
						{backupId}
						dir={n.path}
						depth={depth + 1}
						{selection}
						{expanded}
						{ontoggle}
						{onexpand}
					/>
				</ul>
			{/if}
		</li>
	{:else}
		<li class="status" style:--depth={depth}>This folder is empty.</li>
	{/each}
	{#if contents.data?.truncated}
		<li class="status" style:--depth={depth}>
			Only the first 500 entries are listed. Tick the folder to restore all of it.
		</li>
	{/if}
{/if}

<style>
	ul {
		display: contents;
	}

	.row {
		display: grid;
		grid-template-columns: 20px auto 16px minmax(0, 1fr) 84px 150px;
		align-items: center;
		gap: var(--space-2);
		min-height: 32px;
		padding: 0 var(--space-3) 0 calc(var(--space-3) + var(--depth) * 18px);
		border-radius: var(--radius-md);
		color: var(--text-default);
	}

	.row:hover {
		background: var(--surface-hover);
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
		color: var(--text-muted);
		cursor: pointer;
	}

	span.twisty {
		cursor: default;
	}

	.twisty :global(svg) {
		transition: transform var(--duration-fast) ease;
	}

	.twisty.open :global(svg) {
		transform: rotate(90deg);
	}

	.row :global(.entry-icon) {
		flex: none;
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

	.name.dir {
		padding: 0;
		border: 0;
		background: transparent;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	.size,
	.time {
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-align: right;
		white-space: nowrap;
	}

	.status {
		padding: var(--space-1) var(--space-3) var(--space-1)
			calc(var(--space-3) + 20px + var(--depth) * 18px);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.status.error {
		color: var(--danger);
	}

	@media (max-width: 640px) {
		.row {
			grid-template-columns: 20px auto 16px minmax(0, 1fr) 72px;
		}

		.time {
			display: none;
		}
	}
</style>
