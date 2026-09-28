<script lang="ts">
	// The recent runs of a policy (#13, #14, #20): one row per run (manual
	// or scheduled) with what it did, how it ended in one line ("20 checks,
	// all succeeded"), how long it took, and a link to its job (the first
	// failed one) or to the policy's jobs of that kind.
	import type { Snippet } from 'svelte';
	import { routes } from '$lib/routes';
	import {
		Button,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';
	import { ORIGIN_LABELS, jobKindLabel } from './labels';
	import { runDuration, runJob, runSummary, type JobRun } from './runs';

	interface Props {
		runs: JobRun[];
		label: string;
		empty?: Snippet;
	}

	let { runs, label, empty }: Props = $props();

	const columns: Column<JobRun>[] = [
		{ id: 'when', header: 'Started', cell: whenCell, width: '200px', stack: 'title' },
		{ id: 'what', header: 'Run', cell: whatCell, stack: 'meta' },
		{ id: 'result', header: 'Result', cell: resultCell, stack: 'status' },
		{
			id: 'duration',
			header: 'Duration',
			cell: durationCell,
			numeric: true,
			width: '110px',
			stack: 'meta'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			align: 'end',
			width: '120px',
			stack: 'actions'
		}
	];
</script>

{#snippet whenCell(r: JobRun)}
	<span class="num" title={formatDateTime(r.at)}>{formatRelative(r.at)}</span>
{/snippet}
{#snippet whatCell(r: JobRun)}
	<span>{jobKindLabel(r.kind)}</span>
	<span class="muted"> · {ORIGIN_LABELS[r.origin] ?? r.origin}</span>
{/snippet}
{#snippet resultCell(r: JobRun)}
	{@const s = runSummary(r.jobs, r.kind)}
	<span class="result">
		<StatusBadge status={s.state} kind="job" />
		{#if s.total > 1}<span class="muted">{s.text}</span>{/if}
	</span>
{/snippet}
{#snippet durationCell(r: JobRun)}<span class="num">{runDuration(r) || '—'}</span>{/snippet}
{#snippet actionsCell(r: JobRun)}
	{@const j = runJob(r)}
	{#if j}
		<Button size="sm" variant="ghost" href={routes.job(j.id)}>Open job</Button>
	{:else}
		<Button
			size="sm"
			variant="ghost"
			href={routes.jobs(r.kind, { policyId: r.jobs[0]?.policyId })}>Open jobs</Button
		>
	{/if}
{/snippet}

<Table {label} rows={runs} {columns} rowKey={(r) => r.key} manualSort {empty} />

<style>
	.result {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}
</style>
