<script lang="ts">
	// One-off prune from a resource page (#14): the button and its dialog.
	// Starts from the page's usual prune (all unused images or build cache,
	// stopped containers, unused networks, anonymous volumes; any age),
	// editable for this prune only; a preview shows exactly what goes and what is protected, then
	// the prune.run job runs in place. Nothing is saved: recurring prunes
	// are policies on the Maintenance page. A one-off prune running in the
	// button's environments comes back from the running list after a
	// reload (docs/internal/web.md, "Job progress after reload"): the button
	// says so and opens on its progress.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import BrushCleaning from '@lucide/svelte/icons/brush-cleaning';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { isTerminal } from '$lib/api/job-states';
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
	import { titleCase } from '$lib/ui/format';
	import { actionError } from '$lib/features/common/errors';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
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
	// The prune the dialog follows (one started here, or the one running
	// when it opened); the job itself lives in `prunes`.
	let shownId = $state<string | null>(null);

	// One-off prunes (no policy) in the environments this button prunes.
	const envIds = $derived(environments.map((e) => e.id));
	const prunes = useTrackedJobs(
		() => ({
			kinds: ['prune.run'],
			policyId: null,
			...(envIds.length === 1 ? { environmentId: envIds[0] } : {})
		}),
		{ enabled: () => envIds.length > 0 }
	);
	const mine = $derived(
		prunes.entries.filter((e) => !e.job?.environmentId || envIds.includes(e.job.environmentId))
	);
	/** A prune runs in one of the button's environments. */
	const pruning = $derived(mine.some((e) => e.active));
	const shown = $derived(shownId ? mine.find((e) => e.id === shownId) : undefined);
	const outcome = $derived(
		shown && !shown.active && isTerminal(shown.job?.state) ? shown.job : undefined
	);

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
		shownId = mine.find((e) => e.active)?.id ?? null;
		open = true;
	}

	const nameOf = (id: string | undefined) =>
		(environments.find((e) => e.id === id) ?? scope.targets.find((e) => e.id === id))?.name ??
		'the environment';

	const envName = $derived(environments.find((e) => e.id === env)?.name ?? '');
	const previewProblem = $derived(manualPruneProblem(rules, true));
	const runProblem = $derived(manualPruneProblem(rules, false));
	const running = $derived(!!shown && !outcome);
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
			prunes.add(job, `Prune ${titleCase(info.what)} on ${envName}`);
			shownId = job.id;
		} catch (e) {
			error = e;
		} finally {
			starting = false;
		}
	}

	function finished(j: Job) {
		// A prune started here prunes this page's objects; one found running
		// may have pruned anything.
		const what = mine.find((e) => e.id === j.id)?.title ? info.what : 'unused objects';
		const where = nameOf(j.environmentId);
		prunes.markFinished(j);
		for (const k of [
			queryKeys.containers.all,
			queryKeys.images.all,
			queryKeys.volumes.all,
			queryKeys.networks.all
		])
			void queryClient.invalidateQueries({ queryKey: k });
		if (j.state === 'succeeded') toast.success(`Pruned ${what} on ${where}`);
		else if (j.state === 'partial')
			toast.warn(`Pruned ${what} on ${where} with some failures`, {
				body: 'Open the job to see which objects were skipped or failed.'
			});
	}
</script>

{#if environments.length}
	<Button variant="secondary" icon={BrushCleaning} onclick={start}
		>{pruning ? 'Pruning…' : info.title}</Button
	>
{/if}

<Dialog
	bind:open
	title={info.title}
	description="Removes {info.what} that nothing uses. Docker Manager's own objects, stacks, saved containers and backups are always kept."
	size="lg"
	dismissible={!busy}
>
	<div class="body">
		{#if shown}
			{#key shown.id}
				<JobProgress
					jobId={shown.id}
					title={shown.title ?? `Prune on ${nameOf(shown.job?.environmentId)}`}
					onfinish={finished}
				/>
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
				<Notice tone="warn" title="Not Ready to Prune" live="status">{runProblem}</Notice>
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
		{#if shown}
			<Button variant={running ? 'ghost' : 'primary'} onclick={() => (open = false)}
				>{running ? 'Continue in the Background' : 'Done'}</Button
			>
		{:else if preview}
			<Button
				variant="ghost"
				icon={ArrowLeft}
				onclick={() => (preview = null)}
				disabled={busy}>Change Options</Button
			>
			<Button
				variant="danger"
				icon={BrushCleaning}
				loading={starting}
				disabled={!!runProblem || preview.remove === 0}
				onclick={prune}
				>{preview.remove === 0
					? 'Nothing to Prune'
					: `Remove ${preview.remove} ${preview.remove === 1 ? 'Object' : 'Objects'} (≈ ${formatBytes(preview.bytes)})`}</Button
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
