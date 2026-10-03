<script lang="ts">
	// Restore wizard (#10): choose the scope (stack definition and files,
	// volumes, or one file; system and manager restores go through a fresh
	// Docker Manager), preview exactly what is written and which containers stop,
	// confirm, then follow the restore to a success, partial or failure
	// result. A stack restore never redeploys: it offers the deploy. A
	// restore of what the backup holds that is still running (a reload,
	// coming back, another tab) opens on its progress (docs/internal/web.md,
	// "Job progress after reload").
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import History from '@lucide/svelte/icons/history';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { isTerminal } from '$lib/api/job-states';
	import { environmentsQuery } from '$lib/api/queries';
	import { useCriticalWork } from '$lib/features/common/unsaved.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		Checkbox,
		DeniedState,
		JobProgress,
		Notice,
		PageHeader,
		RadioGroup,
		Skeleton,
		StepWizard,
		Switch,
		TextField,
		TypeToConfirm,
		formatDateTime,
		toast
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	import { environmentName, newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Fields from '$lib/features/common/Fields.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import FilePicker from '$lib/features/common/FilePicker.svelte';
	import RestorePreviewView from '$lib/features/backups/RestorePreviewView.svelte';
	import {
		itemName,
		restoreTargetName,
		type BackupDetail,
		type RestorePreview
	} from '$lib/features/backups/model';
	import { restoreMatch } from '$lib/features/backups/jobs';
	import { backupPlaces, backupSource, PICKER_LIMIT } from '$lib/features/backups/picker';
	import { backupQuery } from '$lib/features/backups/queries';
	import { toggleVolume, volumeChoiceError } from '$lib/features/backups/restore';

	const id = $derived(page.params.backupId ?? '');
	const qc = useQueryClient();
	const backup = createQuery(() => ({ ...backupQuery(id), refetchOnWindowFocus: false }));
	const envs = createQuery(() => environmentsQuery());

	usePage(() => ({
		title: 'Restore',
		crumbs: [
			{ label: 'Backups', href: routes.backups() },
			{ label: 'All Backups', href: routes.backupList() },
			{ label: backup.data ? itemName(backup.data) : 'Backup', href: routes.backup(id) },
			{ label: 'Restore' }
		]
	}));

	let current = $state(0);
	let scope = $state<'stack' | 'volume' | 'file'>('stack');
	let volumes = $state<string[]>([]);
	let filePath = $state('');
	let choosing = $state(false);
	let shutdown = $state(true);
	let preview = $state<RestorePreview | null>(null);
	let confirmText = $state('');
	// The restore this page follows (the job itself lives in `restores`)
	// and whether it started it.
	let followId = $state<string | null>(null);
	let startedHere = $state(false);

	// Restores of what the backup holds: the one started here, else one
	// that is running (the manager refuses a second restore of the same data).
	const restores = useTrackedJobs(() => (backup.data ? restoreMatch(backup.data) : null));
	const job = $derived(
		followId
			? restores.entries.find((e) => e.id === followId)
			: restores.entries.find((e) => e.active)
	);
	const result = $derived(job && !job.active && isTerminal(job.job?.state) ? job.job : undefined);
	/** The restore wrote a stack's definition and files (it offers the deploy). */
	const stackRestore = $derived.by(() => {
		if (startedHere) return scope === 'stack';
		const targets = job?.job?.targets ?? [];
		return targets.some((t) => t.type === 'stack') && !targets.some((t) => t.type === 'volume');
	});

	// Found running when the page opened: follow it and go straight to
	// its progress.
	$effect(() => {
		const found = job;
		if (!found) return;
		untrack(() => {
			if (followId) return;
			followId = found.id;
			if (current < 2) current = 2;
		});
	});

	// An in-progress restore must not be reloaded away (#23).
	useCriticalWork(
		'restore',
		() => (backup.data ? itemName(backup.data) : 'backup'),
		() => !!job && !result
	);

	$effect(() => {
		const b = backup.data;
		if (b && b.kind === 'volume' && scope === 'stack') scope = 'volume';
	});

	// Every volume starts ticked, as an explicit list: unticking the last
	// one leaves none (Review then says to choose one), never "all".
	let volumesFor = '';
	$effect(() => {
		const b = backup.data;
		if (!b || volumesFor === b.id) return;
		volumesFor = b.id;
		volumes = [...(b.volumes ?? [])];
	});
	const volumeError = $derived(volumeChoiceError(scope, backup.data?.volumes ?? [], volumes));

	function scopeOptions(b: BackupDetail) {
		const out = [];
		if (b.kind === 'stack')
			out.push({
				value: 'stack',
				label: 'Stack Definition and Files',
				description:
					'compose.yaml, .env, the workspace and relative bind data. Volumes are not touched; deploy afterwards to apply it.'
			});
		out.push({
			value: 'volume',
			label: b.kind === 'stack' ? 'Volumes of the Stack' : 'The Volume',
			description: 'Named volumes only; the stack definition is not changed.'
		});
		out.push({
			value: 'file',
			label: 'One File',
			description: 'Put one file back in place.'
		});
		return out;
	}

	function body(confirm: boolean) {
		return {
			scope,
			shutdown,
			path: scope === 'file' ? filePath.trim() : undefined,
			volumes:
				scope === 'volume' && (backup.data?.volumes?.length ?? 0) > 1 ? volumes : undefined,
			...(confirm ? { confirm: true } : {})
		};
	}

	async function loadPreview(b: BackupDetail) {
		preview = await unwrap(
			api.POST('/api/v1/backups/{backupId}/restore-previews', {
				params: { path: { backupId: b.id } },
				body: body(false)
			})
		);
	}

	const steps = [
		{
			id: 'scope',
			label: 'What to Restore',
			description: 'Restores go to where the data lives now.'
		},
		{ id: 'preview', label: 'Review' },
		{ id: 'restore', label: 'Restore' }
	];

	async function onnext(step: { id: string }) {
		const b = backup.data;
		if (!b) return false;
		try {
			if (step.id === 'scope') await loadPreview(b);
			if (step.id === 'preview' && !preview?.canRestore) return false;
			if (step.id === 'restore') {
				if (!job) {
					const started = await unwrap(
						api.POST('/api/v1/backups/{backupId}/restores', {
							params: {
								path: { backupId: b.id },
								header: { 'Idempotency-Key': newIdempotencyKey() }
							},
							body: { ...body(true), confirm: true }
						})
					);
					restores.add(started, `Restore ${itemName(b)}`);
					followId = started.id;
					startedHere = true;
					return false;
				}
			}
		} catch (e) {
			throw new Error(
				actionError(e, {
					manager_restore_required:
						'Manager state is restored by importing it into a fresh Docker Manager, not over this one.'
				}),
				{ cause: e }
			);
		}
	}

	function finished(j: Job) {
		restores.markFinished(j);
		void qc.invalidateQueries({ queryKey: ['stacks'] });
		void qc.invalidateQueries({ queryKey: ['volumes'] });
		const name = backup.data ? itemName(backup.data) : 'the backup';
		if (j.state === 'succeeded') toast.success(`Restored ${name}`);
		else if (j.state === 'partial')
			toast.warn(`Restored ${name} partly`, { body: j.error?.recovery });
		else
			toast.error(`${name} was not restored`, {
				body: j.error?.recovery ?? j.error?.message
			});
	}

	const canAdvance = $derived.by(() => {
		const b = backup.data;
		if (!b) return false;
		if (current === 0)
			return !volumeError && (scope !== 'file' || filePath.trim().startsWith('/'));
		if (current === 1) return !!preview?.canRestore;
		if (current === 2) return job ? !!result : confirmText.trim() === itemName(b);
		return true;
	});
</script>

<Page narrow>
	<QueryView
		query={backup}
		errorTitle="The backup could not be loaded."
		notFoundTitle="This backup does not exist."
	>
		{#snippet children(b: BackupDetail)}
			<PageHeader
				title="Restore {itemName(b)}"
				icon={History}
				color="teal"
				description="From the backup of {formatDateTime(b.snapshotTime)}{b.environmentId &&
				b.kind !== 'manager_state'
					? ` on ${environmentName(envs.data, b.environmentId)}`
					: ''}."
			/>
			{#if b.kind === 'manager_state'}
				<Card title="Restore the Manager on a New Docker Manager">
					<ol class="steps" role="list">
						<li>
							Start a new Docker Manager with an empty data volume (keep this one
							running or stopped; it is not touched).
						</li>
						<li>Open its setup page and choose <strong>Import From Backup</strong>.</li>
						<li>
							Enter this repository's location, its access keys if it is S3, and your
							Recovery Key.
						</li>
						<li>
							Pick the backup set of {formatDateTime(b.snapshotTime)}, import it and
							sign in with your old owner account.
						</li>
						<li>
							Re-attach each environment by enrolling its agent again; then restore
							stacks and volumes from their backups.
						</li>
					</ol>
					<p class="muted">Sessions and API tokens of the backup are never revived.</p>
				</Card>
			{:else if !has(b, 'backup.restore')}
				<DeniedState
					title="You can't restore this backup."
					description="Restores need the Restore Backups permission on the backup and on every target."
					level={2}
				/>
			{:else}
				<Card>
					<StepWizard
						label="Restore {itemName(b)}"
						{steps}
						bind:current
						{onnext}
						{canAdvance}
						canGoBack={!job}
						nextLabel={current === 0
							? 'Review'
							: current === 2 && !job
								? 'Restore Now'
								: 'Next'}
						finishLabel={job ? (result ? 'Done' : 'Restoring…') : 'Restore Now'}
						onfinish={() => goto(routes.backup(b.id))}
					>
						{#snippet step(s)}
							{#if s.id === 'scope'}
								<Fields>
									<RadioGroup
										label="Restore"
										options={scopeOptions(b)}
										bind:value={scope}
										onchange={() => (preview = null)}
									/>
									{#if scope === 'volume' && (b.volumes?.length ?? 0) > 1}
										<ChoiceGrid min="200px">
											{#each b.volumes ?? [] as v (v)}
												<Checkbox
													label={v}
													checked={volumes.includes(v)}
													onchange={(e) => {
														volumes = toggleVolume(
															volumes,
															v,
															e.currentTarget.checked
														);
														preview = null;
													}}
												/>
											{/each}
										</ChoiceGrid>
										{#if volumeError}<p class="error" role="alert">
												{volumeError}
											</p>{/if}
									{/if}
									{#if scope === 'file'}
										<div class="file">
											<TextField
												label="File in the Backup"
												mono
												bind:value={filePath}
												placeholder="/…/compose.yaml"
											/>
											{#if has(b, 'backup.contents.read')}
												<Button
													icon={FolderOpen}
													onclick={() => (choosing = true)}
													>Choose File</Button
												>
												<FilePicker
													bind:open={choosing}
													title="Choose a File to Restore"
													description="Backup of {formatDateTime(
														b.snapshotTime
													)}."
													places={backupPlaces(b)}
													source={backupSource(b.id)}
													value={[filePath]}
													unchoosableReason={(e) =>
														e.type === 'symlink'
															? 'A link cannot be restored on its own. Choose the file it points to.'
															: undefined}
													confirmLabel="Choose File"
													truncatedHint="Only the first {PICKER_LIMIT} entries are listed. Paste the file's path instead."
													onpick={([p]) => {
														filePath = p;
														preview = null;
													}}
												/>
											{/if}
										</div>
									{/if}
									<Switch
										label="Stop Containers While Restoring"
										description="Recommended. Off: a restore under running containers is refused."
										bind:checked={shutdown}
										onchange={() => (preview = null)}
									/>
								</Fields>
							{:else if s.id === 'preview'}
								{#if preview}
									<RestorePreviewView {preview} />
								{:else}
									<Skeleton lines={4} height="20px" />
								{/if}
							{:else if s.id === 'restore'}
								{#if !job}
									<Fields>
										<Notice
											tone="danger"
											title="This Overwrites the Current Data"
											live="none"
										>
											<ul class="plain" role="list">
												{#each preview?.targets ?? [] as t (t.path)}
													<li title={t.path}>
														{restoreTargetName(t)}: {t.overwritten}
														files overwritten, {t.removed} removed, {t.added}
														added.
													</li>
												{/each}
												<li>
													{preview?.affectedContainers.filter(
														(c) => c.running && !c.protected
													).length ?? 0} running containers stop and start again
													afterwards.
												</li>
												<li>
													If anything fails, the original files are moved
													back.
												</li>
											</ul>
										</Notice>
										<TypeToConfirm
											text={itemName(b)}
											bind:value={confirmText}
										/>
									</Fields>
								{:else}
									{#key job.id}
										<JobProgress
											jobId={job.id}
											title={job.title ?? `Restore ${itemName(b)}`}
											variant="panel"
											onfinish={finished}
										/>
									{/key}
									{#if result}
										{#if result.state === 'succeeded'}
											<Notice tone="info" title="Restored" live="status">
												{stackRestore
													? 'The definition on disk is the restored one. Deploy the stack to run it; Docker Manager never deploys on its own.'
													: 'The data is back in place.'}
												{#snippet actions()}
													{#if stackRestore && b.stackId}<Button
															size="sm"
															href={routes.stack(b.stackId)}
															>Open the Stack</Button
														>{/if}
												{/snippet}
											</Notice>
										{:else}
											<Notice
												tone={result.state === 'partial'
													? 'warn'
													: 'danger'}
												title={result.state === 'partial'
													? 'Partly Restored'
													: 'Not Restored'}
												live="alert"
											>
												{result.error?.recovery ??
													result.error?.message ??
													'See the job for details.'}
											</Notice>
										{/if}
									{/if}
								{/if}
							{/if}
						{/snippet}
					</StepWizard>
				</Card>
			{/if}
		{/snippet}
	</QueryView>
</Page>

<style>
	.steps {
		display: grid;
		gap: var(--space-2);
		margin-bottom: var(--space-3);
		padding-left: var(--space-5);
		list-style: decimal;
	}

	.plain {
		display: grid;
		gap: 2px;
	}

	.error {
		color: var(--danger);
		font-size: var(--text-caption);
	}

	.file {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-2);
	}

	.file > :global(:first-child) {
		flex: 1 1 280px;
		min-width: 0;
	}
</style>
