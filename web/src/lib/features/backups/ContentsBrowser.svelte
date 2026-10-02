<script lang="ts">
	// Browse a snapshot (#10): directories, files with size and time
	// (permissions and owners on request); download one regular file (up to
	// 2 GiB, audited).
	// Stack snapshots hold compose.yaml and .env, so browsing needs the same
	// permission as reading the stack's definition; the server decides.
	import { createQuery } from '@tanstack/svelte-query';
	import CornerLeftUp from '@lucide/svelte/icons/corner-left-up';
	import Download from '@lucide/svelte/icons/download';
	import File from '@lucide/svelte/icons/file';
	import Folder from '@lucide/svelte/icons/folder';
	import Link from '@lucide/svelte/icons/link';
	import {
		Button,
		Chip,
		IconButton,
		Table,
		formatBytes,
		formatDateTime,
		type Column
	} from '$lib/ui';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { modeText, parentPath, pathCrumbs, type BackupNode } from './model';
	import { backupContentsQuery } from './queries';

	interface Props {
		backupId: string;
		canDownload: boolean;
		/** Start below this path (e.g. the stack's project directory). */
		root?: string;
		/** Restore one file in place (the restore wizard). */
		onrestorefile?: (path: string) => void;
	}

	let { backupId, canDownload, root = '/', onrestorefile }: Props = $props();
	let path = $state('');
	$effect(() => {
		if (!path) path = root;
	});
	const contents = createQuery(() => ({
		...backupContentsQuery(backupId, path || root),
		enabled: !!(path || root)
	}));

	function downloadHref(p: string): string {
		return `/api/v1/backups/${encodeURIComponent(backupId)}/contents/download?path=${encodeURIComponent(p)}`;
	}

	// Permission bits and owners are for experts: shown on request.
	let showPermissions = $state(false);

	const columns = $derived<Column<BackupNode>[]>([
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (n) => `${n.type === 'dir' ? 0 : 1}${n.name}`,
			stack: 'title'
		},
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			numeric: true,
			width: '100px',
			sortValue: (n) => n.size
		},
		...(showPermissions
			? [
					{
						id: 'mode',
						header: 'Permissions',
						cell: modeCell,
						width: '110px',
						mono: true,
						stack: 'hidden' as const
					},
					{
						id: 'owner',
						header: 'Owner',
						cell: ownerCell,
						width: '100px',
						mono: true,
						stack: 'hidden' as const
					}
				]
			: []),
		{
			id: 'mtime',
			header: 'Modified',
			cell: mtimeCell,
			width: '170px',
			sortValue: (n) => n.mtime ?? ''
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '150px',
			stack: 'actions'
		}
	]);
</script>

{#snippet nameCell(n: BackupNode)}
	{#if n.type === 'dir'}
		<button type="button" class="entry dir" onclick={() => (path = n.path)}>
			<Folder size={16} aria-hidden="true" />{n.name}
		</button>
	{:else}
		<span class="entry">
			{#if n.type === 'symlink'}<Link size={16} aria-hidden="true" />{:else}<File
					size={16}
					aria-hidden="true"
				/>{/if}
			<span class="mono">{n.name}</span>
		</span>
	{/if}
{/snippet}
{#snippet sizeCell(n: BackupNode)}<span class="num"
		>{n.type === 'dir' ? '—' : formatBytes(n.size)}</span
	>{/snippet}
{#snippet modeCell(n: BackupNode)}{modeText(n.mode)}{/snippet}
{#snippet ownerCell(n: BackupNode)}{n.uid}:{n.gid}{/snippet}
{#snippet mtimeCell(n: BackupNode)}{#if n.mtime}<span class="num">{formatDateTime(n.mtime)}</span
		>{/if}{/snippet}
{#snippet actionsCell(n: BackupNode)}
	{#if n.type === 'file'}
		<span class="acts">
			{#if canDownload}
				<a
					class="dl"
					href={downloadHref(n.path)}
					download={n.name}
					aria-label="Download {n.name}"
				>
					<Download size={16} aria-hidden="true" />
				</a>
			{/if}
			{#if onrestorefile}
				<Button size="sm" onclick={() => onrestorefile(n.path)}>Restore File</Button>
			{/if}
		</span>
	{/if}
{/snippet}

<div class="browser">
	<nav class="crumbs" aria-label="Path in the Backup">
		{#each pathCrumbs(path || root) as c, i (c.path)}
			{#if i > 0}<span class="sep" aria-hidden="true">/</span>{/if}
			<button
				type="button"
				class="crumb mono"
				onclick={() => (path = c.path)}
				aria-current={c.path === (path || root) ? 'location' : undefined}
			>
				{c.name === '/' ? 'Backup Root' : c.name}
			</button>
		{/each}
		{#if (path || root) !== '/'}
			<IconButton
				label="Up One Directory"
				icon={CornerLeftUp}
				size="sm"
				onclick={() => (path = parentPath(path || root))}
			/>
		{/if}
		<span class="spacer"></span>
		<Chip
			label="Show Permissions"
			size="sm"
			selected={showPermissions}
			onclick={() => (showPermissions = !showPermissions)}
		/>
	</nav>
	<QueryView
		query={contents}
		errorTitle="The backup contents could not be listed."
		deniedTitle="You can't browse this backup."
		deniedDescription="Stack backups need the permission to read the stack's Compose definition; volume backups the permission to read the volume's files."
	>
		{#snippet children(c)}
			<Table
				label="Contents of {c.path}"
				rows={c.entries}
				{columns}
				rowKey={(n) => n.path}
				sort={{ column: 'name', direction: 'asc' }}
				maxHeight="420px"
			>
				{#snippet empty()}<p class="muted pad">This directory is empty.</p>{/snippet}
			</Table>
			{#if c.truncated}<p class="muted pad">Only the first 500 entries are listed.</p>{/if}
		{/snippet}
	</QueryView>
</div>

<style>
	.browser {
		display: grid;
		gap: var(--space-2);
	}

	.crumbs {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1);
		padding: var(--space-3) var(--space-4) 0;
	}

	.crumb,
	.entry.dir {
		padding: 2px var(--space-1);
		border: 0;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--accent-text);
		cursor: pointer;
	}

	.crumb[aria-current='location'] {
		color: var(--text-strong);
	}

	.crumb:hover,
	.entry.dir:hover {
		background: var(--surface-hover);
	}

	.spacer {
		flex: 1;
	}

	.sep {
		color: var(--text-faint);
	}

	.entry {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
	}

	.acts {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
	}

	.dl {
		display: inline-grid;
		place-items: center;
		width: var(--control-height-sm);
		height: var(--control-height-sm);
		border-radius: var(--radius-md);
		color: var(--text-default);
	}

	.dl:hover {
		background: var(--surface-hover);
	}

	.pad {
		padding: var(--space-3) var(--space-4);
	}
</style>
