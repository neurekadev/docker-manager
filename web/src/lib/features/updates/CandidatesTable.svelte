<script lang="ts">
	// Candidates of an update policy (#20): per service the tagged
	// reference, the digest running on the host and the registry's digest
	// for the host platform (with when that image was published, if the
	// registry says so), the status and why it is (not) eligible. The
	// tag text never changes; only digests are compared.
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { Badge, Table, formatDateTime, formatRelative, type Column } from '$lib/ui';
	import Digest from '$lib/features/common/Digest.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import {
		candidateStatus,
		checkErrorText,
		publishedText,
		reasonLabel,
		type UpdateCandidate
	} from './model';

	let { candidates, label }: { candidates: UpdateCandidate[]; label: string } = $props();

	const columns: Column<UpdateCandidate>[] = [
		{
			id: 'service',
			header: 'Service',
			cell: serviceCell,
			sortValue: (c) => c.service,
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (c) => c.status,
			stack: 'status',
			width: '160px'
		},
		{ id: 'current', header: 'Running Digest', cell: currentCell, width: '170px' },
		{ id: 'candidate', header: 'Registry Digest', cell: candidateCell, width: '170px' },
		{ id: 'notes', header: 'Details', cell: notesCell },
		{
			id: 'checked',
			header: 'Checked',
			cell: checkedCell,
			sortValue: (c) => c.checkedAt ?? '',
			width: '150px'
		}
	];
</script>

{#snippet serviceCell(c: UpdateCandidate)}
	<NameCell name={c.service} sub={c.reference} subMono>
		{#snippet extra()}
			{#if c.nonVersionTag}
				<Badge tone="warn">
					<TriangleAlert size={12} aria-hidden="true" /> Tag Can Change Meaning
				</Badge>
			{/if}
		{/snippet}
	</NameCell>
{/snippet}
{#snippet statusCell(c: UpdateCandidate)}
	{@const s = candidateStatus(c.status)}
	<Badge tone={s.tone} dot>{s.label}</Badge>
{/snippet}
{#snippet currentCell(c: UpdateCandidate)}<Digest value={c.currentDigest} />{/snippet}
{#snippet candidateCell(c: UpdateCandidate)}
	<Digest
		value={c.candidateDigest}
		tone={c.status === 'update_available'
			? 'accent'
			: c.status === 'quarantined'
				? 'danger'
				: 'default'}
	/>
	{#if publishedText(c)}<span
			class="muted published"
			title="Published {formatDateTime(c.publishedAt)}">{publishedText(c)}</span
		>{/if}
{/snippet}
{#snippet notesCell(c: UpdateCandidate)}
	{@const reason = reasonLabel(c)}
	{@const err = checkErrorText(c)}
	<div class="notes">
		{#if reason}<span>{reason}</span>{/if}
		{#if c.nonVersionTag && c.status === 'update_available'}<span class="warn"
				>{c.reasonMessage ??
					'This tag can point to a different version at any time, for example a new major release.'}</span
			>{/if}
		{#if err}<span class="danger">{err}</span>{/if}
		{#if c.retryAfterSeconds}<span class="muted"
				>The registry asked to wait {c.retryAfterSeconds} s before the next check.</span
			>{/if}
		{#if c.platform}<span class="muted mono">{c.platform}</span>{/if}
		{#if !reason && !err && !c.platform}<span class="muted">—</span>{/if}
	</div>
{/snippet}
{#snippet checkedCell(c: UpdateCandidate)}
	{#if c.checkedAt}<span class="num" title={formatDateTime(c.checkedAt)}
			>{formatRelative(c.checkedAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}

<Table
	{label}
	rows={candidates}
	{columns}
	rowKey={(c) => c.id}
	sort={{ column: 'service', direction: 'asc' }}
/>

<style>
	.notes {
		display: flex;
		flex-direction: column;
		gap: 2px;
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.danger {
		color: var(--danger);
	}

	.published {
		display: block;
		margin-top: 2px;
		font-size: var(--text-caption);
	}

	.warn {
		color: var(--warn);
	}
</style>
