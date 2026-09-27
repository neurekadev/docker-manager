<script lang="ts">
	// Running backups (#10): one entry per backup job with its progress
	// bar and, below it, the file restic reads right now (updated every
	// second while the page polls GET /backup-activity). The server sends
	// the file only to holders of the item's files-read capability; others
	// see the bar and the counts.
	import { Badge, Meter, formatBytes, formatDuration } from '$lib/ui';
	import {
		activityItemName,
		activityPercent,
		middleTruncate,
		type BackupActivity
	} from './model';

	interface Props {
		jobs: BackupActivity[];
		policyName: (id: string | undefined) => string;
		environmentName: (id: string) => string;
		/** Compact: one job inline (a table row or a drawer). */
		compact?: boolean;
	}

	let { jobs, policyName, environmentName, compact = false }: Props = $props();

	const count = (n: number) => n.toLocaleString('en');

	function where(a: BackupActivity): string {
		return a.kind === 'manager.backup'
			? 'Manager state'
			: a.environmentId
				? environmentName(a.environmentId)
				: 'Environment';
	}

	function waiting(a: BackupActivity): string | undefined {
		if (a.state === 'queued') return 'Waiting to start';
		if (a.state === 'blocked') return 'Waiting for another job on the same data';
		if (a.state === 'cancelling') return 'Stopping';
		if (!a.current) return a.message ? a.message : 'Preparing';
		return undefined;
	}
</script>

<ul class="jobs" class:compact role="list">
	{#each jobs as a (a.jobId)}
		{@const pct = activityPercent(a)}
		{@const c = a.current}
		{@const note = waiting(a)}
		<li class="job">
			{#if !compact}
				<div class="head">
					<span class="title">{policyName(a.policyId)}</span>
					<span class="muted">{where(a)}</span>
					{#if a.state === 'cancelling'}<Badge tone="warn" dot>Stopping</Badge>{/if}
					{#if c?.secondsRemaining}
						<span class="eta muted num"
							>About {formatDuration(c.secondsRemaining)} left</span
						>
					{/if}
				</div>
			{/if}
			<div class="facts muted num">
				{#if c}
					<span class="item">{activityItemName(c)}</span>
					{#if a.itemCount > 1}<span>{c.index + 1} of {a.itemCount}</span>{/if}
					{#if c.filesTotal > 0}<span
							>{count(c.filesDone)} of {count(c.filesTotal)} files</span
						>{/if}
					{#if c.bytesTotal > 0}<span
							>{formatBytes(c.bytesDone)} of {formatBytes(c.bytesTotal)}</span
						>{/if}
					{#if compact && c.secondsRemaining}<span
							>About {formatDuration(c.secondsRemaining)} left</span
						>{/if}
				{:else}
					<span>{note}</span>
				{/if}
			</div>
			<Meter
				value={Math.max(0, pct)}
				max={100}
				role="progressbar"
				tone="neutral"
				size={compact ? 'sm' : 'md'}
				label="Backup progress of {policyName(a.policyId)}, {where(a)}"
				valueText={pct >= 0 ? `${pct}%` : 'Starting'}
			/>
			<p class="file mono" title={c?.currentFile}>
				{#if c?.currentFile}{middleTruncate(
						c.currentFile,
						compact ? 56 : 96
					)}{:else if c}<span class="muted">Reading files</span>{:else}<span class="muted"
						>{note}</span
					>{/if}
			</p>
		</li>
	{/each}
</ul>

<style>
	.jobs {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.jobs.compact {
		gap: var(--space-2);
	}

	.job {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-1) var(--space-3);
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
	}

	.eta {
		margin-left: auto;
	}

	.facts {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-3);
		font-size: var(--text-caption);
	}

	.item {
		color: var(--text-default);
	}

	.file {
		margin: 0;
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
		font-size: var(--text-caption);
		color: var(--text-default);
		min-height: 1.4em;
	}
</style>
