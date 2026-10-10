<script lang="ts">
	// Export Archive (#313) as a dialog over the stack's page: the check
	// runs at once (nothing stops during it) and again when the volumes to
	// include change (debounced): the running services that stop while the
	// archive is written and the downtime, the data size against the
	// archive limit, the named volumes with an Include tick (left out from
	// the start when the user may not download their files; external, not
	// created and other volumes cannot be included and say why), what is
	// not included at all (anonymous volumes, bind mounts outside the
	// project folder), problems and warnings. The newest archive of the
	// stack still on the manager can be downloaded again. The export's
	// progress shows here, also after a reload or when the dialog opens
	// again (the running list, docs/internal/web.md "Job progress after
	// reload"); it ends with the download starting by itself (unless the
	// user may not download one of its volumes: then the manager names no
	// archive and none downloads). Closed while
	// the export runs, the stack's job tray shows it on, and its success
	// toast offers the download. Only the caller's own running export is
	// shown here, never another user's.
	import { onDestroy, untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Download from '@lucide/svelte/icons/download';
	import type { Job } from '$lib/api/client';
	import { sessionQuery } from '$lib/api/queries';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { criticalWork } from '$lib/live';
	import {
		Button,
		Checkbox,
		Dialog,
		ErrorState,
		JobProgress,
		Notice,
		Skeleton,
		Table,
		errorView,
		formatBytes,
		toast,
		type Column
	} from '$lib/ui';
	import { stackJobCopy } from './adopt';
	import {
		downloadStackExport,
		exportTrayEntry,
		previewStackExport,
		startStackExport
	} from './archive-api';
	import {
		archiveFileText,
		archiveHeadline,
		exclusionText,
		exportBlocker,
		exportBody,
		exportDowntime,
		exportKey,
		includeChoice,
		limitRelevant,
		notPermittedKeys,
		ownJob,
		type StackArchiveVolume,
		type StackExportFile,
		type StackExportPreview
	} from './archives';
	import { count, toggled } from './migration';
	import MigrationFindings from './MigrationFindings.svelte';
	import { stackTitle } from './model';
	import { stackKeys, type Stack } from './queries';
	import type { JobTray } from './tray.svelte';

	interface Props {
		open?: boolean;
		stack: Stack;
		tray: JobTray;
	}

	let { open = $bindable(false), stack, tray }: Props = $props();
	const queryClient = useQueryClient();
	const session = createQuery(() => sessionQuery());
	const title = $derived(stackTitle(stack));
	const stackId = $derived(stack.id);

	let preview = $state<StackExportPreview | null>(null);
	let excluded = $state<string[]>([]);
	// The selection the check on screen was run for, a check in flight and
	// the last check's failure.
	let checkedKey = $state<string | null>(null);
	let checking = $state(false);
	let checkError = $state<unknown>(null);
	let starting = $state(false);
	let startError = $state<unknown>(null);
	let jobId = $state<string | null>(null);
	let finished = $state<Job | null>(null);
	// The archive this dialog's export wrote (once the check after it ran).
	let written = $state<StackExportFile | null>(null);
	// The check after the export named no archive: the user may not
	// download one of its volumes.
	let missing = $state(false);

	// The stack's running export: the dialog shows its progress.
	const exports = useTrackedJobs(
		() => ({ targets: [{ type: 'stack', id: stackId }], kinds: ['stack.export'] }),
		{ enabled: () => open }
	);
	// Exports that ended while shown (a later list still listing one does
	// not bring it back); not state, nothing renders from it.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	const ended = new Set<string>();
	let release: (() => void) | null = null;

	const key = $derived(exportKey(excluded));
	const changed = $derived(checkedKey !== null && checkedKey !== key);

	// Only the latest check's answer is shown.
	let seq = 0;
	async function runCheck(): Promise<StackExportPreview | null> {
		const mine = ++seq;
		const sel = [...excluded];
		checking = true;
		checkError = null;
		try {
			const p = await previewStackExport(stackId, exportBody(sel));
			if (mine !== seq) return null;
			preview = p;
			checkedKey = exportKey(sel);
			return p;
		} catch (e) {
			if (mine === seq) checkError = e;
			return null;
		} finally {
			if (mine === seq) checking = false;
		}
	}

	// The first check: the volumes the user may not download are left out
	// at once, and checked again without them.
	async function firstCheck() {
		const p = await runCheck();
		const denied = p ? notPermittedKeys(p.volumes).filter((k) => !excluded.includes(k)) : [];
		if (denied.length) {
			excluded = [...excluded, ...denied];
			await runCheck();
		}
	}

	// Every opening starts with a fresh check, unless an export runs.
	let wasOpen = false;
	$effect(() => {
		const o = open;
		untrack(() => {
			if (o && !wasOpen) opened();
			else if (!o && wasOpen) closed();
			wasOpen = o;
		});
	});

	function opened() {
		if (jobId) return;
		reset();
		void firstCheck();
	}

	function reset() {
		preview = null;
		excluded = [];
		checkedKey = null;
		checkError = null;
		startError = null;
		finished = null;
		written = null;
		missing = false;
	}

	// Closed while the export runs: the stack's job tray shows it on.
	function closed() {
		seq++;
		checking = false;
		handBack();
		jobId = null;
		finished = null;
	}

	function handBack() {
		release?.();
		release = null;
		if (jobId && !finished) tray.add({ id: jobId }, exportTrayEntry(stackId, title));
	}

	onDestroy(handBack);

	// A running export (started here, elsewhere, or before a reload).
	$effect(() => {
		const job = ownJob(exports.running, session.data?.user?.id, ended);
		if (!open || !job) return;
		untrack(() => {
			if (!jobId && !starting) follow(job.id);
		});
	});

	/** Shows the export here and not in the stack's job tray as well. */
	function follow(id: string) {
		jobId = id;
		finished = null;
		release?.();
		release = criticalWork.register('other', `Export of ${title}`);
		tray.add(
			{ id },
			{ kind: 'stack.export', ...stackJobCopy('stack.export', title), silent: true }
		);
		tray.dismiss(id);
	}

	// A change of the volumes to include runs the check again.
	$effect(() => {
		const k = key;
		if (!open || !preview || jobId) return;
		if (k === checkedKey) return;
		const t = setTimeout(() => void runCheck(), 600);
		return () => clearTimeout(t);
	});

	async function start() {
		if (!preview?.allowed || checking || changed) return;
		starting = true;
		startError = null;
		try {
			const job = await startStackExport(stackId, exportBody(excluded));
			exports.add(job, `Export ${title} as an Archive`);
			follow(job.id);
		} catch (e) {
			if (errorView(e).code === 'stack_archive_blocked') await runCheck();
			startError = e;
		} finally {
			starting = false;
		}
	}

	async function done(job: Job) {
		ended.add(job.id);
		finished = job;
		exports.markFinished(job);
		release?.();
		release = null;
		// The stack stopped and started again.
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
		if (job.state === 'succeeded') void lookUp(job.id, true);
	}

	/**
	 * The archive an export wrote, from a check after it (the newest one);
	 * `auto` downloads it at once while the dialog is open.
	 */
	async function lookUp(exportId: string, auto: boolean) {
		const p = await runCheck();
		if (!p) return;
		const file = p.latest?.exportId === exportId ? p.latest : null;
		written = file;
		missing = !file;
		if (!auto || !file || !open) return;
		downloadStackExport(stackId, file.exportId);
		toast.info(`Downloading ${file.fileName}`);
	}

	function download(f: StackExportFile) {
		downloadStackExport(stackId, f.exportId);
		toast.info(`Downloading ${f.fileName}`);
	}

	// After a failed export: check again and start over.
	function tryAgain() {
		jobId = null;
		finished = null;
		startError = null;
		written = null;
		missing = false;
		void runCheck();
	}

	const blocker = $derived(
		exportBlocker({ preview, checking, stale: changed, failed: !!checkError })
	);

	// The footer's Check Again after a failed check.
	function checkAgain() {
		void (preview ? runCheck() : firstCheck());
	}

	const columns: Column<StackArchiveVolume>[] = [
		{ id: 'name', header: 'Volume', cell: volName, stack: 'title', title: (v) => v.name },
		{ id: 'size', header: 'Size', cell: volSize, numeric: true, width: '110px' },
		{ id: 'include', header: 'Include', cell: volInclude, width: '90px', stack: 'actions' }
	];
</script>

{#snippet volName(v: StackArchiveVolume)}
	{@const c = includeChoice(v, excluded)}
	<span class="vol">
		<span class="strong">{v.key}</span>
		{#if c.reason}<span class="muted reason">{c.reason}</span>{/if}
	</span>
{/snippet}
{#snippet volSize(v: StackArchiveVolume)}{formatBytes(v.bytes)}{v.truncated ? '+' : ''}{/snippet}
{#snippet volInclude(v: StackArchiveVolume)}
	{@const c = includeChoice(v, excluded)}
	<Checkbox
		label="Include the Data of Volume {v.key}"
		hideLabel
		checked={c.checked}
		disabled={c.disabled}
		title={c.reason}
		onchange={(e) =>
			(excluded = toggled(excluded, v.key, !(e.currentTarget as HTMLInputElement).checked))}
	/>
{/snippet}

{#snippet latestNotice(f: StackExportFile, heading: string)}
	<Notice tone="info" title={heading} live="none">
		{archiveFileText(f)}
		{#snippet actions()}
			<Button size="sm" icon={Download} onclick={() => download(f)}>Download Archive</Button>
		{/snippet}
	</Notice>
{/snippet}

<Dialog
	bind:open
	title="Export {title} as an Archive"
	description="Its project folder and the data of its volumes in one file."
	size="lg"
	dismissible={!starting}
>
	<div class="body">
		{#if jobId}
			<JobProgress
				{jobId}
				title="Export {title} as an Archive"
				timing
				onfinish={(j) => void done(j)}
			/>
			{#if finished?.state === 'succeeded'}
				{#if written}
					{@render latestNotice(written, `Exported ${title} as an archive.`)}
				{:else if checking}
					<div aria-busy="true"><Skeleton lines={1} /></div>
				{:else if missing}
					<Notice tone="info" title="Exported {title} as an archive." live="none">
						You can't download this archive's volumes.
					</Notice>
				{:else if checkError}
					<ErrorState
						error={checkError}
						title="The archive could not be looked up."
						onretry={() => finished && void lookUp(finished.id, false)}
						bare
						compact
					/>
				{/if}
			{:else if finished}
				<p class="muted">
					{title} starts again on its own. Fix the cause above, then try again.
				</p>
			{/if}
		{:else if !preview && checking}
			<div aria-busy="true"><Skeleton lines={5} /></div>
		{:else if !preview && checkError}
			<ErrorState error={checkError} title="The export could not be checked." bare />
		{:else if preview}
			{@const head = archiveHeadline(preview, 'export')}
			{@const downtime = exportDowntime(title, preview)}
			<div class="check" aria-busy={checking}>
				<Notice tone={head.tone} title={head.title} live="none" />
				{#if checking}
					<p class="muted" role="status">Checking again with your changes…</p>
				{/if}
				{#if checkError}
					<ErrorState
						error={checkError}
						title="The check could not run again with your changes."
						bare
						compact
					/>
				{/if}

				{#if preview.blockers.length}
					<section aria-labelledby="export-blockers">
						<h3 id="export-blockers" class="subsection-title">
							To Fix Before Exporting
						</h3>
						<MigrationFindings list={preview.blockers} tone="danger" />
					</section>
				{/if}

				{#if downtime}
					<Notice tone="warn" title={downtime.title} live="none">{downtime.body}</Notice>
				{/if}

				<dl class="facts">
					<div>
						<dt>Data</dt>
						<dd class="num">
							{formatBytes(preview.totalBytes)}{preview.truncated ? ' or more' : ''}
						</dd>
					</div>
					{#if limitRelevant(preview)}
						<div>
							<dt>Archive Limit</dt>
							<dd class="num">{formatBytes(preview.maxBytes)}</dd>
						</div>
					{/if}
				</dl>

				{#if preview.volumes.length}
					<div class="table">
						<Table
							label="Volumes of {title}"
							rows={preview.volumes}
							{columns}
							rowKey={(v) => v.key}
						/>
					</div>
				{/if}

				{#if preview.notIncluded.length}
					<Disclosure summary="Not Included ({preview.notIncluded.length})">
						<ul class="plain" role="list">
							{#each preview.notIncluded as x, i (`${x.kind}/${x.name}/${i}`)}
								{@const t = exclusionText(x)}
								<li>{t.what} <span class="mono">{x.name}</span>: {t.reason}</li>
							{/each}
						</ul>
					</Disclosure>
				{/if}

				{#if preview.warnings.length}
					<Disclosure
						summary="Show {count(preview.warnings.length, 'warning')}"
						open={preview.warnings.length <= 2}
					>
						<MigrationFindings list={preview.warnings} tone="warn" />
					</Disclosure>
				{/if}

				{#if preview.latest}
					{@render latestNotice(
						preview.latest,
						`Latest archive: ${preview.latest.fileName}`
					)}
				{/if}

				{#if startError}
					<ErrorState error={startError} title="The export did not start." bare compact />
				{/if}
			</div>
		{/if}
	</div>
	{#snippet footer()}
		{#if jobId}
			{#if finished && finished.state !== 'succeeded'}
				<Button onclick={tryAgain}>Try Again</Button>
			{/if}
			<Button variant={finished ? 'primary' : 'ghost'} onclick={() => (open = false)}
				>Close</Button
			>
		{:else}
			{#if blocker}<p class="blocker" role="status">{blocker}</p>{/if}
			<Button variant="ghost" disabled={starting} onclick={() => (open = false)}
				>Cancel</Button
			>
			{#if checkError && !checking}
				<Button onclick={checkAgain}>Check Again</Button>
			{/if}
			<Button
				variant="primary"
				icon={Download}
				loading={starting}
				disabled={!!blocker}
				title={blocker}
				onclick={() => void start()}>Export Archive</Button
			>
		{/if}
	{/snippet}
</Dialog>

<style>
	.body,
	.check {
		display: grid;
		gap: var(--space-4);
	}

	.subsection-title {
		margin-bottom: var(--space-2);
	}

	.facts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
		gap: var(--space-3);
		margin: 0;
	}

	.facts div {
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	dd {
		margin: 2px 0 0;
		color: var(--text-strong);
	}

	.table {
		overflow: hidden;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.vol {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.reason {
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		white-space: normal;
	}

	.plain {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding-left: 18px;
		list-style: disc;
	}

	.strong {
		color: var(--text-strong);
	}

	.blocker {
		flex: 1 1 auto;
		min-width: 0;
		align-self: center;
		margin-right: auto;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}
</style>
