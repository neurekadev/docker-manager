<script lang="ts">
	// Stack KPI row (#22 mockup): status with how many services run (the
	// service and container counts are in the header, not repeated here),
	// CPU with a sparkline, memory against the environment total, uptime
	// and the last deploy with its revision. CPU is a share of the
	// environment's cores and memory the containers' usage (#5 units).
	// Every card has its tile; labels and values stay on one line (KpiRow).
	import Activity from '@lucide/svelte/icons/activity';
	import Clock from '@lucide/svelte/icons/clock';
	import Cpu from '@lucide/svelte/icons/cpu';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import Rocket from '@lucide/svelte/icons/rocket';
	import { METRIC_COLORS, type TileColor } from '$lib/design/hue';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import {
		KpiCard,
		Meter,
		Sparkline,
		clock,
		formatBytes,
		formatDateTime,
		formatPercent,
		formatRelative,
		formatUptime,
		statusInfo
	} from '$lib/ui';
	import { shortHash, stackStatus, statusSummary, type StackUsage } from './model';
	import type { EnvironmentCapacity, Stack } from './queries';

	interface Props {
		stack: Stack;
		usage: StackUsage | null;
		capacity?: EnvironmentCapacity;
		/** Oldest start of a running container. */
		since?: string;
		revisionsHref?: string;
		/** Fixed time (tests); default the shared ticking clock. */
		now?: Date;
	}

	let { stack, usage, capacity, since, revisionsHref, now: fixedNow }: Props = $props();
	// Uptime and the last deploy follow the clock (once a second).
	const now = $derived(fixedNow ?? new Date(clock.now));

	const status = $derived(stackStatus(stack));
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
	const statusColor = $derived<TileColor>(
		tone === 'ok' ? 'green' : tone === 'danger' ? 'rose' : 'slate'
	);
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

<KpiRow>
	<KpiCard
		label="Status"
		value={info.label}
		{tone}
		icon={Activity}
		color={statusColor}
		secondary={statusSummary(stack)}
		href="#services"
	/>
	<KpiCard
		label="CPU Usage"
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
				<Sparkline values={usage.cpu} color={METRIC_COLORS.cpu} label={cpuTrend} />
			{/if}
		{/snippet}
	</KpiCard>
	{#if mem !== null && memTotal}
		<KpiCard
			label="Memory Usage"
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
			label="Memory Usage"
			value={formatBytes(mem)}
			icon={MemoryStick}
			color="indigo"
			secondary={mem === null ? 'No samples yet' : 'Environment total unknown'}
		/>
	{/if}
	<KpiCard
		label="Uptime"
		value={since ? formatUptime((now.getTime() - Date.parse(since)) / 1000) : '—'}
		icon={Clock}
		color="green"
		secondary={since ? `Since ${formatDateTime(since)}` : 'No container is running'}
	/>
	<KpiCard
		label="Last Deploy"
		value={applied?.at ? formatRelative(applied.at, now) : 'Never'}
		icon={Rocket}
		color="violet"
	>
		{#snippet secondary()}
			{#if applied}
				{#if revisionsHref}
					<a
						class="hash"
						href={revisionsHref}
						title="{applied.at
							? `${formatDateTime(applied.at)}, `
							: ''}fingerprint {shortHash(applied.hash)}">Revision {applied.seq}</a
					>
				{:else}
					<span title="Fingerprint {shortHash(applied.hash)}">Revision {applied.seq}</span
					>
				{/if}
			{:else}
				Not deployed by Docker Manager yet
			{/if}
		{/snippet}
	</KpiCard>
</KpiRow>

<style>
	.hash {
		color: var(--accent-text);
	}
</style>
