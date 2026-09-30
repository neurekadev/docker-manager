<script lang="ts">
	// Running backups (#10): one entry per backup job with its progress
	// bar and, below it, the file restic reads right now (updated every
	// second while the page polls GET /backup-activity). The server sends
	// the file only to holders of the item's files-read capability; others
	// see the bar and the counts. Holders of job.cancel (the server's
	// cancellable) can cancel a backup after a confirmation.
	import Square from '@lucide/svelte/icons/square';
	import { api, unwrap } from '$lib/api/client';
	import {
		Badge,
		Button,
		ConfirmDialog,
		Meter,
		formatBytes,
		formatDuration,
		toast
	} from '$lib/ui';
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

	// Jobs cancelled here: stopping at once, before the next poll says so.
	let requested = $state<string[]>([]);
	let confirmOpen = $state(false);
	let confirming = $state<BackupActivity>();

	const stopping = (a: BackupActivity) => a.state === 'cancelling' || requested.includes(a.jobId);

	function askCancel(a: BackupActivity) {
		confirming = a;
		confirmOpen = true;
	}

	function consequences(a: BackupActivity): string[] {
		if (a.state === 'queued' || a.state === 'blocked')
			return ['It has not started yet: nothing is backed up in this run.'];
		if (a.kind === 'manager.backup') return ['The manager state is not saved in this run.'];
		return [
			'What it backs up right now is not saved in this run.',
			'Everything it backed up before stays and can be restored.',
			'Containers stopped for the backup start again.'
		];
	}

	async function cancel() {
		const a = confirming;
		if (!a) return;
		await unwrap(
			api.POST('/api/v1/jobs/{jobId}/cancellations', {
				params: { path: { jobId: a.jobId } }
			})
		);
		requested = [...requested, a.jobId];
		toast.info(`Cancelling ${policyName(a.policyId)}`, { body: where(a) });
	}

	function waiting(a: BackupActivity): string | undefined {
		if (stopping(a)) return 'Stopping';
		if (a.state === 'queued') return 'Waiting to start';
		if (a.state === 'blocked') return 'Waiting for another job on the same data';
		if (!a.current) return a.message ? a.message : 'Preparing';
		return undefined;
	}
</script>

<ul class="jobs" class:compact role="list">
	{#each jobs as a (a.jobId)}
		{@const pct = activityPercent(a)}
		{@const c = a.current}
		{@const note = waiting(a)}
		{@const cancellable = a.cancellable && !stopping(a)}
		<li class="job">
			{#if !compact}
				<div class="head">
					<span class="title">{policyName(a.policyId)}</span>
					<span class="muted">{where(a)}</span>
					{#if stopping(a)}<Badge tone="warn" dot>Stopping</Badge>{/if}
					<span class="end">
						{#if c?.secondsRemaining}
							<span class="muted num"
								>About {formatDuration(c.secondsRemaining)} left</span
							>
						{/if}
						{#if cancellable}{@render cancelButton(a)}{/if}
					</span>
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
				{#if compact && cancellable}<span class="end">{@render cancelButton(a)}</span>{/if}
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

{#snippet cancelButton(a: BackupActivity)}
	<Button
		variant="ghost"
		size="sm"
		icon={Square}
		aria-label="Cancel {policyName(a.policyId)}, {where(a)}"
		onclick={() => askCancel(a)}>Cancel</Button
	>
{/snippet}

{#if confirming}
	<ConfirmDialog
		bind:open={confirmOpen}
		title="Cancel this backup?"
		message="{policyName(confirming.policyId)} · {where(confirming)}"
		consequences={consequences(confirming)}
		confirmLabel="Cancel backup"
		cancelLabel="Keep backing up"
		tone="danger"
		onconfirm={cancel}
	/>
{/if}

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

	.end {
		display: inline-flex;
		align-items: center;
		gap: var(--space-3);
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
