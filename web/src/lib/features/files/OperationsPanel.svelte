<script lang="ts">
	// Long file operations (#15): upload transfers with progress and Cancel,
	// and the file jobs (copy, move, delete, archive, extract, permissions)
	// with JobProgress, their per-item results and a Cancel while running.
	// The jobs come from the root's tracked jobs (docs/internal/web.md, "Job
	// progress after reload"): the ones started here at once, and every
	// running file job of the root from the running list, so they show again
	// after a reload or when the user comes back. At most MAX_JOB_STREAMS
	// running jobs follow their own stream; the others are compact rows fed
	// by the list.
	import X from '@lucide/svelte/icons/x';
	import { api, unwrap, type Job } from '$lib/api/client';
	import {
		Button,
		ConfirmDialog,
		IconButton,
		JobProgress,
		formatBytes,
		formatPercent,
		toast,
		errorMessage
	} from '$lib/ui';
	import { streamedIds, type TrackedEntry } from '$lib/features/jobs/active';
	import JobRow from '$lib/features/jobs/JobRow.svelte';
	import type { TrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import type { UploadQueue } from './uploads.svelte';

	interface Props {
		uploads: UploadQueue;
		/** The root's file jobs (useTrackedJobs with fileJobMatch). */
		jobs: TrackedJobs;
		/** The title of a job not started here (found in the running list). */
		titleOf: (job: Job) => string;
		/** Called once per job when it ends (with the title shown). */
		onfinish: (job: Job, title: string) => void;
	}

	let { uploads, jobs, titleOf, onfinish }: Props = $props();

	const running = $derived(
		uploads.items.filter((i) => i.state === 'queued' || i.state === 'uploading')
	);
	const failed = $derived(uploads.items.filter((i) => i.state === 'failed'));
	const doneCount = $derived(uploads.items.filter((i) => i.state === 'done').length);
	const entries = $derived(jobs.entries);
	const streamed = $derived(streamedIds(entries));

	function titleFor(e: TrackedEntry): string {
		return e.title || (e.job ? titleOf(e.job) : 'File Operation');
	}

	function finished(e: TrackedEntry, job: Job) {
		jobs.markFinished(job);
		onfinish(job, titleFor(e));
	}

	// Cancel asks first (#282): what is done stays, the rest is not done.
	let cancelling = $state<TrackedEntry | null>(null);
	let cancelOpen = $state(false);

	/**
	 * Whether the caller may cancel the job: the server says so on the job
	 * (job.cancel or the kind's own capability); a job started here before
	 * it is loaded was started with the kind's capability.
	 */
	const cancellable = (e: TrackedEntry) => e.job?.cancellable ?? true;

	async function cancelJob(e: TrackedEntry) {
		const title = titleFor(e);
		try {
			await unwrap(
				api.POST('/api/v1/jobs/{jobId}/cancellations', {
					params: { path: { jobId: e.id } }
				})
			);
			toast.info(`Cancelling: ${title}`, {
				body: 'Items finished so far stay; the rest is not done.'
			});
		} catch (err) {
			toast.error(`${title} could not be cancelled`, { body: errorMessage(err) });
		}
	}
</script>

{#if uploads.items.length || entries.length}
	<section class="ops" aria-label="File Operations">
		{#if uploads.items.length}
			<div class="block">
				<div class="head">
					<p class="title">
						{#if running.length}
							Uploading {doneCount + 1 > uploads.items.length
								? uploads.items.length
								: doneCount + 1} of
							{uploads.items.length}
							{uploads.items.length === 1 ? 'file' : 'files'}
						{:else if failed.length}
							{failed.length} of {uploads.items.length} uploads failed
						{:else}
							Uploaded {doneCount} {doneCount === 1 ? 'file' : 'files'}
						{/if}
						<span class="muted num">
							{formatBytes(uploads.loadedBytes)} of {formatBytes(
								uploads.totalBytes
							)}</span
						>
					</p>
					{#if running.length}
						<Button size="sm" variant="ghost" onclick={() => uploads.cancelAll()}
							>Cancel Uploads</Button
						>
					{:else}
						<IconButton
							icon={X}
							size="sm"
							label="Dismiss Uploads"
							onclick={() => uploads.dismiss()}
						/>
					{/if}
				</div>
				<div
					class="bar"
					role="progressbar"
					aria-label="Upload Progress"
					aria-valuemin={0}
					aria-valuemax={100}
					aria-valuenow={uploads.totalBytes
						? Math.round((uploads.loadedBytes / uploads.totalBytes) * 100)
						: 0}
				>
					<span
						class="fill"
						style="width: {uploads.totalBytes
							? (uploads.loadedBytes / uploads.totalBytes) * 100
							: 0}%"
					></span>
				</div>
				<ul class="files" role="list" aria-label="Uploads">
					{#each uploads.items.filter((i) => i.state !== 'done' || running.length) as item (item.id)}
						<li class={item.state}>
							<span class="mono name"
								>{item.dir === '.' ? '' : `${item.dir}/`}{item.name}</span
							>
							<span class="state">
								{#if item.state === 'uploading'}
									{formatPercent((item.loaded / Math.max(1, item.size)) * 100)}
								{:else if item.state === 'queued'}Waiting{:else if item.state === 'done'}Uploaded{#if item.storedAs && item.storedAs !== item.name}
										as {item.storedAs}{/if}{:else if item.state === 'skipped'}Skipped{:else if item.state === 'cancelled'}Cancelled{:else}{item.error}{/if}
							</span>
							{#if item.state === 'uploading' || item.state === 'queued'}
								<IconButton
									icon={X}
									size="sm"
									label="Cancel Upload of {item.name}"
									onclick={() => uploads.cancel(item.id)}
								/>
							{/if}
						</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#each entries as e (e.id)}
			{@const title = titleFor(e)}
			<div class="block job">
				{#if streamed.has(e.id) || !e.job}
					<JobProgress
						jobId={e.id}
						{title}
						variant={!e.active && e.job && e.job.state !== 'succeeded'
							? 'panel'
							: 'inline'}
						onfinish={(job) => finished(e, job)}
					/>
				{:else}
					<JobRow job={e.job} {title} />
				{/if}
				<div class="job-actions">
					{#if e.active}
						{#if cancellable(e)}
							<Button
								size="sm"
								variant="ghost"
								onclick={() => {
									cancelling = e;
									cancelOpen = true;
								}}>Cancel</Button
							>
						{/if}
					{:else}
						<IconButton
							icon={X}
							size="sm"
							label="Dismiss {title}"
							onclick={() => jobs.dismiss(e.id)}
						/>
					{/if}
				</div>
			</div>
		{/each}
	</section>
{/if}

<ConfirmDialog
	bind:open={cancelOpen}
	title="Cancel {cancelling ? titleFor(cancelling) : 'the operation'}?"
	consequences={['Items finished so far stay; the rest is not done.']}
	confirmLabel="Cancel Operation"
	cancelLabel="Keep Running"
	tone="danger"
	onconfirm={() => (cancelling ? cancelJob(cancelling) : undefined)}
/>

<style>
	.ops {
		display: grid;
		gap: var(--space-2);
		max-height: 40%;
		overflow-y: auto;
		padding: var(--space-2) var(--space-3);
		border-top: 1px solid var(--border-subtle);
		background: var(--surface-panel);
	}

	.block {
		display: grid;
		gap: var(--space-2);
	}

	.job {
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: start;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.title .muted {
		margin-left: var(--space-2);
		color: var(--text-muted);
		font-weight: var(--weight-regular);
	}

	.bar {
		position: relative;
		height: 4px;
		overflow: hidden;
		border-radius: var(--radius-full);
		background: var(--surface-raised);
	}

	.fill {
		position: absolute;
		inset: 0 auto 0 0;
		background: var(--accent);
		transition: width var(--duration-open) var(--ease-out);
	}

	.files {
		display: grid;
		gap: 2px;
		max-height: 120px;
		overflow-y: auto;
		font-size: var(--text-caption);
	}

	.files li {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto auto;
		align-items: center;
		gap: var(--space-2);
		min-height: 26px;
	}

	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: 12px;
	}

	.state {
		color: var(--text-muted);
	}

	.failed .state {
		color: var(--danger);
	}

	.job-actions {
		display: flex;
		align-items: center;
	}
</style>
