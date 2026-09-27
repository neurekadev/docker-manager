<script lang="ts">
	// Dashboard (#5, #22): every environment the user can reach, with status,
	// Engine, CPU and memory (the last 30 minutes as sparklines), Docker
	// counts, undeployed changes and available updates, plus failed jobs of
	// the last 24 hours. Everything is live: overview and charts are keyed with
	// liveKeys, so connection, inventory and metrics events refresh them. A
	// Restricted user sees the calm denied state (#17).
	import { createQuery } from '@tanstack/svelte-query';
	import Container from '@lucide/svelte/icons/container';
	import Cpu from '@lucide/svelte/icons/cpu';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import Plus from '@lucide/svelte/icons/plus';
	import Server from '@lucide/svelte/icons/server';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import {
		environmentsQuery,
		myPermissionsQuery,
		overviewQuery,
		recentJobsQuery,
		stacksSummaryQuery,
		updatePoliciesSummaryQuery
	} from '$lib/api/queries';
	import EnvironmentCard from '$lib/features/dashboard/EnvironmentCard.svelte';
	import { dashboardTotals, perEnvironment } from '$lib/features/dashboard/totals';
	import { jobKindLabel } from '$lib/features/jobs/labels';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf, hasAny, isRestricted } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		DeniedState,
		EmptyState,
		ErrorState,
		KpiCard,
		Meter,
		Skeleton,
		formatBytes,
		formatPercent,
		formatRelative
	} from '$lib/ui';

	usePage({ title: 'Dashboard', crumbs: [{ label: 'Dashboard' }] });

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const restricted = $derived(perms.data ? isRestricted(access) : false);
	const ready = $derived(!!perms.data && !restricted);
	const canEnroll = $derived(access.owner || access.allowed.has('agent.enroll'));

	const overview = createQuery(() => ({ ...overviewQuery(), enabled: ready }));
	const envList = createQuery(() => ({ ...environmentsQuery(), enabled: ready }));
	const jobs = createQuery(() => ({ ...recentJobsQuery(50), enabled: ready }));
	const stacks = createQuery(() => ({
		...stacksSummaryQuery(),
		enabled: ready && hasAny(access, 'stack.')
	}));
	const updates = createQuery(() => ({
		...updatePoliciesSummaryQuery(),
		enabled: ready && hasAny(access, 'update_policy.')
	}));

	const rows = $derived(
		(overview.data?.environments ?? []).filter(
			(e) => !environmentSelection.id || e.id === environmentSelection.id
		)
	);
	const since = $derived(new Map((envList.data ?? []).map((e) => [e.id, e.connectionChangedAt])));
	const counts = $derived(perEnvironment(stacks.data, updates.data));
	const recent = $derived(
		(jobs.data?.items ?? []).filter(
			(j) => !environmentSelection.id || j.environmentId === environmentSelection.id
		)
	);
	const totals = $derived(dashboardTotals(rows, recent, Date.now()));
	// Online environments first, then by name.
	const ordered = $derived(
		[...rows].sort(
			(a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name)
		)
	);
</script>

{#if restricted}
	<DeniedState level={1} />
{:else}
	<div class="page">
		<header class="head">
			<div>
				<h1>Dashboard</h1>
				<p class="muted">Every environment you can reach, with what runs on it.</p>
			</div>
			{#if canEnroll}
				<Button icon={Plus} href={routes.addEnvironment()}>Add environment</Button>
			{/if}
		</header>

		{#if overview.isError}
			<ErrorState
				error={overview.error}
				title="The overview could not be loaded."
				onretry={() => overview.refetch()}
			/>
		{:else}
			<section class="kpis" aria-label="Summary" aria-busy={overview.isPending}>
				{#if overview.isPending}
					{#each [0, 1, 2, 3, 4] as i (i)}<div class="kpi-skeleton">
							<Skeleton height="64px" radius="lg" />
						</div>{/each}
				{:else}
					<KpiCard
						label="Environments online"
						value="{totals.online} / {rows.length}"
						icon={Server}
						color="blue"
						secondary={totals.offline ? `${totals.offline} offline` : 'All connected'}
						tone={totals.offline ? 'warn' : rows.length ? 'ok' : undefined}
					/>
					<KpiCard
						label="Containers running"
						value={totals.counted ? `${totals.running} / ${totals.containers}` : '—'}
						icon={Container}
						color="green"
						secondary={totals.counted
							? `${totals.containers - totals.running} not running`
							: 'Counts need system access'}
					/>
					<KpiCard
						label="CPU in use"
						value={formatPercent(totals.cpuAverage)}
						icon={Cpu}
						color="cyan"
						secondary={totals.cpuBusiest
							? `Highest: ${totals.cpuBusiest.name} ${formatPercent(totals.cpuBusiest.value)}`
							: totals.cpuAverage === null
								? 'No usage samples yet'
								: 'Of all cores'}
					/>
					{#if totals.memTotal > 0}
						<KpiCard
							label="Memory in use"
							value={formatBytes(totals.memUsed)}
							unit="/ {formatBytes(totals.memTotal)}"
							icon={MemoryStick}
							color="indigo"
						>
							{#snippet bar()}
								<Meter
									value={totals.memUsed}
									max={totals.memTotal}
									label="Memory in use"
									valueText="{formatBytes(totals.memUsed)} of {formatBytes(
										totals.memTotal
									)}"
								/>
							{/snippet}
						</KpiCard>
					{:else}
						<KpiCard
							label="Memory in use"
							value="—"
							icon={MemoryStick}
							color="indigo"
							secondary="No usage samples yet"
						/>
					{/if}
					<KpiCard
						label="Failed jobs (24 h)"
						value={String(totals.failures)}
						icon={TriangleAlert}
						color={totals.failures ? 'rose' : 'slate'}
						tone={totals.failures ? 'danger' : undefined}
					>
						{#snippet secondary()}
							{#if totals.lastFailure}
								<a href={routes.job(totals.lastFailure.id)}
									>{jobKindLabel(totals.lastFailure.kind)}, {formatRelative(
										totals.lastFailure.createdAt
									)}</a
								>
							{:else}
								Nothing failed
							{/if}
						{/snippet}
					</KpiCard>
				{/if}
			</section>

			<section class="section" aria-labelledby="environments-title">
				<div class="section-head">
					<h2 id="environments-title">Environments</h2>
					<a href={routes.environments()} class="more">View all environments</a>
				</div>
				{#if overview.isPending}
					<div class="grid" aria-busy="true">
						{#each [0, 1] as i (i)}<div class="card-skeleton">
								<Skeleton lines={2} height="18px" />
							</div>{/each}
					</div>
				{:else if !rows.length}
					<Card>
						<EmptyState
							icon={Server}
							color="blue"
							title="No environments yet."
							description="Add an environment: run the Docker Agent on a Docker host and enroll it."
							level={3}
						>
							{#snippet actions()}
								{#if canEnroll}<Button
										variant="primary"
										icon={Plus}
										href={routes.addEnvironment()}>Add environment</Button
									>{/if}
							{/snippet}
						</EmptyState>
					</Card>
				{:else}
					<div class="grid">
						{#each ordered as env (env.id)}
							<EnvironmentCard
								{env}
								since={since.get(env.id)}
								stacks={stacks.data ? (counts.get(env.id)?.stacks ?? 0) : undefined}
								undeployed={counts.get(env.id)?.undeployed ?? 0}
								updates={counts.get(env.id)?.updates ?? 0}
							/>
						{/each}
					</div>
				{/if}
			</section>
		{/if}
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.head {
		display: flex;
		align-items: flex-end;
		justify-content: space-between;
		gap: var(--space-4);
		flex-wrap: wrap;
	}

	.head h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.head p {
		margin-top: 2px;
		font-size: var(--text-control);
	}

	.kpis {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
		gap: var(--space-4);
	}

	/* Five cards: 3 + 2 where 5 do not fit in one row (never 4 + 1). */
	@media (min-width: 768px) and (max-width: 1439px) {
		.kpis {
			grid-template-columns: repeat(3, minmax(0, 1fr));
		}
	}

	/* Phones: two compact KPI cards per row (KpiCard's compact layout). */
	@media (max-width: 767px) {
		.kpis {
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: var(--space-3);
		}
	}

	.kpi-skeleton,
	.card-skeleton {
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.section {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}

	.section-head {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-3);
		padding-top: var(--space-2);
	}

	.section-head h2 {
		font-size: var(--text-section);
		line-height: var(--leading-section);
	}

	.more {
		font-size: var(--text-caption);
	}

	/* One full-width row per environment. */
	.grid {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}
</style>
