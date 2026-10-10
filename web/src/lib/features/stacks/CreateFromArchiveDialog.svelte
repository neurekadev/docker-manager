<script lang="ts">
	// Create Stack From Archive (#313) as a dialog over the stack list: the
	// archive Export Archive wrote is uploaded at once when chosen (or
	// dropped), with its progress and Cancel; the manager checks it on the
	// way and keeps it for a day. Then what it holds (stack, export date,
	// volumes and size, services, what the export left out), the
	// environment (where the caller may create stacks, and volumes when it
	// has volumes; hidden with one), the new stack's name (prefilled; a
	// name the Compose file pins must stay) and display name, and Deploy
	// After Creating (with Deploy Stacks there). The check runs whenever
	// the environment or the name change (debounced) and lists problems,
	// warnings and the volumes whose names follow the new name. Create
	// Stack starts the job that fills the new stack; its progress shows
	// here, also after a reload or when the dialog opens again (the running
	// list, docs/internal/web.md "Job progress after reload"). An upload no
	// stack was created from is discarded when the dialog closes.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { onDestroy, untrack } from 'svelte';
	import FileArchive from '@lucide/svelte/icons/file-archive';
	import Upload from '@lucide/svelte/icons/upload';
	import type { Job } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { routes } from '$lib/routes';
	import {
		Button,
		Checkbox,
		Dialog,
		EmptyState,
		ErrorState,
		JobProgress,
		Meter,
		Notice,
		Select,
		Skeleton,
		TextField,
		errorMessage,
		errorView,
		formatBytes,
		formatDateTime,
		formatPercent,
		toast
	} from '$lib/ui';
	import { discardStackArchive, previewArchiveImport, startArchiveImport } from './archive-api';
	import { uploadStackArchive, type ArchiveUpload } from './archive-upload';
	import {
		archiveContents,
		archiveHeadline,
		defaultName,
		exclusionText,
		importBlocker,
		importBody,
		importKey,
		renamedVolumes,
		type ImportChoice,
		type StackArchive,
		type StackArchiveImportPreview
	} from './archives';
	import { jobStackId } from './importing';
	import { count } from './migration';
	import MigrationFindings from './MigrationFindings.svelte';
	import { canInEnvironment, nameError } from './model';
	import { stackKeys, stackQuery } from './queries';

	let {
		open = $bindable(false),
		environmentId: suggested = null
	}: { open?: boolean; environmentId?: string | null } = $props();

	const queryClient = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());

	// The upload: the chosen file, its progress, the archive the manager
	// answered with, and whether a stack was created from it (then it is
	// kept). The transfer in flight is bookkeeping, not rendered.
	let file = $state<{ name: string; size: number } | null>(null);
	let loaded = $state(0);
	let total = $state(0);
	let uploading = $state(false);
	let uploadError = $state<string | null>(null);
	let archive = $state<StackArchive | null>(null);
	let transfer: ArchiveUpload | null = null;
	let used = false;
	let input = $state<HTMLInputElement | null>(null);
	let dragging = $state(false);

	// Where the caller may create stacks (and volumes, when the archive has
	// some).
	const creatable = $derived(
		(envs.data ?? []).filter((e) => canInEnvironment(perms.data, 'stack.create', e.id))
	);
	const allowed = $derived(
		archive?.volumes.length
			? creatable.filter((e) => canInEnvironment(perms.data, 'volume.create', e.id))
			: creatable
	);

	let environmentId = $state('');
	let name = $state('');
	let displayName = $state('');
	let deployAfter = $state(true);
	$effect(() => {
		if (!open) return;
		if (!environmentId || !allowed.some((e) => e.id === environmentId)) {
			const pick = allowed.find((e) => e.id === suggested) ?? allowed[0];
			environmentId = pick?.id ?? '';
		}
	});
	const env = $derived(allowed.find((e) => e.id === environmentId));
	// Deploying the new stack needs Deploy Stacks in its environment (#282).
	const mayDeploy = $derived(
		!!environmentId && canInEnvironment(perms.data, 'stack.deploy', environmentId)
	);
	const choice = $derived<ImportChoice>({
		environmentId,
		name,
		displayName,
		deploy: deployAfter && mayDeploy
	});
	const nameMsg = $derived(name ? nameError(name) : undefined);

	// The check: the choice it was run for, a check in flight and its failure.
	let preview = $state<StackArchiveImportPreview | null>(null);
	let checkedKey = $state<string | null>(null);
	let checking = $state(false);
	let checkError = $state<unknown>(null);
	const key = $derived(archive ? importKey(archive.id, choice) : null);
	const stale = $derived(checkedKey !== null && checkedKey !== key);

	// The job: started here, or running when the dialog opens.
	let starting = $state(false);
	let startError = $state<unknown>(null);
	let jobId = $state<string | null>(null);
	let finished = $state<Job | null>(null);
	let stackId = $state<string | null>(null);
	let jobName = $state('');
	const imports = useTrackedJobs(
		() => (environmentId ? { kinds: ['stack.import_archive'], environmentId } : null),
		{ enabled: () => open }
	);
	// Jobs that ended while shown; not state, nothing renders from it.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	const ended = new Set<string>();
	// A resumed job names its stack by the stack it fills.
	const created = createQuery(() => ({
		...stackQuery(stackId ?? ''),
		enabled: open && !!stackId && !jobName
	}));
	const jobTitle = $derived(
		jobName || created.data?.name
			? `Create ${jobName || created.data?.name} From Archive`
			: 'Create Stack From Archive'
	);

	const blocker = $derived(
		archive && !jobId
			? importBlocker({ environment: env, name, preview, checking, stale })
			: undefined
	);

	// Every opening starts afresh (unless a job of it runs); closing
	// cancels an upload and discards an archive no stack was created from.
	let wasOpen = false;
	$effect(() => {
		const o = open;
		untrack(() => {
			if (o && !wasOpen) reset();
			else if (!o && wasOpen) {
				cancelUpload();
				discard();
				reset();
			}
			wasOpen = o;
		});
	});

	onDestroy(() => {
		cancelUpload();
		discard();
	});

	function reset() {
		file = null;
		uploadError = null;
		archive = null;
		used = false;
		name = displayName = '';
		deployAfter = true;
		preview = null;
		checkedKey = null;
		checkError = null;
		startError = null;
		jobId = null;
		finished = null;
		stackId = null;
		jobName = '';
	}

	function refresh() {
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
	}

	// Upload ------------------------------------------------------------------

	function choose(f: File) {
		uploadError = null;
		file = { name: f.name, size: f.size };
		loaded = 0;
		total = f.size;
		uploading = true;
		const up = uploadStackArchive(f, {
			onprogress: (l, t) => {
				if (transfer !== up) return;
				loaded = l;
				total = t;
			}
		});
		transfer = up;
		up.done
			.then((a) => {
				// Cancelled meanwhile: the manager keeps it, so discard it.
				if (transfer !== up) return void discardStackArchive(a.id).catch(() => undefined);
				archive = a;
				name = defaultName(a);
				displayName = a.displayName ?? '';
			})
			.catch((e: unknown) => {
				if (transfer !== up) return;
				file = null;
				if (!(e as { cancelled?: boolean }).cancelled) uploadError = errorMessage(e);
			})
			.finally(() => {
				if (transfer !== up) return;
				transfer = null;
				uploading = false;
			});
	}

	function cancelUpload() {
		const up = transfer;
		transfer = null;
		uploading = false;
		file = null;
		up?.abort();
	}

	/** Discards the upload (best effort) unless a stack was created from it. */
	function discard() {
		if (archive && !used) void discardStackArchive(archive.id).catch(() => undefined);
	}

	function chooseAnother() {
		discard();
		archive = null;
		file = null;
		preview = null;
		checkedKey = null;
		checkError = null;
		startError = null;
	}

	// Check -------------------------------------------------------------------

	// Only the latest check's answer is shown.
	let seq = 0;
	async function runCheck() {
		if (!archive) return;
		const mine = ++seq;
		const id = archive.id;
		const c = { ...choice };
		checking = true;
		checkError = null;
		try {
			const p = await previewArchiveImport(id, importBody(c));
			if (mine !== seq) return;
			preview = p;
			checkedKey = importKey(id, c);
		} catch (e) {
			if (mine === seq) checkError = e;
		} finally {
			if (mine === seq) checking = false;
		}
	}

	// A new archive, environment or name runs the check (again).
	$effect(() => {
		const k = key;
		if (!open || !k || jobId || !env || nameError(name)) return;
		if (k === checkedKey) return;
		const t = setTimeout(() => void runCheck(), preview ? 600 : 0);
		return () => clearTimeout(t);
	});

	// Create ------------------------------------------------------------------

	async function create() {
		if (!archive || blocker) return;
		const c = { ...choice };
		starting = true;
		startError = null;
		try {
			const job = await startArchiveImport(archive.id, importBody(c));
			used = true;
			jobName = c.name.trim();
			stackId = jobStackId(job) ?? null;
			imports.add(job, `Create ${jobName} From Archive`);
			jobId = job.id;
			finished = null;
			// The new stack is listed at once (it waits for its files).
			refresh();
		} catch (e) {
			if (errorView(e).code === 'stack_archive_blocked') await runCheck();
			startError = e;
		} finally {
			starting = false;
		}
	}

	// A running job (after a reload, or the dialog opened again).
	$effect(() => {
		const job = imports.running[0];
		if (!open || !job) return;
		untrack(() => {
			if (jobId || starting || uploading || archive || ended.has(job.id)) return;
			jobId = job.id;
			finished = null;
			stackId = jobStackId(job) ?? null;
			jobName = '';
			used = true;
		});
	});

	function done(job: Job) {
		ended.add(job.id);
		imports.markFinished(job);
		finished = job;
		refresh();
		if (job.state === 'succeeded') {
			const what = jobName || created.data?.name || 'the stack';
			toast.success(`Created ${what} from the archive`);
		}
	}

	function openStack() {
		if (!stackId) return;
		const id = stackId;
		open = false;
		void goto(routes.stack(id));
	}

	// After a failed job: the upload stays, check again.
	function tryAgain() {
		jobId = null;
		finished = null;
		startError = null;
		used = false;
		checkedKey = null;
		if (archive) void runCheck();
	}
</script>

{#snippet dropzone()}
	<div
		class="drop"
		class:dragging
		ondragover={(e) => {
			e.preventDefault();
			dragging = true;
		}}
		ondragleave={() => (dragging = false)}
		ondrop={(e) => {
			e.preventDefault();
			dragging = false;
			const f = e.dataTransfer?.files?.[0];
			if (f) choose(f);
		}}
	>
		<FileArchive size={28} strokeWidth={1.5} aria-hidden="true" />
		<p class="muted">Choose the archive file, or drop it here.</p>
		<input
			bind:this={input}
			type="file"
			accept=".tar.gz,.tgz,application/gzip"
			class="sr-only"
			tabindex="-1"
			aria-hidden="true"
			onchange={(e) => {
				const f = e.currentTarget.files?.[0];
				e.currentTarget.value = '';
				if (f) choose(f);
			}}
		/>
		<Button icon={Upload} onclick={() => input?.click()}>Choose Archive</Button>
	</div>
{/snippet}

{#snippet progress()}
	<div class="uploading">
		<p class="file">
			<span class="strong">{file?.name}</span>
			<span class="muted num"
				>{formatBytes(loaded)} of {formatBytes(total)} · {formatPercent(
					total ? (loaded / total) * 100 : 0
				)}</span
			>
		</p>
		<Meter
			value={loaded}
			max={total || 1}
			label="Upload of {file?.name}"
			valueText="{formatBytes(loaded)} of {formatBytes(total)}"
			role="progressbar"
			tone="neutral"
			showPercent={false}
		/>
		<div>
			<Button size="sm" variant="ghost" onclick={cancelUpload}>Cancel Upload</Button>
		</div>
	</div>
{/snippet}

{#snippet summary(a: StackArchive)}
	<section class="summary" aria-labelledby="archive-title">
		<div class="summary-head">
			<h3 id="archive-title" class="subsection-title">
				{a.displayName || a.name}
				{#if a.displayName}<span class="muted mono">{a.name}</span>{/if}
			</h3>
			<Button size="sm" variant="ghost" onclick={chooseAnother}>Choose Another</Button>
		</div>
		<dl class="facts">
			<div>
				<dt>Exported</dt>
				<dd>{formatDateTime(a.exportedAt)}</dd>
			</div>
			<div>
				<dt>Contents</dt>
				<dd class="num">{archiveContents(a)}</dd>
			</div>
			<div>
				<dt>Services</dt>
				<dd title={a.services.join(', ')}>{count(a.services.length, 'service')}</dd>
			</div>
		</dl>
		{#if a.notIncluded.length}
			<Disclosure summary="Not Included ({a.notIncluded.length})">
				<ul class="plain" role="list">
					{#each a.notIncluded as x, i (`${x.kind}/${x.name}/${i}`)}
						{@const t = exclusionText(x)}
						<li>{t.what} <span class="mono">{x.name}</span>: {t.reason}</li>
					{/each}
				</ul>
			</Disclosure>
		{/if}
	</section>
{/snippet}

{#snippet fields()}
	<div class="fields">
		{#if allowed.length > 1}
			<Select
				label="Environment"
				bind:value={environmentId}
				placeholder="Choose an environment"
				options={allowed.map((e) => ({
					value: e.id,
					label: e.online ? e.name : `${e.name} (offline)`
				}))}
				error={env && !env.online
					? `${env.name} is offline. Stacks can be created when it is back.`
					: undefined}
			/>
		{:else if env && !env.online}
			<Notice tone="offline" title="{env.name} is offline.">
				Stacks can be created when it is back.
			</Notice>
		{/if}
		<TextField
			label="Name"
			bind:value={name}
			mono
			required
			description="Lower-case letters, digits, dashes and underscores."
			info="Also names its folder, containers and the volumes named after it."
			error={nameMsg}
		/>
		<TextField label="Display Name" bind:value={displayName} optional />
		{#if mayDeploy}
			<Checkbox bind:checked={deployAfter} label="Deploy After Creating" />
		{/if}
	</div>
{/snippet}

{#snippet check(p: StackArchiveImportPreview)}
	{@const head = archiveHeadline(p, 'create')}
	{@const renamed = renamedVolumes(p.volumes)}
	<div class="check" aria-busy={checking}>
		<Notice tone={head.tone} title={head.title} live="none" />
		{#if checking || stale}
			<p class="muted" role="status">Checking again with your changes…</p>
		{/if}
		{#if p.blockers.length}
			<section aria-labelledby="import-blockers">
				<h3 id="import-blockers" class="subsection-title">To Fix Before Creating</h3>
				<MigrationFindings list={p.blockers} tone="danger" />
			</section>
		{/if}
		{#if p.warnings.length}
			<Disclosure
				summary="Show {count(p.warnings.length, 'warning')}"
				open={p.warnings.length <= 2}
			>
				<MigrationFindings list={p.warnings} tone="warn" />
			</Disclosure>
		{/if}
		{#if renamed.length}
			<section aria-labelledby="import-volumes">
				<h3 id="import-volumes" class="subsection-title">Volume Names</h3>
				<ul class="plain" role="list">
					{#each renamed as v (v.key)}
						<li>
							<span class="mono">{v.source}</span> →
							<span class="mono strong">{v.name}</span>
							<span class="muted num">· {formatBytes(v.bytes)}</span>
						</li>
					{/each}
				</ul>
			</section>
		{/if}
	</div>
{/snippet}

<Dialog
	bind:open
	title="Create Stack From Archive"
	description="Upload an archive from Export Archive."
	size="lg"
	dismissible={!uploading && !starting}
>
	<div class="body">
		{#if jobId}
			<JobProgress {jobId} title={jobTitle} onfinish={done} />
			{#if finished && finished.state !== 'succeeded'}
				<p class="muted">
					{archive
						? 'The archive stays uploaded for a day. Fix the cause above, then try again.'
						: 'Fix the cause above, then create the stack from the archive again.'}
				</p>
			{/if}
		{:else if envs.isPending || perms.isPending}
			<div aria-busy="true"><Skeleton lines={4} /></div>
		{:else if envs.isError}
			<ErrorState
				error={envs.error}
				title="The environments could not be loaded."
				onretry={() => envs.refetch()}
				bare
			/>
		{:else if creatable.length === 0}
			<EmptyState
				icon={FileArchive}
				color="blue"
				title="You can't create stacks in any environment."
				description="Ask the owner of this Docker Manager for the permission to create stacks."
				level={3}
				compact
			/>
		{:else if !archive}
			{#if uploading}{@render progress()}{:else}{@render dropzone()}{/if}
			{#if uploadError}
				<Notice tone="danger" title="The archive was not uploaded." live="alert">
					{uploadError}
				</Notice>
			{/if}
		{:else}
			{@render summary(archive)}
			{#if allowed.length === 0}
				<Notice tone="warn" title="You can't create its volumes in any environment.">
					The archive holds volumes. Ask the owner of this Docker Manager for the
					permission to create volumes where you create stacks.
				</Notice>
			{:else}
				{@render fields()}
				{#if preview}
					{@render check(preview)}
				{:else if checking}
					<div aria-busy="true"><Skeleton lines={2} /></div>
				{/if}
				{#if checkError}
					<ErrorState
						error={checkError}
						title="The archive could not be checked."
						onretry={() => void runCheck()}
						retrying={checking}
						bare
						compact
					/>
				{/if}
				{#if startError}
					<ErrorState
						error={startError}
						title="The stack was not created."
						bare
						compact
					/>
				{/if}
			{/if}
		{/if}
	</div>
	{#snippet footer()}
		{#if jobId}
			{#if finished?.state === 'succeeded' && stackId}
				<Button variant="ghost" onclick={() => (open = false)}>Close</Button>
				<Button variant="primary" onclick={openStack}>Open Stack</Button>
			{:else if finished}
				<Button variant="ghost" onclick={() => (open = false)}>Close</Button>
				{#if archive}<Button variant="primary" onclick={tryAgain}>Try Again</Button>{/if}
			{:else}
				<Button variant="ghost" onclick={() => (open = false)}>Close</Button>
			{/if}
		{:else}
			{#if blocker}<p class="blocker" role="status">{blocker}</p>{/if}
			<Button variant="ghost" disabled={starting} onclick={() => (open = false)}
				>Cancel</Button
			>
			{#if archive}
				<Button
					variant="primary"
					loading={starting}
					disabled={!!blocker}
					title={blocker}
					onclick={() => void create()}>Create Stack</Button
				>
			{/if}
		{/if}
	{/snippet}
</Dialog>

<style>
	.body,
	.check,
	.fields,
	.summary,
	.uploading {
		display: grid;
		gap: var(--space-4);
	}

	.uploading {
		gap: var(--space-2);
	}

	.drop {
		display: grid;
		justify-items: center;
		gap: var(--space-3);
		padding: var(--space-6) var(--space-4);
		border: 1px dashed var(--border-strong);
		border-radius: var(--radius-md);
		color: var(--text-muted);
		text-align: center;
	}

	.drop.dragging {
		border-color: var(--accent);
		background: var(--surface-raised);
	}

	.file {
		display: flex;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-2);
		overflow-wrap: anywhere;
	}

	.summary-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}

	.summary-head .subsection-title {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-2);
		margin: 0;
		overflow-wrap: anywhere;
	}

	.subsection-title {
		margin-bottom: var(--space-2);
	}

	.facts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
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

	.plain {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding-left: 18px;
		list-style: disc;
		overflow-wrap: anywhere;
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
