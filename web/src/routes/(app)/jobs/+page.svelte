<script lang="ts">
	// Jobs (#26): everything Docker Manager did or is doing, newest first,
	// in one ListCard: a search over the loaded jobs and filters for state,
	// kind and (with every environment shown) environment, applied by the
	// server and kept per list and browser tab like the other lists. The
	// environment switcher scopes the list; ?environment= (links from an
	// environment) sets the environment filter once. The list refreshes
	// live on job events; "Load more" follows the cursor.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query';
	import Activity from '@lucide/svelte/icons/activity';
	import {
		environmentsQuery,
		jobsInfiniteQuery,
		myPermissionsQuery,
		stacksSummaryQuery
	} from '$lib/api/queries';
	import JobsTable from '$lib/features/jobs/JobsTable.svelte';
	import { jobFilters, jobQuery, jobSearch } from '$lib/features/jobs/filters';
	import { stackNames } from '$lib/features/jobs/labels';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering, listSummary } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { accessOf, hasAny, isRestricted } from '$lib/shell/nav';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, DeniedState, EmptyState, ErrorState, PageHeader, Skeleton } from '$lib/ui';

	usePage({ title: 'Jobs', crumbs: [{ label: 'Jobs' }], environmentScoped: true });

	const filters = new ListFilters('jobs');
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const stacks = createQuery(() => ({
		...stacksSummaryQuery(),
		enabled: hasAny(accessOf(perms.data), 'stack.')
	}));
	const names = $derived(new Map((envs.data ?? []).map((e) => [e.id, e.name])));
	const nameOf = $derived(stackNames(stacks.data));

	// A link from an environment (?environment=<id>) sets the environment
	// filter once (or the switcher, when it shows another environment).
	onMount(() => {
		const id = page.url.searchParams.get('environment');
		if (id === null) return;
		if (id && id !== 'all') {
			if (environmentSelection.id && environmentSelection.id !== id)
				environmentSelection.select(id);
			else filters.set('environment', id);
		}
		const url = new URL(page.url);
		url.searchParams.delete('environment');
		void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
	});

	const defs = $derived(
		jobFilters({
			envs: environmentSelection.id
				? []
				: (envs.data ?? []).map((e) => ({ id: e.id, name: e.name }))
		})
	);
	const query = $derived(jobQuery(defs, filters.state, environmentSelection.id));
	const jobs = createInfiniteQuery(() => jobsInfiniteQuery(query));
	const all = $derived(jobs.data?.pages.flatMap((p) => p.items) ?? []);
	const rows = $derived(
		applyListFilters(
			all,
			defs,
			filters.state,
			jobSearch(nameOf, (id) => names.get(id))
		)
	);
	const filtered = $derived(isFiltering(defs, filters.state));
	const summary = $derived(
		jobs.data
			? listSummary(
					rows.length,
					all.length,
					filtered && rows.length !== all.length,
					'job',
					'jobs'
				) + (jobs.hasNextPage ? ' loaded' : '')
			: undefined
	);
</script>

{#if perms.data && isRestricted(accessOf(perms.data))}
	<DeniedState level={1} />
{:else}
	<div class="page">
		<PageHeader
			title="Jobs"
			description="Everything Docker Manager did or is doing: deploys, pulls, updates, backups, prunes and file operations."
		/>

		{#if jobs.isError}
			<ErrorState
				error={jobs.error}
				title="The jobs could not be loaded."
				onretry={() => jobs.refetch()}
			/>
		{:else}
			<ListCard
				title="All jobs"
				id="jobs"
				{summary}
				label="Filter jobs"
				searchLabel="Search jobs"
				placeholder="Search by kind, target or environment"
				filters={defs}
				store={filters}
			>
				{#if jobs.isPending}
					<div class="pad" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
				{:else}
					<JobsTable
						jobs={rows}
						label="Jobs"
						environments={environmentSelection.id ? undefined : names}
						{nameOf}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="jobs"
									icon={Activity}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									icon={Activity}
									color="violet"
									title="No jobs yet."
									description="Deploy a stack, pull an image or run a prune: every long operation shows up here with its progress."
									level={3}
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
			</ListCard>
		{/if}
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.pad {
		padding: var(--space-5);
	}

	.more {
		display: flex;
		justify-content: center;
		padding: var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}
</style>
