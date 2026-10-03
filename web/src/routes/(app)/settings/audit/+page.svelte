<script lang="ts">
	// Audit log (#30): who did what to which resource, from where, with what
	// result. One list card: a search over the loaded records and a few
	// filters (who, outcome, category, when) in its header; exact action
	// keys, a resource, an environment and custom dates wait under "More
	// Filters". Records read in words (action labels, resource names);
	// runs of identical records collapse into one row ("24 times"). Each
	// record opens with its details and the rule or settings diff of an
	// edit; raw keys and IDs stay under Advanced. Export as NDJSON or CSV
	// (audit.export). audit.read is all-or-nothing: records are not
	// filtered per resource.
	import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query';
	import Download from '@lucide/svelte/icons/download';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import X from '@lucide/svelte/icons/x';
	import {
		environmentsQuery,
		gitCredentialsQuery,
		myPermissionsQuery,
		registriesQuery,
		stacksSummaryQuery
	} from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Chip,
		DeniedState,
		Drawer,
		EmptyState,
		ErrorState,
		Select,
		Skeleton,
		Table,
		TextField,
		formatDateTime,
		type Column
	} from '$lib/ui';
	import { can, isDenied } from '$lib/features/common/access';
	import { environmentName, onlyOneEnvironment } from '$lib/features/common/data';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import Facts from '$lib/features/common/Facts.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import type { ListFilter } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { catalogQuery, groupsQuery, usersQuery } from '$lib/features/access/queries';
	import { displayName } from '$lib/features/access/model';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';
	import {
		ACTOR_LABELS,
		CATEGORY_LABELS,
		OUTCOME,
		RANGES,
		auditActionLabel,
		detailPairs,
		diffRows,
		groupAuditRows,
		localToRFC3339,
		parseWho,
		rangeSince,
		targetText,
		type AuditRow
	} from '$lib/features/settings/audit';
	import { notificationChannelsQuery } from '$lib/features/notifications/queries';
	import {
		auditExportHref,
		auditPage,
		cleanFilter,
		settingsKeys,
		type AuditEvent,
		type AuditFilter
	} from '$lib/features/settings/queries';

	usePage({
		title: 'Audit Log',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Audit Log' }]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const allowed = $derived(can(access, 'audit.read'));
	const envs = createQuery(() => environmentsQuery());
	const catalog = createQuery(() => catalogQuery());
	// Names of the resources records point at (each only when readable).
	const users = createQuery(() => ({ ...usersQuery(), enabled: access.owner }));
	const groups = createQuery(() => ({ ...groupsQuery(), enabled: access.owner }));
	const stacks = createQuery(() => ({
		...stacksSummaryQuery(),
		enabled: can(access, 'stack.read'),
		retry: false
	}));
	const registries = createQuery(() => ({
		...registriesQuery(),
		enabled: can(access, 'registry.read'),
		retry: false
	}));
	const gitCredentials = createQuery(() => ({
		...gitCredentialsQuery(),
		enabled: can(access, 'git_credential.read'),
		retry: false
	}));
	const channels = createQuery(() => ({
		...notificationChannelsQuery(),
		enabled: access.owner,
		retry: false
	}));
	const oneEnvironment = $derived(onlyOneEnvironment(envs.data));

	function nameOf(type: string, id: string): string | undefined {
		switch (type) {
			case 'user': {
				const u = users.data?.find((x) => x.id === id);
				return u ? displayName(u) : undefined;
			}
			case 'group':
				return groups.data?.find((x) => x.id === id)?.name;
			case 'environment':
				return envs.data?.find((x) => x.id === id)?.name;
			case 'stack':
				return stacks.data?.find((x) => x.id === id)?.name;
			case 'registry':
				return registries.data?.find((x) => x.id === id)?.name;
			case 'git_credential':
				return gitCredentials.data?.find((x) => x.id === id)?.name;
			case 'notification_channel':
				return channels.data?.find((x) => x.id === id)?.name;
		}
		return undefined;
	}

	// Search and filters, kept per browser tab like the other lists.
	const store = new ListFilters('audit');
	const serverSide = () => true; // the API filters; ListCard only renders them
	const filters = $derived<ListFilter<AuditRow>[]>([
		{
			id: 'who',
			label: 'Who',
			all: 'Anyone',
			dynamic: true,
			options: [
				...(users.data ?? [])
					.map((u) => ({ value: `user:${u.id}`, label: displayName(u) }))
					.sort((a, b) => a.label.localeCompare(b.label)),
				...Object.entries(ACTOR_LABELS).map(([k, label]) => ({
					value: `kind:${k}`,
					label: k === 'user' ? 'Any User' : k === 'api_token' ? 'Any API Token' : label
				}))
			],
			match: serverSide
		},
		{
			id: 'outcome',
			label: 'Outcome',
			all: 'Any Outcome',
			options: Object.entries(OUTCOME).map(([value, o]) => ({ value, label: o.label })),
			match: serverSide
		},
		{
			id: 'category',
			label: 'Category',
			all: 'Any Category',
			options: Object.entries(CATEGORY_LABELS).map(([value, label]) => ({ value, label })),
			match: serverSide
		},
		{
			id: 'when',
			label: 'When',
			all: 'Any Time',
			options: RANGES.map((r) => ({ value: r.value, label: r.label })),
			match: serverSide
		}
	]);

	// "More Filters": exact action keys, one resource, an environment, dates.
	const MORE = ['action', 'resource', 'environment', 'from', 'until'];
	const moreSet = $derived(MORE.some((k) => store.get(k) !== ''));
	function clearMore() {
		for (const k of MORE) store.set(k, '');
	}

	const filter = $derived<AuditFilter>(
		cleanFilter({
			...parseWho(store.get('who')),
			outcome: store.get('outcome')
				? [store.get('outcome') as AuditEvent['outcome']]
				: undefined,
			category: store.get('category')
				? [store.get('category') as AuditEvent['category']]
				: undefined,
			action: store
				.get('action')
				.split(',')
				.map((s) => s.trim())
				.filter(Boolean),
			resource: store.get('resource').trim() || undefined,
			environmentId: store.get('environment') || undefined,
			since: localToRFC3339(store.get('from')) ?? rangeSince(store.get('when'), Date.now()),
			until: localToRFC3339(store.get('until'))
		})
	);

	// Debounce typed filters so each keystroke is not a request.
	let settled = $state<AuditFilter>({});
	$effect(() => {
		const f = filter;
		const t = setTimeout(() => (settled = f), 300);
		return () => clearTimeout(t);
	});

	const records = createInfiniteQuery(() => ({
		queryKey: settingsKeys.audit(settled),
		queryFn: ({ pageParam, signal }) => auditPage(settled, pageParam, signal),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: (last: { nextCursor?: string }) => last.nextCursor,
		enabled: allowed,
		retry: false
	}));
	const loaded = $derived((records.data?.pages ?? []).flatMap((p) => p.items));

	function actionText(e: AuditEvent): string {
		return auditActionLabel(e.action, catalog.data);
	}

	function actorText(e: AuditEvent): string {
		const u = users.data?.find((x) => x.id === e.actor.userId);
		const who = u ? displayName(u) : undefined;
		switch (e.actor.kind) {
			case 'user':
				return who ?? 'A user';
			case 'api_token':
				return who ? `${who}'s API token` : 'An API token';
			case 'agent':
				return 'An agent';
			default:
				return ACTOR_LABELS[e.actor.kind];
		}
	}

	function targetsOf(e: AuditEvent) {
		return e.targets.map((t) => targetText(t, nameOf));
	}

	const q = $derived(store.q.trim().toLowerCase());
	const shown = $derived(
		q
			? loaded.filter((e) =>
					[
						actionText(e),
						e.action,
						actorText(e),
						e.clientIp,
						...targetsOf(e).map((t) => t.name)
					].some((t) => t?.toLowerCase().includes(q))
				)
			: loaded
	);
	const rows = $derived(groupAuditRows(shown));
	const filtering = $derived(
		q !== '' || moreSet || ['who', 'outcome', 'category', 'when'].some((k) => store.get(k))
	);
	const summary = $derived(
		records.data
			? q
				? `${shown.length} of ${loaded.length} loaded records`
				: `${loaded.length}${records.hasNextPage ? '+' : ''} ${loaded.length === 1 ? 'record' : 'records'}`
			: undefined
	);

	let open = $state<AuditRow | null>(null);
	let current = $state<AuditEvent | null>(null);
	let drawerOpen = $state(false);
	function show(r: AuditRow) {
		open = r;
		current = r.events[0];
		drawerOpen = true;
	}

	const columns: Column<AuditRow>[] = [
		{ id: 'action', header: 'What', cell: actionCell, stack: 'title' },
		{ id: 'target', header: 'On', cell: targetCell, width: '220px', maxWidth: '220px' },
		{ id: 'actor', header: 'Who', cell: actorCell, width: '200px', maxWidth: '200px' },
		{ id: 'outcome', header: 'Outcome', cell: outcomeCell, width: '130px', stack: 'status' },
		{ id: 'at', header: 'When', cell: atCell, width: '170px' }
	];
</script>

{#snippet actionCell(r: AuditRow)}
	{@const e = r.events[0]}
	<button type="button" class="open" onclick={() => show(r)}>
		<NameCell name={actionText(e)} sub={CATEGORY_LABELS[e.category]}>
			{#snippet extra()}{#if r.events.length > 1}<Badge>{r.events.length} times</Badge
					>{/if}{/snippet}
		</NameCell>
	</button>
{/snippet}
{#snippet targetCell(r: AuditRow)}
	{@const ts = targetsOf(r.events[0])}
	{#if ts.length}
		<NameCell
			name={ts[0].name}
			sub={ts.length > 1
				? `${ts[0].type} and ${ts.length - 1} more`
				: ts[0].name === ts[0].type
					? undefined
					: ts[0].type}
		/>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actorCell(r: AuditRow)}
	<NameCell name={actorText(r.events[0])} sub={r.events[0].clientIp} subMono />
{/snippet}
{#snippet outcomeCell(r: AuditRow)}
	{@const o = OUTCOME[r.events[0].outcome]}
	<Badge tone={o.tone} dot>{o.label}</Badge>
{/snippet}
{#snippet atCell(r: AuditRow)}
	{@const last = r.events.at(-1)}
	<NameCell
		name={formatDateTime(r.events[0].at)}
		sub={r.events.length > 1 && last ? `since ${formatDateTime(last.at)}` : undefined}
	/>
{/snippet}

<Page>
	<SettingsHeader title="Audit Log" description="Records cannot be changed or deleted.">
		{#snippet actions()}
			{#if can(access, 'audit.export')}
				<Button icon={Download} href={auditExportHref(settled, 'csv')}>Export CSV</Button>
				<Button icon={Download} href={auditExportHref(settled, 'ndjson')}
					>Export NDJSON</Button
				>
			{/if}
		{/snippet}
	</SettingsHeader>

	{#if perms.data && !allowed}
		<DeniedState
			level={2}
			title="You can't read the audit log."
			description="Ask the owner for the View Audit Log permission."
		/>
	{:else}
		<ListCard
			title="Audit Records"
			id="audit-records"
			{summary}
			label="Filter Audit Records"
			searchLabel="Search Records"
			placeholder="Search records"
			{filters}
			{store}
		>
			<div class="more">
				<Disclosure
					summary={moreSet ? 'More Filters (In Use)' : 'More Filters'}
					open={moreSet}
				>
					<div class="more-fields">
						{#if !oneEnvironment}
							<Select
								label="Environment"
								placeholder="Any environment"
								options={[
									{ value: '', label: 'Any Environment' },
									...(envs.data ?? []).map((e) => ({
										value: e.id,
										label: e.name
									}))
								]}
								bind:value={
									() => store.get('environment'),
									(v) => store.set('environment', v)
								}
							/>
						{/if}
						<TextField
							label="From"
							type="datetime-local"
							optional
							description="Replaces the When filter."
							bind:value={() => store.get('from'), (v) => store.set('from', v)}
						/>
						<TextField
							label="Until"
							type="datetime-local"
							optional
							bind:value={() => store.get('until'), (v) => store.set('until', v)}
						/>
						<TextField
							label="Action Keys"
							mono
							placeholder="stack.deploy, auth.sign_in"
							optional
							description="Comma-separated, as in a record's details."
							bind:value={() => store.get('action'), (v) => store.set('action', v)}
						/>
						<TextField
							label="Resource"
							mono
							placeholder="stack:0190a6e0-…"
							optional
							description="Type and ID, as in a record's details."
							bind:value={
								() => store.get('resource'), (v) => store.set('resource', v)
							}
						/>
					</div>
					{#if moreSet}
						<div>
							<Button size="sm" variant="ghost" icon={X} onclick={clearMore}
								>Clear More Filters</Button
							>
						</div>
					{/if}
				</Disclosure>
			</div>

			{#if records.isPending}
				<div class="pad" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
			{:else if records.isError && isDenied(records.error)}
				<DeniedState level={3} title="You can't read the audit log." />
			{:else if records.isError}
				<ErrorState
					bare
					error={records.error}
					title="The audit log could not be loaded."
					onretry={() => records.refetch()}
				/>
			{:else}
				<Table label="Audit Records" {rows} {columns} rowKey={(r) => r.key} manualSort>
					{#snippet empty()}
						{#if filtering}
							<NoMatches
								what="records"
								icon={ScrollText}
								onclear={() => store.clear()}
							/>
						{:else}
							<EmptyState
								icon={ScrollText}
								color="slate"
								title="No records yet."
								description="Sign-ins, permission changes and operations appear here as they happen."
								level={3}
								compact
							/>
						{/if}
					{/snippet}
				</Table>
				{#if records.hasNextPage}
					<div class="load">
						{#if q}<p class="muted small">
								The search covers the records loaded so far.
							</p>{/if}
						<Button
							loading={records.isFetchingNextPage}
							onclick={() => records.fetchNextPage()}>Load Older Records</Button
						>
					</div>
				{/if}
			{/if}
		</ListCard>
	{/if}
</Page>

<Drawer
	bind:open={drawerOpen}
	title={current ? actionText(current) : 'Record'}
	side="right"
	size="560px"
>
	{#if current}
		{@const e = current}
		{@const diff = diffRows(e.details)}
		{@const pairs = detailPairs(e.details)}
		<div class="record">
			<Facts
				columns={1}
				items={[
					{ label: 'When', value: formatDateTime(e.at) },
					{ label: 'Who', value: actorText(e) },
					{ label: 'Outcome', value: OUTCOME[e.outcome].label },
					{
						label: 'Environment',
						value: e.environmentId
							? environmentName(envs.data, e.environmentId)
							: undefined
					},
					{
						label: e.targets.length > 1 ? 'Resources' : 'Resource',
						value:
							targetsOf(e)
								.map((t) => (t.name === t.type ? t.type : `${t.type} ${t.name}`))
								.join(', ') || undefined
					},
					{ label: 'Client Address', value: e.clientIp, mono: true },
					{ label: 'Browser or Client', value: e.userAgent }
				]}
			/>
			{#if open && open.events.length > 1}
				<section>
					<h3 class="subsection-title">{open.events.length} Identical Records</h3>
					<div class="times">
						{#each open.events as x (x.id)}
							<Chip
								size="sm"
								label={formatDateTime(x.at)}
								selected={x.id === e.id}
								onclick={() => (current = x)}
							/>
						{/each}
					</div>
				</section>
			{/if}
			{#if diff.length}
				<section>
					<h3 class="subsection-title">What Changed</h3>
					<ul class="diff" role="list">
						{#each diff as d (d.field)}
							<li>
								<span class="field mono">{d.field}</span>
								{#if d.added || d.removed}
									{#each d.removed ?? [] as r, i (`${i}:${r}`)}<div
											class="removed mono"
										>
											<span class="sr-only">Removed:</span>− {r}
										</div>{/each}
									{#each d.added ?? [] as r, i (`${i}:${r}`)}<div
											class="added mono"
										>
											<span class="sr-only">Added:</span>+ {r}
										</div>{/each}
								{:else}
									<div class="mono">
										<span class="removed">{d.before ?? '—'}</span> →
										<span class="added">{d.after ?? '—'}</span>
									</div>
								{/if}
							</li>
						{/each}
					</ul>
				</section>
			{/if}
			{#if e.jobId}<a href={routes.job(e.jobId)}>Open the Job</a>{/if}
			<Disclosure summary="Advanced">
				<Facts
					columns={1}
					items={[
						{ label: 'Action Key', value: e.action, mono: true },
						{
							label: 'Targets',
							value:
								e.targets.map((t) => `${t.type}:${t.id}`).join(', ') || undefined,
							mono: true
						},
						{ label: 'Error Class', value: e.errorClass, mono: true },
						{ label: 'Request ID', value: e.requestId, mono: true },
						{ label: 'Job', value: e.jobId, mono: true },
						{ label: 'Chain Position', value: e.seq }
					]}
				/>
				{#if pairs.length}
					<dl class="pairs">
						{#each pairs as [k, v] (k)}
							<dt class="mono">{k}</dt>
							<dd class="mono">{v}</dd>
						{/each}
					</dl>
				{/if}
			</Disclosure>
		</div>
	{/if}
</Drawer>

<style>
	.more {
		padding: var(--space-3) var(--space-5);
		border-bottom: 1px solid var(--border-subtle);
	}

	.more-fields {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(240px, 100%), 1fr));
		gap: var(--space-3);
		align-items: start;
	}

	.open {
		padding: 0;
		border: 0;
		background: transparent;
		text-align: left;
		cursor: pointer;
	}

	.open:hover :global(.name) {
		color: var(--accent-text);
	}

	.pad {
		padding: var(--space-4);
	}

	.load {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-2);
		padding: var(--space-3);
	}

	.small {
		font-size: var(--text-caption);
	}

	.record {
		display: grid;
		gap: var(--space-5);
	}

	.subsection-title {
		margin-bottom: var(--space-2);
	}

	.times {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.diff {
		display: grid;
		gap: var(--space-2);
	}

	.field {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.added {
		color: var(--ok);
		overflow-wrap: anywhere;
	}

	.removed {
		color: var(--danger);
		overflow-wrap: anywhere;
	}

	.pairs {
		display: grid;
		grid-template-columns: max-content minmax(0, 1fr);
		gap: var(--space-1) var(--space-3);
		margin: var(--space-3) 0 0;
		font-size: var(--text-caption);
	}

	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}

	@media (max-width: 767px) {
		.more {
			padding: var(--space-3) var(--space-4);
		}
	}
</style>
