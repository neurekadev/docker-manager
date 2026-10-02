<script lang="ts">
	// One environment on the dashboard (#5, #22), a long full-width row:
	// status, Engine, CPU and memory with the last 30 minutes as sparklines
	// (gaps are breaks), disk, Docker counts, stacks with undeployed changes
	// and available updates. An offline environment shows its last known
	// values and says so. The whole card opens the environment (its name
	// link covers the card; the counts and flags inside stay links of their
	// own). The layout follows the card's own width (a size container),
	// not the viewport's.
	import { createQuery } from '@tanstack/svelte-query';
	import type { Schema } from '$lib/api/client';
	import { environmentMetricsQuery, environmentSystemQuery } from '$lib/api/queries';
	import { METRIC_COLORS } from '$lib/design/hue';
	import { environmentIcon } from '$lib/features/common/resourceIcons';
	import { seriesValues } from '$lib/features/environments/model';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import {
		Badge,
		IconTile,
		Meter,
		Sparkline,
		StatusBadge,
		formatBytes,
		formatDateTime,
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
	// A count opens its list scoped to this environment.
	const scope = () => environmentSelection.select(env.id);
</script>

<div class="frame">
	<article class="env" class:offline={!env.online} aria-labelledby="env-{env.id}">
		<div class="ident">
			<header class="head">
				<IconTile {...environmentIcon(env.online)} size="md" />
				<div class="names">
					<h3 id="env-{env.id}">
						<a class="cover" href={routes.environment(env.id)}>{env.name}</a>
					</h3>
					<p class="engine">
						{#if engine}
							<span title="Docker Engine {engine.version}"
								>Docker {engine.version}</span
							><span class="sep" aria-hidden="true"></span><span class="platform"
								>{engine.os}/{engine.arch}</span
							>
						{:else if canSystem && system.isPending}
							<span class="muted">Reading Docker…</span>
						{:else}
							<span class="muted">Docker version not known yet</span>
						{/if}
					</p>
				</div>
				<span class="status"
					><StatusBadge status={env.online ? 'online' : 'offline'} /></span
				>
			</header>

			{#if !env.online}
				<p class="offline-note">
					{#if since}Offline since <time datetime={since} title={formatDateTime(since)}
							>{formatRelative(since, now)}</time
						>.{:else}Offline.{/if} Showing the last known values.
				</p>
			{/if}

			{#if undeployed || updates}
				<div class="flags">
					{#if undeployed}
						<a class="inner" href={routes.stacks()} onclick={scope}
							><Badge tone="warn" dot
								>{undeployed}
								{undeployed === 1 ? 'stack has' : 'stacks have'} undeployed changes</Badge
							></a
						>
					{/if}
					{#if updates}
						<a class="inner" href={routes.updates()}
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
							color={METRIC_COLORS.cpu}
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
							color={METRIC_COLORS.memoryUsed}
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
						<a class="inner" href={routes.containers()} onclick={scope}
							>{known(docker.containersRunning)}
							<span class="muted">/ {known(docker.containers)}</span></a
						>
					</dd>
				</div>
				<div>
					<dt>Stacks</dt>
					<dd class="num">
						{#if stacks !== undefined}<a
								class="inner"
								href={routes.stacks()}
								onclick={scope}>{stacks}</a
							>{:else}—{/if}
					</dd>
				</div>
				<div>
					<dt>Images</dt>
					<dd class="num">
						<a class="inner" href={routes.images()} onclick={scope}
							>{known(docker.images)}</a
						>
					</dd>
				</div>
				<div>
					<dt>Volumes</dt>
					<dd class="num">
						<a class="inner" href={routes.volumes()} onclick={scope}
							>{known(docker.volumes)}</a
						>
					</dd>
				</div>
			</dl>
		{/if}
	</article>
</div>

<style>
	/* The card's width decides its layout (container queries below), so
	   it fits the dashboard column whatever the sidebar takes. */
	.frame {
		container-type: inline-size;
		min-width: 0;
	}

	/* Wide: one long, thin row with identity, CPU, memory, disk and the
	   Docker counts side by side. */
	.env {
		position: relative;
		display: grid;
		grid-template-columns: minmax(260px, 1.4fr) repeat(3, minmax(130px, 1fr)) auto;
		align-items: center;
		gap: var(--space-3) var(--space-6);
		min-width: 0;
		padding: var(--space-3) var(--space-5);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		transition: border-color var(--duration-fast) var(--ease-out);
	}

	.env:hover {
		border-color: var(--border-strong);
	}

	.env:has(.cover:focus-visible) {
		outline: var(--focus-ring);
		outline-offset: 2px;
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

	.status {
		flex-shrink: 0;
	}

	/* A step below the section heading ("Environments", 16 px). */
	h3 {
		overflow: hidden;
		font-size: var(--text-subsection);
		line-height: var(--leading-subsection);
		font-weight: var(--weight-semibold);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	h3 a {
		color: var(--text-strong);
		text-decoration: none;
	}

	/* The name's hit area covers the whole card. */
	.cover::after {
		content: '';
		position: absolute;
		inset: 0;
		border-radius: var(--radius-lg);
	}

	.cover:focus-visible {
		outline: none;
	}

	/* Links inside the card stay clickable above the cover. */
	.inner {
		position: relative;
		z-index: 1;
		color: inherit;
		text-decoration: none;
	}

	.inner:hover {
		text-decoration: underline;
	}

	/* Engine and platform on one line: never broken inside a value, cut
	   with an ellipsis before it reaches the status badge. */
	.engine {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		white-space: nowrap;
	}

	.engine > span {
		flex-shrink: 0;
	}

	.engine > .platform {
		flex-shrink: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
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

	/* Medium cards (laptops with the sidebar, tablets): identity and counts
	   share the first row, the three usage columns the second. */
	@container (max-width: 1099px) {
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

	/* Narrow cards (phones): stacked, each usage figure one thin row with
	   its label and value, then its sparkline or bar, clear of each other. */
	@container (max-width: 599px) {
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
			grid-template-columns: minmax(0, 1fr) minmax(64px, 30%);
			align-items: center;
			gap: var(--space-5);
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
			padding-top: var(--space-3);
			border-top: 1px solid var(--border-subtle);
		}
	}
</style>
