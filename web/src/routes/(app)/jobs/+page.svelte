<script lang="ts">
	// Jobs (#26): everything Docker Manager did or is doing, newest first, with
	// filters for state, kind, environment and origin (kept in the URL).
	// The list refreshes live on job events; "Load more" follows the cursor.
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query';
	import Activity from '@lucide/svelte/icons/activity';
	import FilterX from '@lucide/svelte/icons/filter-x';
	import type { Job } from '$lib/api/client';
	import {
		environmentsQuery,
		jobsInfiniteQuery,
		myPermissionsQuery,
		stacksSummaryQuery,
		type JobFilters
	} from '$lib/api/queries';
	import JobsTable from '$lib/features/jobs/JobsTable.svelte';
	import {
		JOB_KIND_LABELS,
		ORIGIN_LABELS,
		STATE_FILTERS,
		stackNames
	} from '$lib/features/jobs/labels';
	import { accessOf, hasAny, isRestricted } from '$lib/shell/nav';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		DeniedState,
		EmptyState,
		ErrorState,
		PageHeader,
		Select,
		Skeleton
	} from '$lib/ui';

	usePage({ title: 'Jobs', crumbs: [{ label: 'Jobs' }] });

	const params = $derived(page.url.searchParams);
	const stateId = $derived(params.get('state') ?? '');
	const kind = $derived(params.get('kind') ?? '');
	const origin = $derived(params.get('origin') ?? '');
	// ?environment= wins (links from an environment); otherwise the switcher.
	const envParam = $derived(params.get('environment'));
	const environmentId = $derived(
		envParam !== null ? (envParam === 'all' ? '' : envParam) : (environmentSelection.id ?? '')
	);

	const filters = $derived<JobFilters>({
		states: STATE_FILTERS.find((s) => s.id === stateId)?.states ?? [],
		kind,
		environmentId,
		origins: origin ? [origin as Job['origin']] : []
	});
	const filtered = $derived(!!(stateId || kind || origin || environmentId));

	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const stacks = createQuery(() => ({
		...stacksSummaryQuery(),
		enabled: hasAny(accessOf(perms.data), 'stack.')
	}));
	const names = $derived(new Map((envs.data ?? []).map((e) => [e.id, e.name])));
	const jobs = createInfiniteQuery(() => jobsInfiniteQuery(filters));
	const rows = $derived(jobs.data?.pages.flatMap((p) => p.items) ?? []);

	const stateOptions = STATE_FILTERS.map((s) => ({ value: s.id, label: s.label }));
	const kindOptions = [
		{ value: '', label: 'All kinds' },
		...Object.entries(JOB_KIND_LABELS)
			.map(([value, label]) => ({ value, label }))
			.sort((a, b) => a.label.localeCompare(b.label))
	];
	const originOptions = [
		{ value: '', label: 'All origins' },
		...Object.entries(ORIGIN_LABELS).map(([value, label]) => ({ value, label }))
	];
	const envOptions = $derived([
		{ value: 'all', label: 'All environments' },
		...(envs.data ?? []).map((e) => ({ value: e.id, label: e.name }))
	]);

	function setParam(key: string, value: string) {
		const url = new URL(page.url);
		if (value) url.searchParams.set(key, value);
		else url.searchParams.delete(key);
		void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
	}

	function clear() {
		void goto(page.url.pathname + '?environment=all', {
			replaceState: true,
			keepFocus: true,
			noScroll: true
		});
	}
</script>

{#if perms.data && isRestricted(accessOf(perms.data))}
	<DeniedState level={1} />
{:else}
	<div class="page">
		<PageHeader
			title="Jobs"
			description="Everything Docker Manager did or is doing: deploys, pulls, updates, backups, prunes and file operations."
		/>

		<div class="filters" role="group" aria-label="Filter jobs">
			<Select
				label="State"
				options={stateOptions}
				value={stateId}
				onchange={(ev) => setParam('state', ev.currentTarget.value)}
			/>
			<Select
				label="Kind"
				options={kindOptions}
				value={kind}
				onchange={(ev) => setParam('kind', ev.currentTarget.value)}
			/>
			<Select
				label="Environment"
				options={envOptions}
				value={environmentId || 'all'}
				onchange={(ev) => setParam('environment', ev.currentTarget.value)}
			/>
			<Select
				label="Origin"
				options={originOptions}
				value={origin}
				onchange={(ev) => setParam('origin', ev.currentTarget.value)}
			/>
			{#if filtered}
				<div class="clear">
					<Button variant="ghost" icon={FilterX} onclick={clear}>Clear filters</Button>
				</div>
			{/if}
		</div>

		<Card padding="none">
			{#if jobs.isPending}
				<div class="pad" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
			{:else if jobs.isError}
				<div class="pad">
					<ErrorState
						error={jobs.error}
						title="The jobs could not be loaded."
						onretry={() => jobs.refetch()}
					/>
				</div>
			{:else}
				<JobsTable
					jobs={rows}
					label="Jobs"
					environments={names}
					nameOf={stackNames(stacks.data)}
				>
					{#snippet empty()}
						{#if filtered}
							<EmptyState
								icon={FilterX}
								title="No jobs match these filters."
								description="Clear the filters to see every job you can read."
								level={2}
								compact
							>
								{#snippet actions()}<Button onclick={clear}>Clear filters</Button
									>{/snippet}
							</EmptyState>
						{:else}
							<EmptyState
								icon={Activity}
								color="violet"
								title="No jobs yet."
								description="Deploy a stack, pull an image or run a prune: every long operation shows up here with its progress."
								level={2}
								compact
							/>
						{/if}
					{/snippet}
				</JobsTable>
				{#if jobs.hasNextPage}
					<div class="more">
						<Button
							loading={jobs.isFetchingNextPage}
							onclick={() => jobs.fetchNextPage()}>Load more jobs</Button
						>
					</div>
				{/if}
			{/if}
		</Card>
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.filters {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 220px)) auto;
		align-items: end;
		gap: var(--space-3);
	}

	.clear {
		justify-self: start;
	}

	.pad {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.more {
		display: flex;
		justify-content: center;
		padding: var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}

	@media (max-width: 1023px) {
		.filters {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
