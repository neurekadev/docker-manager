<script lang="ts">
	// The Notifications tab: finished backups, restores, prunes and
	// update runs the user can see, newest first, grouped by day ("Today",
	// "Yesterday", "Sun, Sep 27") in one ListCard: a search over the loaded
	// ones and filters for the kind, the outcome and, while every
	// environment is shown, the environment, applied by the server and kept
	// per list and browser tab. Each item leads with its kind's tile and its
	// title (the link to the run's job, stretched over the item), then the
	// detail, the short labelled values side by side and the longer ones on
	// lines of their own; beside it how it went (Done, Warning, Failed), the
	// environment and when. A history: nothing to dismiss. "Load more"
	// follows the cursor; new notifications arrive live (topic alerts).
	import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query';
	import { environmentsQuery } from '$lib/api/queries';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import {
		Badge,
		Button,
		EmptyState,
		ErrorState,
		IconTile,
		Skeleton,
		formatDateTime,
		formatRelative
	} from '$lib/ui';
	import {
		NOTIFICATIONS_LIST,
		notificationFilters,
		notificationQuery,
		notificationSearch
	} from './filters';
	import {
		blockFields,
		groupByDay,
		inlineFields,
		notificationHref,
		notificationTile,
		notificationsSummary,
		outcomeLabel,
		outcomeTone,
		type Notification
	} from './model';
	import { notificationsInfiniteQuery } from './queries';

	interface Props {
		/** The environment the switcher shows (null: all). */
		environmentId: string | null;
		now?: Date;
	}

	let { environmentId, now }: Props = $props();

	const filters = new ListFilters(NOTIFICATIONS_LIST);
	const envs = createQuery(() => environmentsQuery());
	const names = $derived(new Map((envs.data ?? []).map((e) => [e.id, e.name])));
	const single = singleEnvironment();
	// The environment and its filter show while several environments are listed.
	const allEnvironments = $derived(!environmentId && !single.current);

	const defs = $derived(
		notificationFilters({
			envs: allEnvironments ? (envs.data ?? []).map((e) => ({ id: e.id, name: e.name })) : []
		})
	);
	const query = $derived(notificationQuery(defs, filters.state, environmentId));
	const list = createInfiniteQuery(() => notificationsInfiniteQuery(query));
	const all = $derived(list.data?.pages.flatMap((p) => p.items) ?? []);
	const total = $derived(list.data?.pages[0]?.total);
	const rows = $derived(
		applyListFilters(
			all,
			defs,
			filters.state,
			notificationSearch((id) => names.get(id))
		)
	);
	const groups = $derived(groupByDay(rows, now));
	const filtered = $derived(isFiltering(defs, filters.state));
	const searching = $derived(!!filters.q.trim());
	const summary = $derived(
		list.data
			? notificationsSummary({
					shown: rows.length,
					loaded: all.length,
					total: searching ? undefined : total,
					more: list.hasNextPage
				})
			: undefined
	);

	const envName = (n: Notification) =>
		n.environmentId ? (names.get(n.environmentId) ?? 'Unknown Environment') : 'Docker Manager';
</script>

{#snippet item(n: Notification)}
	{@const tile = notificationTile(n.kind)}
	{@const inline = inlineFields(n)}
	{@const lines = blockFields(n)}
	<li class="item">
		<span class="tile"><IconTile icon={tile.icon} color={tile.color} size="xs" /></span>
		<div class="main">
			<a class="title row-link" href={notificationHref(n)}>{n.title}</a>
			{#if n.detail}<p class="detail">{n.detail}</p>{/if}
			{#if inline.length}
				<dl class="facts">
					{#each inline as f, i (i)}
						<div class="fact">
							<dt>{f.name}</dt>
							<dd>{f.value}</dd>
						</div>
					{/each}
				</dl>
			{/if}
			{#if lines.length}
				<dl class="lines">
					{#each lines as f, i (i)}
						<div class="line">
							<dt>{f.name}</dt>
							<dd>{f.value}</dd>
						</div>
					{/each}
				</dl>
			{/if}
		</div>
		<div class="aside">
			<Badge tone={outcomeTone(n.outcome)} dot>{outcomeLabel(n.outcome)}</Badge>
			<span class="when">
				{#if allEnvironments}<span class="env" title={envName(n)}>{envName(n)}</span>{/if}
				<time datetime={n.createdAt} title={formatDateTime(n.createdAt)}
					>{formatRelative(n.createdAt, now)}</time
				>
			</span>
		</div>
	</li>
{/snippet}

<p class="intro">
	Finished backups, restores, prunes and image updates of the last 90 days. Open one to see its
	job.
</p>

{#if list.isError}
	<ErrorState
		error={list.error}
		title="The notifications could not be loaded."
		onretry={() => list.refetch()}
	/>
{:else}
	<ListCard
		title="All Notifications"
		id="notifications"
		{summary}
		label="Filter Notifications"
		searchLabel="Search Notifications"
		placeholder="Search notifications"
		filters={defs}
		store={filters}
	>
		{#if list.isPending}
			<div class="pad" aria-busy="true"><Skeleton lines={5} height="20px" /></div>
		{:else if rows.length === 0}
			<div class="empty">
				{#if filtered && searching && list.hasNextPage}
					<EmptyState
						{...resourceIcon('notification')}
						title="No loaded notifications match the search."
						description="Load more to search older ones, or clear the search and filters."
						level={3}
						compact
					>
						{#snippet actions()}
							<Button
								variant="secondary"
								loading={list.isFetchingNextPage}
								onclick={() => list.fetchNextPage()}>Load More Notifications</Button
							>
							<Button variant="ghost" onclick={() => filters.clear()}
								>Clear Filters</Button
							>
						{/snippet}
					</EmptyState>
				{:else if filtered}
					<NoMatches
						what="notifications"
						icon={resourceIcon('notification').icon}
						onclear={() => filters.clear()}
					/>
				{:else}
					<EmptyState
						{...resourceIcon('notification')}
						title="No notifications yet."
						description="Finished backups, restores, prunes and updates show up here."
						level={3}
						compact
					/>
				{/if}
			</div>
		{:else}
			<div class="days">
				{#each groups as g (g.key)}
					<section class="day" aria-labelledby="notifications-day-{g.key}">
						<h3 class="subsection-title day-title" id="notifications-day-{g.key}">
							{g.label}
						</h3>
						<ul class="items" aria-label="Notifications of {g.label}">
							{#each g.items as n (n.id)}{@render item(n)}{/each}
						</ul>
					</section>
				{/each}
			</div>
			{#if list.hasNextPage}
				<div class="more">
					<Button loading={list.isFetchingNextPage} onclick={() => list.fetchNextPage()}
						>Load More Notifications</Button
					>
				</div>
			{/if}
		{/if}
	</ListCard>
{/if}

<style>
	.intro {
		margin: 0 0 var(--space-4);
		color: var(--text-muted);
	}

	.pad {
		padding: var(--space-5);
	}

	.empty {
		border-top: 1px solid var(--border-subtle);
	}

	.day + .day {
		border-top: 1px solid var(--border-subtle);
	}

	.day-title {
		margin: 0;
		padding: var(--space-3) var(--space-5) var(--space-2);
		color: var(--text-muted);
	}

	.items {
		margin: 0;
		padding: 0;
		list-style: none;
	}

	/* An item: the kind's tile, the text, then the outcome and when. The
	   title link covers the item, so the whole item opens the run. */
	.item {
		position: relative;
		display: grid;
		grid-template-columns: 24px minmax(0, 1fr) auto;
		gap: var(--space-1) var(--space-3);
		align-items: start;
		padding: var(--space-3) var(--space-5);
		border-top: 1px solid var(--border-subtle);
		cursor: pointer;
	}

	.item:hover {
		background: var(--surface-hover);
	}

	.item:hover .title {
		color: var(--accent-text);
	}

	.tile {
		display: flex;
		padding-top: 1px;
	}

	.main {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.title {
		overflow: hidden;
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.row-link::after {
		content: '';
		position: absolute;
		inset: 0;
	}

	.detail,
	.facts,
	.lines {
		margin: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.facts {
		display: flex;
		flex-wrap: wrap;
		gap: 2px var(--space-4);
		margin-top: 2px;
	}

	.fact,
	.line {
		display: flex;
		gap: var(--space-1);
		min-width: 0;
	}

	.facts dd,
	.lines dd {
		margin: 0;
		color: var(--text-default);
	}

	.lines dd {
		white-space: pre-line;
		overflow-wrap: anywhere;
	}

	.lines dt {
		flex-shrink: 0;
	}

	.lines dt::after {
		content: ':';
	}

	.aside {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: var(--space-1);
		min-width: 0;
	}

	.when {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: 0 var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		white-space: nowrap;
	}

	.env {
		max-width: 160px;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.more {
		display: flex;
		justify-content: center;
		padding: var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}

	@media (max-width: 767px) {
		.item {
			grid-template-columns: 24px minmax(0, 1fr);
			padding: var(--space-3) var(--space-4);
		}

		.title {
			white-space: normal;
		}

		.aside {
			grid-column: 2;
			flex-direction: row;
			flex-wrap: wrap;
			align-items: center;
			justify-content: flex-start;
			gap: var(--space-2);
		}

		.when {
			justify-content: flex-start;
		}

		.day-title {
			padding: var(--space-3) var(--space-4) var(--space-2);
		}
	}
</style>
