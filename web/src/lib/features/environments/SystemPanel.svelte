<script lang="ts">
	// System information of one environment (#3, #5, #27, #28, #34): host
	// identity and capacity, the Engine and its negotiated API, the agent
	// with its version compatibility and upgrade instructions, transport
	// (a plain-HTTP internal URL is flagged), storage roots and diagnostics.
	import type { Environment, EnvironmentSystem } from '$lib/api/client';
	import {
		Badge,
		Card,
		CopyButton,
		Notice,
		StatusBadge,
		formatBytes,
		formatDateTime,
		formatDuration,
		formatRelative
	} from '$lib/ui';
	import { COMPATIBILITY } from './model';

	let { env, system, now }: { env: Environment; system: EnvironmentSystem; now?: Date } =
		$props();

	const host = $derived(system.host);
	const engine = $derived(system.engine);
	const agent = $derived(system.agent);
	const transport = $derived(system.transport);
	const compat = $derived(agent ? COMPATIBILITY[agent.compatibility] : undefined);
	const rootLabel: Record<string, string> = {
		stacks: 'Stacks volume',
		volumes: 'Docker volumes',
		bind: 'Additional root'
	};
	const watchLabel: Record<string, string> = {
		inotify: 'Changes appear within seconds',
		poll: 'Checked every 30 seconds',
		none: 'Not watched'
	};
</script>

<div class="panels">
	{#if system.diagnostics.length}
		<div class="diagnostics">
			{#each system.diagnostics as d (d.code)}
				<Notice
					tone="warn"
					title={d.area === 'storage' ? 'Storage check' : 'Engine check'}
					live="none"
				>
					{d.message} <span class="muted mono">({d.code})</span>
				</Notice>
			{/each}
		</div>
	{/if}

	{#if agent && agent.compatibility !== 'current'}
		<Notice
			tone={agent.compatibility === 'unsupported' ? 'danger' : 'warn'}
			title={agent.compatibility === 'unsupported'
				? `The agent ${agent.version} is too old for this DockYard`
				: `The agent ${agent.version} is outdated`}
			live="none"
		>
			{agent.compatibility === 'unsupported'
				? 'DockYard refuses its connection until it is upgraded.'
				: 'It works, but upgrade it soon: the next DockYard release will refuse it.'}
			{#if agent.upgradeInstructions ?? env.upgradeInstructions}
				<pre class="mono instructions">{agent.upgradeInstructions ??
						env.upgradeInstructions}</pre>
			{/if}
		</Notice>
	{/if}

	{#if transport?.plainHttp}
		<Notice tone="warn" title="The agent uses plain HTTP" live="none">
			It connects to <span class="mono">{transport.managerUrl}</span> without TLS. That is only
			safe on the manager's own Docker network (DOCKYARD_MANAGER_ALLOW_HTTP).
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
					<dt>API</dt>
					<dd class="num">
						{engine.apiVersion}
						<span class="muted"
							>negotiated{engine.minApiVersion && engine.maxApiVersion
								? `, Engine serves ${engine.minApiVersion}–${engine.maxApiVersion}`
								: ''}</span
						>
					</dd>
					<dt>Engine ID</dt>
					<dd class="id">
						<span class="mono">{engine.id}</span><CopyButton
							value={engine.id}
							what="Engine ID"
						/>
					</dd>
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
					{#if system.inventoryAt}<dt>Read</dt>
						<dd title={formatDateTime(system.inventoryAt)}>
							{formatRelative(system.inventoryAt, now)}
						</dd>{/if}
				</dl>
			{:else}
				<p class="muted">The agent has not reported its Engine yet.</p>
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
					<dd>
						<StatusBadge
							status={agent.connected ? 'online' : 'offline'}
							label={agent.connected ? 'Connected' : 'Not connected'}
						/>
					</dd>
					<dt>Platform</dt>
					<dd>{agent.os}/{agent.arch}</dd>
					<dt>Protocol</dt>
					<dd class="mono">{agent.protocols.join(', ')}</dd>
					<dt>Agent ID</dt>
					<dd class="id">
						<span class="mono">{agent.id}</span><CopyButton
							value={agent.id}
							what="agent ID"
						/>
					</dd>
					{#if transport}
						<dt>Manager URL</dt>
						<dd class="flags">
							<span class="mono">{transport.managerUrl}</span>
							{#if transport.plainHttp}<Badge tone="warn" dot>Plain HTTP</Badge>{/if}
							{#if transport.customCa}<Badge tone="info">Custom CA</Badge>{/if}
						</dd>
					{/if}
					{#if system.clockSkewSeconds !== undefined}
						<dt>Clock offset</dt>
						<dd class="num">
							{system.clockSkewSeconds.toFixed(1)} s
							<span class="muted">(corrected in metrics)</span>
						</dd>
					{/if}
					{#if system.reportedAt}<dt>Reported</dt>
						<dd title={formatDateTime(system.reportedAt)}>
							{formatRelative(system.reportedAt, now)}
						</dd>{/if}
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
				<p class="muted">No file roots reported.</p>
			{/if}
			<p class="muted small">
				{system.commands.length} job kinds, {system.requests.length} requests and {system
					.streams.length} stream kinds served.
			</p>
		</Card>
	</div>
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

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
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

	.instructions {
		margin: var(--space-2) 0 0;
		white-space: pre-wrap;
	}

	.small {
		margin-top: var(--space-3);
		font-size: var(--text-caption);
	}
</style>
