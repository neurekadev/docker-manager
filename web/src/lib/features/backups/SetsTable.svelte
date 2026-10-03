<script lang="ts">
	// Backup sets (#10, #246): one line per backup run with its state
	// (partial when any backup failed or is missing, never complete), a
	// summary of its backups, duration and size. Details opens a drawer
	// with every backup and, while the set runs, its live progress.
	// Partial and failed sets offer to retry only what did not complete.
	import { useQueryClient } from '@tanstack/svelte-query';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import {
		Badge,
		Button,
		Drawer,
		Meter,
		Table,
		formatBytes,
		formatDateTime,
		formatDuration,
		type Column
	} from '$lib/ui';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { retrySet } from './actions';
	import SetDetail from './SetDetail.svelte';
	import {
		activityPercent,
		setBytes,
		setDuration,
		setState,
		setSummary,
		type Backup,
		type BackupActivity,
		type BackupSet
	} from './model';

	type Row = BackupSet;

	type Props = {
		sets: Row[];
		label: string;
		/** The caller may back up (retry). */
		canRetry?: boolean;
		environmentName: (id: string) => string;
		/** A repository's name by its ID (each backup has a copy per repository). */
		repositoryName?: (id: string) => string;
		/** Backups of the sets (their sizes). */
		backups?: Backup[];
		/** Running backup jobs (progress of pending sets). */
		activity?: BackupActivity[];
	};

	let {
		sets,
		label,
		canRetry = false,
		environmentName,
		repositoryName,
		backups,
		activity = []
	}: Props = $props();
	const qc = useQueryClient();
	let retrying = $state<string | null>(null);
	let selectedId = $state<string | null>(null);
	let drawerOpen = $state(false);
	const selected = $derived(sets.find((s) => s.id === selectedId));

	const jobsOf = (id: string) => activity.filter((a) => a.setId === id);

	async function retry(s: Row) {
		retrying = s.id;
		await retrySet(qc, s);
		retrying = null;
	}

	function details(s: Row) {
		selectedId = s.id;
		drawerOpen = true;
	}

	/** Overall progress of a running set: the mean of its jobs. */
	function setPercent(s: Row): number | undefined {
		const jobs = jobsOf(s.id);
		if (!jobs.length) return undefined;
		const known = jobs.map(activityPercent).filter((p) => p >= 0);
		return known.length ? known.reduce((a, b) => a + b, 0) / jobs.length : 0;
	}

	const columns = $derived<Column<Row>[]>([
		{
			id: 'started',
			header: 'Started',
			cell: startedCell,
			sortValue: (s) => s.startedAt,
			width: '190px',
			stack: 'title'
		},
		{ id: 'state', header: 'State', cell: stateCell, width: '170px', stack: 'status' },
		{ id: 'backups', header: 'Backups', cell: backupsCell, stack: 'meta' },
		{
			id: 'duration',
			header: 'Duration',
			cell: durationCell,
			sortValue: (s) => setDuration(s) ?? -1,
			width: '110px',
			numeric: true,
			stack: 'hidden'
		},
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			sortValue: (s) => setBytes(backups, s.id) ?? -1,
			title: () => 'The size of the data this run backed up',
			width: '100px',
			numeric: true,
			stack: 'hidden'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '220px',
			pin: 'end',
			stack: 'actions'
		}
	]);
</script>

{#snippet startedCell(s: Row)}
	<NameCell
		icon="backup"
		name={formatDateTime(s.startedAt)}
		sub={s.origin === 'scheduled'
			? 'Scheduled'
			: s.origin === 'api_token'
				? 'API Token'
				: 'Started by Hand'}
	/>
{/snippet}
{#snippet stateCell(s: Row)}
	{@const st = setState(s.state)}
	{@const pct = s.state === 'pending' ? setPercent(s) : undefined}
	{#if pct !== undefined}
		<span class="running">
			<Badge tone={st.tone} dot>{st.label}</Badge>
			<Meter
				value={pct}
				max={100}
				role="progressbar"
				tone="neutral"
				label="Progress of the Set Started {formatDateTime(s.startedAt)}"
			/>
		</span>
	{:else}
		<Badge tone={st.tone} dot>{st.label}</Badge>
	{/if}
{/snippet}
{#snippet backupsCell(s: Row)}<span class="muted">{setSummary(s)}</span>{/snippet}
{#snippet durationCell(s: Row)}
	{@const d = setDuration(s)}
	<span class="num">{d !== undefined ? formatDuration(d) : '—'}</span>
{/snippet}
{#snippet sizeCell(s: Row)}<span class="num">{formatBytes(setBytes(backups, s.id))}</span>{/snippet}
{#snippet actionsCell(s: Row)}
	<div class="row-actions">
		{#if (s.state === 'partial' || s.state === 'failed') && canRetry}
			<Button
				size="sm"
				variant="secondary"
				icon={RotateCcw}
				loading={retrying === s.id}
				onclick={() => retry(s)}>Retry Missing</Button
			>
		{/if}
		<Button size="sm" variant="ghost" onclick={() => details(s)}>Details</Button>
	</div>
{/snippet}

<Table
	{label}
	rows={sets}
	{columns}
	rowKey={(s) => s.id}
	sort={{ column: 'started', direction: 'desc' }}
/>

<Drawer
	bind:open={drawerOpen}
	title={selected ? `Run of ${formatDateTime(selected.startedAt)}` : 'Backup Run'}
	size="640px"
>
	{#if selected}
		{#key selected.id}
			<SetDetail
				set={selected}
				{environmentName}
				{repositoryName}
				bytes={setBytes(backups, selected.id)}
				activity={jobsOf(selected.id)}
			/>
		{/key}
	{/if}
	{#snippet footer()}
		{#if selected}
			{#if (selected.state === 'partial' || selected.state === 'failed') && canRetry}
				<Button
					icon={RotateCcw}
					loading={retrying === selected.id}
					onclick={() => selected && retry(selected)}>Retry Missing</Button
				>
			{/if}
		{/if}
	{/snippet}
</Drawer>

<style>
	.running {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
		min-width: 0;
	}

	.row-actions {
		display: flex;
		justify-content: flex-end;
		align-items: center;
		gap: var(--space-1);
	}
</style>
