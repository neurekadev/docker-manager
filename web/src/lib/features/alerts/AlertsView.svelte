<script lang="ts">
	// The Alerts page (#159): every alert the user can see, newest first,
	// in one ListCard: a search over title, detail, environment and kind;
	// filters for the state (Active, the default, Dismissed or Resolved),
	// the kind and, while every environment is shown, the environment,
	// applied by the server and kept per list and browser tab. A row leads
	// with the alert's title (the link to the page it is about, stretched
	// over the row) and its detail, then the severity, the environment and
	// since when; dismissed rows say who dismissed them and when, resolved
	// ones how and when they ended. Active alerts the user may dismiss have
	// a Dismiss button; "Dismiss all" (confirmed) dismisses every listed one
	// the user may dismiss. Everything is live (topic alerts).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import BellRing from '@lucide/svelte/icons/bell-ring';
	import CheckCheck from '@lucide/svelte/icons/check-check';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import IconCell from '$lib/features/common/IconCell.svelte';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import {
		Button,
		ConfirmDialog,
		EmptyState,
		ErrorState,
		PageHeader,
		Skeleton,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';
	import { dismissMany, dismissOne } from './actions';
	import { ALERTS_LIST, alertFilters, alertQuery, alertSearch, alertsSummary } from './filters';
	import {
		alertCount,
		alertHref,
		canDismiss,
		dismissedText,
		resolutionHint,
		resolutionLabel,
		severityRank,
		sortAlerts,
		type Alert
	} from './model';
	import { alertsQuery } from './queries';

	interface Props {
		/** The environment the switcher shows (null: all). */
		environmentId: string | null;
		/** The owner may set up notification channels (the empty state links there). */
		owner?: boolean;
		now?: Date;
	}

	let { environmentId, owner = false, now }: Props = $props();

	const qc = useQueryClient();
	const filters = new ListFilters(ALERTS_LIST);
	const envs = createQuery(() => environmentsQuery());
	const names = $derived(new Map((envs.data ?? []).map((e) => [e.id, e.name])));
	const single = singleEnvironment();
	// The environment column and filter show while several environments are listed.
	const allEnvironments = $derived(!environmentId && !single.current);

	const defs = $derived(
		alertFilters({
			envs: allEnvironments ? (envs.data ?? []).map((e) => ({ id: e.id, name: e.name })) : []
		})
	);
	const query = $derived(alertQuery(defs, filters.state, environmentId));
	const view = $derived(query.state ?? 'active');
	const alerts = createQuery(() => alertsQuery(query));
	const all = $derived(alerts.data ?? []);
	// Worst first, then the newest (like the bell); a column header re-sorts.
	const rows = $derived(
		sortAlerts(
			applyListFilters(
				all,
				defs,
				filters.state,
				alertSearch((id) => names.get(id))
			)
		)
	);
	// Nothing matches the search, kind or environment (the state has its own
	// empty states).
	const filtered = $derived(
		isFiltering(
			defs.filter((f) => f.id !== 'state'),
			filters.state
		)
	);
	const dismissible = $derived(view === 'active' ? rows.filter(canDismiss) : []);

	let pending = $state<string[]>([]);
	let confirmOpen = $state(false);

	async function dismiss(a: Alert) {
		pending = [...pending, a.id];
		try {
			await dismissOne(a, { queryClient: qc });
		} finally {
			pending = pending.filter((id) => id !== a.id);
		}
	}

	async function dismissAll() {
		await dismissMany(
			dismissible.map((a) => a.id),
			{ queryClient: qc }
		);
	}

	const envName = (a: Alert) => (a.environmentId ? names.get(a.environmentId) : undefined);

	const columns = $derived.by(() => {
		const cols: Column<Alert>[] = [
			{
				id: 'alert',
				header: 'Alert',
				cell: alertCell,
				sortValue: (a) => a.title,
				maxWidth: '560px',
				title: (a) => [a.title, a.detail].filter(Boolean).join('\n'),
				stack: 'title'
			},
			{
				id: 'severity',
				header: 'Severity',
				cell: severityCell,
				sortValue: (a) => severityRank(a.severity),
				width: '120px',
				stack: 'status'
			}
		];
		if (allEnvironments)
			cols.push({
				id: 'environment',
				header: 'Environment',
				cell: environmentCell,
				sortValue: (a) => envName(a) ?? '',
				width: '160px',
				maxWidth: '200px',
				truncate: true,
				title: (a) => envName(a) ?? 'Docker Manager',
				stack: 'hidden'
			});
		cols.push({
			id: 'since',
			header: view === 'resolved' ? 'Started' : 'Since',
			cell: sinceCell,
			sortValue: (a) => a.startedAt,
			width: '140px',
			stack: view === 'active' ? 'meta' : 'hidden'
		});
		if (view === 'dismissed')
			cols.push({
				id: 'dismissed',
				header: 'Dismissed',
				cell: dismissedCell,
				sortValue: (a) => a.dismissedAt,
				width: '240px',
				stack: 'meta'
			});
		if (view === 'resolved')
			cols.push({
				id: 'resolution',
				header: 'Resolution',
				cell: resolutionCell,
				sortValue: (a) => a.resolvedAt,
				width: '240px',
				stack: 'meta'
			});
		if (view === 'active' && all.some(canDismiss))
			cols.push({
				id: 'actions',
				header: 'Actions',
				cell: actionsCell,
				hideHeader: true,
				align: 'end',
				width: '110px',
				pin: 'end',
				stack: 'actions'
			});
		return cols;
	});
</script>

{#snippet time(iso: string | undefined)}
	{#if iso}<time datetime={iso} title={formatDateTime(iso)}>{formatRelative(iso, now)}</time
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet alertCell(a: Alert)}
	<IconCell icon="alert">
		<div class="alert">
			<a href={alertHref(a)} class="title row-link">{a.title}</a>
			{#if a.detail}<span class="detail muted">{a.detail}</span>{/if}
		</div>
	</IconCell>
{/snippet}
{#snippet severityCell(a: Alert)}
	<StatusBadge status={a.severity} />
{/snippet}
{#snippet environmentCell(a: Alert)}
	{#if envName(a)}{envName(a)}{:else}<span class="muted">Docker Manager</span>{/if}
{/snippet}
{#snippet sinceCell(a: Alert)}
	<span class="muted">{@render time(a.startedAt)}</span>
{/snippet}
{#snippet dismissedCell(a: Alert)}
	<span class="muted" title={formatDateTime(a.dismissedAt)}>{dismissedText(a, now)}</span>
{/snippet}
{#snippet resolutionCell(a: Alert)}
	<span class="resolution">
		<span title={resolutionHint(a)}>{resolutionLabel(a)}</span>
		<span class="muted">{@render time(a.resolvedAt)}</span>
	</span>
{/snippet}
{#snippet actionsCell(a: Alert)}
	{#if canDismiss(a)}
		<span class="above">
			<Button
				size="sm"
				variant="secondary"
				loading={pending.includes(a.id)}
				onclick={() => dismiss(a)}
				aria-label="Dismiss {a.title}"
				title="Dismiss for everyone">Dismiss</Button
			>
		</span>
	{/if}
{/snippet}

<PageHeader
	title="Alerts"
	description="Problems Docker Manager found: disks and RAID arrays, offline environments, failed scheduled jobs and available updates."
>
	{#snippet actions()}
		{#if dismissible.length}
			<Button icon={CheckCheck} onclick={() => (confirmOpen = true)}>Dismiss all</Button>
		{/if}
	{/snippet}
</PageHeader>

{#if alerts.isError}
	<ErrorState
		error={alerts.error}
		title="The alerts could not be loaded."
		onretry={() => alerts.refetch()}
	/>
{:else}
	<ListCard
		title="All alerts"
		id="alerts"
		summary={alerts.data ? alertsSummary(rows.length, all.length) : undefined}
		label="Filter alerts"
		searchLabel="Search alerts"
		placeholder="Search alerts"
		filters={defs}
		store={filters}
	>
		{#if alerts.isPending}
			<div class="pad" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
		{:else}
			<div class="rows">
				<!-- Newest first as the server sends them, until a header is sorted. -->
				<Table label="Alerts" {rows} {columns} rowKey={(a) => a.id}>
					{#snippet empty()}
						{#if filtered}
							<NoMatches
								what="alerts"
								icon={resourceIcon('alert').icon}
								onclear={() => filters.clear()}
							/>
						{:else if view === 'dismissed'}
							<EmptyState
								{...resourceIcon('alert')}
								title="No dismissed alerts."
								description="A dismissed alert stays here while its problem lasts and opens again if it gets worse."
								level={3}
								compact
							/>
						{:else if view === 'resolved'}
							<EmptyState
								{...resourceIcon('alert')}
								title="No resolved alerts."
								description="Alerts move here when their problem is gone and stay for 90 days."
								level={3}
								compact
							/>
						{:else}
							<EmptyState
								{...resourceIcon('alert')}
								title="No active alerts."
								description="Disks and RAID arrays with problems, environments that go offline, failed scheduled jobs and available updates raise alerts here.{owner
									? ' Add a notification channel to have them sent to you.'
									: ''}"
								level={3}
								compact
							>
								{#snippet actions()}
									{#if owner}
										<Button
											variant="secondary"
											icon={BellRing}
											href={routes.notifications()}
											>Set up notifications</Button
										>
									{/if}
								{/snippet}
							</EmptyState>
						{/if}
					{/snippet}
				</Table>
			</div>
		{/if}
	</ListCard>
{/if}

<ConfirmDialog
	bind:open={confirmOpen}
	title="Dismiss all alerts"
	message="Dismisses {alertCount(
		dismissible.length
	)} for everyone. They stay in Alerts and open again if they get worse."
	consequences={rows.length > dismissible.length
		? [
				`${alertCount(rows.length - dismissible.length)} you may not dismiss ${
					rows.length - dismissible.length === 1 ? 'stays' : 'stay'
				} active.`
			]
		: []}
	confirmLabel="Dismiss {alertCount(dismissible.length)}"
	onconfirm={dismissAll}
/>

<style>
	.pad {
		padding: var(--space-5);
	}

	.alert {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.title,
	.detail {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.detail {
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.resolution {
		display: inline-flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	/* A whole row opens the page the alert is about: the title link covers
	   the row (and the phone card); the Dismiss button stays above it. */
	.rows :global(tbody tr),
	.rows :global(li.card) {
		position: relative;
	}

	.rows :global(.row-link::after) {
		content: '';
		position: absolute;
		inset: 0;
	}

	.rows :global(tbody tr:has(.row-link)) {
		cursor: pointer;
	}

	.rows :global(tbody tr:has(.row-link):hover .row-link) {
		color: var(--accent-text);
	}

	.above {
		position: relative;
		z-index: 2;
	}
</style>
