<script lang="ts">
	// Jobs as a table (#26): on what, what, where, state, why and when. Rows
	// lead with the target's name (jobHeadline: "zerobyte" over "Check for
	// Updates"), so fifty checks do not read as fifty identical rows. On
	// phones a row is two lines: the headline with its state and start time.
	// Used by the jobs page, the dashboard and the environment page. Rows
	// arrive newest first from the server (manualSort: no client re-sorting).
	import type { Snippet } from 'svelte';
	import type { Job } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import IconCell from '$lib/features/common/IconCell.svelte';
	import { StatusBadge, Table, formatDateTime, formatRelative, type Column } from '$lib/ui';
	import { ORIGIN_LABELS, jobDuration, jobHeadline, type NameOf } from './labels';

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
			{ id: 'job', header: 'Job', cell: jobCell, maxWidth: '420px', stack: 'title' },
			{ id: 'state', header: 'State', cell: stateCell, width: '150px', stack: 'status' }
		];
		if (environments)
			cols.push({
				id: 'environment',
				header: 'Environment',
				cell: envCell,
				width: '150px',
				stack: 'hidden'
			});
		if (!compact)
			cols.push({
				id: 'origin',
				header: 'Origin',
				cell: originCell,
				width: '120px',
				stack: 'hidden'
			});
		cols.push({
			id: 'created',
			header: 'Started',
			cell: createdCell,
			width: '150px',
			stack: 'status'
		});
		if (!compact)
			cols.push({
				id: 'duration',
				header: 'Duration',
				cell: durationCell,
				width: '100px',
				numeric: true,
				stack: 'hidden'
			});
		return cols;
	});
</script>

{#snippet jobCell(j: Job)}
	{@const h = jobHeadline(j, {
		nameOf,
		fallback: j.environmentId ? environments?.get(j.environmentId) : undefined
	})}
	<IconCell icon="job">
		<div class="job">
			<a href={routes.job(j.id)} class="name">{h.title}</a>
			{#if h.subtitle}<span class="kind">{h.subtitle}</span>{/if}
		</div>
	</IconCell>
{/snippet}
{#snippet stateCell(j: Job)}
	<StatusBadge status={j.state} kind="job" />
{/snippet}
{#snippet envCell(j: Job)}
	{#if j.environmentId}
		<a href={routes.environment(j.environmentId)} class="env"
			>{environments?.get(j.environmentId) ?? 'Unknown environment'}</a
		>
	{:else}<span class="muted">Manager</span>{/if}
{/snippet}
{#snippet originCell(j: Job)}
	<span>{ORIGIN_LABELS[j.origin] ?? j.origin}</span>
{/snippet}
{#snippet createdCell(j: Job)}
	<time class="muted" datetime={j.createdAt} title={formatDateTime(j.createdAt)}
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

	.name {
		overflow: hidden;
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	a.name:hover {
		color: var(--accent-text);
	}

	.kind {
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.env {
		color: var(--text-default);
	}
</style>
