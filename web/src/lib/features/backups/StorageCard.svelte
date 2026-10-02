<script lang="ts">
	// Backup storage (#10): what the repositories take up, measured after
	// every backup and prune (index and directory metadata only). One
	// headline (stored in the repositories), a meter of stored against the
	// unique data it holds, and three figures: that data, what compression
	// saved and the ratio. Figures are deduplicated, so they differ from the
	// size of one run (the data a run read).
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import { Card, EmptyState, Meter, formatBytes } from '$lib/ui';
	import { ratioText, type StorageTotals } from './model';

	let { totals }: { totals: StorageTotals | undefined } = $props();
</script>

<Card title="Storage" subtitle="Updated after every backup and prune.">
	{#if totals}
		<div class="storage">
			<p class="headline">
				<strong class="num">{formatBytes(totals.sizeBytes)}</strong> stored in {totals
					.repositories.length === 1
					? 'the repository'
					: `${totals.repositories.length} repositories`}
			</p>
			<Meter
				value={totals.sizeBytes}
				max={Math.max(totals.uncompressedBytes, totals.sizeBytes)}
				label="Stored Size of the Backed-Up Data"
				valueText="{formatBytes(totals.sizeBytes)} stored for {formatBytes(
					totals.uncompressedBytes
				)} of data"
				tone="neutral"
				size="md"
				showPercent={false}
			/>
			<dl class="stats">
				<div>
					<dt>Unique Data Backed Up</dt>
					<dd class="num">{formatBytes(totals.uncompressedBytes)}</dd>
				</div>
				<div>
					<dt>Saved by Compression</dt>
					<dd class="num">{formatBytes(totals.freedBytes)}</dd>
				</div>
				<div>
					<dt>Compression Ratio</dt>
					<dd class="num">{ratioText(totals.ratio)}</dd>
				</div>
			</dl>
			{#if totals.repositories.length > 1}
				<ul class="repos" role="list" aria-label="Storage per Repository">
					{#each totals.repositories as r (r.id)}
						<li>
							<span class="name" title={r.name}>{r.name}</span>
							<span class="muted num"
								>{formatBytes(r.sizeBytes)} stored for {formatBytes(
									r.uncompressedBytes
								)}</span
							>
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	{:else}
		<EmptyState
			icon={HardDrive}
			color="blue"
			title="Not measured yet."
			description="Storage figures appear after the next backup or prune of a repository."
			level={3}
			compact
		/>
	{/if}
</Card>

<style>
	.storage {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.headline {
		margin: 0;
		color: var(--text-default);
	}

	.headline strong {
		color: var(--text-strong);
		font-size: var(--text-section);
		font-weight: var(--weight-semibold);
	}

	.stats {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: var(--space-3);
		margin: 0;
	}

	.stats div {
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		min-width: 0;
	}

	dt {
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	dd {
		margin: var(--space-1) 0 0;
		color: var(--text-strong);
		font-size: var(--text-section);
		font-weight: var(--weight-semibold);
	}

	.repos {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.repos li {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		gap: var(--space-3);
		font-size: var(--text-caption);
	}

	.repos .name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	@media (max-width: 767px) {
		.stats {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
