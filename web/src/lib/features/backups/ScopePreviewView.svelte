<script lang="ts">
	// The scope preview of the backups (#10), computed by each environment's
	// agent. Per environment a short summary (items, estimated size), then
	// one row per stack or volume: its name, estimated size and how many of
	// its sources are included, left out or need attention. The sources
	// (with why) and, with shutdown on, the containers that stop are its
	// details: open on their own when something needs the user (an opt-in,
	// a blocked or missing source, an error, a conflict). Sources are listed
	// once (the agent may repeat one) and never keyed by their text.
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import type { Schema } from '$lib/api/client';
	import { Badge, Checkbox, Notice, Table, formatBytes, type Column } from '$lib/ui';
	import {
		scopeCounts,
		scopeItemTitle,
		scopeNeedsAttention,
		scopeSources,
		sourceState,
		type ScopeItem,
		type ScopePreview
	} from './model';

	interface Props {
		preview: ScopePreview;
		/** External bind sources opted in, per stack. */
		optedIn?: (stackId: string, path: string) => boolean;
		onOptIn?: (stackId: string, path: string, on: boolean) => void;
		/** Show the shutdown plan (affected containers). */
		showShutdown?: boolean;
		/** A stack's name by its ID (items carry only the ID). */
		stackName?: (stackId: string) => string | undefined;
	}

	let { preview, optedIn, onOptIn, showShutdown = true, stackName }: Props = $props();

	type Affected = Schema<'AffectedContainer'>;
	const stopColumns: Column<Affected>[] = [
		{ id: 'order', header: 'Stop Order', cell: orderCell, width: '100px', stack: 'meta' },
		{ id: 'name', header: 'Container', cell: nameCell, stack: 'title' },
		{ id: 'now', header: 'Now', cell: nowCell, width: '120px', stack: 'status' },
		{ id: 'during', header: 'During the Backup', cell: duringCell }
	];

	const title = (i: ScopeItem) => scopeItemTitle(i, stackName);
	const count = (n: number) => n.toLocaleString('en');

	function size(i: {
		estimatedBytes: number;
		estimatedFiles: number;
		estimateComplete: boolean;
	}) {
		const more = i.estimateComplete ? '' : '+';
		return `≈ ${formatBytes(i.estimatedBytes)}${more} · ${count(i.estimatedFiles)}${more} files`;
	}

	function envTotals(items: ScopeItem[]) {
		return {
			estimatedBytes: items.reduce((n, i) => n + i.estimatedBytes, 0),
			estimatedFiles: items.reduce((n, i) => n + i.estimatedFiles, 0),
			estimateComplete: items.every((i) => i.estimateComplete)
		};
	}

	const ERROR_TEXT: Record<string, string> = {
		agent_offline: 'the agent is offline',
		timeout: 'the agent did not answer in time (large folders take long to measure)'
	};
	const errorText = (c: string) => ERROR_TEXT[c] ?? c.replaceAll('_', ' ');

	function sourceName(s: ScopeItem['sources'][number]): string {
		return s.path || s.name || s.kind.replaceAll('_', ' ');
	}

	function sourceDetail(s: ScopeItem['sources'][number]): string {
		const kind = s.kind.replaceAll('_', ' ');
		return [
			s.path && s.name ? `${kind} ${s.name}` : kind,
			s.service ? `service ${s.service}` : ''
		]
			.filter(Boolean)
			.join(', ');
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
	{#each preview.warnings ?? [] as w, i (i)}<Notice tone="warn" title="Warning" live="none"
			>{w}</Notice
		>{/each}

	{#if preview.manager}
		<section class="env">
			<div class="env-head">
				<h3>Manager State</h3>
				<span class="muted small num">
					Database {formatBytes(preview.manager.databaseBytes)} · metrics {preview.manager
						.metricsIncluded
						? `included (${formatBytes(preview.manager.metricsBytes)})`
						: `not backed up (${formatBytes(preview.manager.metricsBytes)})`}
				</span>
			</div>
			{#each preview.manager.notes as n, i (i)}<p class="muted small">{n}</p>{/each}
		</section>
	{/if}

	{#each preview.environments as env (env.environmentId)}
		{@const items = env.items ?? []}
		<section class="env">
			<div class="env-head">
				<h3>{env.environmentName ?? 'Environment'}</h3>
				{#if env.errorClass}
					<Badge tone="danger">Preview Unavailable: {errorText(env.errorClass)}</Badge>
				{:else if items.length}
					<span class="muted small num"
						>{items.length}
						{items.length === 1 ? 'item' : 'items'} · {size(envTotals(items))}</span
					>
				{/if}
			</div>
			{#if showShutdown && preview.shutdown && env.downtime}
				<Notice tone="warn" title="Downtime During Backups" live="none"
					>{env.downtime}</Notice
				>
			{/if}
			{#if items.length}
				<ul class="items" role="list">
					{#each items as item, i (i)}
						{@const sources = scopeSources(item.sources)}
						<li>
							<details class="item" open={scopeNeedsAttention(item)}>
								<summary>
									<ChevronRight size={14} aria-hidden="true" class="chev" />
									<span class="name">{title(item)}</span>
									<Badge>{item.kind === 'stack' ? 'Stack' : 'Volume'}</Badge>
									<span class="counts">
										{#each scopeCounts(item.sources) as c (c.state)}
											{@const st = sourceState(c.state)}
											<Badge tone={st.tone} dot
												>{c.count} {st.label.toLowerCase()}</Badge
											>
										{/each}
									</span>
									<span class="size muted small num">{size(item)}</span>
								</summary>
								<div class="body">
									{#if item.error}<p class="danger small">{item.error}</p>{/if}
									<ul class="sources" role="list">
										{#each sources as s, j (j)}
											{@const st = sourceState(s.state)}
											<li>
												<Badge tone={st.tone} dot>{st.label}</Badge>
												<span class="mono path">{sourceName(s)}</span>
												<span class="muted small">{sourceDetail(s)}</span>
												{#if s.reason}<span class="muted small"
														>— {s.reason}</span
													>{/if}
												{#if s.state === 'requires_opt_in' && onOptIn && item.stackId}
													<Checkbox
														label="Include This Path"
														checked={optedIn?.(item.stackId, s.path) ??
															false}
														onchange={(e) =>
															onOptIn(
																item.stackId!,
																s.path,
																e.currentTarget.checked
															)}
													/>
												{/if}
											</li>
										{/each}
									</ul>
									{#if item.excludes?.length}
										<p class="muted small">
											Excluded paths: <span class="mono"
												>{item.excludes.join(', ')}</span
											>
										</p>
									{/if}
									{#each item.warnings ?? [] as w, j (j)}<p class="warn small">
											{w}
										</p>{/each}
									{#each item.conflicts ?? [] as c, j (j)}<p class="danger small">
											{c}
										</p>{/each}
									{#if showShutdown && preview.shutdown && item.affectedContainers?.length}
										<Table
											label="Containers Stopped for {title(item)}"
											rows={[...item.affectedContainers].sort(
												(a, b) => (a.stopOrder || 99) - (b.stopOrder || 99)
											)}
											columns={stopColumns}
											rowKey={(c) => c.name}
											manualSort
										/>
									{/if}
								</div>
							</details>
						</li>
					{/each}
				</ul>
			{:else if !env.errorClass}
				<p class="muted small">Nothing selected on this environment.</p>
			{/if}
		</section>
	{:else}
		{#if !preview.manager}
			<p class="muted small">Nothing to back up with this selection.</p>
		{/if}
	{/each}
</div>

<style>
	.scope {
		display: grid;
		gap: var(--space-5);
		min-width: 0;
	}

	.env {
		display: grid;
		gap: var(--space-2);
		min-width: 0;
	}

	.env-head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-1) var(--space-3);
	}

	h3 {
		font-size: var(--text-control);
		color: var(--text-strong);
	}

	.items {
		display: grid;
		margin: 0;
		padding: 0;
		list-style: none;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	.items > li + li {
		border-top: 1px solid var(--border-subtle);
	}

	summary {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-2);
		padding: var(--space-2) var(--space-3);
		list-style: none;
		cursor: pointer;
	}

	summary::-webkit-details-marker {
		display: none;
	}

	summary :global(.chev) {
		flex: none;
		color: var(--text-muted);
		transition: transform var(--duration-fast) var(--ease-out);
	}

	details[open] > summary :global(.chev) {
		transform: rotate(90deg);
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.counts {
		display: inline-flex;
		flex-wrap: wrap;
		gap: var(--space-1);
	}

	.size {
		margin-left: auto;
	}

	.body {
		display: grid;
		gap: var(--space-2);
		padding: 0 var(--space-3) var(--space-3) calc(var(--space-3) + 14px + var(--space-2));
		min-width: 0;
	}

	.sources {
		display: grid;
		gap: var(--space-1);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.sources li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-2);
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
