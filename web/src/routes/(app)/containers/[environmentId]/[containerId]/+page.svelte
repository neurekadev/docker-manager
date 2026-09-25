<script lang="ts">
	// Container overview (#6): usage (#5 metrics: CPU as % of the
	// environment's cores, memory against the limit), configuration, ports,
	// networks, mounts, environment variable names (values are never
	// returned) and labels. The minimal view (#17: a restart or logs grant
	// without details) shows the status only.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Activity from '@lucide/svelte/icons/activity';
	import Cpu from '@lucide/svelte/icons/cpu';
	import HeartPulse from '@lucide/svelte/icons/heart-pulse';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import { containerMetricsQuery, containerQuery, type Container } from '$lib/api/queries';
	import { serviceSeriesColor, TILE_HEX } from '$lib/design/hue';
	import { routes } from '$lib/routes';
	import {
		Card,
		ErrorState,
		KpiCard,
		Meter,
		Notice,
		Select,
		Skeleton,
		Sparkline,
		Table,
		formatBytes,
		formatPercent,
		type Column
	} from '$lib/ui';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import MetricChart from '$lib/features/resources/MetricChart.svelte';
	import { joinCommand, portHref, portText, uniquePorts } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	type Mount = NonNullable<Container['mounts']>[number];

	const env = $derived(page.params.environmentId ?? '');
	const name = $derived(page.params.containerId ?? '');
	const scope = useEnvironmentScope();
	const q = createQuery(() => containerQuery(env, name));
	const c = $derived(q.data);
	const d = $derived(c?.details);

	let range = $state('3600');
	const metricsAllowed = $derived(!!c && can(c.actions, 'container.metrics.read'));
	const metrics = createQuery(() => ({
		...containerMetricsQuery(env, name, Number(range)),
		enabled: metricsAllowed,
		refetchInterval: 30_000
	}));

	function series(key: string): (number | null)[] {
		return metrics.data?.series.find((s) => s.key === key)?.values ?? [];
	}
	function latest(values: (number | null)[]): number | null {
		for (let i = values.length - 1; i >= 0; i--) if (values[i] !== null) return values[i];
		return null;
	}
	const cpu = $derived(series('cpu.percent'));
	const mem = $derived(series('memory.used_bytes'));
	const memLimit = $derived(
		latest(series('memory.limit_bytes')) ?? d?.resources.memoryBytes ?? null
	);
	const times = $derived((metrics.data?.timestamps ?? []).map((t) => new Date(t)));
	const cpuColor = $derived(TILE_HEX.cyan.fg);
	const memColor = $derived(c ? serviceSeriesColor(env, c.name) : TILE_HEX.indigo.fg);
	const cpuSeries = $derived([
		{
			name: 'CPU',
			color: cpuColor,
			points: times.map((at, i) => ({ at, value: cpu[i] ?? null }))
		}
	]);
	const memSeries = $derived([
		{
			name: 'Memory',
			color: memColor,
			points: times.map((at, i) => ({
				at,
				value:
					mem[i] === null || mem[i] === undefined
						? null
						: Math.round((mem[i] as number) / 1048576)
			}))
		}
	]);
	const serviceAddress = $derived(scope.environment(env)?.serviceAddress);

	const config = $derived<Fact[]>(
		!c || !d
			? []
			: [
					{
						label: 'Image',
						value: c.image,
						mono: true,
						href: c.imageId ? routes.image(env, c.imageId) : undefined
					},
					{ label: 'Command', value: joinCommand(d.cmd), mono: true },
					{ label: 'Entrypoint', value: joinCommand(d.entrypoint), mono: true },
					{ label: 'Working directory', value: d.workingDir, mono: true },
					{ label: 'User', value: d.user || 'root (image default)' },
					{ label: 'Hostname', value: d.hostname, mono: true },
					{ label: 'Restart policy', value: d.restartPolicy || 'no', mono: true },
					{ label: 'Network mode', value: d.networkMode, mono: true },
					{ label: 'Platform', value: d.platform },
					{ label: 'Container ID', value: c.id.slice(0, 12), mono: true, title: c.id }
				]
	);
	const limits = $derived<Fact[]>(
		!d
			? []
			: [
					{
						label: 'CPU limit',
						value: d.resources.cpus ? `${d.resources.cpus} CPUs` : 'No limit'
					},
					{ label: 'CPU weight', value: d.resources.cpuShares || 'Default' },
					{
						label: 'Memory limit',
						value: d.resources.memoryBytes
							? formatBytes(d.resources.memoryBytes)
							: 'No limit'
					},
					{
						label: 'Processes',
						value:
							d.resources.pidsLimit === undefined || d.resources.pidsLimit < 0
								? 'No limit'
								: d.resources.pidsLimit
					},
					{
						label: 'Health check',
						value: d.healthcheck ? joinCommand(d.healthcheck.test) : 'None',
						mono: !!d.healthcheck,
						note: d.healthcheck
							? [
									d.healthcheck.intervalSeconds &&
										`every ${d.healthcheck.intervalSeconds}s`,
									d.healthcheck.retries && `${d.healthcheck.retries} retries`
								]
									.filter(Boolean)
									.join(', ')
							: undefined
					},
					{ label: 'Restarts', value: d.restartCount },
					...(d.running
						? []
						: [
								{
									label: 'Exit code',
									value: d.exitCode,
									note: d.oomKilled ? 'killed: out of memory' : undefined
								},
								...(d.error ? [{ label: 'Error', value: d.error }] : [])
							])
				]
	);

	const mountColumns: Column<Mount>[] = [
		{ id: 'type', header: 'Type', cell: mountType, width: '90px', stack: 'status' },
		{ id: 'source', header: 'Source', cell: mountSource, stack: 'title' },
		{ id: 'destination', header: 'Path in container', cell: mountDest },
		{ id: 'mode', header: 'Mode', cell: mountMode, width: '110px' }
	];
</script>

{#snippet mountType(m: Mount)}<span class="muted">{m.type}</span>{/snippet}
{#snippet mountSource(m: Mount)}
	{#if m.type === 'volume' && m.name}
		<a class="mono" href={routes.volume(env, m.name)}>{m.name}</a>
	{:else}<span class="mono">{m.source || '—'}</span>{/if}
{/snippet}
{#snippet mountDest(m: Mount)}<span class="mono">{m.destination}</span>{/snippet}
{#snippet mountMode(m: Mount)}{m.readOnly ? 'Read-only' : 'Read-write'}{/snippet}

{#if c}
	{#if c.view !== 'full'}
		<Notice tone="info" title="You can see this container's status" live="none">
			Its configuration needs the "View container details" permission. Ask the owner of this
			DockYard if you need it.
		</Notice>
	{/if}

	{#if metricsAllowed || d}
		<div class="kpis">
			{#if metricsAllowed}
				<KpiCard
					label="CPU"
					value={formatPercent(latest(cpu))}
					icon={Cpu}
					color="cyan"
					secondary="of {scope.environment(env)?.name ?? 'the host'}'s cores"
				>
					{#snippet sparkline()}
						{#if cpu.length}
							<Sparkline
								values={cpu.slice(-60)}
								color={cpuColor}
								label="CPU recently"
							/>
						{/if}
					{/snippet}
				</KpiCard>
				<KpiCard
					label="Memory"
					value={formatBytes(latest(mem))}
					unit={memLimit ? `/ ${formatBytes(memLimit)}` : undefined}
					icon={MemoryStick}
					color="indigo"
					secondary={memLimit ? undefined : 'No memory limit'}
				>
					{#snippet bar()}
						{#if memLimit && latest(mem) !== null}
							<Meter
								value={latest(mem) ?? 0}
								max={memLimit}
								label="Memory of {c.name}"
								valueText="{formatBytes(latest(mem))} of {formatBytes(memLimit)}"
								warnAt={0.8}
								dangerAt={0.95}
							/>
						{/if}
					{/snippet}
				</KpiCard>
			{/if}
			{#if d}
				<KpiCard
					label="Restarts"
					value={String(d.restartCount)}
					icon={RotateCcw}
					color="violet"
					secondary={d.running
						? 'Since it was created'
						: `Exited with code ${d.exitCode}`}
					tone={d.restartCount > 5 ? 'warn' : undefined}
				/>
				<KpiCard
					label="Health"
					value={c.health && c.health !== 'none'
						? c.health[0].toUpperCase() + c.health.slice(1)
						: 'No check'}
					icon={HeartPulse}
					color="green"
					tone={c.health === 'healthy'
						? 'ok'
						: c.health === 'unhealthy'
							? 'danger'
							: undefined}
					secondary={d.healthcheck
						? joinCommand(d.healthcheck.test)
						: 'The image defines no health check'}
				/>
			{/if}
		</div>
	{/if}

	{#if metricsAllowed}
		<Card title="Usage">
			{#snippet actions()}
				<div class="range">
					<Select
						label="Time range"
						hideLabel
						bind:value={range}
						options={[
							{ value: '3600', label: 'Last hour' },
							{ value: '21600', label: 'Last 6 hours' },
							{ value: '86400', label: 'Last 24 hours' }
						]}
					/>
				</div>
			{/snippet}
			{#if metrics.isError}
				<ErrorState
					error={metrics.error}
					title="Usage could not be loaded."
					compact
					onretry={() => metrics.refetch()}
				/>
			{:else if metrics.isPending}
				<div class="charts" aria-busy="true">
					<Skeleton height="180px" /><Skeleton height="180px" />
				</div>
			{:else}
				<p class="hint">
					Averages per interval. Gaps mean no samples, for example while the agent was
					offline.
				</p>
				<div class="charts">
					<div>
						<h3 class="chart-title">
							<Activity size={14} aria-hidden="true" /> CPU (% of the host)
						</h3>
						<MetricChart
							label="CPU of {c.name}"
							series={cpuSeries}
							unit="%"
							summary={formatPercent(latest(cpu))}
						/>
					</div>
					<div>
						<h3 class="chart-title">
							<MemoryStick size={14} aria-hidden="true" /> Memory (MB)
						</h3>
						<MetricChart
							label="Memory of {c.name}"
							series={memSeries}
							unit=" MB"
							summary={formatBytes(latest(mem))}
						/>
					</div>
				</div>
			{/if}
		</Card>
	{/if}

	{#if d}
		<div class="grid">
			<Card title="Configuration">
				<Facts items={config} label="Configuration of {c.name}" />
			</Card>
			<Card title="Limits and health">
				<Facts items={limits} label="Limits and health of {c.name}" />
			</Card>
			<Card title="Ports">
				{#if uniquePorts(c.ports).length}
					<ul class="list" role="list">
						{#each uniquePorts(c.ports) as p (portText(p))}
							{@const href = portHref(p, serviceAddress)}
							<li class="mono">
								{#if href}<a {href} target="_blank" rel="noopener noreferrer"
										>{portText(p)}</a
									>
								{:else}{portText(p)}{/if}
								{#if !p.hostPort}<span class="note">exposed, not published</span
									>{/if}
							</li>
						{/each}
					</ul>
				{:else}<p class="muted">No ports are published.</p>{/if}
			</Card>
			<Card title="Networks">
				{#if d.networks.length}
					<ul class="list" role="list">
						{#each d.networks as n (n.name)}
							<li>
								<a class="mono" href={routes.network(env, n.name)}>{n.name}</a>
								{#if n.ipAddress}<span class="mono note">{n.ipAddress}</span>{/if}
								{#if n.aliases?.length}<span class="note"
										>aliases {n.aliases.join(', ')}</span
									>{/if}
							</li>
						{/each}
					</ul>
				{:else}<p class="muted">
						Network mode {d.networkMode || 'none'}: no networks attached.
					</p>{/if}
			</Card>
		</div>

		{#if c.mounts?.length}
			<Card title="Mounts" padding="none">
				<Table
					label="Mounts of {c.name}"
					rows={c.mounts}
					columns={mountColumns}
					rowKey={(m) => m.destination}
				/>
			</Card>
		{:else}
			<Card title="Mounts"><p class="muted">No volumes or host paths are mounted.</p></Card>
		{/if}

		<div class="grid">
			<Card title="Environment variables">
				{#if d.recreate.envKeys?.length}
					<p class="hint">
						Names only: DockYard stores the values sealed and never shows them.
					</p>
					<ul class="chips" role="list">
						{#each d.recreate.envKeys as k (k)}<li class="mono">{k}</li>{/each}
					</ul>
				{:else}
					<p class="muted">
						{c.managed
							? 'No variables are set.'
							: 'DockYard never reads environment variables from the Engine (they often hold secrets). It shows their names for containers it created.'}
					</p>
				{/if}
			</Card>
			<Card title="Labels">
				{#if c.labels && Object.keys(c.labels).length}
					<Facts
						items={Object.entries(c.labels)
							.sort(([a], [b]) => a.localeCompare(b))
							.map(([k, v]) => ({ label: k, value: v, mono: true }))}
						label="Labels of {c.name}"
					/>
				{:else}<p class="muted">No labels.</p>{/if}
			</Card>
		</div>
	{/if}
{/if}

<style>
	.kpis {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
		gap: var(--space-4);
	}

	.range {
		width: 170px;
	}

	.charts {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-5);
	}

	.chart-title {
		display: flex;
		align-items: center;
		gap: 6px;
		margin-bottom: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-4);
	}

	.list {
		display: grid;
		gap: var(--space-2);
	}

	.list a,
	.grid :global(td a),
	a.mono {
		color: var(--accent-text);
		text-decoration: none;
	}

	.note {
		margin-left: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.hint {
		margin-bottom: var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.chips li {
		padding: 2px 8px;
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
		font-size: var(--text-caption);
	}

	@media (max-width: 767px) {
		.kpis {
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: var(--space-3);
		}
	}

	@media (max-width: 1023px) {
		.charts,
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
