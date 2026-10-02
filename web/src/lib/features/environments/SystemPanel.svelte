<script lang="ts">
	// System information of one environment (#3, #5, #27, #28, #34): host
	// identity and capacity, the Engine, the agent with its version and
	// connection, storage roots and diagnostics. A plain-HTTP connection is
	// flagged in plain words. A disconnected agent shows when it was last
	// seen (refreshed about every minute while it was connected, so at most
	// a minute early); a connected one since when. Identifiers (environment, agent and Engine
	// IDs), the protocol and the API details wait under "Advanced". The
	// outdated-agent notice is the page's (shown once, above the tabs).
	// Below the facts: the disks' health and the RAID arrays (#143), each
	// disk and array with a firing alert marked (#159).
	import type { Environment, EnvironmentSystem } from '$lib/api/client';
	import type { Alert } from '$lib/features/alerts/model';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import DiskHealthCard from './DiskHealthCard.svelte';
	import RaidCard from './RaidCard.svelte';
	import { showRaidCard } from './diskHealth';
	import {
		Badge,
		Card,
		CopyButton,
		Notice,
		StatusBadge,
		formatBytes,
		formatDateTime,
		formatDuration,
		formatNumber,
		formatRelative
	} from '$lib/ui';
	import { COMPATIBILITY } from './model';

	let {
		env,
		system,
		alerts = [],
		now
	}: {
		env: Environment;
		system: EnvironmentSystem;
		/** The environment's firing disk and RAID alerts. */
		alerts?: Alert[];
		now?: Date;
	} = $props();

	const host = $derived(system.host);
	const engine = $derived(system.engine);
	const agent = $derived(system.agent);
	const transport = $derived(system.transport);
	const compat = $derived(agent ? COMPATIBILITY[agent.compatibility] : undefined);
	const rootLabel: Record<string, string> = {
		stacks: 'Stacks volume',
		volumes: 'Docker volumes',
		bind: 'Additional folder'
	};
	const watchLabel: Record<string, string> = {
		inotify: 'Changes appear within seconds',
		poll: 'Checked every 30 seconds',
		none: 'Not watched'
	};
</script>

{#snippet when(iso: string | undefined)}
	{#if iso}<time datetime={iso} title={formatDateTime(iso)}>{formatRelative(iso, now)}</time
		>{:else}<span class="muted">—</span>{/if}
{/snippet}

<div class="panels">
	{#if system.diagnostics.length}
		<div class="diagnostics">
			{#each system.diagnostics as d (d.code)}
				<Notice
					tone="warn"
					title={d.area === 'storage' ? 'Storage check' : 'Docker check'}
					live="none"
				>
					{d.message}
				</Notice>
			{/each}
		</div>
	{/if}

	{#if transport?.plainHttp}
		<Notice tone="warn" title="The agent connects without encryption" live="none">
			It reaches Docker Manager over plain HTTP. That is only safe when the agent runs next to
			Docker Manager on the same host. For any other host, give the agent Docker Manager's
			HTTPS address and restart it.
		</Notice>
	{/if}

	<div class="grid">
		<Card title="Host">
			{#if host}
				<dl class="facts">
					<dt>Host name</dt>
					<dd class="mono">{host.hostname}</dd>
					<dt>Operating system</dt>
					<dd>{host.operatingSystem ?? host.os}</dd>
					<dt>Architecture</dt>
					<dd>{host.os}/{host.arch}</dd>
					{#if host.kernelVersion}<dt>Kernel</dt>
						<dd class="mono">{host.kernelVersion}</dd>{/if}
					<dt>CPUs</dt>
					<dd class="num">{host.cpus}</dd>
					<dt>Memory</dt>
					<dd class="num">{formatBytes(host.memoryBytes)}</dd>
					{#if host.uptimeSeconds !== undefined}<dt>Uptime</dt>
						<dd>{formatDuration(host.uptimeSeconds)}</dd>{/if}
				</dl>
			{:else}
				<p class="muted">The agent has not reported the host yet.</p>
			{/if}
		</Card>

		<Card title="Docker Engine">
			{#if engine}
				<dl class="facts">
					<dt>Version</dt>
					<dd class="num">{engine.version}</dd>
					{#if engine.storageDriver}<dt>Storage driver</dt>
						<dd>{engine.storageDriver}</dd>{/if}
					{#if engine.cgroupVersion}<dt>Cgroups</dt>
						<dd>v{engine.cgroupVersion}</dd>{/if}
					<dt>Mode</dt>
					<dd class="flags">
						{#if engine.rootless}<Badge tone="warn">Rootless</Badge>{:else}Rootful{/if}
						{#if engine.dockerDesktop}<Badge
								tone="danger"
								title="Docker Desktop is not supported"
								>Docker Desktop: unsupported</Badge
							>{/if}
					</dd>
					{#if system.docker}
						<dt>Objects</dt>
						<dd class="num">
							{system.docker.containers} containers, {system.docker.images} images, {system
								.docker.volumes}
							volumes, {system.docker.networks} networks
						</dd>
					{/if}
					{#if system.inventoryAt}<dt>Counted</dt>
						<dd>{@render when(system.inventoryAt)}</dd>{/if}
				</dl>
			{:else}
				<p class="muted">The agent has not reported Docker yet.</p>
			{/if}
		</Card>

		<Card title="Agent">
			{#if agent}
				<dl class="facts">
					<dt>Version</dt>
					<dd class="flags">
						<span class="num">{agent.version}</span>
						{#if compat}<Badge tone={compat.tone} dot>{compat.label}</Badge>{/if}
					</dd>
					<dt>Connection</dt>
					<dd class="flags">
						<StatusBadge
							status={agent.connected ? 'online' : 'offline'}
							label={agent.connected ? 'Connected' : 'Not connected'}
						/>
						{#if !agent.connected && env.lastSeenAt}<span class="muted"
								>last seen {@render when(env.lastSeenAt)}</span
							>{:else if env.connectionChangedAt}<span class="muted"
								>since <time datetime={env.connectionChangedAt}
									>{formatDateTime(env.connectionChangedAt)}</time
								></span
							>{/if}
					</dd>
					<dt>Platform</dt>
					<dd>{agent.os}/{agent.arch}</dd>
					{#if transport}
						<dt>Docker Manager address</dt>
						<dd class="flags">
							<span class="mono">{transport.managerUrl}</span>
							{#if transport.plainHttp}<Badge tone="warn" dot>Not encrypted</Badge
								>{/if}
							{#if transport.customCa}<Badge tone="info"
									>Own certificate authority</Badge
								>{/if}
						</dd>
					{/if}
					{#if system.clockSkewSeconds !== undefined}
						<dt>Clock difference</dt>
						<dd class="num">
							{formatNumber(system.clockSkewSeconds)} s
							<span class="muted">(corrected in the charts)</span>
						</dd>
					{/if}
				</dl>
			{:else}
				<p class="muted">
					No agent is attached. Re-attach the environment with a new agent.
				</p>
			{/if}
		</Card>

		<Card title="Storage">
			{#if system.roots.length}
				<dl class="facts">
					{#each system.roots as r, i (i)}
						<dt>{rootLabel[r.kind] ?? r.kind}</dt>
						<dd>{watchLabel[r.watch] ?? r.watch}</dd>
					{/each}
				</dl>
			{:else}
				<p class="muted">No folders reported.</p>
			{/if}
		</Card>

		<Card title="Environment">
			<dl class="facts">
				<dt>Added</dt>
				<dd>{formatDateTime(env.createdAt)}</dd>
				{#if env.serviceAddress}<dt>Service address</dt>
					<dd class="mono">{env.serviceAddress}</dd>{/if}
			</dl>
			<div class="advanced">
				<Disclosure summary="Advanced">
					<dl class="facts">
						<dt>Environment ID</dt>
						<dd class="id">
							<span class="mono">{env.id}</span><CopyButton
								value={env.id}
								what="environment ID"
							/>
						</dd>
						{#if agent}
							<dt>Agent ID</dt>
							<dd class="id">
								<span class="mono">{agent.id}</span><CopyButton
									value={agent.id}
									what="agent ID"
								/>
							</dd>
							<dt>Protocol</dt>
							<dd class="mono">{agent.protocols.join(', ')}</dd>
						{/if}
						{#if engine}
							<dt>Engine ID</dt>
							<dd class="id">
								<span class="mono">{engine.id}</span><CopyButton
									value={engine.id}
									what="Engine ID"
								/>
							</dd>
							<dt>Engine API</dt>
							<dd class="num">
								{engine.apiVersion}
								<span class="muted"
									>negotiated{engine.minApiVersion && engine.maxApiVersion
										? `, Engine serves ${engine.minApiVersion}–${engine.maxApiVersion}`
										: ''}</span
								>
							</dd>
						{/if}
						{#if system.reportedAt}<dt>Capabilities reported</dt>
							<dd>{@render when(system.reportedAt)}</dd>{/if}
						<dt>Served</dt>
						<dd>
							{system.commands.length} job kinds, {system.requests.length} requests,
							{system.streams.length} stream kinds
						</dd>
					</dl>
				</Disclosure>
			</div>
		</Card>
	</div>

	{#if system.diskHealth}
		<DiskHealthCard
			{env}
			health={system.diskHealth}
			raid={system.raid}
			online={system.online}
			{alerts}
			{now}
		/>
	{/if}
	{#if system.raid && showRaidCard(system.raid)}
		<RaidCard
			{env}
			raid={system.raid}
			devices={system.diskHealth?.devices}
			online={system.online}
			{alerts}
			{now}
		/>
	{/if}
</div>

<style>
	.panels {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.diagnostics {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}

	/* Cards keep their own height (never stretched to a neighbour's). */
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
		align-items: start;
		gap: var(--space-4);
	}

	.facts {
		display: grid;
		grid-template-columns: minmax(110px, max-content) 1fr;
		gap: var(--space-2) var(--space-4);
		margin: 0;
	}

	dt {
		color: var(--text-muted);
	}

	dd {
		min-width: 0;
		margin: 0;
		overflow-wrap: anywhere;
	}

	.id,
	.flags {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.advanced {
		margin-top: var(--space-4);
	}
</style>
