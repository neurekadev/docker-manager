<script lang="ts">
	// One-off prune from a resource page (#14): the button and its dialog.
	// Starts from the page's usual prune (all unused images or build cache,
	// stopped containers, unused networks, anonymous volumes; any age),
	// editable for this prune only; a preview shows exactly what goes and what is protected, then
	// the prune.run job runs in place. Nothing is saved: recurring prunes
	// are policies on the Maintenance page.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import BrushCleaning from '@lucide/svelte/icons/brush-cleaning';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { queryKeys } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Dialog,
		JobProgress,
		Notice,
		Select,
		Skeleton,
		formatBytes,
		toast
	} from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import { idempotencyKey } from '$lib/features/resources/jobs.svelte';
	import type { EnvironmentScope } from '$lib/features/resources/scope.svelte';
	import PrunePreviewView from './PrunePreviewView.svelte';
	import RuleEditor from './RuleEditor.svelte';
	import {
		PRUNE_TARGETS,
		manualPruneProblem,
		manualPruneRules,
		type MaintenanceRule,
		type PrunePreview,
		type PruneTarget
	} from './model';
	import { maintenanceDefaultsQuery } from './queries';

	let { target, scope }: { target: PruneTarget; scope: EnvironmentScope } = $props();

	const queryClient = useQueryClient();
	const info = $derived(PRUNE_TARGETS[target]);
	// Online environments where the caller may both preview and run prunes.
	const environments = $derived(
		scope.creatable('maintenance.run').filter((e) => scope.can('maintenance.preview', e.id))
	);

	let open = $state(false);
	let env = $state('');
	let rules = $state<MaintenanceRule[]>([]);
	let preview = $state<PrunePreview | null>(null);
	let previewing = $state(false);
	let starting = $state(false);
	let error = $state<unknown>(null);
	let key = $state('');
	let jobId = $state<string | null>(null);
	let outcome = $state<Job | null>(null);

	// Category descriptions and Engine limitations (needs settings.read;
	// the editor falls back to plain labels without them).
	const defaults = createQuery(() => ({ ...maintenanceDefaultsQuery(), enabled: open }));

	function start() {
		env =
			(scope.single ? environments.find((e) => e.id === scope.targets[0]?.id)?.id : '') ||
			environments[0]?.id ||
			'';
		rules = manualPruneRules(target);
		preview = null;
		error = null;
		jobId = null;
		outcome = null;
		open = true;
	}

	const envName = $derived(environments.find((e) => e.id === env)?.name ?? '');
	const previewProblem = $derived(manualPruneProblem(rules, true));
	const runProblem = $derived(manualPruneProblem(rules, false));
	const running = $derived(jobId !== null && outcome === null);
	const busy = $derived(previewing || starting);

	function setRule(r: MaintenanceRule) {
		rules = rules.map((x) => (x.category === r.category ? r : x));
		preview = null;
	}

	async function loadPreview() {
		previewing = true;
		error = null;
		try {
			preview = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/prune-previews', {
					params: { path: { environmentId: env } },
					body: { rules }
				})
			);
			key = idempotencyKey();
		} catch (e) {
			error = e;
		} finally {
			previewing = false;
		}
	}

	async function prune() {
		starting = true;
		error = null;
		try {
			const job = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/prunes', {
					params: { path: { environmentId: env }, header: { 'Idempotency-Key': key } },
					body: { rules, confirm: true }
				})
			);
			jobId = job.id;
		} catch (e) {
			error = e;
		} finally {
			starting = false;
		}
	}

	function finished(j: Job) {
		outcome = j;
		for (const k of [
			queryKeys.containers.all,
			queryKeys.images.all,
			queryKeys.volumes.all,
			queryKeys.networks.all
		])
			void queryClient.invalidateQueries({ queryKey: k });
		if (j.state === 'succeeded') toast.success(`Pruned ${info.what} on ${envName}`);
		else if (j.state === 'partial')
			toast.warn(`Pruned ${info.what} on ${envName} with some failures`, {
				body: 'Open the job to see which objects were skipped or failed.'
			});
	}
</script>

{#if environments.length}
	<Button variant="secondary" icon={BrushCleaning} onclick={start}>{info.title}</Button>
{/if}

<Dialog
	bind:open
	title={info.title}
	description="Removes {info.what} that nothing uses. DockYard's own objects, stacks, saved containers and backups are always kept. Nothing here is saved; recurring prunes are policies on the Maintenance page."
	size="lg"
	dismissible={!busy}
>
	<div class="body">
		{#if jobId}
			{#key jobId}
				<JobProgress {jobId} title="Prune {info.what} on {envName}" onfinish={finished} />
			{/key}
			{#if outcome && outcome.state !== 'succeeded'}
				<p class="muted">
					<a href={routes.job(outcome.id)}>Open the job</a> for every object and the reason
					it was skipped or failed.
				</p>
			{/if}
		{:else if preview}
			<PrunePreviewView {preview} info={defaults.data?.categories} />
			{#if runProblem}
				<Notice tone="warn" title="Not ready to prune" live="status">{runProblem}</Notice>
			{/if}
		{:else}
			{#if environments.length > 1}
				<Select
					label="Environment"
					bind:value={env}
					options={environments.map((e) => ({ value: e.id, label: e.name }))}
					onchange={() => (preview = null)}
				/>
			{/if}
			{#if open && defaults.isPending && !defaults.isError}
				<Skeleton lines={3} height="20px" />
			{:else}
				{#each rules as rule (rule.category)}
					<RuleEditor {rule} info={defaults.data?.categories} onchange={setRule} />
				{/each}
				{#if !rules.some((r) => r.enabled)}
					<p class="muted">Turn on at least one rule to preview the prune.</p>
				{/if}
			{/if}
		{/if}
		{#if error}
			<Notice
				tone="danger"
				title={preview ? 'The prune did not start' : 'The preview failed'}
				live="alert">{actionError(error)}</Notice
			>
		{/if}
	</div>

	{#snippet footer()}
		{#if jobId}
			<Button variant={running ? 'ghost' : 'primary'} onclick={() => (open = false)}
				>{running ? 'Continue in the background' : 'Done'}</Button
			>
		{:else if preview}
			<Button
				variant="ghost"
				icon={ArrowLeft}
				onclick={() => (preview = null)}
				disabled={busy}>Change options</Button
			>
			<Button
				variant="danger"
				icon={BrushCleaning}
				loading={starting}
				disabled={!!runProblem || preview.remove === 0}
				onclick={prune}
				>{preview.remove === 0
					? 'Nothing to prune'
					: `Remove ${preview.remove} ${preview.remove === 1 ? 'object' : 'objects'} (≈ ${formatBytes(preview.bytes)})`}</Button
			>
		{:else}
			<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
			<Button
				variant="primary"
				loading={previewing}
				disabled={!env || !!previewProblem}
				title={previewProblem ?? undefined}
				onclick={loadPreview}>Preview</Button
			>
		{/if}
	{/snippet}
</Dialog>

<style>
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}
</style>
