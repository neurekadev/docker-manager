<script lang="ts">
	// Stack KPI row (#22 mockup: six cards): status, services running,
	// CPU with a sparkline, memory against the environment total, uptime
	// and the last deploy with its revision hash. CPU is a share of the
	// environment's cores and memory the containers' usage (#5 units).
	import Clock from '@lucide/svelte/icons/clock';
	import Cpu from '@lucide/svelte/icons/cpu';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import Package from '@lucide/svelte/icons/package';
	import Rocket from '@lucide/svelte/icons/rocket';
	import { TILE_HEX } from '$lib/design/hue';
	import {
		KpiCard,
		Meter,
		Sparkline,
		formatBytes,
		formatDuration,
		formatPercent,
		formatRelative,
		statusInfo
	} from '$lib/ui';
	import { serviceCounts, shortHash, stackStatus, statusSummary, type StackUsage } from './model';
	import type { EnvironmentCapacity, Stack } from './queries';

	interface Props {
		stack: Stack;
		usage: StackUsage | null;
		capacity?: EnvironmentCapacity;
		/** Oldest start of a running container. */
		since?: string;
		revisionsHref?: string;
		now?: Date;
	}

	let { stack, usage, capacity, since, revisionsHref, now = new Date() }: Props = $props();

	const status = $derived(stackStatus(stack));
	const formatDate = (iso: string) =>
		new Intl.DateTimeFormat('en', { dateStyle: 'medium' }).format(new Date(iso));
	const info = $derived(statusInfo(status));
	const tone = $derived(
		info.tone === 'ok'
			? 'ok'
			: info.tone === 'danger'
				? 'danger'
				: info.tone === 'warn'
					? 'warn'
					: undefined
	);
	const counts = $derived(serviceCounts(stack));
	const memTotal = $derived(capacity?.memoryTotalBytes);
	const mem = $derived(usage?.memoryNow ?? null);
	const applied = $derived(stack.appliedRevision);
	const cpuTrend = $derived.by(() => {
		const v = (usage?.cpu ?? []).filter((x): x is number => x !== null);
		if (v.length < 2) return 'No CPU history yet';
		const min = Math.min(...v);
		const max = Math.max(...v);
		return `CPU between ${formatPercent(min)} and ${formatPercent(max)} over the last hour`;
	});
</script>

<div class="wrap">
	<div class="kpis">
		<KpiCard label="Stack status" value={info.label} {tone} secondary={statusSummary(stack)} />
		<KpiCard
			label="Services"
			value="{counts.servicesRunning} / {counts.services}"
			icon={Package}
			color="blue"
			secondary="{counts.containersRunning} / {counts.containers} containers"
		/>
		<KpiCard
			label="CPU usage"
			value={formatPercent(usage?.cpuNow)}
			icon={Cpu}
			color="cyan"
			secondary={usage && usage.cpuNow !== null
				? capacity
					? `of ${capacity.cpus} ${capacity.cpus === 1 ? 'core' : 'cores'}`
					: undefined
				: 'No samples yet'}
		>
			{#snippet sparkline()}
				{#if usage && usage.cpu.some((v) => v !== null)}
					<Sparkline values={usage.cpu} color={TILE_HEX.cyan.fg} label={cpuTrend} />
				{/if}
			{/snippet}
		</KpiCard>
		{#if mem !== null && memTotal}
			<KpiCard
				label="Memory usage"
				value={formatBytes(mem)}
				unit="/ {formatBytes(memTotal)}"
				icon={MemoryStick}
				color="indigo"
			>
				{#snippet bar()}
					<Meter
						value={mem}
						max={memTotal}
						label="Memory of {stack.name}"
						valueText="{formatBytes(mem)} of {formatBytes(memTotal)}"
					/>
				{/snippet}
			</KpiCard>
		{:else}
			<KpiCard
				label="Memory usage"
				value={formatBytes(mem)}
				icon={MemoryStick}
				color="indigo"
				secondary={mem === null ? 'No samples yet' : 'Environment total unknown'}
			/>
		{/if}
		<KpiCard
			label="Uptime"
			value={since ? formatDuration((now.getTime() - Date.parse(since)) / 1000) : '—'}
			icon={Clock}
			color="green"
			secondary={since ? `Since ${formatDate(since)}` : 'No container is running'}
		/>
		<KpiCard
			label="Last deploy"
			value={applied?.at ? formatRelative(applied.at, now) : 'Never'}
			icon={Rocket}
			color="violet"
		>
			{#snippet secondary()}
				{#if applied}
					{#if revisionsHref}
						<a
							class="hash mono"
							href={revisionsHref}
							title="Revision {applied.seq}: {applied.hash}"
							aria-label="Revision {applied.seq} ({shortHash(applied.hash)})"
							>{shortHash(applied.hash)}</a
						>
					{:else}
						<span class="mono" title={applied.hash}>{shortHash(applied.hash)}</span>
					{/if}
				{:else}
					Not deployed by DockYard yet
				{/if}
			{/snippet}
		</KpiCard>
	</div>
</div>

<style>
	/* Six cards in one row as in the mockup when the page is wide enough
	   (container width, so the sidebar and rail are accounted for). */
	.wrap {
		container-type: inline-size;
	}

	.kpis {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: var(--space-4);
	}

	@container (min-width: 1280px) {
		.kpis {
			grid-template-columns: repeat(6, minmax(0, 1fr));
		}
	}

	@container (max-width: 759px) {
		.kpis {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}

	@container (max-width: 459px) {
		.kpis {
			grid-template-columns: 1fr;
		}
	}

	.hash {
		color: var(--accent-text);
	}
</style>
