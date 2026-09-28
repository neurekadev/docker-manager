<script lang="ts">
	// One environment on the dashboard (#5, #22), a long full-width row:
	// status, Engine, CPU and memory with the last 30 minutes as sparklines
	// (gaps are breaks), disk, Docker counts, stacks with undeployed changes
	// and available updates. An offline environment shows its last known
	// values and says so.
	import { createQuery } from '@tanstack/svelte-query';
	import Server from '@lucide/svelte/icons/server';
	import type { Schema } from '$lib/api/client';
	import { environmentMetricsQuery, environmentSystemQuery } from '$lib/api/queries';
	import { TILE_HEX } from '$lib/design/hue';
	import { seriesValues } from '$lib/features/environments/model';
	import { routes } from '$lib/routes';
	import {
		Badge,
		IconTile,
		Meter,
		Sparkline,
		StatusBadge,
		formatBytes,
		formatPercent,
		formatRelative,
		gapIntervals
	} from '$lib/ui';

	interface Props {
		env: Schema<'OverviewEnvironment'>;
		/** When it went offline (Environment.connectionChangedAt). */
		since?: string;
		stacks?: number;
		undeployed?: number;
		updates?: number;
		now?: Date;
	}

	let { env, since, stacks, undeployed = 0, updates = 0, now }: Props = $props();

	const canMetrics = $derived(env.actions.includes('environment.metrics.read'));
	const canSystem = $derived(env.actions.includes('environment.system.read'));

	const system = createQuery(() => ({
		...environmentSystemQuery(env.id),
		enabled: canSystem
	}));
	// The raw 10 s samples (180 points): the sparklines move with every new
	// sample; the figures beside them are the live values (overview usage,
	// about every second while the stream is open).
	const metrics = createQuery(() => ({
		...environmentMetricsQuery(env.id, 1800, {
			series: ['cpu.percent', 'memory.used_bytes'],
			stepSeconds: 10
		}),
		enabled: canMetrics
	}));

	const cpu = $derived(seriesValues(metrics.data, 'cpu.percent'));
	const mem = $derived(seriesValues(metrics.data, 'memory.used_bytes'));
	const cpuGaps = $derived(
		metrics.data
			? gapIntervals(
					metrics.data.timestamps.map((t) => Date.parse(t)),
					cpu,
					Date.parse(metrics.data.to)
				)
			: []
	);
	const usage = $derived(env.usage);
	const engine = $derived(system.data?.engine);
	const docker = $derived(env.docker);
	const disk = $derived(
		usage?.diskUsedBytes !== undefined && usage?.diskTotalBytes
			? { used: usage.diskUsedBytes, total: usage.diskTotalBytes }
			: undefined
	);
	const known = (n: number | undefined) => (n === undefined || n < 0 ? '—' : String(n));
</script>

<article class="env" class:offline={!env.online} aria-labelledby="env-{env.id}">
	<div class="ident">
		<header class="head">
			<IconTile icon={Server} color={env.online ? 'blue' : 'slate'} size="md" />
			<div class="names">
				<h3 id="env-{env.id}"><a href={routes.environment(env.id)}>{env.name}</a></h3>
				<p class="engine">
					{#if engine}
						Docker {engine.version}<span class="sep" aria-hidden="true"
						></span>{engine.os}/{engine.arch}
					{:else if canSystem && system.isPending}
						<span class="muted">Reading Engine…</span>
					{:else}
						<span class="muted">Engine not reported yet</span>
					{/if}
				</p>
			</div>
			<StatusBadge status={env.online ? 'online' : 'offline'} />
		</header>

		{#if !env.online}
			<p class="offline-note">
				{since ? `Offline since ${formatRelative(since, now)}.` : 'Offline.'} Values are the last
				known.
			</p>
		{/if}

		{#if undeployed || updates}
			<div class="flags">
				{#if undeployed}
					<a href={routes.stacks()}
						><Badge tone="warn" dot
							>{undeployed}
							{undeployed === 1 ? 'stack has' : 'stacks have'} undeployed changes</Badge
						></a
					>
				{/if}
				{#if updates}
					<a href={routes.updates()}
						><Badge tone="warn" dot
							>{updates} {updates === 1 ? 'update' : 'updates'} available</Badge
						></a
					>
				{/if}
			</div>
		{/if}
	</div>

	{#if canMetrics}
		{#if usage}
			<div class="metric">
				<p class="line">
					<span class="k">CPU</span>
					<span class="value num">{formatPercent(usage.cpuPercent)}</span>
				</p>
				<span class="spark">
					<Sparkline
						values={cpu}
						color={TILE_HEX.cyan.fg}
						label="CPU of {env.name}, last 30 minutes{cpuGaps.length
							? ', with gaps without samples'
							: ''}"
					/>
				</span>
			</div>
			<div class="metric">
				<p class="line">
					<span class="k">Memory</span>
					<span class="value num">
						{formatBytes(usage.memoryUsedBytes)}
						{#if usage.memoryTotalBytes}<span class="muted"
								>/ {formatBytes(usage.memoryTotalBytes)}</span
							>{/if}
					</span>
				</p>
				<span class="spark">
					<Sparkline
						values={mem}
						color={TILE_HEX.indigo.fg}
						label="Memory of {env.name}, last 30 minutes"
					/>
				</span>
			</div>
			<div class="metric">
				<p class="line">
					<span class="k">Disk</span>
					{#if disk}
						<span class="value num">
							{formatBytes(disk.used)}
							<span class="muted">/ {formatBytes(disk.total)}</span>
						</span>
					{:else}
						<span class="value num muted">—</span>
					{/if}
				</p>
				{#if disk}
					<span class="bar">
						<Meter
							value={disk.used}
							max={disk.total}
							label="Docker data disk of {env.name}"
							valueText="{formatBytes(disk.used)} of {formatBytes(disk.total)}"
						/>
					</span>
				{/if}
			</div>
		{:else}
			<p class="muted none">No usage samples yet.</p>
		{/if}
	{/if}

	{#if docker}
		<dl class="counts">
			<div>
				<dt>Containers</dt>
				<dd class="num">
					{known(docker.containersRunning)}
					<span class="muted">/ {known(docker.containers)}</span>
				</dd>
			</div>
			<div>
				<dt>Stacks</dt>
				<dd class="num">{stacks ?? '—'}</dd>
			</div>
			<div>
				<dt>Images</dt>
				<dd class="num">{known(docker.images)}</dd>
			</div>
			<div>
				<dt>Volumes</dt>
				<dd class="num">{known(docker.volumes)}</dd>
			</div>
		</dl>
	{/if}
</article>

<style>
	/* A long, thin row: identity, CPU, memory, disk and the Docker counts
	   side by side; the columns stack on narrower screens. */
	.env {
		display: grid;
		grid-template-columns: minmax(240px, 1.3fr) repeat(3, minmax(140px, 1fr)) auto;
		align-items: center;
		gap: var(--space-3) var(--space-6);
		min-width: 0;
		padding: var(--space-3) var(--space-5);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.ident {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
	}

	.head {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
	}

	.names {
		flex: 1;
		min-width: 0;
	}

	h3 {
		overflow: hidden;
		font-size: var(--text-section);
		line-height: var(--leading-section);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	h3 a {
		color: var(--text-strong);
	}

	.engine {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.sep {
		width: 1px;
		height: 12px;
		background: var(--border-strong);
	}

	.offline-note {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.metric {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
		min-width: 0;
	}

	.line {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-2);
		white-space: nowrap;
	}

	dt,
	.k {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	dd {
		margin: 0;
	}

	.value {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		white-space: nowrap;
	}

	.spark {
		display: block;
		height: 28px;
		min-width: 0;
	}

	.bar {
		display: flex;
		align-items: center;
		height: 28px;
	}

	.bar :global(.meter-row) {
		flex: 1;
	}

	.none {
		grid-column: span 3;
	}

	.counts {
		display: grid;
		grid-template-columns: repeat(4, auto);
		gap: var(--space-5);
		margin: 0;
	}

	.counts dd {
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
	}

	.flags {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.offline .value,
	.offline .counts dd {
		color: var(--text-default);
	}

	/* Laptops and tablets: identity and counts share the first row, the
	   three usage columns the second. */
	@media (max-width: 1279px) {
		.env {
			grid-template-columns: repeat(3, minmax(0, 1fr));
		}

		.ident {
			grid-column: span 2;
		}

		.counts {
			grid-row: 1;
			grid-column: 3;
			justify-self: end;
		}

		.none {
			grid-column: 1 / -1;
		}
	}

	/* Phones: a stacked card like before, each usage figure one thin row
	   (label and value, then its sparkline or bar). */
	@media (max-width: 639px) {
		.env {
			grid-template-columns: minmax(0, 1fr);
			gap: var(--space-2);
			padding: var(--space-4);
		}

		.ident {
			padding-bottom: var(--space-2);
		}

		.metric {
			display: grid;
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
			align-items: center;
			gap: var(--space-3);
		}

		.counts {
			padding-top: var(--space-3);
			border-top: 1px solid var(--border-subtle);
		}

		.ident,
		.none,
		.counts {
			grid-row: auto;
			grid-column: auto;
		}

		.counts {
			justify-self: stretch;
			grid-template-columns: repeat(4, minmax(0, 1fr));
			gap: var(--space-3);
		}
	}
</style>
