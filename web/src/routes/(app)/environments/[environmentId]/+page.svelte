<script lang="ts">
	// Environment detail (#3, #5, #34): identity and status, usage and
	// metrics charts (live, gaps visible), system information with the
	// agent's compatibility, transport and diagnostics, the agents with
	// credential rotation and removal, recent jobs, edit (name, service
	// address) and archive with the removal preview. The tab lives in the
	// URL (?tab=system|agents|jobs).
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createQuery } from '@tanstack/svelte-query';
	import Activity from '@lucide/svelte/icons/activity';
	import Archive from '@lucide/svelte/icons/archive';
	import Cpu from '@lucide/svelte/icons/cpu';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Gauge from '@lucide/svelte/icons/gauge';
	import Globe from '@lucide/svelte/icons/globe';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import MonitorCog from '@lucide/svelte/icons/monitor-cog';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Server from '@lucide/svelte/icons/server';
	import Timer from '@lucide/svelte/icons/timer';
	import Truck from '@lucide/svelte/icons/truck';
	import Undo2 from '@lucide/svelte/icons/undo-2';
	import {
		environmentCapacityQuery,
		environmentQuery,
		environmentSystemQuery,
		myPermissionsQuery,
		recentJobsQuery
	} from '$lib/api/queries';
	import AgentsPanel from '$lib/features/environments/AgentsPanel.svelte';
	import ArchiveEnvironmentDialog from '$lib/features/environments/ArchiveEnvironmentDialog.svelte';
	import EditEnvironmentDialog from '$lib/features/environments/EditEnvironmentDialog.svelte';
	import MetricsPanel from '$lib/features/environments/MetricsPanel.svelte';
	import SystemPanel from '$lib/features/environments/SystemPanel.svelte';
	import { environmentStatus, mountLabel } from '$lib/features/environments/model';
	import JobsTable from '$lib/features/jobs/JobsTable.svelte';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		EmptyState,
		ErrorState,
		IconButton,
		KpiCard,
		Menu,
		Meter,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		StatusBadge,
		Tabs,
		formatBytes,
		formatDateTime,
		formatDuration,
		formatPercent,
		formatRelative,
		type MenuEntry,
		type MetaItem
	} from '$lib/ui';

	const id = $derived(page.params.environmentId ?? '');
	const env = createQuery(() => environmentQuery(id));
	const e = $derived(env.data);

	usePage(() => ({
		title: e?.name ?? 'Environment',
		crumbs: [{ label: 'Environments', href: routes.environments() }, { label: e?.name ?? '…' }]
	}));

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const canEnroll = $derived(access.owner || access.allowed.has('agent.enroll'));
	const can = (key: string) => !!e?.actions.includes(key);
	const archived = $derived(e?.status === 'archived');

	const TABS = ['overview', 'system', 'agents', 'jobs'] as const;
	const tab = $derived.by(() => {
		const t = page.url.searchParams.get('tab') ?? 'overview';
		return (TABS as readonly string[]).includes(t) ? t : 'overview';
	});
	const system = createQuery(() => ({
		...environmentSystemQuery(id),
		enabled: !!e && can('environment.system.read') && !archived
	}));
	const capacity = createQuery(() => ({
		...environmentCapacityQuery(id),
		enabled: !!e && can('environment.metrics.read') && !archived
	}));
	const jobs = createQuery(() => ({
		...recentJobsQuery(50, { environmentId: id }),
		enabled: !!e && !archived && tab === 'jobs'
	}));

	const tabItems = $derived(
		[
			{ id: 'overview', label: 'Overview' },
			{ id: 'system', label: 'System', disabled: !can('environment.system.read') },
			{ id: 'agents', label: 'Agents', disabled: !can('environment.read') },
			{ id: 'jobs', label: 'Jobs' }
		].filter((t) => !t.disabled)
	);
	function selectTab(t: string) {
		const url = new URL(page.url);
		if (t === 'overview') url.searchParams.delete('tab');
		else url.searchParams.set('tab', t);
		void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
	}

	let range = $state('1h');
	let editOpen = $state(false);
	let archiveOpen = $state(false);

	const cap = $derived(capacity.data);
	const docker = $derived(system.data?.docker);
	const dockerDisk = $derived(cap?.disks.find((d) => d.mount === 'docker'));
	const meta = $derived.by<MetaItem[]>(() => {
		const out: MetaItem[] = [];
		const host = system.data?.host;
		const engine = system.data?.engine;
		if (host)
			out.push({
				icon: MonitorCog,
				label: host.hostname,
				mono: true,
				title: 'Engine host name'
			});
		if (engine)
			out.push({
				icon: Server,
				label: `Docker ${engine.version}`,
				title: `Engine API ${engine.apiVersion}`
			});
		if (host) out.push({ label: `${host.os}/${host.arch}` });
		if (e?.serviceAddress)
			out.push({
				icon: Globe,
				label: e.serviceAddress,
				mono: true,
				title: 'Service address'
			});
		if (e?.agentVersion) out.push({ label: `Agent ${e.agentVersion}` });
		const up = capacity.data?.uptimeSeconds ?? host?.uptimeSeconds;
		if (up !== undefined && e?.online)
			out.push({ icon: Timer, label: `Up ${formatDuration(up)}` });
		return out;
	});
	const menu = $derived.by<MenuEntry[]>(() => {
		const out: MenuEntry[] = [];
		if (can('environment.read'))
			out.push({
				label: 'Migrate stacks',
				icon: Truck,
				onSelect: () => {
					environmentSelection.select(id);
					void goto(routes.stacks());
				}
			});
		out.push({
			label: 'View jobs',
			icon: Activity,
			href: `${routes.jobs()}?environment=${encodeURIComponent(id)}`
		});
		if (can('environment.remove')) {
			out.push({ separator: true });
			out.push({
				label: 'Archive environment',
				icon: Archive,
				tone: 'danger',
				onSelect: () => (archiveOpen = true)
			});
		}
		return out;
	});
</script>

{#if env.isError}
	<ErrorState
		error={env.error}
		title="This environment could not be loaded."
		onretry={() => env.refetch()}
	/>
{:else if !e}
	<div class="page" aria-busy="true">
		<Skeleton height="72px" radius="lg" />
		<Skeleton height="96px" radius="lg" />
		<Skeleton height="320px" radius="lg" />
	</div>
{:else}
	<div class="page">
		<PageHeader title={e.name} icon={Server} color={e.online ? 'blue' : 'slate'} {meta}>
			{#snippet status()}<StatusBadge status={environmentStatus(e)} />{/snippet}
			{#snippet actions()}
				{#if archived}
					{#if canEnroll}
						<Button variant="primary" icon={Undo2} href={routes.addEnvironment(e.id)}
							>Re-attach</Button
						>
					{/if}
				{:else}
					{#if can('environment.manage')}
						<Button icon={Pencil} onclick={() => (editOpen = true)}>Edit</Button>
					{/if}
					<Menu items={menu} label="More actions for {e.name}">
						{#snippet trigger(props)}
							<IconButton
								{...props}
								label="More actions"
								icon={Ellipsis}
								variant="secondary"
							/>
						{/snippet}
					</Menu>
				{/if}
			{/snippet}
		</PageHeader>

		{#if archived}
			<Notice tone="info" title="{e.name} is archived" live="none">
				Archived {e.archivedAt ? formatRelative(e.archivedAt) : ''}: hidden from operations;
				its stacks, history and backups are kept. Re-attach it by enrolling an agent on the
				same Docker Engine.
			</Notice>
		{:else if !e.online}
			<OfflineEnvironment name={e.name} since={e.connectionChangedAt} />
		{/if}
		{#if !archived && e.compatibility && e.compatibility !== 'current'}
			<Notice
				tone={e.compatibility === 'unsupported' ? 'danger' : 'warn'}
				title={e.compatibility === 'unsupported'
					? `The agent ${e.agentVersion ?? ''} is too old: DockYard refuses it`
					: `The agent ${e.agentVersion ?? ''} is outdated`}
				live="none"
			>
				{e.upgradeInstructions ??
					'Upgrade the agent container on the host (there is no in-app self-update).'}
				{#snippet actions()}
					{#if tab !== 'system'}<Button size="sm" onclick={() => selectTab('system')}
							>Upgrade instructions</Button
						>{/if}
				{/snippet}
			</Notice>
		{/if}

		<Tabs items={tabItems} value={tab} label="{e.name} sections" onchange={selectTab}>
			{#snippet panel(t)}
				{#if t === 'overview'}
					<div class="stack">
						{#if can('environment.metrics.read') && !archived}
							<section
								class="kpis"
								aria-label="Current usage"
								aria-busy={capacity.isPending}
							>
								{#if !cap}
									{#each [0, 1, 2, 3] as i (i)}<div class="kpi-skeleton">
											<Skeleton height="64px" radius="lg" />
										</div>{/each}
								{:else}
									<KpiCard
										label="CPU"
										value={formatPercent(cap.cpuPercent)}
										icon={Cpu}
										color="cyan"
										secondary="{cap.cpus} {cap.cpus === 1 ? 'core' : 'cores'}"
									/>
									<KpiCard
										label="Memory"
										value={formatBytes(cap.memoryUsedBytes)}
										unit={cap.memoryTotalBytes
											? `/ ${formatBytes(cap.memoryTotalBytes)}`
											: undefined}
										icon={MemoryStick}
										color="indigo"
									>
										{#snippet bar()}
											{#if cap.memoryUsedBytes !== undefined && cap.memoryTotalBytes}
												<Meter
													value={cap.memoryUsedBytes}
													max={cap.memoryTotalBytes}
													label="Memory in use"
													valueText="{formatBytes(
														cap.memoryUsedBytes
													)} of {formatBytes(cap.memoryTotalBytes)}"
												/>
											{/if}
										{/snippet}
									</KpiCard>
									{#if dockerDisk}
										<KpiCard
											label="Docker data disk"
											value={formatBytes(dockerDisk.usedBytes)}
											unit="/ {formatBytes(dockerDisk.totalBytes)}"
											icon={HardDrive}
											color="teal"
										>
											{#snippet bar()}
												<Meter
													value={dockerDisk.usedBytes}
													max={dockerDisk.totalBytes}
													label="Docker data disk in use"
													valueText="{formatBytes(
														dockerDisk.usedBytes
													)} of {formatBytes(dockerDisk.totalBytes)}"
												/>
											{/snippet}
										</KpiCard>
									{/if}
									<KpiCard
										label="Load"
										value={cap.load1 !== undefined ? cap.load1.toFixed(2) : '—'}
										icon={Gauge}
										color="violet"
										secondary={cap.load5 !== undefined &&
										cap.load15 !== undefined
											? `${cap.load5.toFixed(2)} (5 min), ${cap.load15.toFixed(2)} (15 min)`
											: undefined}
									/>
								{/if}
							</section>
						{/if}

						{#if docker}
							<Card title="Docker objects">
								<dl class="counts">
									<div>
										<dt>Containers</dt>
										<dd class="num">
											{docker.containersRunning} running, {docker.containersStopped}
											stopped{docker.containersPaused
												? `, ${docker.containersPaused} paused`
												: ''}
										</dd>
									</div>
									<div>
										<dt>Images</dt>
										<dd class="num">{docker.images}</dd>
									</div>
									<div>
										<dt>Volumes</dt>
										<dd class="num">{docker.volumes}</dd>
									</div>
									<div>
										<dt>Networks</dt>
										<dd class="num">{docker.networks}</dd>
									</div>
								</dl>
								{#if cap?.disks.length}
									<p class="muted small">
										Filesystems: {cap.disks
											.map(
												(d) =>
													`${mountLabel(d.mount)} ${formatBytes(d.usedBytes)} of ${formatBytes(d.totalBytes)}`
											)
											.join('; ')}.
									</p>
								{/if}
							</Card>
						{/if}

						{#if can('environment.metrics.read')}
							<MetricsPanel
								environmentId={e.id}
								name={e.name}
								memoryTotal={cap?.memoryTotalBytes}
								bind:range
							/>
						{:else if !docker}
							<Card>
								<EmptyState
									title="Nothing to show here for your access."
									description="Metrics need the environment.metrics.read permission; Docker counts need environment.system.read."
									level={3}
									compact
								/>
							</Card>
						{/if}
					</div>
				{:else if t === 'system'}
					{#if system.isError}
						<ErrorState
							error={system.error}
							title="The system information could not be loaded."
							onretry={() => system.refetch()}
						/>
					{:else if !system.data}
						<Skeleton height="320px" radius="lg" />
					{:else}
						<SystemPanel env={e} system={system.data} />
					{/if}
				{:else if t === 'agents'}
					<AgentsPanel env={e} />
				{:else if t === 'jobs'}
					<Card title="Jobs" padding="none">
						{#snippet actions()}
							<a
								class="small"
								href="{routes.jobs()}?environment={encodeURIComponent(e.id)}"
								>Filter all jobs</a
							>
						{/snippet}
						{#if jobs.isPending}
							<div class="pad"><Skeleton lines={4} height="20px" /></div>
						{:else if jobs.isError}
							<div class="pad">
								<ErrorState
									error={jobs.error}
									title="The jobs could not be loaded."
									onretry={() => jobs.refetch()}
									compact
								/>
							</div>
						{:else}
							<JobsTable jobs={jobs.data?.items ?? []} label="Jobs on {e.name}">
								{#snippet empty()}
									<EmptyState
										title="No jobs on {e.name} yet."
										description="Deploys, pulls, prunes and backups on this environment show up here."
										level={3}
										compact
									/>
								{/snippet}
							</JobsTable>
						{/if}
					</Card>
				{/if}
			{/snippet}
		</Tabs>

		<p class="muted small">
			Created {e.createdAt ? formatDateTime(e.createdAt) : '—'}{e.lastSeenAt
				? `, last seen ${formatRelative(e.lastSeenAt)}`
				: ''}. ID <span class="mono">{e.id}</span>
		</p>
	</div>

	<EditEnvironmentDialog env={e} bind:open={editOpen} />
	<ArchiveEnvironmentDialog env={e} bind:open={archiveOpen} />
{/if}

<style>
	.page,
	.stack {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.kpis {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
		gap: var(--space-4);
	}

	.kpi-skeleton {
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.counts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
		gap: var(--space-3) var(--space-5);
		margin: 0;
	}

	.counts dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.counts dd {
		margin: 0;
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.small {
		font-size: var(--text-caption);
	}

	.counts + .small {
		margin-top: var(--space-4);
	}

	.pad {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}
</style>
