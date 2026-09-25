<script lang="ts">
	// A backup policy's scope preview (#10), computed by each environment's
	// agent: the sources of every stack and volume with their state
	// (included, excluded, needs opt-in, blocked) and why, the estimated
	// size, and with shutdown on the containers that stop, in which order,
	// the expected downtime and the conflicts a shutdown cannot cover.
	import type { Schema } from '$lib/api/client';
	import { Badge, Checkbox, Notice, Table, formatBytes, type Column } from '$lib/ui';
	import { itemName, sourceState, type ScopeItem, type ScopePreview } from './model';

	interface Props {
		preview: ScopePreview;
		/** External bind sources the policy opted in, per stack. */
		optedIn?: (stackId: string, path: string) => boolean;
		onOptIn?: (stackId: string, path: string, on: boolean) => void;
		/** Show the shutdown plan (affected containers). */
		showShutdown?: boolean;
	}

	let { preview, optedIn, onOptIn, showShutdown = true }: Props = $props();

	type Affected = Schema<'AffectedContainer'>;
	const stopColumns: Column<Affected>[] = [
		{ id: 'order', header: 'Stop order', cell: orderCell, width: '100px', stack: 'meta' },
		{ id: 'name', header: 'Container', cell: nameCell, stack: 'title' },
		{ id: 'now', header: 'Now', cell: nowCell, width: '120px', stack: 'status' },
		{ id: 'during', header: 'During the backup', cell: duringCell }
	];

	function title(i: ScopeItem): string {
		return itemName({
			kind: i.kind as 'stack' | 'volume',
			stackName: i.item,
			volume: i.volume,
			item: i.item
		});
	}
</script>

{#snippet orderCell(c: Affected)}<span class="num">{c.stopOrder || '—'}</span>{/snippet}
{#snippet nameCell(c: Affected)}
	<span class="mono">{c.name}</span>{#if c.service}<span class="muted small">
			{c.service}</span
		>{/if}
{/snippet}
{#snippet nowCell(c: Affected)}
	{#if c.running}<Badge tone="ok" dot>Running</Badge>{:else}<Badge dot>Stopped</Badge>{/if}
{/snippet}
{#snippet duringCell(c: Affected)}
	<span class="small">
		{#if c.protected}<span class="muted">Keeps running: {c.protected}</span>
		{:else if c.stopOrder}Stops, then starts again if it was running{:else}<span class="muted"
				>Not stopped</span
			>{/if}
	</span>
{/snippet}

<div class="scope">
	{#each preview.warnings ?? [] as w (w)}<Notice tone="warn" title="Warning" live="none"
			>{w}</Notice
		>{/each}

	{#if preview.manager}
		<section class="env">
			<h3>Manager state</h3>
			<p>
				Database <span class="num">{formatBytes(preview.manager.databaseBytes)}</span>;
				metrics {preview.manager.metricsIncluded
					? `included (${formatBytes(preview.manager.metricsBytes)})`
					: `excluded (${formatBytes(preview.manager.metricsBytes)} not backed up)`}.
			</p>
			{#each preview.manager.notes as n (n)}<p class="muted small">{n}</p>{/each}
		</section>
	{/if}

	{#each preview.environments as env (env.environmentId)}
		<section class="env">
			<h3>
				{env.environmentName ?? 'Environment'}
				{#if env.errorClass}<Badge tone="danger"
						>Preview unavailable: {env.errorClass.replaceAll('_', ' ')}</Badge
					>{/if}
			</h3>
			{#if showShutdown && preview.shutdown && env.downtime}
				<Notice tone="warn" title="Downtime during backups" live="none"
					>{env.downtime}</Notice
				>
			{/if}
			{#each env.items ?? [] as item (item.item + (item.volume ?? ''))}
				<div class="item">
					<div class="item-head">
						<strong>{title(item)}</strong>
						<Badge>{item.kind === 'stack' ? 'Stack' : 'Volume'}</Badge>
						<span class="muted num">
							≈ {formatBytes(item.estimatedBytes)}, {item.estimatedFiles} files{item.estimateComplete
								? ''
								: ' or more'}
						</span>
					</div>
					{#if item.error}<p class="danger small">{item.error}</p>{/if}
					<ul class="sources" role="list">
						{#each item.sources as s (s.kind + s.path)}
							{@const st = sourceState(s.state)}
							<li>
								<Badge tone={st.tone} dot>{st.label}</Badge>
								<span class="mono path">{s.path}</span>
								<span class="muted small">
									{s.kind.replaceAll('_', ' ')}{s.name
										? ` ${s.name}`
										: ''}{s.service ? `, service ${s.service}` : ''}
								</span>
								{#if s.reason}<span class="muted small">— {s.reason}</span>{/if}
								{#if s.state === 'requires_opt_in' && onOptIn && item.stackId}
									<Checkbox
										label="Include this path"
										checked={optedIn?.(item.stackId, s.path) ?? false}
										onchange={(e) =>
											onOptIn(item.stackId!, s.path, e.currentTarget.checked)}
									/>
								{/if}
							</li>
						{/each}
					</ul>
					{#if item.excludes?.length}
						<p class="muted small">
							Excluded paths: <span class="mono">{item.excludes.join(', ')}</span>
						</p>
					{/if}
					{#each item.warnings ?? [] as w (w)}<p class="warn small">{w}</p>{/each}
					{#each item.conflicts ?? [] as c (c)}<p class="danger small">{c}</p>{/each}
					{#if showShutdown && preview.shutdown && item.affectedContainers?.length}
						<Table
							label="Containers stopped for {title(item)}"
							rows={[...item.affectedContainers].sort(
								(a, b) => (a.stopOrder || 99) - (b.stopOrder || 99)
							)}
							columns={stopColumns}
							rowKey={(c) => c.name}
							manualSort
						/>
					{/if}
				</div>
			{:else}
				{#if !env.errorClass}<p class="muted">Nothing selected on this environment.</p>{/if}
			{/each}
		</section>
	{/each}
</div>

<style>
	.scope {
		display: grid;
		gap: var(--space-4);
	}

	.env {
		display: grid;
		gap: var(--space-3);
	}

	h3 {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-control);
		color: var(--text-strong);
	}

	.item {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		min-width: 0;
	}

	.item-head,
	.sources li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-2);
	}

	.item-head strong {
		color: var(--text-strong);
	}

	.sources {
		display: grid;
		gap: var(--space-1);
	}

	.path {
		overflow-wrap: anywhere;
	}

	.small {
		font-size: var(--text-caption);
	}

	.danger {
		color: var(--danger);
	}

	.warn {
		color: var(--warn);
	}
</style>
