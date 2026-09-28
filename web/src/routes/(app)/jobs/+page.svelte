<script lang="ts">
	// Jobs (#26): everything Docker Manager did or is doing, newest first,
	// in one ListCard: a search over the loaded jobs and filters for state,
	// kind and (with every environment shown) environment, applied by the
	// server and kept per list and browser tab like the other lists. The
	// environment switcher scopes the list; ?environment= (links from an
	// environment page) sets the environment filter once, ?kind= (a policy
	// run's "Open jobs") the kind filter. Rows lead with the target's name.
	// The search covers the loaded jobs only and says so; "Load more"
	// follows the cursor (also from the no-matches state). The list
	// refreshes live on job events.
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
	import Page from '$lib/features/common/Page.svelte';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';

	usePage({ title: 'Jobs', crumbs: [{ label: 'Jobs' }], environmentScoped: true });

	const filters = new ListFilters('jobs');
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const stacks = createQuery(() => ({
		...stacksSummaryQuery(),
		enabled: hasAny(accessOf(perms.data), 'stack.')
	}));
	const names = $derived(new Map((envs.data ?? []).map((e) => [e.id, e.name])));
	const single = singleEnvironment();
	// The environment column and filter show while several environments are listed.
	const allEnvironments = $derived(!environmentSelection.id && !single.current);
	const nameOf = $derived(stackNames(stacks.data));

	// A link from an environment (?environment=<id>) sets the environment
	// filter once (or the switcher, when it shows another environment).
	onMount(() => {
		const id = page.url.searchParams.get('environment');
		const kind = page.url.searchParams.get('kind');
		if (id === null && kind === null) return;
		if (id && id !== 'all') {
			if (environmentSelection.id && environmentSelection.id !== id)
				environmentSelection.select(id);
			else filters.set('environment', id);
		}
		if (kind) filters.set('kind', kind);
		const url = new URL(page.url);
		url.searchParams.delete('environment');
		url.searchParams.delete('kind');
		void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
	});

	const defs = $derived(
		jobFilters({
			envs: allEnvironments ? (envs.data ?? []).map((e) => ({ id: e.id, name: e.name })) : []
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
	const searching = $derived(!!filters.q.trim());
	const summary = $derived(
		jobs.data
			? searching && jobs.hasNextPage
				? `${rows.length} of the ${all.length} loaded jobs`
				: listSummary(
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
	<Page>
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
				placeholder="Search jobs"
				filters={defs}
				store={filters}
			>
				{#if jobs.isPending}
					<div class="pad" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
				{:else}
					<JobsTable
						jobs={rows}
						label="Jobs"
						environments={allEnvironments ? names : undefined}
						{nameOf}
					>
						{#snippet empty()}
							{#if filtered && searching && jobs.hasNextPage}
								<EmptyState
									icon={Activity}
									color="slate"
									title="No loaded jobs match the search."
									description="Searched the {all.length} loaded jobs. Load more to search older ones, or clear the search and filters."
									level={3}
									compact
								>
									{#snippet actions()}
										<Button
											variant="secondary"
											loading={jobs.isFetchingNextPage}
											onclick={() => jobs.fetchNextPage()}
											>Load more jobs</Button
										>
										<Button variant="ghost" onclick={() => filters.clear()}
											>Clear filters</Button
										>
									{/snippet}
								</EmptyState>
							{:else if filtered}
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
					{#if jobs.hasNextPage && rows.length}
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
	</Page>
{/if}

<style>
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
