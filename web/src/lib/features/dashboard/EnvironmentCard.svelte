<script lang="ts">
	// One environment on the dashboard (#5, #22): status, Engine, CPU and
	// memory with the last 30 minutes as sparklines (gaps are breaks), disk,
	// Docker counts, stacks with undeployed changes and available updates.
	// An offline environment shows its last known values and says so.
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
	const metrics = createQuery(() => ({
		...environmentMetricsQuery(env.id, 1800, {
			series: ['cpu.percent', 'memory.used_bytes'],
			stepSeconds: 60
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
	const known = (n: number | undefined) => (n === undefined || n < 0 ? '—' : String(n));
</script>

<article class="env" class:offline={!env.online} aria-labelledby="env-{env.id}">
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

	{#if canMetrics}
		<div class="usage">
			<div class="row">
				<span class="k">CPU</span>
				<span class="value num">{formatPercent(usage?.cpuPercent)}</span>
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
			<div class="row">
				<span class="k">Memory</span>
				<span class="value num">
					{formatBytes(usage?.memoryUsedBytes)}
					{#if usage?.memoryTotalBytes}<span class="muted"
							>/ {formatBytes(usage.memoryTotalBytes)}</span
						>{/if}
				</span>
				<span class="spark">
					<Sparkline
						values={mem}
						color={TILE_HEX.indigo.fg}
						label="Memory of {env.name}, last 30 minutes"
					/>
				</span>
			</div>
			{#if usage?.memoryUsedBytes !== undefined && usage?.memoryTotalBytes}
				<div class="meter">
					<Meter
						value={usage.memoryUsedBytes}
						max={usage.memoryTotalBytes}
						label="Memory of {env.name}"
						valueText="{formatBytes(usage.memoryUsedBytes)} of {formatBytes(
							usage.memoryTotalBytes
						)}"
					/>
				</div>
			{/if}
			{#if usage?.diskUsedBytes !== undefined && usage?.diskTotalBytes}
				<div class="row disk">
					<span class="k">Disk</span>
					<span class="value num">
						{formatBytes(usage.diskUsedBytes)}
						<span class="muted">/ {formatBytes(usage.diskTotalBytes)}</span>
					</span>
					<span class="spark">
						<Meter
							value={usage.diskUsedBytes}
							max={usage.diskTotalBytes}
							label="Docker data disk of {env.name}"
							valueText="{formatBytes(usage.diskUsedBytes)} of {formatBytes(
								usage.diskTotalBytes
							)}"
						/>
					</span>
				</div>
			{/if}
			{#if !usage}
				<p class="muted none">No usage samples yet.</p>
			{/if}
		</div>
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
</article>

<style>
	.env {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
		padding: var(--space-4) var(--space-5) var(--space-5);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
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
		font-size: var(--text-section);
		line-height: var(--leading-section);
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
	}

	.usage {
		display: grid;
		gap: var(--space-2);
		margin: 0;
	}

	.row {
		display: grid;
		grid-template-columns: 64px minmax(96px, auto) 1fr;
		align-items: center;
		gap: var(--space-3);
		min-height: 32px;
	}

	dt,
	.k {
		color: var(--text-muted);
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
		height: 32px;
		min-width: 0;
	}

	.disk .spark {
		display: flex;
		align-items: center;
	}

	.disk .spark :global(.meter-row) {
		flex: 1;
	}

	.meter {
		padding-left: calc(64px + var(--space-3));
	}

	.none {
		padding: var(--space-1) 0;
	}

	.counts {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: var(--space-3);
		margin: 0;
		padding-top: var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}

	.counts dt {
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
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
</style>
