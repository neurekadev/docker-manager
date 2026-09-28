<script lang="ts">
	// What a restore would write (#10), computed by the environment's agent:
	// each target by name (its host path and the owners its files carry in
	// the tooltip) with files and bytes, how many files are overwritten,
	// removed and added, and free space; the containers that stop and
	// restart; what blocks the restore.
	import type { Schema } from '$lib/api/client';
	import { Badge, Notice, Table, formatBytes, type Column } from '$lib/ui';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { restoreTargetName, type RestorePreview } from './model';

	type Target = Schema<'RestoreTarget'>;
	type Affected = Schema<'AffectedContainer'>;

	let { preview }: { preview: RestorePreview } = $props();

	const targetColumns: Column<Target>[] = [
		{
			id: 'path',
			header: 'Target',
			cell: pathCell,
			maxWidth: '320px',
			title: (t) =>
				[t.path, t.owners?.length ? `Files owned by ${t.owners.join(', ')}` : '']
					.filter(Boolean)
					.join('\n'),
			stack: 'title'
		},
		{ id: 'files', header: 'Files', cell: filesCell, numeric: true, width: '110px' },
		{ id: 'changes', header: 'Changes', cell: changesCell, width: '250px' },
		{ id: 'space', header: 'Free space', cell: spaceCell, numeric: true, width: '120px' }
	];
	const containerColumns: Column<Affected>[] = [
		{ id: 'name', header: 'Container', cell: cName, stack: 'title' },
		{ id: 'now', header: 'Now', cell: cNow, width: '120px', stack: 'status' },
		{ id: 'during', header: 'During the restore', cell: cDuring }
	];
</script>

{#snippet pathCell(t: Target)}
	<NameCell
		name={restoreTargetName(t)}
		sub={t.exists ? 'Replaced in place' : 'Created by the restore'}
	/>
{/snippet}
{#snippet filesCell(t: Target)}
	<span class="num">{t.files}{t.complete ? '' : '+'} ({formatBytes(t.bytes)})</span>
{/snippet}
{#snippet changesCell(t: Target)}
	<span class="changes">
		{#if t.overwritten}<Badge tone="warn">{t.overwritten} overwritten</Badge>{/if}
		{#if t.removed}<Badge tone="danger">{t.removed} removed</Badge>{/if}
		{#if t.added}<Badge tone="ok">{t.added} added</Badge>{/if}
		{#if !t.overwritten && !t.removed && !t.added}<span class="muted">No changes</span>{/if}
	</span>
{/snippet}
{#snippet spaceCell(t: Target)}<span class="num"
		>{t.freeBytes < 0 ? 'Unknown' : formatBytes(t.freeBytes)}</span
	>{/snippet}
{#snippet cName(c: Affected)}<NameCell name={c.name} sub={c.service} />{/snippet}
{#snippet cNow(c: Affected)}
	{#if c.running}<Badge tone="ok" dot>Running</Badge>{:else}<Badge dot>Stopped</Badge>{/if}
{/snippet}
{#snippet cDuring(c: Affected)}
	<span class="small">
		{#if c.protected}Keeps running: {c.protected}{:else if c.running}Stops, then starts again{:else}Stays
			stopped{/if}
	</span>
{/snippet}

<div class="preview">
	{#if preview.blocked?.length}
		<Notice tone="danger" title="This restore can't run as chosen" live="alert">
			<ul class="plain" role="list">
				{#each preview.blocked as b (b)}<li>{b}</li>{/each}
			</ul>
		</Notice>
	{/if}
	{#each preview.conflicts ?? [] as c (c)}<Notice tone="warn" title="Conflict" live="none"
			>{c}</Notice
		>{/each}
	{#each preview.warnings ?? [] as w (w)}<Notice tone="warn" title="Warning" live="none"
			>{w}</Notice
		>{/each}
	<Table
		label="Restore targets"
		rows={preview.targets}
		columns={targetColumns}
		rowKey={(t) => t.path}
		manualSort
	/>
	{#if preview.affectedContainers.length}
		<h3>Containers using this data</h3>
		<Table
			label="Containers using the restored data"
			rows={preview.affectedContainers}
			columns={containerColumns}
			rowKey={(c) => c.name}
			manualSort
		/>
	{:else}
		<p class="muted">No container uses this data: nothing stops.</p>
	{/if}
</div>

<style>
	.preview {
		display: grid;
		gap: var(--space-3);
	}

	h3 {
		font-size: var(--text-control);
		color: var(--text-strong);
	}

	.changes {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1);
	}

	.small {
		font-size: var(--text-caption);
	}

	.plain {
		display: grid;
		gap: 2px;
	}
</style>
