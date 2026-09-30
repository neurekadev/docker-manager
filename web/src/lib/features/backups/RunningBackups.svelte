<script lang="ts">
	// Running backups and retentions (#10), fed by GET /backup-activity
	// (polled every second while one runs). One steady line per job: what
	// it is (policy, where, how many stacks and volumes), the item it is
	// on with its file and byte counts, the progress bar, the time left and
	// Cancel; below it one line with the file restic reads right now, like
	// restic's own output. Every line keeps its height whatever it shows,
	// so nothing moves while jobs progress. A retention has no current
	// item: its line shows the stage (removing backups, freeing space). The
	// server sends the file only to holders of the item's files-read
	// capability; others see the counts. Holders of job.cancel (the
	// server's cancellable) can cancel after a confirmation.
	import Square from '@lucide/svelte/icons/square';
	import { api, unwrap } from '$lib/api/client';
	import {
		Badge,
		Button,
		ConfirmDialog,
		Meter,
		formatBytes,
		formatDuration,
		formatPercent,
		toast
	} from '$lib/ui';
	import {
		activityItemName,
		activityPercent,
		isRetentionActivity,
		middleTruncate,
		sentenceCase,
		type BackupActivity
	} from './model';

	interface Props {
		jobs: BackupActivity[];
		policyName: (id: string | undefined) => string;
		environmentName: (id: string) => string;
		/** Compact: inside a drawer (no policy name, a shorter file line). */
		compact?: boolean;
	}

	let { jobs, policyName, environmentName, compact = false }: Props = $props();

	const count = (n: number) => n.toLocaleString('en');
	const plural = (n: number, one: string, many: string) => `${count(n)} ${n === 1 ? one : many}`;

	function where(a: BackupActivity): string {
		return a.kind.startsWith('manager.')
			? 'Manager state'
			: a.environmentId
				? environmentName(a.environmentId)
				: 'Environment';
	}

	/** What the job is: "Nightly", "Retention of Nightly". */
	function what(a: BackupActivity): string {
		return isRetentionActivity(a)
			? `Retention of ${policyName(a.policyId)}`
			: policyName(a.policyId);
	}

	/** What a backup covers: "12 stacks, 3 volumes". */
	function covers(a: BackupActivity): string {
		if (a.kind !== 'backup.run') return '';
		return [
			a.stacks ? plural(a.stacks, 'stack', 'stacks') : '',
			a.volumes ? plural(a.volumes, 'volume', 'volumes') : ''
		]
			.filter(Boolean)
			.join(', ');
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
			return ['It has not started yet: nothing changes.'];
		if (isRetentionActivity(a))
			return [
				'Backups it already removed stay removed.',
				'The space it has not freed yet is freed by the next retention; the repository stays usable.'
			];
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
		toast.info(`Cancelling ${what(a)}`, { body: where(a) });
	}

	/** What a job without a current item is doing. */
	function note(a: BackupActivity): string {
		if (stopping(a)) return 'Finishing the current step';
		if (a.state === 'queued') return 'Waiting to start';
		if (a.state === 'blocked') return 'Waiting for another job on the same data';
		if (a.message) return sentenceCase(a.message);
		return isRetentionActivity(a) ? 'Starting' : 'Preparing';
	}
</script>

<ul class="jobs" class:compact role="list">
	{#each jobs as a (a.jobId)}
		{@const pct = activityPercent(a)}
		{@const c = a.current}
		{@const cancellable = a.cancellable && !stopping(a)}
		{@const retention = isRetentionActivity(a)}
		<li class="job">
			<div class="line">
				<span class="who">
					{#if !compact}<span class="title">{what(a)}</span>{/if}
					<span class="muted">{where(a)}</span>
					{#if covers(a)}<span class="muted small">{covers(a)}</span>{/if}
				</span>
				<span class="facts muted small num">
					{#if c}
						<span class="item">{activityItemName(c)}</span>
						{#if a.itemCount > 1}<span>{c.index + 1} of {a.itemCount}</span>{/if}
						{#if c.filesTotal > 0}<span
								>{count(c.filesDone)} of {count(c.filesTotal)} files</span
							>{/if}
						{#if c.bytesTotal > 0}<span
								>{formatBytes(c.bytesDone)} of {formatBytes(c.bytesTotal)}</span
							>{/if}
					{:else}
						<span>{note(a)}</span>
					{/if}
				</span>
				<span class="bar">
					<Meter
						value={Math.max(0, pct)}
						max={100}
						role="progressbar"
						tone="neutral"
						size="sm"
						label="{retention ? 'Retention' : 'Backup'} progress of {policyName(
							a.policyId
						)}, {where(a)}"
						valueText={pct >= 0 ? formatPercent(pct) : 'Starting'}
					/>
				</span>
				<span class="end muted small num">
					{#if stopping(a)}<Badge tone="warn" dot>Stopping</Badge>
					{:else if c?.secondsRemaining}<span
							>About {formatDuration(c.secondsRemaining)} left</span
						>{:else if pct >= 0}<span>{formatPercent(pct)}</span>{/if}
					{#if cancellable}
						<Button
							variant="ghost"
							size="sm"
							icon={Square}
							aria-label="Cancel {policyName(a.policyId)}, {where(a)}"
							onclick={() => askCancel(a)}>Cancel</Button
						>
					{/if}
				</span>
			</div>
			<p class="file mono" title={c?.currentFile}>
				{#if c?.currentFile && !stopping(a)}{middleTruncate(
						c.currentFile,
						compact ? 64 : 120
					)}{:else if c && !stopping(a)}<span class="muted">Reading files</span
					>{:else}&nbsp;{/if}
			</p>
		</li>
	{/each}
</ul>

{#if confirming}
	{@const retention = isRetentionActivity(confirming)}
	<ConfirmDialog
		bind:open={confirmOpen}
		title="Cancel this {retention ? 'retention' : 'backup'}?"
		message="{what(confirming)} · {where(confirming)}"
		consequences={consequences(confirming)}
		confirmLabel="Cancel {retention ? 'retention' : 'backup'}"
		cancelLabel={retention ? 'Keep running' : 'Keep backing up'}
		tone="danger"
		onconfirm={cancel}
	/>
{/if}

<style>
	.jobs {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.job {
		display: grid;
		gap: 2px;
		min-width: 0;
	}

	.job + .job {
		padding-top: var(--space-2);
		border-top: 1px solid var(--border-subtle);
	}

	/* One row: who · what it is on · bar · time and Cancel, at a fixed
	   height so changing text never moves what is below. */
	.line {
		display: grid;
		grid-template-columns: minmax(140px, 1.2fr) minmax(0, 2fr) minmax(120px, 1fr) auto;
		align-items: center;
		gap: var(--space-3);
		min-height: 30px;
	}

	.who,
	.facts {
		display: flex;
		align-items: baseline;
		gap: var(--space-2);
		min-width: 0;
		overflow: hidden;
		white-space: nowrap;
	}

	.who > *,
	.facts > * {
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
	}

	.item {
		color: var(--text-default);
	}

	.end {
		display: inline-flex;
		align-items: center;
		justify-content: flex-end;
		gap: var(--space-2);
		min-width: 88px;
		white-space: nowrap;
	}

	.file {
		margin: 0;
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
		font-size: var(--text-caption);
		color: var(--text-default);
		line-height: 1.4;
		min-height: 1.4em;
	}

	.small {
		font-size: var(--text-caption);
	}

	.compact .line {
		grid-template-columns: minmax(80px, 0.8fr) minmax(0, 2fr) minmax(100px, 1fr) auto;
	}

	@media (max-width: 767px) {
		.line,
		.compact .line {
			grid-template-columns: minmax(0, 1fr) auto;
		}

		.facts,
		.bar {
			grid-column: 1 / -1;
		}
	}
</style>
