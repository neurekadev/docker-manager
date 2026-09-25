<script lang="ts" module>
	export interface FileOperation {
		id: number;
		jobId: string;
		/** "Copy 3 items to config" (progress title). */
		title: string;
		/** "Copied 3 items to config" (success toast). */
		done: string;
		finished: boolean;
		state?: string;
	}
</script>

<script lang="ts">
	// Long file operations (#15): upload transfers with progress and Cancel,
	// and the file jobs (copy, move, delete, archive, extract, permissions)
	// with JobProgress, their per-item results and a Cancel while running.
	import X from '@lucide/svelte/icons/x';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { Button, IconButton, JobProgress, formatBytes, toast, errorMessage } from '$lib/ui';
	import type { UploadQueue } from './uploads.svelte';

	interface Props {
		uploads: UploadQueue;
		operations: FileOperation[];
		onfinish: (op: FileOperation, job: Job) => void;
		ondismiss: (op: FileOperation) => void;
	}

	let { uploads, operations, onfinish, ondismiss }: Props = $props();

	const running = $derived(
		uploads.items.filter((i) => i.state === 'queued' || i.state === 'uploading')
	);
	const failed = $derived(uploads.items.filter((i) => i.state === 'failed'));
	const doneCount = $derived(uploads.items.filter((i) => i.state === 'done').length);

	async function cancelJob(op: FileOperation) {
		try {
			await unwrap(
				api.POST('/api/v1/jobs/{jobId}/cancellations', {
					params: { path: { jobId: op.jobId } }
				})
			);
			toast.info(`Cancelling: ${op.title}`, {
				body: 'Items finished so far stay; the rest is not done.'
			});
		} catch (e) {
			toast.error(`${op.title} could not be cancelled`, { body: errorMessage(e) });
		}
	}
</script>

{#if uploads.items.length || operations.length}
	<section class="ops" aria-label="File operations">
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
							>Cancel uploads</Button
						>
					{:else}
						<IconButton
							icon={X}
							size="sm"
							label="Dismiss uploads"
							onclick={() => uploads.dismiss()}
						/>
					{/if}
				</div>
				<div
					class="bar"
					role="progressbar"
					aria-label="Upload progress"
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
									{Math.round((item.loaded / Math.max(1, item.size)) * 100)}%
								{:else if item.state === 'queued'}Waiting{:else if item.state === 'done'}Uploaded{#if item.storedAs && item.storedAs !== item.name}
										as {item.storedAs}{/if}{:else if item.state === 'skipped'}Skipped{:else if item.state === 'cancelled'}Cancelled{:else}{item.error}{/if}
							</span>
							{#if item.state === 'uploading' || item.state === 'queued'}
								<IconButton
									icon={X}
									size="sm"
									label="Cancel upload of {item.name}"
									onclick={() => uploads.cancel(item.id)}
								/>
							{/if}
						</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#each operations as op (op.id)}
			<div class="block job">
				<JobProgress
					jobId={op.jobId}
					title={op.title}
					variant={op.finished && op.state !== 'succeeded' ? 'panel' : 'inline'}
					onfinish={(job) => onfinish(op, job)}
				/>
				<div class="job-actions">
					{#if !op.finished}
						<Button size="sm" variant="ghost" onclick={() => cancelJob(op)}
							>Cancel</Button
						>
					{:else}
						<IconButton
							icon={X}
							size="sm"
							label="Dismiss {op.title}"
							onclick={() => ondismiss(op)}
						/>
					{/if}
				</div>
			</div>
		{/each}
	</section>
{/if}

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
