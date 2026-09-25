<script lang="ts">
	// Containers (#6): every container of the selected environment (or of
	// all visible ones), filtered by name/image, state, stack and label.
	// DockYard's own containers carry the "DockYard system" badge (#32),
	// containers of a Compose project their stack. Row actions follow the
	// container's state and granted actions (#17); refusals show the
	// server's reason.
	import { createQuery } from '@tanstack/svelte-query';
	import ContainerIcon from '@lucide/svelte/icons/container';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Plus from '@lucide/svelte/icons/plus';
	import Layers from '@lucide/svelte/icons/layers';
	import { containersQuery, type Container } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		DeniedState,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		PageHeader,
		Select,
		Skeleton,
		StatusBadge,
		Table,
		TextField,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import ContainerActionHost from '$lib/features/resources/ContainerActionHost.svelte';
	import EnvironmentGaps from '$lib/features/resources/EnvironmentGaps.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import StackBadge from '$lib/features/resources/StackBadge.svelte';
	import Toolbar from '$lib/features/resources/Toolbar.svelte';
	import { ChangeTracker } from '$lib/features/resources/changes.svelte';
	import { containerActions } from '$lib/features/resources/container-actions';
	import {
		containerStatus,
		filterContainers,
		portText,
		uniquePorts
	} from '$lib/features/resources/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Containers', crumbs: [{ label: 'Containers' }], environmentScoped: true });

	const scope = useEnvironmentScope();
	const list = createQuery(() => ({
		...containersQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0
	}));

	let q = $state('');
	let stateFilter = $state('');
	let stackFilter = $state('');
	let labelFilter = $state('');
	let host = $state<ContainerActionHost>();

	const all = $derived(list.data?.items ?? []);
	const rows = $derived(
		filterContainers(all, { q, state: stateFilter, stack: stackFilter, label: labelFilter })
	);
	const filtered = $derived(!!(q || stateFilter || stackFilter || labelFilter));
	const projects = $derived(
		[...new Set(all.flatMap((c) => (c.stack ? [c.stack.project] : [])))].sort()
	);
	const creatable = $derived(scope.creatable('container.create'));
	const key = (c: Container) => `${c.environmentId}/${c.name}`;

	const tracker = new ChangeTracker<Container>(key, (c) => `${c.state}/${c.health ?? ''}`);
	$effect(() => {
		if (list.data) tracker.update(list.data.items);
	});

	const envName = (id: string) => scope.name(id);

	function menu(c: Container): MenuEntry[] {
		const entries: MenuEntry[] = [
			{ label: 'Open', href: routes.container(c.environmentId, c.name) }
		];
		const acts = containerActions(c);
		if (acts.length) entries.push({ separator: true });
		for (const a of acts)
			entries.push({
				label: a.verb === 'remove' ? 'Remove…' : a.verb === 'stop' ? 'Stop…' : a.label,
				tone: a.danger && a.verb === 'remove' ? 'danger' : 'default',
				onSelect: () => host?.request(c, a.verb)
			});
		return entries;
	}

	function clearFilters() {
		q = '';
		stateFilter = '';
		stackFilter = '';
		labelFilter = '';
	}

	const columns: Column<Container>[] = $derived([
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (c) => c.name,
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (c) => containerStatus(c),
			width: '140px',
			stack: 'status'
		},
		{ id: 'stack', header: 'Stack', cell: stackCell, sortValue: (c) => c.stack?.project ?? '' },
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (c: Container) => envName(c.environmentId)
					} satisfies Column<Container>
				]),
		{ id: 'ports', header: 'Ports', cell: portsCell },
		{
			id: 'created',
			header: 'Created',
			cell: createdCell,
			sortValue: (c) => c.createdAt ?? '',
			width: '130px'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '56px',
			align: 'end',
			stack: 'actions'
		}
	]);
</script>

{#snippet nameCell(c: Container)}
	<div class="name-cell">
		<a class="name" href={routes.container(c.environmentId, c.name)}>{c.name}</a>
		{#if c.image}<span class="sub mono">{c.image}</span>{/if}
		{#if c.protection}<span class="tag"><ProtectionBadge protection={c.protection} /></span
			>{/if}
	</div>
{/snippet}
{#snippet statusCell(c: Container)}
	<StatusBadge status={containerStatus(c)} />
{/snippet}
{#snippet stackCell(c: Container)}
	{#if c.stack}<StackBadge stack={c.stack} />{:else}<span class="muted">Standalone</span>{/if}
{/snippet}
{#snippet envCell(c: Container)}
	<span>{envName(c.environmentId)}</span>
{/snippet}
{#snippet portsCell(c: Container)}
	{@const ports = uniquePorts(c.ports).filter((p) => p.hostPort)}
	{#if ports.length}
		<span class="mono ports"
			>{ports.slice(0, 2).map(portText).join(', ')}{#if ports.length > 2}<span class="muted">
					+{ports.length - 2}</span
				>{/if}</span
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet createdCell(c: Container)}
	{#if c.createdAt}<span class="muted nowrap" title={c.createdAt}
			>{formatRelative(c.createdAt)}</span
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionsCell(c: Container)}
	<Menu items={menu(c)} label="Actions for {c.name}" align="end">
		{#snippet trigger(props)}
			<IconButton {...props} icon={Ellipsis} label="Actions for {c.name}" size="sm" />
		{/snippet}
	</Menu>
{/snippet}

<ContainerActionHost bind:this={host} environmentName={envName} />

{#if scope.restricted}
	<DeniedState level={1} />
{:else if scope.perms.data && !scope.hasAny('container.')}
	<DeniedState
		level={1}
		title="You don't have access to containers."
		description="Ask the owner of this DockYard to grant access."
	/>
{:else}
	<Page>
		<PageHeader
			title="Containers"
			description="Every container on {scope.single
				? scope.targets[0]?.name
				: 'your environments'}, running or not."
		>
			{#snippet actions()}
				{#if creatable.length}
					<Button
						variant="primary"
						icon={Plus}
						href={routes.newContainer(scope.single ? scope.targets[0]?.id : undefined)}
						>Create container</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if list.data}
			<EnvironmentGaps
				unavailable={list.data.unavailable}
				what="containers"
				single={scope.single}
			/>
		{/if}

		<Toolbar
			label="Filter containers"
			summary={list.data
				? filtered
					? `${rows.length} of ${all.length} containers`
					: `${all.length} containers`
				: undefined}
		>
			<TextField
				label="Search containers"
				hideLabel
				type="search"
				placeholder="Search by name or image"
				bind:value={q}
			/>
			<Select
				label="State"
				bind:value={stateFilter}
				options={[
					{ value: '', label: 'All states' },
					{ value: 'running', label: 'Running' },
					{ value: 'paused', label: 'Paused' },
					{ value: 'restarting', label: 'Restarting' },
					{ value: 'exited', label: 'Exited' },
					{ value: 'created', label: 'Created' },
					{ value: 'dead', label: 'Dead' }
				]}
			/>
			<Select
				label="Stack"
				bind:value={stackFilter}
				options={[
					{ value: '', label: 'All stacks' },
					{ value: '-', label: 'Standalone only' },
					...projects.map((p) => ({ value: p, label: p }))
				]}
			/>
			<TextField
				label="Label"
				placeholder="key or key=value"
				mono
				bind:value={labelFilter}
				autocomplete="off"
				spellcheck="false"
			/>
		</Toolbar>

		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The containers could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			<Card padding="none">
				{#if !list.data && (list.isPending || !scope.ready)}
					<div class="loading" aria-busy="true">
						<Skeleton lines={6} height="20px" />
					</div>
				{:else}
					<Table
						label="Containers"
						{rows}
						{columns}
						rowKey={key}
						changed={tracker.changed}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<EmptyState
									icon={ContainerIcon}
									color="slate"
									title="No containers match these filters."
									description="Change the filters or clear them to see every container."
									level={2}
									compact
								>
									{#snippet actions()}
										<Button variant="secondary" onclick={clearFilters}
											>Clear filters</Button
										>
									{/snippet}
								</EmptyState>
							{:else}
								<EmptyState
									icon={ContainerIcon}
									color="blue"
									title="No containers on {scope.single
										? scope.targets[0]?.name
										: 'your environments'} yet."
									description="Create a container from an image, or deploy a Compose stack for anything with several services."
									level={2}
									compact
								>
									{#snippet actions()}
										{#if creatable.length}
											<Button
												variant="primary"
												icon={Plus}
												href={routes.newContainer(
													scope.single ? scope.targets[0]?.id : undefined
												)}>Create container</Button
											>
										{/if}
										<Button
											variant="secondary"
											icon={Layers}
											href={routes.stacks()}>Go to stacks</Button
										>
									{/snippet}
								</EmptyState>
							{/if}
						{/snippet}
					</Table>
				{/if}
			</Card>
		{/if}
	</Page>
{/if}

<style>
	.name-cell {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		text-decoration: none;
	}

	.name:hover {
		color: var(--accent-text);
	}

	.sub {
		max-width: 34ch;
		color: var(--text-muted);
		font-size: var(--text-caption);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.tag {
		margin-top: 2px;
	}

	.ports {
		font-size: var(--text-caption);
	}

	.nowrap {
		white-space: nowrap;
	}

	.loading {
		padding: var(--space-5);
	}
</style>
