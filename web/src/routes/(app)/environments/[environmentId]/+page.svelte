<script lang="ts">
	// Environment detail (#3, #5, #34): identity and status (one sentence,
	// or one notice when offline, archived, detached or with an outdated agent), usage
	// KPIs, Docker object counts linking to this environment's lists,
	// filesystems and metrics charts (live, gaps visible), system
	// information with identifiers under "Advanced", the agents with
	// credential rotation and removal, the jobs (paged), edit (name, service
	// address), "Migrate Environment" (its stacks to another environment;
	// with a second environment and a stack the caller may migrate), a
	// notice while stacks migrated away left their old copies here
	// ("Review the Migration") and archive with the removal preview. Active
	// disk health and RAID alerts (#159) show as one notice leading to the
	// System tab, where each disk and array with an alert is marked. The
	// tab lives in the URL (?tab=system|agents|jobs).
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import Cpu from '@lucide/svelte/icons/cpu';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Globe from '@lucide/svelte/icons/globe';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Layers from '@lucide/svelte/icons/layers';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import MonitorCog from '@lucide/svelte/icons/monitor-cog';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Server from '@lucide/svelte/icons/server';
	import Timer from '@lucide/svelte/icons/timer';
	import Truck from '@lucide/svelte/icons/truck';
	import Undo2 from '@lucide/svelte/icons/undo-2';
	import type { EnvironmentSystem } from '$lib/api/client';
	import {
		environmentCapacityQuery,
		environmentQuery,
		environmentSystemQuery,
		jobsInfiniteQuery,
		myPermissionsQuery,
		stacksSummaryQuery
	} from '$lib/api/queries';
	import { healthAlerts, healthNotice, isActive } from '$lib/features/alerts/model';
	import { alertsQuery } from '$lib/features/alerts/queries';
	import Columns from '$lib/features/common/Columns.svelte';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import AgentsPanel from '$lib/features/environments/AgentsPanel.svelte';
	import ArchiveEnvironmentDialog from '$lib/features/environments/ArchiveEnvironmentDialog.svelte';
	import EditEnvironmentDialog from '$lib/features/environments/EditEnvironmentDialog.svelte';
	import EnvironmentMigrationNotice from '$lib/features/environments/EnvironmentMigrationNotice.svelte';
	import MetricsPanel from '$lib/features/environments/MetricsPanel.svelte';
	import SystemPanel from '$lib/features/environments/SystemPanel.svelte';
	import {
		connectionSummary,
		environmentStatus,
		mountLabel
	} from '$lib/features/environments/model';
	import JobsTable from '$lib/features/jobs/JobsTable.svelte';
	import { stackNames } from '$lib/features/jobs/labels';
	import { stacksQuery } from '$lib/features/stacks/queries';
	import { routes } from '$lib/routes';
	import { environmentIcon } from '$lib/features/common/resourceIcons';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf, hasAny } from '$lib/shell/nav';
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
		formatDuration,
		formatPercent,
		formatRelative,
		type MenuEntry,
		type MetaItem
	} from '$lib/ui';

	const JOBS_PER_PAGE = 20;

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
	const only = singleEnvironment();
	const can = (key: string) => !!e?.actions.includes(key);
	const archived = $derived(e?.status === 'archived');
	// Detached: its agent was removed; a re-attach enrollment brings it back.
	const detached = $derived(!!e && !archived && e.view === 'full' && !e.agentId);

	const TABS = ['overview', 'system', 'agents', 'jobs'] as const;
	const tab = $derived.by(() => {
		const t = page.url.searchParams.get('tab') ?? 'overview';
		return (TABS as readonly string[]).includes(t) ? t : 'overview';
	});
	const system = createQuery(() => ({
		...environmentSystemQuery(id),
		enabled: !!e && can('environment.system.read') && !archived
	}));
	// Firing disk health and RAID alerts (#159; live, topic alerts): the
	// notice above the tabs (active ones) and the System tab's marks.
	const alerts = createQuery(() => ({
		...alertsQuery({ state: 'firing', environmentId: id }),
		enabled: !!e && can('environment.system.read') && !archived
	}));
	const diskAlerts = $derived(healthAlerts(alerts.data ?? [], id));
	const alertNotice = $derived(healthNotice(diskAlerts.filter(isActive)));
	const alertNoticeAction = $derived(
		diskAlerts.some((a) => isActive(a) && a.kind === 'disk_health')
			? 'View Disks'
			: 'View RAID Arrays'
	);
	const capacity = createQuery(() => ({
		...environmentCapacityQuery(id),
		enabled: !!e && can('environment.metrics.read') && !archived
	}));
	const jobs = createInfiniteQuery(() => ({
		...jobsInfiniteQuery({ environmentId: id }, JOBS_PER_PAGE),
		enabled: !!e && !archived && tab === 'jobs'
	}));
	const jobRows = $derived(jobs.data?.pages.flatMap((p) => p.items) ?? []);
	const stacks = createQuery(() => ({
		...stacksSummaryQuery(),
		enabled: !!e && tab === 'jobs' && hasAny(access, 'stack.')
	}));
	const nameOf = $derived(stackNames(stacks.data));
	// "Migrate Environment": a second environment and a stack here the
	// caller may migrate.
	const envStacks = createQuery(() => ({
		...stacksQuery(id),
		enabled: !!e && !archived && !only.current && hasAny(access, 'stack.')
	}));
	const canMigrate = $derived(
		!only.current &&
			(envStacks.data ?? []).some(
				(s) => s.environmentId === id && s.actions.includes('stack.migrate')
			)
	);

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
	// The Docker data disk is a KPI; the other filesystems get meters.
	const otherDisks = $derived((cap?.disks ?? []).filter((d) => d.mount !== 'docker'));
	const summary = $derived(e ? connectionSummary(e, Date.now()) : undefined);
	const meta = $derived.by<MetaItem[]>(() => {
		const out: MetaItem[] = [];
		const host = system.data?.host;
		const engine = system.data?.engine;
		// The host name only when it differs from the environment's name.
		if (host && host.hostname !== e?.name)
			out.push({
				icon: MonitorCog,
				label: host.hostname,
				mono: true,
				title: 'Host Name'
			});
		if (engine)
			out.push({
				icon: Server,
				label: `Docker ${engine.version}`,
				title: `Docker Engine ${engine.version}`
			});
		if (host) out.push({ label: `${host.os}/${host.arch}` });
		if (e?.serviceAddress)
			out.push({
				icon: Globe,
				label: e.serviceAddress,
				mono: true,
				title: 'Service Address'
			});
		if (e?.agentVersion) out.push({ label: `Agent ${e.agentVersion}` });
		return out;
	});
	const menu = $derived.by<MenuEntry[]>(() => {
		const out: MenuEntry[] = [];
		if (canMigrate)
			out.push({
				label: 'Migrate Environment',
				icon: Truck,
				href: routes.environmentMigrate(id)
			});
		if (can('environment.remove')) {
			if (out.length) out.push({ separator: true });
			out.push({
				label: 'Archive Environment',
				icon: Archive,
				tone: 'danger',
				onSelect: () => (archiveOpen = true)
			});
		}
		return out;
	});
	type DockerCounts = NonNullable<EnvironmentSystem['docker']>;
	// The lists open scoped to this environment.
	const scope = () => environmentSelection.select(id);
</script>

{#snippet countLink(href: string, text: string)}
	<a class="count" {href} onclick={scope}>{text}</a>
{/snippet}

{#if env.isError}
	<ErrorState
		error={env.error}
		title="This environment could not be loaded."
		onretry={() => env.refetch()}
	/>
{:else if !e}
	<Page>
		<div class="loading" aria-busy="true">
			<Skeleton height="72px" radius="lg" />
			<Skeleton height="96px" radius="lg" />
			<Skeleton height="320px" radius="lg" />
		</div>
	</Page>
{:else}
	<Page>
		<PageHeader title={e.name} description={summary} {...environmentIcon(e.online)} {meta}>
			{#snippet status()}<StatusBadge status={environmentStatus(e)} />{/snippet}
			{#snippet actions()}
				{#if archived}
					{#if canEnroll}
						<Button variant="primary" icon={Undo2} href={routes.addEnvironment(e.id)}
							>Re-Attach</Button
						>
					{/if}
				{:else}
					{#if detached && canEnroll}
						<Button variant="primary" icon={Undo2} href={routes.addEnvironment(e.id)}
							>Re-Attach</Button
						>
					{/if}
					{#if hasAny(access, 'stack.')}
						<Button icon={Layers} href={routes.stacks()} onclick={scope}>Stacks</Button>
					{/if}
					{#if can('environment.manage')}
						<Button icon={Pencil} onclick={() => (editOpen = true)}>Edit</Button>
					{/if}
					{#if menu.length}
						<Menu items={menu} label="More Actions for {e.name}">
							{#snippet trigger(props)}
								<IconButton
									{...props}
									label="More Actions"
									icon={Ellipsis}
									variant="secondary"
								/>
							{/snippet}
						</Menu>
					{/if}
				{/if}
			{/snippet}
		</PageHeader>

		<!-- One notice at most about the connection (the shell's offline banner
		     stays away from this page), and the agent's version once. -->
		{#if archived}
			<Notice tone="info" title="{e.name} is archived" live="none">
				Archived {e.archivedAt ? formatRelative(e.archivedAt) : ''}. Its stacks, history and
				backups are kept.
			</Notice>
		{:else if detached}
			<Notice tone="offline" title="{e.name} has no agent" live="none">
				Its agent was removed. Re-attach it to bring it back online.
			</Notice>
		{:else if !e.online}
			<OfflineEnvironment name={e.name} since={e.connectionChangedAt} />
		{/if}
		{#if !archived && e.compatibility && e.compatibility !== 'current'}
			<Notice
				tone={e.compatibility === 'unsupported' ? 'danger' : 'warn'}
				title={e.compatibility === 'unsupported'
					? `The agent ${e.agentVersion ?? ''} is too old: Docker Manager refuses it`
					: `The agent ${e.agentVersion ?? ''} is outdated`}
				live="none"
			>
				{e.compatibility === 'unsupported'
					? 'It cannot connect until you upgrade it on the host.'
					: 'It works, but upgrade it soon: the next Docker Manager release will refuse it.'}
				{#if e.upgradeInstructions}
					<Disclosure summary="How to Upgrade">
						<pre class="mono instructions">{e.upgradeInstructions}</pre>
					</Disclosure>
				{:else}
					Upgrade the agent container on the host; it does not update itself.
				{/if}
			</Notice>
		{/if}

		<!-- Disks and RAID arrays with active alerts; the System tab marks
		     each of them itself. -->
		{#if alertNotice && tab !== 'system' && !archived}
			<Notice tone={alertNotice.tone} title={alertNotice.title} live="none">
				{#if alertNotice.body}{alertNotice.body}{/if}
				{#snippet actions()}
					<Button size="sm" href={routes.environment(e.id, 'system')}
						>{alertNoticeAction}</Button
					>
				{/snippet}
			</Notice>
		{/if}

		<!-- Stacks migrated away whose old copies are still here (callers who
		     may migrate stacks; the server lists only what they see). -->
		{#if !archived}
			<EnvironmentMigrationNotice
				environmentId={e.id}
				enabled={hasAny(access, 'stack.migrate')}
			/>
		{/if}

		<Tabs items={tabItems} value={tab} label="{e.name} Sections" onchange={selectTab}>
			{#snippet panel(t)}
				{#if t === 'overview'}
					<div class="stack">
						{#if can('environment.metrics.read') && !archived}
							<section aria-label="Current Usage" aria-busy={capacity.isPending}>
								<KpiRow>
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
											secondary="Across {cap.cpus} {cap.cpus === 1
												? 'CPU'
												: 'CPUs'}"
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
														label="Memory in Use"
														valueText="{formatBytes(
															cap.memoryUsedBytes
														)} of {formatBytes(cap.memoryTotalBytes)}"
													/>
												{/if}
											{/snippet}
										</KpiCard>
										{#if dockerDisk}
											<KpiCard
												label="Docker Data Disk"
												value={formatBytes(dockerDisk.usedBytes)}
												unit="/ {formatBytes(dockerDisk.totalBytes)}"
												icon={HardDrive}
												color="teal"
											>
												{#snippet bar()}
													<Meter
														value={dockerDisk.usedBytes}
														max={dockerDisk.totalBytes}
														label="Docker Data Disk in Use"
														valueText="{formatBytes(
															dockerDisk.usedBytes
														)} of {formatBytes(dockerDisk.totalBytes)}"
													/>
												{/snippet}
											</KpiCard>
										{/if}
										<KpiCard
											label="Uptime"
											value={cap.uptimeSeconds !== undefined && e.online
												? formatDuration(cap.uptimeSeconds)
												: '—'}
											icon={Timer}
											color="green"
											secondary={e.online ? undefined : 'Offline'}
										/>
									{/if}
								</KpiRow>
							</section>
						{/if}

						{#if docker}
							{#snippet objects(d: DockerCounts)}
								<Card title="Docker Objects">
									<dl class="counts">
										<div>
											<dt>Containers</dt>
											<dd class="num">
												{@render countLink(
													routes.containers(),
													`${d.containersRunning} running, ${d.containersStopped} stopped${
														d.containersPaused
															? `, ${d.containersPaused} paused`
															: ''
													}`
												)}
											</dd>
										</div>
										<div>
											<dt>Images</dt>
											<dd class="num">
												{@render countLink(
													routes.images(),
													String(d.images)
												)}
											</dd>
										</div>
										<div>
											<dt>Volumes</dt>
											<dd class="num">
												{@render countLink(
													routes.volumes(),
													String(d.volumes)
												)}
											</dd>
										</div>
										<div>
											<dt>Networks</dt>
											<dd class="num">
												{@render countLink(
													routes.networks(),
													String(d.networks)
												)}
											</dd>
										</div>
									</dl>
								</Card>
							{/snippet}
							{#if otherDisks.length}
								<Columns ratio="equal">
									{@render objects(docker)}
									<Card title="Filesystems">
										<ul class="disks" role="list">
											{#each otherDisks as d (d.mount)}
												<li>
													<p class="disk-line">
														<span>{mountLabel(d.mount)}</span>
														<span class="num muted"
															>{formatBytes(d.usedBytes)} of {formatBytes(
																d.totalBytes
															)}</span
														>
													</p>
													<Meter
														value={d.usedBytes}
														max={d.totalBytes}
														label="{mountLabel(d.mount)} in Use"
														valueText="{formatBytes(
															d.usedBytes
														)} of {formatBytes(d.totalBytes)}"
													/>
												</li>
											{/each}
										</ul>
									</Card>
								</Columns>
							{:else}
								{@render objects(docker)}
							{/if}
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
									title="You don't have access to this environment's metrics."
									description="Ask the owner of this Docker Manager."
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
						<SystemPanel env={e} system={system.data} alerts={diskAlerts} />
					{/if}
				{:else if t === 'agents'}
					<AgentsPanel env={e} />
				{:else if t === 'jobs'}
					<Card title="Jobs" padding="none">
						{#snippet actions()}
							<a
								class="small"
								href="{routes.jobs()}?environment={encodeURIComponent(e.id)}"
								>View All Jobs</a
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
									bare
									compact
								/>
							</div>
						{:else}
							<JobsTable jobs={jobRows} label="Jobs on {e.name}" {nameOf}>
								{#snippet empty()}
									<EmptyState
										title="No jobs on {e.name} yet."
										description="Deploys, pulls, prunes and backups show up here."
										level={3}
										compact
									/>
								{/snippet}
							</JobsTable>
							{#if jobs.hasNextPage}
								<div class="more">
									<Button
										loading={jobs.isFetchingNextPage}
										onclick={() => jobs.fetchNextPage()}>Load More Jobs</Button
									>
								</div>
							{/if}
						{/if}
					</Card>
				{/if}
			{/snippet}
		</Tabs>
	</Page>

	<EditEnvironmentDialog env={e} bind:open={editOpen} />
	<ArchiveEnvironmentDialog env={e} bind:open={archiveOpen} />
{/if}

<style>
	.loading,
	.stack {
		display: flex;
		flex-direction: column;
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
		font-weight: var(--weight-medium);
	}

	.count {
		color: var(--text-strong);
	}

	.disks {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.disk-line {
		display: flex;
		justify-content: space-between;
		gap: var(--space-3);
		margin-bottom: var(--space-1);
	}

	.instructions {
		margin: 0;
		white-space: pre-wrap;
	}

	.small {
		font-size: var(--text-caption);
	}

	.pad {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.more {
		display: flex;
		justify-content: center;
		padding: var(--space-3);
		border-top: 1px solid var(--border-subtle);
	}
</style>
