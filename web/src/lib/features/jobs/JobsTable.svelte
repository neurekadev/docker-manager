<script lang="ts">
	// Jobs as a table (#26): what, on what, where, state, why and when. Used
	// by the jobs page, the dashboard and the environment page. Rows arrive
	// newest first from the server (manualSort: no client re-sorting).
	import type { Snippet } from 'svelte';
	import type { Job } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { StatusBadge, Table, formatDateTime, formatRelative, type Column } from '$lib/ui';
	import {
		ORIGIN_LABELS,
		jobDuration,
		jobKindLabel,
		jobTargetLabel,
		type NameOf
	} from './labels';

	interface Props {
		jobs: Job[];
		label: string;
		/** Environment names by ID (hidden column when absent). */
		environments?: Map<string, string>;
		nameOf?: NameOf;
		/** Leave out the origin and duration columns (dashboard). */
		compact?: boolean;
		empty?: Snippet;
		now?: Date;
	}

	let { jobs, label, environments, nameOf, compact = false, empty, now }: Props = $props();

	const columns = $derived.by(() => {
		const cols: Column<Job>[] = [
			{ id: 'job', header: 'Job', cell: jobCell, stack: 'title' },
			{ id: 'state', header: 'State', cell: stateCell, width: '150px', stack: 'status' }
		];
		if (environments)
			cols.push({ id: 'environment', header: 'Environment', cell: envCell, width: '150px' });
		if (!compact)
			cols.push({ id: 'origin', header: 'Origin', cell: originCell, width: '120px' });
		cols.push({ id: 'created', header: 'Started', cell: createdCell, width: '150px' });
		if (!compact)
			cols.push({
				id: 'duration',
				header: 'Duration',
				cell: durationCell,
				width: '100px',
				numeric: true
			});
		return cols;
	});
</script>

{#snippet jobCell(j: Job)}
	<div class="job">
		<a href={routes.job(j.id)} class="kind">{jobKindLabel(j.kind)}</a>
		{#if jobTargetLabel(j, nameOf)}<span class="target mono">{jobTargetLabel(j, nameOf)}</span
			>{/if}
	</div>
{/snippet}
{#snippet stateCell(j: Job)}
	<StatusBadge status={j.state} kind="job" />
{/snippet}
{#snippet envCell(j: Job)}
	{#if j.environmentId}
		<a href={routes.environment(j.environmentId)} class="env"
			>{environments?.get(j.environmentId) ?? j.environmentId.slice(0, 8)}</a
		>
	{:else}<span class="muted">DockYard</span>{/if}
{/snippet}
{#snippet originCell(j: Job)}
	<span>{ORIGIN_LABELS[j.origin] ?? j.origin}</span>
{/snippet}
{#snippet createdCell(j: Job)}
	<time datetime={j.createdAt} title={formatDateTime(j.createdAt)}
		>{formatRelative(j.createdAt, now)}</time
	>
{/snippet}
{#snippet durationCell(j: Job)}
	<span class="num">{jobDuration(j, now) || '—'}</span>
{/snippet}

<Table {label} rows={jobs} {columns} rowKey={(j) => j.id} manualSort {empty} />

<style>
	.job {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.kind {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.target {
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.env {
		color: var(--text-default);
	}
</style>
