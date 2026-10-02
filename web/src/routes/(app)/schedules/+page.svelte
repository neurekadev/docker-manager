<script lang="ts">
	// Schedules (#13): every scheduled policy the user can read, across
	// backups, update checks and runs, prunes and repository verification,
	// with its schedule in words (the expression as tooltip), the next run
	// (DST annotated) and the last run's outcome. Each policy links to its
	// own page; policies that share a name show where they apply. One
	// ListCard: a search and filters for kind, state and (with several
	// environments shown) environment, kept per list and browser tab. A
	// row's Details open the next runs and the history.
	import { createQuery } from '@tanstack/svelte-query';
	import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
	import type { Schedule } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery, schedulesQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { can } from '$lib/features/common/access';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import IconCell from '$lib/features/common/IconCell.svelte';
	import { resourceIcon, scheduleResource } from '$lib/features/common/resourceIcons';
	import Page from '$lib/features/common/Page.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering, listSummary } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import ScheduleDetail from '$lib/features/schedules/ScheduleDetail.svelte';
	import { scheduleFilters, scheduleSearch } from '$lib/features/schedules/filters';
	import {
		dstLabel,
		formatRunTime,
		policyHref,
		runStatus,
		scheduleScope,
		scheduleState,
		sharedNames
	} from '$lib/features/schedules/model';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf, isRestricted } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		DeniedState,
		Drawer,
		EmptyState,
		ErrorState,
		PageHeader,
		Skeleton,
		StatusBadge,
		Table,
		describeCron,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';

	usePage({ title: 'Schedules', crumbs: [{ label: 'Schedules' }], environmentScoped: true });

	const perms = createQuery(() => myPermissionsQuery());
	const restricted = $derived(perms.data ? isRestricted(accessOf(perms.data)) : false);
	const single = singleEnvironment();
	const showEnvironment = $derived(!environmentSelection.id && !single.current);

	const filters = new ListFilters('schedules');
	const schedules = createQuery(() => ({
		...schedulesQuery({ environmentId: environmentSelection.id ?? undefined }),
		enabled: !restricted
	}));
	const envs = createQuery(() => environmentsQuery());
	const names = $derived(new Map((envs.data ?? []).map((e) => [e.id, e.name])));
	const envName = (id: string) => names.get(id);

	const all = $derived(schedules.data ?? []);
	const defs = $derived(
		scheduleFilters(all, {
			envs: showEnvironment ? (envs.data ?? []).map((e) => ({ id: e.id, name: e.name })) : []
		})
	);
	const rows = $derived(applyListFilters(all, defs, filters.state, scheduleSearch(envName)));
	const shared = $derived(sharedNames(all));
	const filtered = $derived(isFiltering(defs, filters.state));

	let selected = $state<Schedule | null>(null);
	let drawerOpen = $state(false);
	function open(s: Schedule) {
		selected = s;
		drawerOpen = true;
	}

	const columns: Column<Schedule>[] = $derived([
		{
			id: 'policy',
			header: 'Policy',
			cell: policyCell,
			sortValue: (s) => s.policyName,
			maxWidth: '320px',
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
		...(showEnvironment
			? [
					{
						id: 'environment',
						header: 'Applies To',
						cell: envCell,
						sortValue: (s: Schedule) => scheduleScope(s, envName),
						width: '160px',
						stack: 'meta'
					} satisfies Column<Schedule>
				]
			: []),
		{
			id: 'schedule',
			header: 'Schedule',
			cell: cronCell,
			sortValue: (s) => describeCron(s.cron, s.timeZone),
			width: '220px',
			stack: 'meta'
		},
		{
			id: 'next',
			header: 'Next Run',
			cell: nextCell,
			sortValue: (s) => (s.enabled && s.nextRun ? Date.parse(s.nextRun.utc) : null),
			width: '230px',
			stack: 'meta'
		},
		{ id: 'last', header: 'Last Run', cell: lastCell, width: '190px', stack: 'meta' },
		{
			id: 'actions',
			header: 'Actions',
			cell: actionsCell,
			hideHeader: true,
			align: 'end',
			width: '110px',
			pin: 'end',
			stack: 'actions'
		}
	]);
</script>

{#snippet policyCell(s: Schedule)}
	<IconCell icon={scheduleResource(s.kind)}>
		<div class="policy">
			<a href={policyHref(s.kind, s.policyId)} class="strong">{s.policyName}</a>
			<span class="muted small"
				>{s.kindLabel}{shared.has(s.policyName)
					? ` · ${scheduleScope(s, envName)}`
					: ''}</span
			>
		</div>
	</IconCell>
{/snippet}
{#snippet stateCell(s: Schedule)}
	{@const st = scheduleState(s)}
	<StatusBadge status={st.status} label={st.label} title={s.invalidReason} />
{/snippet}
{#snippet envCell(s: Schedule)}
	{#if s.environmentId}{scheduleScope(s, envName)}{:else}<span class="muted"
			>{scheduleScope(s, envName)}</span
		>{/if}
{/snippet}
{#snippet cronCell(s: Schedule)}
	<span title="{s.cron} ({s.timeZone})">{describeCron(s.cron, s.timeZone)}</span>
{/snippet}
{#snippet nextCell(s: Schedule)}
	{#if s.nextRun && s.enabled && !s.invalidReason}
		<div class="policy">
			<span class="num" title={formatDateTime(s.nextRun.utc)}
				>{formatRunTime(s.nextRun.at, s.timeZone)}</span
			>
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
	<Page>
		<PageHeader
			title="Schedules"
			description="Every scheduled policy in one place. Times are shown in each policy's own time zone."
		>
			{#snippet actions()}
				{#if can(accessOf(perms.data), 'settings.read')}
					<Button icon={SlidersHorizontal} href={routes.scheduleDefaults()}
						>Change Defaults</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if schedules.isError}
			<ErrorState
				error={schedules.error}
				title="The schedules could not be loaded."
				onretry={() => schedules.refetch()}
			/>
		{:else}
			<ListCard
				title="All Schedules"
				id="schedules"
				summary={schedules.data
					? listSummary(rows.length, all.length, filtered, 'schedule', 'schedules')
					: undefined}
				label="Filter Schedules"
				searchLabel="Search Schedules"
				placeholder="Search schedules"
				filters={defs}
				store={filters}
			>
				{#if schedules.isPending}
					<div class="pad" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
				{:else}
					<Table
						label="Schedules"
						{rows}
						{columns}
						rowKey={(s) => s.id}
						sort={{ column: 'next', direction: 'asc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="schedules"
									icon={resourceIcon('schedule').icon}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									{...resourceIcon('schedule')}
									title="No scheduled policies yet."
									description="Backups, update checks and prunes run on schedules. Create a policy and choose when it runs; new policies start disabled."
									level={3}
									compact
								/>
							{/if}
						{/snippet}
					</Table>
				{/if}
			</ListCard>
		{/if}
	</Page>

	<Drawer bind:open={drawerOpen} title={selected ? selected.policyName : 'Schedule'} size="460px">
		{#if selected}{#key selected.id}<ScheduleDetail schedule={selected} />{/key}{/if}
	</Drawer>
{/if}

<style>
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
		padding: var(--space-5);
	}
</style>
