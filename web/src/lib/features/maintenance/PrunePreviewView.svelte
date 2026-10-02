<script lang="ts">
	// A prune preview (#14): per category the candidates in removal order
	// with their reason and approximate size, then what is protected,
	// excluded or kept and why. Nothing was removed; a run revalidates every
	// candidate right before removing it.
	import { Badge, Table, formatBytes, formatRelative, type Column } from '$lib/ui';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import {
		DECISION,
		categoryLabel,
		itemBytes,
		type CategoryInfo,
		type PruneItem,
		type PrunePreview
	} from './model';

	let { preview, info }: { preview: PrunePreview; info?: CategoryInfo[] } = $props();

	const columns: Column<PruneItem>[] = [
		{
			id: 'name',
			header: 'Object',
			cell: nameCell,
			sortValue: (i) => i.name ?? i.id,
			stack: 'title'
		},
		{ id: 'decision', header: 'Decision', cell: decisionCell, width: '130px', stack: 'status' },
		{ id: 'reason', header: 'Why', cell: reasonCell },
		{
			id: 'bytes',
			header: 'Size',
			cell: bytesCell,
			numeric: true,
			width: '100px',
			sortValue: (i) => i.bytes
		},
		{ id: 'since', header: 'Age From', cell: sinceCell, width: '130px' }
	];

	const shown = $derived(preview.categories.filter((c) => c.items.length > 0 || c.remove > 0));
</script>

{#snippet nameCell(i: PruneItem)}
	<NameCell
		name={i.name || i.id.slice(0, 12)}
		sub={i.name ? i.id.slice(0, 19) : undefined}
		subMono
		mono={!i.name}
	/>
{/snippet}
{#snippet decisionCell(i: PruneItem)}
	<Badge tone={DECISION[i.decision].tone} dot
		>{i.decision === 'remove' ? 'Will Be Removed' : DECISION[i.decision].label}</Badge
	>
{/snippet}
{#snippet reasonCell(i: PruneItem)}<span class="reason">{i.reason}</span>{/snippet}
{#snippet bytesCell(i: PruneItem)}<span class="num">{itemBytes(i.bytes)}</span>{/snippet}
{#snippet sinceCell(i: PruneItem)}
	{#if i.since}<span class="num" title={i.since}>{formatRelative(i.since)}</span>{:else}<span
			class="muted">—</span
		>{/if}
{/snippet}

<div class="preview">
	<p class="total">
		<strong class="num">{preview.remove}</strong>
		{preview.remove === 1 ? 'object' : 'objects'} would be removed, about
		<strong class="num">{formatBytes(preview.bytes)}</strong>.
		<span class="muted">Previewed {formatRelative(preview.at)}.</span>
	</p>
	{#if preview.notes.length}
		<Disclosure summary="How Runs Decide">
			{#each preview.notes as n (n)}<p class="muted note">{n}</p>{/each}
		</Disclosure>
	{/if}

	{#each shown as c (c.category)}
		<section class="cat">
			<h3>
				{categoryLabel(c.category, info)}
				<span class="counts">
					<Badge tone={c.remove ? 'danger' : 'neutral'}>{c.remove} to remove</Badge>
					{#if c.protected}<Badge tone="ok">{c.protected} protected</Badge>{/if}
					{#if c.excluded}<Badge>{c.excluded} excluded</Badge>{/if}
					{#if c.retained}<Badge>{c.retained} kept</Badge>{/if}
					<span class="muted num"
						>≈ {formatBytes(c.bytes)}{c.unknownSizes
							? `, ${c.unknownSizes} of unknown size`
							: ''}</span
					>
				</span>
			</h3>
			<Table
				label="{categoryLabel(c.category, info)} in the Preview"
				rows={c.items}
				{columns}
				rowKey={(i) => i.id}
				manualSort
			/>
			{#if c.truncated}<p class="muted note">Only the first 200 objects are listed.</p>{/if}
		</section>
	{:else}
		<p class="muted">No rule finds anything to remove right now.</p>
	{/each}
</div>

<style>
	.preview {
		display: grid;
		gap: var(--space-4);
	}

	.total {
		color: var(--text-default);
	}

	.total strong {
		color: var(--text-strong);
	}

	.note {
		font-size: var(--text-caption);
	}

	.cat {
		display: grid;
		gap: var(--space-2);
		min-width: 0;
	}

	h3 {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-3);
		font-size: var(--text-control);
		color: var(--text-strong);
	}

	.counts {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		font-weight: var(--weight-regular);
		font-size: var(--text-caption);
	}

	.reason {
		font-size: var(--text-caption);
	}
</style>
