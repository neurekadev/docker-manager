<script lang="ts">
	// Audit log (#30): who did what to which resource, from where, with what
	// result. Filters by actor, action, resource, environment, category,
	// outcome and time; each record opens with its details and the rule or
	// settings diff of an edit. Export as NDJSON or CSV (audit.export).
	// audit.read is all-or-nothing: records are not filtered per resource.
	import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query';
	import Download from '@lucide/svelte/icons/download';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
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
	import { environmentName } from '$lib/features/common/data';
	import Facts from '$lib/features/common/Facts.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import { usersQuery } from '$lib/features/access/queries';
	import { displayName } from '$lib/features/access/model';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';
	import {
		ACTOR_LABELS,
		CATEGORY_LABELS,
		OUTCOME,
		detailPairs,
		diffRows,
		localToRFC3339
	} from '$lib/features/settings/audit';
	import {
		auditExportHref,
		auditPage,
		cleanFilter,
		settingsKeys,
		type AuditEvent,
		type AuditFilter
	} from '$lib/features/settings/queries';

	usePage({
		title: 'Audit log',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Audit log' }]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const allowed = $derived(can(access, 'audit.read'));
	const envs = createQuery(() => environmentsQuery());
	const users = createQuery(() => ({ ...usersQuery(), enabled: access.owner }));

	let actorKind = $state('');
	let actorId = $state('');
	let action = $state('');
	let resource = $state('');
	let environmentId = $state('');
	let outcome = $state('');
	let category = $state('');
	let since = $state('');
	let until = $state('');

	const filter = $derived<AuditFilter>(
		cleanFilter({
			actorKind: actorKind ? [actorKind as AuditEvent['actor']['kind']] : undefined,
			actorId: actorId.trim() || undefined,
			action: action
				.split(',')
				.map((s) => s.trim())
				.filter(Boolean),
			resource: resource.trim() || undefined,
			environmentId: environmentId || undefined,
			outcome: outcome ? [outcome as AuditEvent['outcome']] : undefined,
			category: category ? [category as AuditEvent['category']] : undefined,
			since: localToRFC3339(since),
			until: localToRFC3339(until)
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
	const rows = $derived((records.data?.pages ?? []).flatMap((p) => p.items));
	let open = $state<AuditEvent | null>(null);
	let drawerOpen = $state(false);

	function actorText(e: AuditEvent): string {
		const u = users.data?.find((x) => x.id === e.actor.userId);
		const who = u ? displayName(u) : e.actor.userId;
		switch (e.actor.kind) {
			case 'user':
				return who ?? 'A user';
			case 'api_token':
				return `${who ?? 'A user'}'s API token`;
			case 'agent':
				return `Agent ${e.actor.agentId?.slice(0, 8) ?? ''}`;
			default:
				return ACTOR_LABELS[e.actor.kind];
		}
	}

	function targetText(e: AuditEvent): string {
		if (!e.targets.length) return '—';
		const t = e.targets[0];
		const more = e.targets.length > 1 ? ` and ${e.targets.length - 1} more` : '';
		return `${t.type} ${t.id.length > 24 ? t.id.slice(0, 12) + '…' : t.id}${more}`;
	}

	const columns: Column<AuditEvent>[] = [
		{ id: 'at', header: 'When', cell: atCell, width: '180px', stack: 'meta' },
		{ id: 'action', header: 'Action', cell: actionCell, stack: 'title' },
		{ id: 'actor', header: 'Who', cell: actorCell, width: '200px' },
		{ id: 'target', header: 'Target', cell: targetCell, width: '220px' },
		{ id: 'outcome', header: 'Outcome', cell: outcomeCell, width: '130px', stack: 'status' }
	];
</script>

{#snippet atCell(e: AuditEvent)}<span class="num">{formatDateTime(e.at)}</span>{/snippet}
{#snippet actionCell(e: AuditEvent)}
	<button
		type="button"
		class="open"
		onclick={() => {
			open = e;
			drawerOpen = true;
		}}
	>
		<NameCell name={e.action} mono sub={CATEGORY_LABELS[e.category]} />
	</button>
{/snippet}
{#snippet actorCell(e: AuditEvent)}
	<NameCell name={actorText(e)} sub={e.clientIp} subMono />
{/snippet}
{#snippet targetCell(e: AuditEvent)}<span class="mono small">{targetText(e)}</span>{/snippet}
{#snippet outcomeCell(e: AuditEvent)}
	<Badge tone={OUTCOME[e.outcome].tone} dot>{OUTCOME[e.outcome].label}</Badge>
{/snippet}

<Page>
	<SettingsHeader
		title="Audit log"
		description="Every sign-in, permission change and operation, with who did it and the result. Records cannot be changed or deleted."
	>
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
			description="It shows activity on every resource, so the owner grants it separately (View audit log)."
		/>
	{:else}
		<Card title="Filters">
			<Fields columns={2}>
				<Select
					label="Who"
					bind:value={actorKind}
					placeholder="Anyone"
					options={Object.entries(ACTOR_LABELS).map(([value, label]) => ({
						value,
						label
					}))}
				/>
				{#if access.owner && users.data}
					<Select
						label="User"
						bind:value={actorId}
						placeholder="Any user"
						options={users.data.map((u) => ({ value: u.id, label: displayName(u) }))}
					/>
				{:else}
					<TextField
						label="Actor ID"
						bind:value={actorId}
						mono
						description="Optional. A user, API token or agent ID."
					/>
				{/if}
				<TextField
					label="Action"
					bind:value={action}
					mono
					placeholder="stack.deploy, group.permissions"
					description="Optional. Action keys, comma-separated."
				/>
				<TextField
					label="Resource"
					bind:value={resource}
					mono
					placeholder="stack:0190a6e0-…"
					description="Optional. type:id"
				/>
				<Select
					label="Environment"
					bind:value={environmentId}
					placeholder="Any environment"
					options={(envs.data ?? []).map((e) => ({ value: e.id, label: e.name }))}
				/>
				<Select
					label="Outcome"
					bind:value={outcome}
					placeholder="Any outcome"
					options={Object.entries(OUTCOME).map(([value, o]) => ({
						value,
						label: o.label
					}))}
				/>
				<Select
					label="Category"
					bind:value={category}
					placeholder="Any category"
					options={Object.entries(CATEGORY_LABELS).map(([value, label]) => ({
						value,
						label
					}))}
				/>
				<div class="range">
					<TextField label="From" type="datetime-local" bind:value={since} />
					<TextField label="Until" type="datetime-local" bind:value={until} />
				</div>
			</Fields>
		</Card>

		<Card title="Records" subtitle="Newest first." padding="none">
			{#if records.isPending}
				<div class="pad" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
			{:else if records.isError && isDenied(records.error)}
				<DeniedState level={3} title="You can't read the audit log." />
			{:else if records.isError}
				<ErrorState
					error={records.error}
					title="The audit log could not be loaded."
					onretry={() => records.refetch()}
				/>
			{:else}
				<Table label="Audit records" {rows} {columns} rowKey={(e) => e.id} manualSort>
					{#snippet empty()}
						<EmptyState
							icon={ScrollText}
							color="slate"
							title="No records match."
							description="Widen the filters or the time range."
							level={3}
							compact
						/>
					{/snippet}
				</Table>
				{#if records.hasNextPage}
					<div class="more">
						<Button
							loading={records.isFetchingNextPage}
							onclick={() => records.fetchNextPage()}>Load older records</Button
						>
					</div>
				{/if}
			{/if}
		</Card>
	{/if}
</Page>

<Drawer bind:open={drawerOpen} title={open?.action ?? 'Record'} side="right" size="560px">
	{#if open}
		{@const diff = diffRows(open.details)}
		<div class="record">
			<Facts
				columns={1}
				items={[
					{ label: 'When', value: formatDateTime(open.at) },
					{ label: 'Who', value: actorText(open) },
					{
						label: 'Outcome',
						value:
							OUTCOME[open.outcome].label +
							(open.errorClass ? ` (${open.errorClass})` : '')
					},
					{
						label: 'Environment',
						value: open.environmentId
							? environmentName(envs.data, open.environmentId)
							: undefined
					},
					{
						label: 'Targets',
						value: open.targets.map((t) => `${t.type}:${t.id}`).join(', ') || undefined,
						mono: true
					},
					{ label: 'Client address', value: open.clientIp, mono: true },
					{ label: 'Browser or client', value: open.userAgent },
					{ label: 'Request ID', value: open.requestId, mono: true },
					{ label: 'Job', value: open.jobId, mono: true },
					{ label: 'Chain position', value: open.seq }
				]}
			/>
			{#if diff.length}
				<section>
					<h3>What changed</h3>
					<ul class="diff" role="list">
						{#each diff as d (d.field)}
							<li>
								<span class="field mono">{d.field}</span>
								{#if d.added || d.removed}
									{#each d.removed ?? [] as r (r)}<div class="removed mono">
											<span class="sr-only">Removed:</span>− {r}
										</div>{/each}
									{#each d.added ?? [] as r (r)}<div class="added mono">
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
			{#if detailPairs(open.details).length}
				<section>
					<h3>Details</h3>
					<dl class="pairs">
						{#each detailPairs(open.details) as [k, v] (k)}
							<dt class="mono">{k}</dt>
							<dd class="mono">{v}</dd>
						{/each}
					</dl>
				</section>
			{/if}
			{#if open.jobId}<a href={routes.job(open.jobId)}>Open the job</a>{/if}
		</div>
	{/if}
</Drawer>

<style>
	.range {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: var(--space-3);
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

	.more {
		display: flex;
		justify-content: center;
		padding: var(--space-3);
	}

	.small {
		font-size: var(--text-caption);
	}

	.record {
		display: grid;
		gap: var(--space-5);
	}

	h3 {
		margin-bottom: var(--space-2);
		font-size: var(--text-control);
		color: var(--text-strong);
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
		margin: 0;
		font-size: var(--text-caption);
	}

	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
</style>
