<script lang="ts">
	// Jobs (#26): everything Docker Manager did or is doing, newest first,
	// in one ListCard: a search over the loaded jobs and filters for state,
	// kind and (with every environment shown) environment, applied by the
	// server and kept per list and browser tab like the other lists. The
	// environment switcher scopes the list; ?environment= (links from an
	// environment page) sets the environment filter once, ?kind= (a policy
	// run's "Open Jobs") the kind filter, ?policyId= the policy filter
	// (named from the schedules, else by its kind) and ?state=active (the
	// top bar's running jobs) the state filter. Rows lead with the
	// target's name. The count says how many jobs match when the server
	// knows ("50 of 1,234 jobs"); the search covers the loaded jobs only and
	// says so; "Load More Jobs" follows the cursor (also from the no-matches
	// state). The list refreshes live on job events.
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query';
	import {
		environmentsQuery,
		jobsInfiniteQuery,
		myPermissionsQuery,
		schedulesQuery,
		stacksSummaryQuery
	} from '$lib/api/queries';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import JobsTable from '$lib/features/jobs/JobsTable.svelte';
	import {
		jobFilters,
		jobQuery,
		jobSearch,
		jobsSearchedText,
		jobsSummary
	} from '$lib/features/jobs/filters';
	import { policyPage, STATE_FILTERS, stackNames } from '$lib/features/jobs/labels';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering } from '$lib/features/resources/filters';
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
		const policy = page.url.searchParams.get('policyId');
		const state = page.url.searchParams.get('state');
		if (id === null && kind === null && policy === null && state === null) return;
		if (id && id !== 'all') {
			if (environmentSelection.id && environmentSelection.id !== id)
				environmentSelection.select(id);
			else filters.set('environment', id);
		}
		if (kind) filters.set('kind', kind);
		if (policy !== null) filters.set('policy', policy);
		if (state && STATE_FILTERS.some((s) => s.id === state)) filters.set('state', state);
		const url = new URL(page.url);
		url.searchParams.delete('environment');
		url.searchParams.delete('kind');
		url.searchParams.delete('policyId');
		url.searchParams.delete('state');
		void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
	});

	// The policy filter (set by ?policyId=) shows the policy's name when a
	// schedule names it, else what kind of policy it is.
	const policyId = $derived(filters.get('policy'));
	const schedules = createQuery(() => ({ ...schedulesQuery(), enabled: !!policyId }));
	const policyName = $derived(
		(schedules.data ?? []).find((s) => s.policyId === policyId)?.policyName ||
			(filters.get('kind') ? policyPage(filters.get('kind')).label : 'Selected Policy')
	);
	const defs = $derived(
		jobFilters({
			envs: allEnvironments ? (envs.data ?? []).map((e) => ({ id: e.id, name: e.name })) : [],
			policy: policyId ? { id: policyId, name: policyName } : undefined
		})
	);
	const query = $derived(jobQuery(defs, filters.state, environmentSelection.id));
	const jobs = createInfiniteQuery(() => jobsInfiniteQuery(query));
	const all = $derived(jobs.data?.pages.flatMap((p) => p.items) ?? []);
	// Jobs matching the server-side filters, when the server knows exactly.
	const total = $derived(jobs.data?.pages[0]?.total);
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
			? jobsSummary({
					shown: rows.length,
					loaded: all.length,
					total,
					more: jobs.hasNextPage,
					searching,
					filtered
				})
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
				title="All Jobs"
				id="jobs"
				{summary}
				label="Filter Jobs"
				searchLabel="Search Jobs"
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
									icon={resourceIcon('job').icon}
									color="slate"
									title="No loaded jobs match the search."
									description="{jobsSearchedText(
										all.length,
										total
									)} Load more to search older ones, or clear the search and filters."
									level={3}
									compact
								>
									{#snippet actions()}
										<Button
											variant="secondary"
											loading={jobs.isFetchingNextPage}
											onclick={() => jobs.fetchNextPage()}
											>Load More Jobs</Button
										>
										<Button variant="ghost" onclick={() => filters.clear()}
											>Clear Filters</Button
										>
									{/snippet}
								</EmptyState>
							{:else if filtered}
								<NoMatches
									what="jobs"
									icon={resourceIcon('job').icon}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									{...resourceIcon('job')}
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
								onclick={() => jobs.fetchNextPage()}>Load More Jobs</Button
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
