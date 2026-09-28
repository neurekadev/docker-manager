<script lang="ts">
	// Volume migration wizard (#35): copy a standalone named volume to
	// another environment, optionally under a new name. The preflight
	// preview comes first (conflicts, size against free space, downtime,
	// transport); containers using the volume must be stopped for a
	// consistent copy, or the user acknowledges a crash-consistent one. The
	// source stays as it is; the copy runs as a job with progress. A running
	// migration of the volume comes from the running list: after a reload or
	// coming back, the page shows it (and its outcome) instead of the wizard.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import { api, unwrap, type Job, type Schema } from '$lib/api/client';
	import { queryKeys, volumeQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Card,
		Checkbox,
		EmptyState,
		JobProgress,
		Notice,
		Select,
		StepWizard,
		TextField,
		formatBytes,
		formatDuration,
		toast,
		type WizardStep
	} from '$lib/ui';
	import { idempotencyKey } from '$lib/features/resources/jobs.svelte';
	import { migrationCopy, volumeMigrationJobs } from '$lib/features/resources/object-jobs';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { NAME_RE } from '$lib/features/resources/model';
	import {
		doneTitle,
		jobFailure,
		refusal,
		RefusalError,
		sentence,
		type Refusal
	} from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	type Preview = Schema<'MigrationPreview'>;

	const env = $derived(page.params.environmentId ?? '');
	const name = $derived(page.params.volumeId ?? '');
	const queryClient = useQueryClient();
	const scope = useEnvironmentScope();
	const vq = createQuery(() => volumeQuery(env, name));
	const v = $derived(vq.data);

	const targets = $derived(
		(scope.envs.data ?? []).filter(
			(e) =>
				e.id !== env &&
				e.status === 'active' &&
				e.online &&
				scope.can('volume.create', e.id)
		)
	);
	let target = $state('');
	let newName = $state('');
	let ack = $state(false);
	let current = $state(0);
	let preview = $state<Preview | null>(null);
	let previewing = $state(false);
	let outcome = $state<{ job: Job; failure?: Refusal } | null>(null);

	// The migration shown: started here, or running (found in the running
	// list after a reload). Migrations into this volume from elsewhere
	// (their source is another environment) are left out.
	const jobs = useTrackedJobs(() => (env && name ? volumeMigrationJobs(env, name) : null));
	const run = $derived(jobs.entries.find((e) => !e.job || e.job.environmentId === env));
	// Where it copies to: from the job (after a reload), else the form.
	const copy = $derived(migrationCopy(run?.job));
	const runTarget = $derived(copy?.environmentId ?? target);
	const runCopy = $derived(copy?.name ?? (newName.trim() || name));
	const ended = $derived(outcome && outcome.job.id === run?.id ? outcome : null);

	$effect(() => {
		if (!target && targets.length) target = targets[0].id;
	});
	const targetName = $derived(targets.find((e) => e.id === target)?.name ?? '');
	const running = $derived((v?.usedBy ?? []).filter((c) => c.state === 'running'));
	const nameError = $derived(
		newName && !NAME_RE.test(newName) ? 'Use letters, digits, ".", "_" and "-".' : null
	);

	const steps: WizardStep[] = [
		{
			id: 'target',
			label: 'Destination',
			description: 'Where the copy goes. The source volume stays as it is.'
		},
		{
			id: 'preview',
			label: 'Check',
			description: 'What Docker Manager found before anything is copied.'
		}
	];

	async function loadPreview(acknowledge: boolean) {
		previewing = true;
		try {
			preview = await unwrap(
				api.POST(
					'/api/v1/environments/{environmentId}/volumes/{volumeId}/migration-previews',
					{
						params: { path: { environmentId: env, volumeId: name } },
						body: {
							targetEnvironmentId: target,
							targetName: newName.trim() || undefined,
							acknowledgeCrashConsistency: acknowledge || undefined
						}
					}
				)
			);
		} catch (e) {
			const r = refusal(e, {
				kind: 'volume',
				name,
				verb: 'migrate',
				protection: v?.protection,
				environmentName: scope.name(env)
			});
			throw new RefusalError(r);
		} finally {
			previewing = false;
		}
	}

	async function onnext(step: WizardStep) {
		if (step.id === 'target') {
			ack = false;
			await loadPreview(false);
		}
	}

	async function start() {
		try {
			const job = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/volumes/{volumeId}/migrations', {
					params: {
						path: { environmentId: env, volumeId: name },
						header: { 'Idempotency-Key': idempotencyKey() }
					},
					body: {
						targetEnvironmentId: target,
						targetName: newName.trim() || undefined,
						acknowledgeCrashConsistency: ack || undefined
					}
				})
			);
			outcome = null;
			jobs.add(job);
		} catch (e) {
			throw new RefusalError(
				refusal(e, {
					kind: 'volume',
					name,
					verb: 'migrate',
					protection: v?.protection,
					environmentName: scope.name(env)
				})
			);
		}
	}

	function finished(j: Job) {
		jobs.markFinished(j);
		void queryClient.invalidateQueries({ queryKey: queryKeys.volumes.all });
		if (j.state === 'succeeded') {
			toast.success(doneTitle('migrate', name));
			outcome = { job: j };
		} else
			outcome = { job: j, failure: jobFailure(j, { kind: 'volume', name, verb: 'migrate' }) };
	}

	const onlyRunningBlocks = $derived(
		!!preview &&
			preview.blockers.length > 0 &&
			preview.blockers.every((b) => b.code === 'containers_running')
	);
	const copyName = $derived(newName.trim() || name);
</script>

{#if v?.protection}
	<Card>
		<EmptyState
			icon={ArrowRightLeft}
			color="slate"
			title="Docker Manager's own volumes can't be migrated."
			description={sentence(v.protection.reason)}
			level={2}
			compact
		/>
	</Card>
{:else if scope.envs.data && targets.length === 0 && !run}
	<Card>
		<EmptyState
			icon={ArrowRightLeft}
			color="slate"
			title="No other environment can take this volume."
			description="A migration needs another online environment where you may create volumes."
			level={2}
			compact
		>
			{#snippet actions()}
				<Button variant="secondary" href={routes.environments()}>Go to environments</Button>
			{/snippet}
		</EmptyState>
	</Card>
{:else if run}
	<Card title="Migrate {name} to {scope.name(runTarget)}">
		<div class="run">
			{#key run.id}
				<JobProgress
					jobId={run.id}
					title="Copy {name} to {scope.name(runTarget)} as {runCopy}"
					onfinish={finished}
				/>
			{/key}
			{#if ended?.failure}
				<Notice tone="danger" title={ended.failure.title} live="alert">
					{ended.failure.body} The source volume on {scope.name(env)} is unchanged.
				</Notice>
			{:else if ended}
				<Notice tone="info" title="{runCopy} is on {scope.name(runTarget)}" live="status">
					The source volume on {scope.name(env)} is unchanged. Point containers on {scope.name(
						runTarget
					)} at the copy, then remove the source when you no longer need it.
					{#snippet actions()}
						<Button variant="secondary" href={routes.volume(runTarget, runCopy)}
							>Open the copy</Button
						>
					{/snippet}
				</Notice>
			{/if}
		</div>
	</Card>
{:else}
	<Card>
		<StepWizard
			label="Volume migration"
			{steps}
			bind:current
			{onnext}
			onfinish={start}
			finishLabel="Migrate volume"
			canAdvance={current === 0
				? !!target && !nameError
				: !!preview && preview.allowed && !previewing}
		>
			{#snippet step(s)}
				{#if s.id === 'target'}
					<div class="fields">
						<Select
							label="Destination environment"
							bind:value={target}
							options={targets.map((e) => ({ value: e.id, label: e.name }))}
						/>
						<TextField
							label="Name on the destination"
							mono
							bind:value={newName}
							placeholder={name}
							description="Optional. Default: the same name."
							error={nameError}
						/>
					</div>
					{#if running.length}
						<Notice tone="warn" title="Containers are using {name}" live="none">
							{running.map((c) => c.name).join(', ')}
							{running.length === 1 ? 'is' : 'are'} running. Stop {running.length ===
							1
								? 'it'
								: 'them'} for a consistent copy, or acknowledge a crash-consistent copy
							in the next step.
						</Notice>
					{/if}
				{:else if preview}
					<div class="preview">
						{#if preview.blockers.length}
							<Notice
								tone="danger"
								title="The migration can't start yet"
								live="status"
							>
								<ul class="bullets">
									{#each preview.blockers as b, i (i)}<li>
											{b.message}
										</li>{/each}
								</ul>
							</Notice>
						{/if}
						{#if onlyRunningBlocks || ack}
							<Checkbox
								label="Copy while the containers keep running"
								description="The copy is crash-consistent only: like pulling the power cord, files being written may be incomplete. Stopping the containers first is safer."
								bind:checked={ack}
								onchange={(e) =>
									void loadPreview(e.currentTarget.checked).catch(
										(err: RefusalError) =>
											toast.error(err.refusal.title, {
												body: err.refusal.body
											})
									)}
							/>
						{/if}
						{#if preview.warnings.length}
							<Notice tone="warn" title="Check before you start" live="none">
								<ul class="bullets">
									{#each preview.warnings as w, i (i)}<li>
											{w.message}
										</li>{/each}
								</ul>
							</Notice>
						{/if}
						<dl class="summary">
							<div>
								<dt>Copy</dt>
								<dd class="mono">
									{name} → {targetName}/{preview.volumes[0]?.target ?? copyName}
								</dd>
							</div>
							<div>
								<dt>Data</dt>
								<dd class="num">
									{formatBytes(preview.data.volumeBytes)}{preview.data.truncated
										? ' or more'
										: ''}
									{#if preview.volumes[0]}, {preview.volumes[0].entries} files{/if}
								</dd>
							</div>
							<div>
								<dt>Free on {targetName}</dt>
								<dd class="num">
									{preview.data.destinationVolumesFree < 0
										? 'Unknown'
										: formatBytes(preview.data.destinationVolumesFree)}
								</dd>
							</div>
							<div>
								<dt>Expected time</dt>
								<dd>
									{formatDuration(preview.downtime.estimatedSeconds)}
									<span class="muted">({preview.downtime.basis})</span>
								</dd>
							</div>
							<div>
								<dt>Transfer</dt>
								<dd>
									Through the manager{preview.transport
										.bandwidthLimitBytesPerSecond
										? `, limited to ${formatBytes(preview.transport.bandwidthLimitBytesPerSecond)}/s`
										: ''}{preview.transport.sourcePlainHttp ||
									preview.transport.destinationPlainHttp
										? '; unencrypted on the internal network (plain HTTP agent URL)'
										: ', encrypted'}
								</dd>
							</div>
						</dl>
						{#if preview.leftovers.length}
							<p class="muted">
								Partial data of an earlier attempt on {targetName} is removed first.
							</p>
						{/if}
					</div>
				{/if}
			{/snippet}
		</StepWizard>
	</Card>
{/if}

<style>
	.fields {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
		gap: var(--space-4);
		margin-bottom: var(--space-4);
	}

	.preview,
	.run {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.bullets {
		display: grid;
		gap: 4px;
		padding-left: 18px;
	}

	.summary {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: var(--space-3) var(--space-5);
		margin: 0;
	}

	.summary dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.summary dd {
		margin: 2px 0 0;
		color: var(--text-strong);
	}
</style>
