<script lang="ts">
	// Backup storage (#10): what the repositories hold, as restic stats
	// measures it after every backup and prune (index and directory
	// metadata only): the data before compression, what is stored, what
	// compression freed, the ratio and the share stored compressed.
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import {
		Card,
		EmptyState,
		Meter,
		formatBytes,
		formatDateTime,
		formatPercent,
		formatRelative
	} from '$lib/ui';
	import { ratioText, type StorageTotals } from './model';

	let { totals }: { totals: StorageTotals | undefined } = $props();
</script>

<Card
	title="Storage"
	subtitle={totals?.measuredAt
		? `Measured after every backup and prune; oldest figure ${formatRelative(totals.measuredAt)}.`
		: 'Measured after every backup and prune.'}
>
	{#if totals}
		<div class="storage">
			<p class="headline">
				<strong class="num">{formatBytes(totals.uncompressedBytes)}</strong> of data across
				<strong class="num">{totals.snapshots.toLocaleString('en')}</strong>
				{totals.snapshots === 1 ? 'snapshot' : 'snapshots'}
			</p>
			<div class="bar">
				<Meter
					value={totals.sizeBytes}
					max={totals.uncompressedBytes}
					label="Stored on disk of the backed-up data"
					valueText="{formatBytes(totals.sizeBytes)} on disk of {formatBytes(
						totals.uncompressedBytes
					)}"
					tone="neutral"
					size="md"
					showPercent={false}
				/>
				<div class="legend muted num">
					<span>On disk {formatBytes(totals.sizeBytes)}</span>
					<span>Total {formatBytes(totals.uncompressedBytes)}</span>
				</div>
			</div>
			<dl class="stats">
				<div>
					<dt>On disk</dt>
					<dd class="num">{formatBytes(totals.sizeBytes)}</dd>
				</div>
				<div>
					<dt>Freed by compression</dt>
					<dd class="num">{formatBytes(totals.freedBytes)}</dd>
				</div>
				<div>
					<dt>Ratio</dt>
					<dd class="num">{ratioText(totals.ratio)}</dd>
				</div>
				<div>
					<dt>Snapshots</dt>
					<dd class="num">{totals.snapshots.toLocaleString('en')}</dd>
				</div>
				<div>
					<dt>Compressed</dt>
					<dd class="num">{formatPercent(totals.compressedPercent)}</dd>
				</div>
			</dl>
			{#if totals.repositories.length > 1}
				<ul class="repos" role="list" aria-label="Storage per repository">
					{#each totals.repositories as r (r.id)}
						<li>
							<span class="name">{r.name}</span>
							<span class="muted num"
								>{formatBytes(r.sizeBytes)} on disk of {formatBytes(
									r.uncompressedBytes
								)}</span
							>
							<span class="num">{ratioText(r.ratio)}</span>
						</li>
					{/each}
				</ul>
			{/if}
			{#if totals.measuredAt}
				<p class="muted note">
					Snapshots are restic's: one per backed-up stack, volume or manager state, plus a
					small manifest per run. Oldest measurement {formatDateTime(totals.measuredAt)}.
				</p>
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

	.bar {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
	}

	.legend {
		display: flex;
		justify-content: space-between;
		font-size: var(--text-caption);
	}

	.stats {
		display: grid;
		grid-template-columns: repeat(5, minmax(0, 1fr));
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
		color: var(--text-muted);
		font-size: var(--text-caption);
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
		grid-template-columns: minmax(0, 1fr) auto 64px;
		gap: var(--space-3);
		font-size: var(--text-caption);
	}

	.repos .name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.repos li > :last-child {
		text-align: right;
	}

	.note {
		margin: 0;
		font-size: var(--text-caption);
	}

	@media (max-width: 767px) {
		.stats {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
