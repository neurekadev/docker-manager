<script lang="ts">
	// Restore wizard (#10): choose the scope (stack definition and files,
	// volumes, or one file; system and manager restores go through a fresh
	// DockYard), preview exactly what is written and which containers stop,
	// confirm, then follow the restore to a success, partial or failure
	// result. A stack restore never redeploys: it offers the deploy.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import History from '@lucide/svelte/icons/history';
	import { api, unwrap, type Job } from '$lib/api/client';
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
	import ContentsBrowser from '$lib/features/backups/ContentsBrowser.svelte';
	import RestorePreviewView from '$lib/features/backups/RestorePreviewView.svelte';
	import { itemName, type BackupDetail, type RestorePreview } from '$lib/features/backups/model';
	import { backupQuery } from '$lib/features/backups/queries';

	const id = $derived(page.params.backupId ?? '');
	const qc = useQueryClient();
	const backup = createQuery(() => ({ ...backupQuery(id), refetchOnWindowFocus: false }));
	const envs = createQuery(() => environmentsQuery());

	usePage(() => ({
		title: 'Restore',
		crumbs: [
			{ label: 'Backups', href: routes.backups() },
			{ label: backup.data ? itemName(backup.data) : 'Backup', href: routes.backup(id) },
			{ label: 'Restore' }
		]
	}));

	let current = $state(0);
	let scope = $state<'stack' | 'volume' | 'file'>('stack');
	let volumes = $state<string[]>([]);
	let filePath = $state('');
	let shutdown = $state(true);
	let preview = $state<RestorePreview | null>(null);
	let confirmText = $state('');
	let job = $state<Job | null>(null);
	let result = $state<Job | null>(null);

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

	function scopeOptions(b: BackupDetail) {
		const out = [];
		if (b.kind === 'stack')
			out.push({
				value: 'stack',
				label: 'Stack definition and files',
				description:
					'compose.yaml, .env, the workspace and relative bind data. Volumes are not touched; deploy afterwards to apply it.'
			});
		out.push({
			value: 'volume',
			label: b.kind === 'stack' ? 'Volumes of the stack' : 'The volume',
			description:
				'Named volumes only. The stack definition is not changed. A missing volume is created.'
		});
		out.push({
			value: 'file',
			label: 'One file',
			description:
				'Put one file back in place. To get a copy instead, download it from the backup.'
		});
		return out;
	}

	function body(confirm: boolean) {
		return {
			scope,
			shutdown,
			path: scope === 'file' ? filePath.trim() : undefined,
			volumes: scope === 'volume' && volumes.length ? volumes : undefined,
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
			label: 'What to restore',
			description: 'Restores go to where the data lives now.'
		},
		{
			id: 'preview',
			label: 'Review',
			description: 'Exactly what is written and which containers stop.'
		},
		{ id: 'restore', label: 'Restore', description: 'Confirm, then follow the restore.' }
	];

	async function onnext(step: { id: string }) {
		const b = backup.data;
		if (!b) return false;
		try {
			if (step.id === 'scope') await loadPreview(b);
			if (step.id === 'preview' && !preview?.canRestore) return false;
			if (step.id === 'restore') {
				if (!job) {
					job = await unwrap(
						api.POST('/api/v1/backups/{backupId}/restores', {
							params: {
								path: { backupId: b.id },
								header: { 'Idempotency-Key': newIdempotencyKey() }
							},
							body: { ...body(true), confirm: true }
						})
					);
					return false;
				}
			}
		} catch (e) {
			throw new Error(
				actionError(e, {
					manager_restore_required:
						'Manager state is restored by importing it into a fresh DockYard, not over this one.'
				}),
				{ cause: e }
			);
		}
	}

	function finished(j: Job) {
		result = j;
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
		if (current === 0) return scope !== 'file' || filePath.trim().startsWith('/');
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
				<Card title="Restore the manager on a new DockYard">
					<ol class="steps" role="list">
						<li>
							Start a new DockYard with an empty data volume (keep this one running or
							stopped; it is not touched).
						</li>
						<li>Open its setup page and choose <strong>Import from backup</strong>.</li>
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
					<p class="muted">
						A full system restore is exactly this: the manager first, then each host's
						data. Sessions and API tokens of the backup are never revived.
					</p>
				</Card>
			{:else if !has(b, 'backup.restore')}
				<DeniedState
					title="You can't restore this backup."
					description="Restores need the Restore backups permission on the backup and on every target."
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
						nextLabel={current === 0
							? 'Review'
							: current === 2 && !job
								? 'Restore now'
								: 'Next'}
						finishLabel={job ? (result ? 'Done' : 'Restoring…') : 'Restore now'}
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
													checked={volumes.length === 0 ||
														volumes.includes(v)}
													onchange={(e) => {
														const all = volumes.length
															? volumes
															: [...(b.volumes ?? [])];
														volumes = e.currentTarget.checked
															? [...new Set([...all, v])]
															: all.filter((x) => x !== v);
													}}
												/>
											{/each}
										</ChoiceGrid>
									{/if}
									{#if scope === 'file'}
										<TextField
											label="File in the backup"
											mono
											bind:value={filePath}
											placeholder="/…/compose.yaml"
											description="Pick it below or paste its absolute path inside the backup."
										/>
										{#if has(b, 'backup.contents.read')}
											<div class="browser">
												<ContentsBrowser
													backupId={b.id}
													canDownload={false}
													onrestorefile={(p) => (filePath = p)}
												/>
											</div>
										{/if}
									{/if}
									<Switch
										label="Stop the containers that use this data while restoring"
										description="Recommended. They stop in reverse dependency order and only the ones that were running start again. Off: a restore under running containers is refused."
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
											title="This overwrites the current data"
											live="none"
										>
											<ul class="plain" role="list">
												{#each preview?.targets ?? [] as t (t.path)}
													<li>
														<span class="mono">{t.path}</span>: {t.overwritten}
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
										<TextField
											label="Type {itemName(b)} to confirm"
											bind:value={confirmText}
											autocomplete="off"
											spellcheck="false"
										/>
									</Fields>
								{:else}
									<JobProgress
										jobId={job.id}
										title="Restore {itemName(b)}"
										variant="panel"
										onfinish={finished}
									/>
									{#if result}
										{#if result.state === 'succeeded'}
											<Notice tone="info" title="Restored" live="status">
												{scope === 'stack'
													? 'The definition on disk is the restored one. Deploy the stack to run it; DockYard never deploys on its own.'
													: 'The data is back in place.'}
												{#snippet actions()}
													{#if scope === 'stack' && b.stackId}<Button
															size="sm"
															href={routes.stack(b.stackId)}
															>Open the stack</Button
														>{/if}
												{/snippet}
											</Notice>
										{:else}
											<Notice
												tone={result.state === 'partial'
													? 'warn'
													: 'danger'}
												title={result.state === 'partial'
													? 'Partly restored'
													: 'Not restored'}
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

	.browser {
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}
</style>
