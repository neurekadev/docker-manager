<script lang="ts">
	// Container overview (#6): usage (#5 metrics: CPU as % of the
	// environment's cores, memory against the limit; TimeSeriesChart keeps
	// gaps as breaks and lists them), configuration in words (restart
	// policy, health) with the technical detail (command, entrypoint, the
	// health check's command, the container ID) behind "Advanced", ports,
	// networks, mounts, environment variable names (values are never
	// returned) and labels (system labels folded). Health and restarts show
	// once, in the KPI row. The minimal view (#17: a restart or logs grant
	// without details) shows the status only, with a note.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Cpu from '@lucide/svelte/icons/cpu';
	import HeartPulse from '@lucide/svelte/icons/heart-pulse';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import {
		containerMetricsQuery,
		containerQuery,
		latestContainerMetricsQuery,
		type Container
	} from '$lib/api/queries';
	import { pollWhileDown } from '$lib/live';
	import { TILE_HEX } from '$lib/design/hue';
	import { routes } from '$lib/routes';
	import {
		Card,
		Chip,
		ErrorState,
		KpiCard,
		Meter,
		Notice,
		Select,
		Skeleton,
		Sparkline,
		Table,
		TimeSeriesChart,
		formatBytes,
		formatDuration,
		formatNumber,
		formatPercent,
		type Column
	} from '$lib/ui';
	import Columns from '$lib/features/common/Columns.svelte';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import LabelsCard from '$lib/features/resources/LabelsCard.svelte';
	import {
		healthCommand,
		healthLabel,
		hostnameIsId,
		joinCommand,
		networkAliases,
		portHref,
		portText,
		restartPolicyLabel,
		uniquePorts
	} from '$lib/features/resources/model';
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
	// The charts: stored samples, refreshed by new samples (every 10 s).
	const metrics = createQuery(() => ({
		...containerMetricsQuery(env, name, Number(range)),
		enabled: metricsAllowed,
		refetchInterval: pollWhileDown(30_000)
	}));
	// The KPI figures: the current CPU and memory (live, about every second
	// while the stream is open), the charts' newest values until it answers.
	const usage = createQuery(() => ({
		...latestContainerMetricsQuery(env),
		enabled: metricsAllowed
	}));
	const current = $derived(c ? usage.data?.[c.name] : undefined);

	function series(key: string): (number | null)[] {
		return metrics.data?.series.find((s) => s.key === key)?.values ?? [];
	}
	function latest(values: (number | null)[]): number | null {
		for (let i = values.length - 1; i >= 0; i--) if (values[i] !== null) return values[i];
		return null;
	}
	const cpu = $derived(series('cpu.percent'));
	const mem = $derived(series('memory.used_bytes'));
	const cpuNow = $derived(current?.cpuPercent ?? latest(cpu));
	const memNow = $derived(current?.memoryUsedBytes ?? latest(mem));
	// With a current memory reading its limit is current too (absent: none).
	const memLimit = $derived(
		(current && current.memoryUsedBytes !== undefined
			? current.memoryLimitBytes
			: latest(series('memory.limit_bytes'))) ??
			d?.resources.memoryBytes ??
			null
	);
	const cpuColor = $derived(TILE_HEX.cyan.fg);
	const memColor = TILE_HEX.indigo.fg;
	const serviceAddress = $derived(scope.environment(env)?.serviceAddress);
	const hostName = $derived(scope.environment(env)?.name ?? 'the host');

	const check = $derived(healthCommand(d?.healthcheck?.test));
	const healthSecondary = $derived(
		!check
			? 'The image defines no health check'
			: d?.healthcheck?.intervalSeconds
				? `Checked every ${formatDuration(d.healthcheck.intervalSeconds)}`
				: 'Checked by the image'
	);
	const healthTone = $derived(
		c?.health === 'healthy' ? 'ok' : c?.health === 'unhealthy' ? 'danger' : undefined
	);

	// Configuration in words; the hostname only when it is not the ID's prefix.
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
					{
						label: 'Restart policy',
						value: restartPolicyLabel(d.restartPolicy, d.restartMaxRetries)
					},
					...(d.hostname && !hostnameIsId(d.hostname, c.id)
						? [{ label: 'Hostname', value: d.hostname, mono: true }]
						: []),
					{ label: 'Network mode', value: d.networkMode, mono: true },
					{ label: 'User', value: d.user || 'Image default' },
					...(d.running
						? []
						: [
								{
									label: 'Exit code',
									value: d.exitCode,
									note: d.oomKilled ? 'out of memory' : undefined
								},
								...(d.error ? [{ label: 'Error', value: d.error }] : [])
							])
				]
	);
	const advanced = $derived<Fact[]>(
		!c || !d
			? []
			: [
					{ label: 'Command', value: joinCommand(d.cmd), mono: true },
					{ label: 'Entrypoint', value: joinCommand(d.entrypoint), mono: true },
					{ label: 'Working directory', value: d.workingDir, mono: true },
					...(check
						? [
								{
									label: 'Health check',
									value: check,
									mono: true,
									note: d.healthcheck?.retries
										? `${d.healthcheck.retries} retries`
										: undefined
								}
							]
						: []),
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
						value: d.resources.cpus
							? `${formatNumber(d.resources.cpus)} CPUs`
							: 'No limit'
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
					}
				]
	);

	const mountColumns: Column<Mount>[] = [
		{ id: 'type', header: 'Type', cell: mountType, width: '90px', stack: 'status' },
		{ id: 'source', header: 'Source', cell: mountSource, stack: 'title', maxWidth: '360px' },
		{ id: 'destination', header: 'Path in container', cell: mountDest, maxWidth: '360px' },
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
			Docker Manager if you need it.
		</Notice>
	{/if}

	{#if metricsAllowed || d}
		<KpiRow>
			{#if metricsAllowed}
				<KpiCard
					label="CPU"
					value={formatPercent(cpuNow)}
					icon={Cpu}
					color="cyan"
					secondary="of {hostName}'s cores"
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
					value={formatBytes(memNow)}
					unit={memLimit ? `/ ${formatBytes(memLimit)}` : undefined}
					icon={MemoryStick}
					color="indigo"
					secondary={memLimit ? undefined : 'No memory limit'}
				>
					{#snippet sparkline()}
						{#if mem.length}
							<Sparkline
								values={mem.slice(-60)}
								color={memColor}
								label="Memory recently"
							/>
						{/if}
					{/snippet}
					{#snippet bar()}
						{#if memLimit && memNow !== null}
							<Meter
								value={memNow}
								max={memLimit}
								label="Memory of {c.name}"
								valueText="{formatBytes(memNow)} of {formatBytes(memLimit)}"
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
					value={healthLabel(c.health)}
					icon={HeartPulse}
					color="green"
					tone={healthTone}
					secondary={healthSecondary}
				/>
			{/if}
		</KpiRow>
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
					bare
					compact
					onretry={() => metrics.refetch()}
				/>
			{:else if metrics.isPending}
				<div class="charts" aria-busy="true">
					<Skeleton height="180px" /><Skeleton height="180px" />
				</div>
			{:else if metrics.data}
				<p class="hint">Averages per interval. Gaps mean no samples were taken.</p>
				<div class="charts">
					<TimeSeriesChart
						title="CPU"
						unit="percent"
						detail="of {hostName}'s cores"
						timestamps={metrics.data.timestamps}
						from={metrics.data.from}
						to={metrics.data.to}
						lines={[{ name: 'CPU', values: cpu, color: cpuColor, area: true }]}
					/>
					<TimeSeriesChart
						title="Memory"
						unit="bytes"
						detail={memLimit ? `of ${formatBytes(memLimit)}` : undefined}
						timestamps={metrics.data.timestamps}
						from={metrics.data.from}
						to={metrics.data.to}
						lines={[{ name: 'Memory', values: mem, color: memColor, area: true }]}
					/>
				</div>
			{/if}
		</Card>
	{/if}

	{#if d}
		<Columns ratio="equal">
			<Card title="Configuration">
				<Facts items={config} label="Configuration of {c.name}" />
				<div class="advanced">
					<Disclosure summary="Advanced">
						<Facts items={advanced} label="Advanced configuration of {c.name}" />
					</Disclosure>
				</div>
			</Card>
			<Card title="Limits">
				<Facts items={limits} label="Limits of {c.name}" />
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
							{@const aliases = networkAliases(n.aliases, {
								name: c.name,
								id: c.id,
								hostname: d.hostname
							})}
							<li>
								<a class="mono" href={routes.network(env, n.name)}>{n.name}</a>
								{#if n.ipAddress}<span class="mono note">{n.ipAddress}</span>{/if}
								{#if aliases.length}<span class="note"
										>also reachable as {aliases.join(', ')}</span
									>{/if}
							</li>
						{/each}
					</ul>
				{:else}<p class="muted">
						Network mode {d.networkMode || 'none'}: no networks attached.
					</p>{/if}
			</Card>
		</Columns>

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

		<Columns ratio="equal">
			<Card title="Environment variables">
				{#if d.recreate.envKeys?.length}
					<p class="hint">
						Names only: Docker Manager stores the values sealed and never shows them.
					</p>
					<ul class="chips" role="list">
						{#each d.recreate.envKeys as k (k)}<li>
								<Chip label={k} size="sm" />
							</li>{/each}
					</ul>
				{:else}
					<p class="muted">
						{c.managed
							? 'No variables are set.'
							: 'Docker Manager never reads environment variables from Docker (they often hold secrets). It shows their names for containers it created.'}
					</p>
				{/if}
			</Card>
			<LabelsCard labels={c.labels} label="Labels of {c.name}" />
		</Columns>
	{/if}
{/if}

<style>
	.range {
		width: 170px;
	}

	.charts {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-5);
	}

	.advanced {
		margin-top: var(--space-3);
	}

	.list {
		display: grid;
		gap: var(--space-2);
	}

	.list a,
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
		margin: 0;
		padding: 0;
		list-style: none;
	}

	@media (max-width: 1023px) {
		.charts {
			grid-template-columns: 1fr;
		}
	}
</style>
