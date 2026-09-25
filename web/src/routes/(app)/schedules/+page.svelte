<script lang="ts">
	// Schedules (#13): every scheduled policy the user can read, across
	// backups, update checks and runs, prunes and repository verification,
	// with its cron in its own time zone, the next run (DST annotated) and
	// the last run's outcome. A row opens the next runs and the history.
	import { createQuery } from '@tanstack/svelte-query';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import type { Schedule } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery, schedulesQuery } from '$lib/api/queries';
	import ScheduleDetail from '$lib/features/schedules/ScheduleDetail.svelte';
	import {
		dstLabel,
		formatRunTime,
		policyHref,
		runStatus,
		scheduleState
	} from '$lib/features/schedules/model';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf, isRestricted } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		DeniedState,
		Drawer,
		EmptyState,
		ErrorState,
		PageHeader,
		Select,
		Skeleton,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';

	usePage({ title: 'Schedules', crumbs: [{ label: 'Schedules' }], environmentScoped: true });

	const perms = createQuery(() => myPermissionsQuery());
	const restricted = $derived(perms.data ? isRestricted(accessOf(perms.data)) : false);

	let kind = $state('');
	let enabled = $state('');
	const schedules = createQuery(() => ({
		...schedulesQuery({ environmentId: environmentSelection.id ?? undefined }),
		enabled: !restricted
	}));
	const envs = createQuery(() => environmentsQuery());
	const names = $derived(new Map((envs.data ?? []).map((e) => [e.id, e.name])));

	const all = $derived(schedules.data ?? []);
	const rows = $derived(
		all.filter(
			(s) =>
				(!kind || s.kind === kind) &&
				(!enabled ||
					(enabled === 'enabled'
						? s.enabled && !s.invalidReason
						: !s.enabled || !!s.invalidReason))
		)
	);
	const kindOptions = $derived([
		{ value: '', label: 'All kinds' },
		...[...new Map(all.map((s) => [s.kind, s.kindLabel])).entries()].map(([value, label]) => ({
			value,
			label
		}))
	]);
	const enabledOptions = [
		{ value: '', label: 'Any state' },
		{ value: 'enabled', label: 'Enabled' },
		{ value: 'disabled', label: 'Disabled or invalid' }
	];

	let selected = $state<Schedule | null>(null);
	let drawerOpen = $state(false);
	function open(s: Schedule) {
		selected = s;
		drawerOpen = true;
	}

	const columns: Column<Schedule>[] = [
		{
			id: 'policy',
			header: 'Policy',
			cell: policyCell,
			sortValue: (s) => s.policyName,
			stack: 'title'
		},
		{
			id: 'state',
			header: 'State',
			cell: stateCell,
			sortValue: (s) => (s.enabled ? 0 : 1),
			width: '120px',
			stack: 'status'
		},
		{
			id: 'environment',
			header: 'Environment',
			cell: envCell,
			sortValue: (s) => names.get(s.environmentId ?? '') ?? '',
			width: '140px'
		},
		{ id: 'schedule', header: 'Schedule', cell: cronCell, width: '190px' },
		{
			id: 'next',
			header: 'Next run',
			cell: nextCell,
			sortValue: (s) => (s.enabled && s.nextRun ? Date.parse(s.nextRun.utc) : null),
			width: '230px'
		},
		{ id: 'last', header: 'Last run', cell: lastCell, width: '190px' },
		{
			id: 'actions',
			header: 'Actions',
			cell: actionsCell,
			hideHeader: true,
			align: 'end',
			width: '110px',
			stack: 'actions'
		}
	];
</script>

{#snippet policyCell(s: Schedule)}
	<div class="policy">
		<a href={policyHref(s.kind)} class="strong">{s.policyName}</a>
		<span class="muted small">{s.kindLabel}</span>
	</div>
{/snippet}
{#snippet stateCell(s: Schedule)}
	{@const st = scheduleState(s)}
	<StatusBadge status={st.status} label={st.label} title={s.invalidReason} />
{/snippet}
{#snippet envCell(s: Schedule)}
	{#if s.environmentId}{names.get(s.environmentId) ?? s.environmentId.slice(0, 8)}{:else}<span
			class="muted">DockYard</span
		>{/if}
{/snippet}
{#snippet cronCell(s: Schedule)}
	<div class="policy">
		<span class="mono">{s.cron}</span>
		<span class="muted small">{s.timeZone}</span>
	</div>
{/snippet}
{#snippet nextCell(s: Schedule)}
	{#if s.nextRun && s.enabled && !s.invalidReason}
		<div class="policy">
			<span class="num">{formatRunTime(s.nextRun.at, s.timeZone)}</span>
			<span class="muted small">{formatRelative(s.nextRun.utc)}</span>
			{#if dstLabel(s.nextRun)}<Badge tone="warn" title={s.nextRun.dstNote}
					>{dstLabel(s.nextRun)}</Badge
				>{/if}
		</div>
	{:else}<span class="muted">{s.invalidReason ? 'Never (invalid)' : 'Not scheduled'}</span>{/if}
{/snippet}
{#snippet lastCell(s: Schedule)}
	{@const r = s.recentRuns[0]}
	{#if r}
		{@const st = runStatus(r)}
		<div class="policy">
			<StatusBadge status={st.status} kind={st.kind} label={st.label || undefined} />
			<span class="muted small" title={formatDateTime(r.scheduledFor)}
				>{formatRelative(r.scheduledFor)}</span
			>
		</div>
	{:else}<span class="muted">No runs yet</span>{/if}
{/snippet}
{#snippet actionsCell(s: Schedule)}
	<Button size="sm" variant="ghost" onclick={() => open(s)}>Details</Button>
{/snippet}

{#if restricted}
	<DeniedState level={1} />
{:else}
	<div class="page">
		<PageHeader
			title="Schedules"
			description="Every scheduled policy in one place. Times are shown in each policy's own time zone."
			icon={CalendarClock}
			color="violet"
		/>

		<div class="filters" role="group" aria-label="Filter schedules">
			<Select label="Kind" options={kindOptions} bind:value={kind} />
			<Select label="State" options={enabledOptions} bind:value={enabled} />
		</div>

		<Card padding="none">
			{#if schedules.isPending}
				<div class="pad" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
			{:else if schedules.isError}
				<div class="pad">
					<ErrorState
						error={schedules.error}
						title="The schedules could not be loaded."
						onretry={() => schedules.refetch()}
					/>
				</div>
			{:else}
				<Table
					label="Schedules"
					{rows}
					{columns}
					rowKey={(s) => s.id}
					sort={{ column: 'next', direction: 'asc' }}
				>
					{#snippet empty()}
						<EmptyState
							icon={CalendarClock}
							color="violet"
							title={all.length
								? 'No schedules match these filters.'
								: 'No scheduled policies yet.'}
							description={all.length
								? 'Change the filters to see the other schedules.'
								: 'Backups, update checks and prunes run on schedules. Create a policy and choose when it runs; new policies start disabled.'}
							level={2}
							compact
						/>
					{/snippet}
				</Table>
			{/if}
		</Card>
	</div>

	<Drawer bind:open={drawerOpen} title={selected ? selected.policyName : 'Schedule'} size="460px">
		{#if selected}{#key selected.id}<ScheduleDetail schedule={selected} />{/key}{/if}
	</Drawer>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.filters {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 240px));
		gap: var(--space-3);
	}

	.policy {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 2px;
		min-width: 0;
	}

	.strong {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.small {
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.pad {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	@media (max-width: 767px) {
		.filters {
			grid-template-columns: 1fr 1fr;
		}
	}
</style>
