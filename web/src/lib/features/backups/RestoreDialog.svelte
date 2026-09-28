<script lang="ts">
	// Confirm and follow a restore from a stack's or volume's Backups tab
	// (#10). The agent previews exactly what is written and which
	// containers stop; the danger button says what gets replaced: all of it
	// (full; typed confirmation) or only the selected items. Affected
	// containers stop first and only the ones that were running start again;
	// starting them meanwhile is refused (restore_in_progress). Opened while
	// a restore of the subject runs (after a reload, from another tab), it
	// shows that restore's progress instead (docs/internal/web.md, "Job
	// progress after reload").
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { isTerminal } from '$lib/api/job-states';
	import { liveKeys } from '$lib/live/keys';
	import {
		Button,
		Dialog,
		JobProgress,
		Notice,
		Skeleton,
		Switch,
		TypeToConfirm,
		formatDateTime,
		toast
	} from '$lib/ui';
	import { newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import { useCriticalWork } from '$lib/features/common/unsaved.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { restoreMatch } from './jobs';
	import RestorePreviewView from './RestorePreviewView.svelte';
	import type { Backup } from './model';
	import { restoreBody, restoreConsequences, type RestorePlan } from './restore';

	interface Props {
		open?: boolean;
		backup: Backup;
		plan: RestorePlan;
		/** The stack's or volume's name: copy and typed confirmation. */
		subject: string;
		/** Only this volume of a stack backup (a volume's page). */
		volume?: string;
		/** The user may deploy the stack (offers the redeploy of a full restore). */
		canDeploy?: boolean;
	}

	let {
		open = $bindable(false),
		backup,
		plan,
		subject,
		volume,
		canDeploy = false
	}: Props = $props();

	const qc = useQueryClient();
	let redeploy = $state(true);
	let typed = $state('');
	let starting = $state(false);
	let error = $state<string | null>(null);
	// The restore the dialog follows (started here, or found running when
	// it opened); the job itself lives in `restores`.
	let followId = $state<string | null>(null);

	// Restores of the subject (the manager refuses a second one).
	const restores = useTrackedJobs(() => restoreMatch(backup, volume), {
		enabled: () => open
	});
	const job = $derived(
		followId
			? restores.entries.find((e) => e.id === followId)
			: open
				? restores.entries.find((e) => e.active)
				: undefined
	);
	const result = $derived(job && !job.active && isTerminal(job.job?.state) ? job.job : undefined);

	const full = $derived(plan.kind === 'full');
	const deploys = $derived(full && canDeploy && backup.kind === 'stack' && !volume && redeploy);
	const body = $derived(restoreBody(backup, plan, { volume, redeploy: deploys }));

	$effect(() => {
		const found = job;
		if (open) {
			// Keep following a restore found running until it ends.
			if (found) untrack(() => (followId ??= found.id));
			return;
		}
		typed = '';
		error = null;
		untrack(() => {
			if (!result) return;
			restores.dismiss(result.id);
			followId = null;
		});
	});

	const preview = createQuery(() => ({
		queryKey: liveKeys.item('backups', backup.id, 'restore-preview', JSON.stringify(body)),
		queryFn: () =>
			unwrap(
				api.POST('/api/v1/backups/{backupId}/restore-previews', {
					params: { path: { backupId: backup.id } },
					body
				})
			),
		enabled: open && !job,
		retry: false,
		staleTime: 0,
		gcTime: 0,
		refetchOnWindowFocus: false
	}));

	const consequences = $derived(
		restoreConsequences(backup, plan, {
			subject,
			volume,
			redeploy: deploys,
			running:
				preview.data?.affectedContainers.filter((c) => c.running && !c.protected).length ??
				0
		})
	);
	const canConfirm = $derived(
		!!preview.data?.canRestore && !starting && (!full || typed.trim() === subject)
	);

	// A restore in progress must not be reloaded away (#23).
	useCriticalWork(
		'restore',
		() => subject,
		() => !!job && !result
	);

	const overrides = {
		agent_unsupported:
			"This environment's agent is older than the manager. Upgrade it to restore a whole backup or selected files.",
		restore_in_progress: 'Another restore of this data is running. Wait for it to finish.'
	};

	async function start() {
		starting = true;
		error = null;
		try {
			const started = await unwrap(
				api.POST('/api/v1/backups/{backupId}/restores', {
					params: {
						path: { backupId: backup.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { ...body, confirm: true }
				})
			);
			restores.add(started, `Restore ${subject}`);
			followId = started.id;
		} catch (e) {
			error = actionError(e, overrides);
		} finally {
			starting = false;
		}
	}

	function finished(j: Job) {
		restores.markFinished(j);
		void qc.invalidateQueries({ queryKey: ['stacks'] });
		void qc.invalidateQueries({ queryKey: ['volumes'] });
		void qc.invalidateQueries({ queryKey: ['jobs'] });
		if (j.state === 'succeeded')
			toast.success(`Restored ${subject}`, {
				body: deploys ? `Deploying ${subject} from the restored definition.` : undefined
			});
		else if (j.state === 'partial')
			toast.warn(`Restored ${subject} partly`, { body: j.error?.recovery });
		else
			toast.error(`${subject} was not restored`, {
				body: j.error?.recovery ?? j.error?.message
			});
	}
</script>

<Dialog
	bind:open
	size="lg"
	alert
	dismissible={!starting}
	title={full
		? `Restore all of ${subject}`
		: `Restore ${plan.kind === 'paths' ? plan.paths.length : 0} selected items`}
	description="From the backup of {formatDateTime(backup.snapshotTime)}."
>
	{#if !job}
		<Notice
			tone="danger"
			title={full ? 'Everything is replaced' : 'Only the selected items are replaced'}
			live="none"
		>
			<ul class="consequences">
				{#each consequences as c (c)}<li>{c}</li>{/each}
			</ul>
		</Notice>
		{#if plan.kind === 'paths'}
			<div class="selected">
				<p class="label">Selected</p>
				<ul class="paths">
					{#each plan.paths.slice(0, 12) as p (p)}<li class="mono">{p}</li>{/each}
					{#if plan.paths.length > 12}<li class="more">
							and {plan.paths.length - 12} more
						</li>{/if}
				</ul>
			</div>
		{/if}
		{#if full && canDeploy && backup.kind === 'stack' && !volume}
			<Switch
				label="Deploy {subject} afterwards"
				description="Deploys the restored definition with the services that were running before. Off: the stack shows undeployed changes until you deploy it."
				bind:checked={redeploy}
			/>
		{/if}
		{#if preview.isPending}
			<Skeleton lines={4} height="20px" />
		{:else if preview.isError}
			<Notice tone="danger" title="The restore cannot be previewed" live="alert">
				{actionError(preview.error, overrides)}
			</Notice>
		{:else if preview.data}
			<RestorePreviewView preview={preview.data} />
		{/if}
		{#if full}
			<TypeToConfirm text={subject} bind:value={typed} />
		{/if}
		{#if error}<p class="error" role="alert">{error}</p>{/if}
	{:else}
		{#key job.id}
			<JobProgress
				jobId={job.id}
				title={job.title ?? `Restore ${subject}`}
				variant="panel"
				onfinish={finished}
			/>
		{/key}
		{#if !result}
			<p class="muted">
				You can close this window: the restore goes on, and the containers that were running
				start again when it ends.
			</p>
		{/if}
	{/if}
	{#snippet footer()}
		{#if !job}
			<Button variant="ghost" disabled={starting} onclick={() => (open = false)}
				>Cancel</Button
			>
			<Button variant="danger" loading={starting} disabled={!canConfirm} onclick={start}>
				{full
					? 'Replace everything'
					: `Replace ${plan.kind === 'paths' ? plan.paths.length : 0} ${
							plan.kind === 'paths' && plan.paths.length === 1 ? 'item' : 'items'
						}`}
			</Button>
		{:else}
			<Button variant={result ? 'primary' : 'ghost'} onclick={() => (open = false)}
				>{result ? 'Done' : 'Close'}</Button
			>
		{/if}
	{/snippet}
</Dialog>

<style>
	.consequences {
		display: grid;
		gap: var(--space-1);
		padding-left: var(--space-4);
		list-style: disc;
	}

	.selected {
		display: grid;
		gap: var(--space-1);
		margin: var(--space-3) 0;
	}

	.label {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.paths {
		display: grid;
		gap: 2px;
		max-height: 160px;
		overflow: auto;
		font-size: var(--text-caption);
	}

	.more,
	.muted {
		color: var(--text-muted);
	}

	.error {
		color: var(--danger);
	}
</style>
