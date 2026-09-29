<script lang="ts">
	// Environment migration wizard (#35, #22 high-risk flow): every stack of
	// the environment moves to another environment. Destination and the
	// stacks to move (Docker Manager's own stack moves with Docker Manager;
	// stacks the caller may not migrate are shown but off), the check
	// computed before anything stops (problems of the whole migration and of
	// each stack, data against free space, the longest downtime, the order:
	// stacks that share a network or volume form a group that stops together
	// and moves one after the other, networks created first, what is not
	// moved; EnvironmentMigrationCheck, shared with the manager move),
	// confirmation, then the environment.migrate job's progress and
	// each stack's outcome. Moved stacks' old copies stay stopped on the
	// source until the user removes them (one request per stack, one summary
	// toast); what did not move can be migrated again. A running migration
	// of the environment (after a reload, or when the user comes back) opens
	// the wizard at the move step on its progress (the running list,
	// docs/internal/web.md "Job progress after reload").
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { onDestroy, untrack } from 'svelte';
	import Archive from '@lucide/svelte/icons/archive';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { Environment, Job } from '$lib/api/client';
	import { containersQuery, environmentsQuery } from '$lib/api/queries';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { bulkSummary } from '$lib/features/resources/bulk';
	import { count, migrationTargets, toggled } from '$lib/features/stacks/migration';
	import { downtimeText, stackTitle } from '$lib/features/stacks/model';
	import { stackKeys, stacksQuery } from '$lib/features/stacks/queries';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Checkbox,
		DestructiveConfirm,
		EmptyState,
		ErrorState,
		JobProgress,
		Notice,
		Select,
		Skeleton,
		StepWizard,
		errorView,
		formatBytes,
		toast,
		type WizardStep
	} from '$lib/ui';
	import EnvironmentMigrationCheck from './EnvironmentMigrationCheck.svelte';
	import {
		CHOICE_REASONS,
		chosenStacks,
		destinationOptions,
		environmentCheckHeadline,
		environmentMigrationBody,
		environmentMigrationMatch,
		everyStackMoved,
		finishToast,
		migrationOutcome,
		moveState,
		oldCopies,
		ownStackIds,
		stackChoices,
		warningCount,
		withOwnFromCheck,
		type EnvironmentMigration,
		type EnvironmentMigrationPreview,
		type EnvironmentMigrationSelection,
		type TitleOf
	} from './environment-migration';
	import {
		previewEnvironmentMigration,
		removeOldCopies,
		startEnvironmentMigration
	} from './migration-actions';
	import { environmentMigrationQuery } from './queries';

	interface Props {
		/** The environment whose stacks move (the source). */
		environment: Environment;
	}

	let { environment }: Props = $props();
	const queryClient = useQueryClient();
	const source = untrack(() => environment.id);
	const sourceName = $derived(environment.name);

	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery(source));
	// Docker Manager's own stack: the stack list does not say, its
	// protected containers do (the check confirms it).
	const containers = createQuery(() => ({
		...containersQuery([{ id: source, name: environment.name, online: environment.online }]),
		enabled: environment.online
	}));
	let ownFromCheck = $state<string[]>([]);
	const ownAll = $derived([
		...new Set([...ownStackIds(containers.data?.items), ...ownFromCheck])
	]);
	const choices = $derived(stackChoices(stacks.data ?? [], source, ownAll));
	const movable = $derived(choices.filter((c) => !c.blocked).length);

	// The titles of the stacks seen: moved stacks leave the source's list.
	let titles = $state<Record<string, string>>({});
	$effect(() => {
		const list = stacks.data;
		if (!list) return;
		untrack(() => {
			titles = { ...titles, ...Object.fromEntries(list.map((s) => [s.id, stackTitle(s)])) };
		});
	});
	const titleOf: TitleOf = (id, name) => titles[id] ?? name;

	const others = $derived(migrationTargets(envs.data, source));
	const destOptions = $derived(destinationOptions(others));
	const anyOffline = $derived(others.some((e) => !e.online));
	const envName = (id: string) =>
		envs.data?.find((e) => e.id === id)?.name ?? 'the other environment';

	let current = $state(0);
	let target = $state('');
	let deselected = $state<string[]>([]);
	let preview = $state<EnvironmentMigrationPreview | null>(null);
	let checking = $state(false);
	let checkError = $state<unknown>(null);
	let acknowledged = $state(false);
	let jobId = $state<string | null>(null);
	let finished = $state<Job | null>(null);

	// One destination online: nothing to choose.
	$effect(() => {
		if (target) return;
		const online = others.filter((e) => e.online);
		if (online.length === 1) target = online[0].id;
	});
	const chosen = $derived(chosenStacks(choices, deselected));

	// The environment's running migration: the wizard opens on its progress.
	const runs = useTrackedJobs(() => environmentMigrationMatch(source));
	// Migrations this wizard showed (a later list still listing one that
	// ended does not bring it back) and a start in flight; not state,
	// nothing renders from them.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	const shown = new Set<string>();
	let starting = false;
	let release: (() => void) | null = null;
	$effect(() => {
		const job = runs.running[0];
		if (!job) return;
		untrack(() => resume(job));
	});

	function resume(job: Job) {
		if (jobId || starting || shown.has(job.id)) return;
		follow(job.id);
		current = steps.length - 1;
	}

	function follow(id: string) {
		shown.add(id);
		jobId = id;
		finished = null;
		release?.();
		release = criticalWork.register('other', `Migration of ${environment.name}`);
	}

	onDestroy(() => release?.());

	// The run's record: where it goes (after a reload) and each stack's state.
	const record = createQuery(() => ({
		...environmentMigrationQuery(source, jobId ?? ''),
		enabled: !!jobId
	}));
	const rec = $derived<EnvironmentMigration | undefined>(
		jobId && record.data?.id === jobId ? record.data : undefined
	);
	const dest = $derived(target || rec?.targetEnvironmentId || '');
	const destName = $derived(dest ? envName(dest) : 'the other environment');

	const steps: WizardStep[] = [
		{
			id: 'destination',
			label: 'Destination',
			description: 'Where the stacks and their data go, and which stacks move.'
		},
		{
			id: 'check',
			label: 'Check',
			description:
				'Checked before anything stops. Fix any problems it finds; warnings are accepted by starting.'
		},
		{ id: 'confirm', label: 'Confirm', description: 'What happens when the migration starts.' },
		{
			id: 'move',
			label: 'Move',
			description:
				'The stacks move group by group; each starts on the destination once its files and data are copied and checked.'
		}
	];

	const selection = (): EnvironmentMigrationSelection => ({
		target,
		deselected: [...deselected]
	});

	// Only the latest check's answer is shown.
	let seq = 0;
	async function runCheck() {
		const mine = ++seq;
		checking = true;
		checkError = null;
		try {
			const p = await previewEnvironmentMigration(
				source,
				environmentMigrationBody(selection(), choices)
			);
			if (mine !== seq) return;
			preview = p;
			ownFromCheck = withOwnFromCheck(ownFromCheck, p);
		} finally {
			if (mine === seq) checking = false;
		}
	}

	function checkAgain() {
		runCheck().catch((e) => (checkError = e));
	}

	async function onnext(step: WizardStep) {
		switch (step.id) {
			case 'destination':
				if (!target) throw new Error('Choose the destination environment.');
				if (!chosen.length) throw new Error('Choose at least one stack to migrate.');
				await runCheck();
				return;
			case 'check':
				if (checking) throw new Error('Wait for the check to finish.');
				if (!preview?.allowed)
					throw new Error(
						'Fix the problems first, then check again. Nothing was stopped.'
					);
				return;
			case 'confirm': {
				starting = true;
				try {
					const job = await startEnvironmentMigration(
						source,
						environmentMigrationBody(selection(), choices)
					);
					runs.add(job);
					follow(job.id);
				} catch (e) {
					if (errorView(e).code === 'migration_blocked') {
						await runCheck().catch(() => undefined);
						current = 1;
						throw new Error(
							'Something changed: the check found problems again. Review them.',
							{ cause: e }
						);
					}
					throw e;
				} finally {
					starting = false;
				}
				return;
			}
			case 'move':
				void goto(routes.environment(source));
				return false;
		}
	}

	async function done(job: Job) {
		finished = job;
		runs.markFinished(job);
		release?.();
		release = null;
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
		void queryClient.invalidateQueries({ queryKey: ['overview'] });
		let r: EnvironmentMigration | undefined;
		try {
			r = await queryClient.fetchQuery({
				...environmentMigrationQuery(source, job.id),
				staleTime: 0
			});
		} catch {
			r = undefined;
		}
		const t = finishToast(r, job, { source: environment.name, destination: destName }, titleOf);
		const open = t.jobId;
		toast[t.tone](t.title, {
			body: t.body,
			action: open
				? { label: 'Open job', onclick: () => void goto(routes.job(open)) }
				: undefined
		});
	}

	// After a run that left stacks on the source: check again for the rest.
	async function migrateRest() {
		target = dest;
		jobId = null;
		finished = null;
		acknowledged = false;
		preview = null;
		checkError = null;
		deselected = [];
		removal = 'idle';
		await queryClient.invalidateQueries({ queryKey: stackKeys.list(source) });
		current = 1;
		checkAgain();
	}

	// The old copies of the moved stacks, stopped on the source.
	let removing = $state(false);
	let removal = $state<'idle' | 'running' | 'done'>('idle');
	const outcome = $derived(rec ? migrationOutcome(rec) : null);
	const copies = $derived(rec ? oldCopies(rec, titleOf) : []);

	function removeCopies() {
		const list = copies;
		removal = 'running';
		void removeOldCopies(list).then((o) => {
			removal = 'done';
			const s = bulkSummary('remove', { one: 'old copy', many: 'old copies' }, o);
			const title = o.succeeded.length ? `${s.title} from ${environment.name}` : s.title;
			toast[s.tone](title, s.body ? { body: s.body } : undefined);
			void queryClient.invalidateQueries({ queryKey: stackKeys.all });
		});
	}

	const canAdvance = $derived.by(() => {
		switch (steps[current].id) {
			case 'destination':
				return !!target && chosen.length > 0 && environment.online;
			case 'check':
				return !!preview?.allowed && !checking;
			case 'confirm':
				return acknowledged;
			case 'move':
				return !!finished;
		}
		return true;
	});
	const nextLabel = $derived(
		steps[current].id === 'destination'
			? 'Check migration'
			: steps[current].id === 'confirm'
				? 'Start migration'
				: 'Next'
	);
</script>

{#snippet step(s: WizardStep)}
	{#if s.id === 'destination'}
		<div class="form">
			{#if !environment.online}
				<Notice tone="offline" title="{sourceName} is offline.">
					The check needs it online. Try again when it has reconnected.
				</Notice>
			{/if}
			<Select
				label="Destination environment"
				options={destOptions}
				bind:value={target}
				placeholder="Choose an environment"
				description={anyOffline
					? 'Offline environments cannot be chosen: the check needs them online.'
					: undefined}
			/>
			<fieldset class="stacks">
				<legend class="subsection-title">Stacks</legend>
				<p class="muted">{chosen.length} of {count(movable, 'stack')} selected.</p>
				{#each choices as c (c.id)}
					<Checkbox
						label={c.title}
						description={c.blocked ? CHOICE_REASONS[c.blocked] : undefined}
						disabled={!!c.blocked}
						checked={!c.blocked && !deselected.includes(c.id)}
						onchange={(e) =>
							(deselected = toggled(
								deselected,
								c.id,
								!(e.currentTarget as HTMLInputElement).checked
							))}
					/>
				{/each}
			</fieldset>
			<p class="muted">
				Each stack keeps its name, history and details; its update and backup policies
				follow it.
			</p>
		</div>
	{:else if s.id === 'check'}
		{#if !preview}
			{#if checkError}
				<ErrorState
					error={checkError}
					title="The check could not run."
					onretry={checkAgain}
					retrying={checking}
					bare
					compact
				/>
			{:else}
				<div aria-busy="true">
					<p class="muted" role="status">Checking every stack and {destName}…</p>
					<Skeleton lines={4} />
				</div>
			{/if}
		{:else}
			{@const head = environmentCheckHeadline(preview, destName)}
			<div class="check" aria-busy={checking}>
				<Notice tone={head.tone} title={head.title} live="none">
					{#snippet actions()}
						<Button size="sm" icon={RotateCw} loading={checking} onclick={checkAgain}
							>Check again</Button
						>
					{/snippet}
				</Notice>
				{#if checkError}
					<ErrorState
						error={checkError}
						title="The check could not run again."
						onretry={checkAgain}
						retrying={checking}
						bare
						compact
					/>
				{/if}
				<EnvironmentMigrationCheck
					{preview}
					{sourceName}
					{destName}
					{titleOf}
					own={ownAll}
				/>
			</div>
		{/if}
	{:else if s.id === 'confirm' && preview}
		{@const warnings = warningCount(preview)}
		<div class="confirm">
			<ul class="plain" role="list">
				<li>
					Moves {count(preview.stacks.length, 'stack')} ({formatBytes(
						preview.data.totalBytes
					)}) from {sourceName} to {destName}, group by group.
				</li>
				{#if preview.networks.length}
					<li>
						First creates {count(preview.networks.length, 'network')} on {destName}.
					</li>
				{/if}
				<li>
					The stacks of a group stop together, then move one after the other and start on
					{destName}. A group is unavailable for up to
					{downtimeText(preview.downtime.estimatedSeconds).toLowerCase()}; the other
					groups keep running until their turn.
				</li>
				<li>
					The old copies stay stopped on {sourceName} until you remove them; nothing is deleted
					automatically.
				</li>
				<li>
					If a stack fails to move, the migration stops: that stack and the rest of its
					group run on {sourceName} again, stacks moved before stay on {destName}, and the
					other stacks stay running on {sourceName}. Migrate again to move the rest.
				</li>
			</ul>
			{#if warnings}
				<p class="warn-line">
					<TriangleAlert size={16} strokeWidth={1.75} aria-hidden="true" />
					Starting accepts {count(warnings, 'warning')} from the check.
				</p>
			{/if}
			<Checkbox
				bind:checked={acknowledged}
				label="I understand that each group's stacks are unavailable while it moves."
			/>
		</div>
	{:else if s.id === 'move' && jobId}
		<div class="move">
			<JobProgress
				{jobId}
				title={dest ? `Migrate ${sourceName} to ${destName}` : `Migrate ${sourceName}`}
				onfinish={done}
			/>
			{#if finished}
				{#if rec && outcome}
					<section aria-labelledby="result-title">
						<h3 id="result-title" class="subsection-title">Stacks</h3>
						<ul class="result" role="list">
							{#each rec.stacks as st (st.stackId)}
								{@const ms = moveState(st.state)}
								<li>
									<a href={routes.stack(st.stackId)}
										>{titleOf(st.stackId, st.name)}</a
									>
									<Badge tone={ms.tone} dot>{ms.label}</Badge>
								</li>
							{/each}
						</ul>
					</section>
					{#if outcome.moved.length}
						<Notice
							tone="info"
							title="{count(outcome.moved.length, 'stack')} {outcome.moved.length ===
							1
								? 'runs'
								: 'run'} on {destName} now."
							live="none"
						>
							Their old copies on {sourceName} are stopped and kept. Remove them once you
							are sure.
							{#snippet actions()}
								{#if copies.length && removal === 'idle'}
									<Button
										size="sm"
										variant="danger-soft"
										icon={Trash2}
										onclick={() => (removing = true)}
										>Remove old copies from {sourceName}</Button
									>
								{:else if removal === 'running'}
									<span class="muted" role="status">Removing the old copies…</span
									>
								{/if}
							{/snippet}
						</Notice>
					{/if}
					{#if outcome.left.length}
						<div class="again">
							<p class="muted">
								{count(outcome.left.length, 'stack')}
								{outcome.left.length === 1 ? 'is' : 'are'} still on {sourceName}.
								Fix the cause above, then migrate the rest.
							</p>
							<Button icon={ArrowRightLeft} onclick={migrateRest}
								>Migrate the rest</Button
							>
						</div>
					{:else if everyStackMoved(rec)}
						<div class="again">
							<p class="muted">
								Every stack moved. Archive {sourceName} from its page once you no longer
								need it.
							</p>
							<Button icon={Archive} href={routes.environment(source)}
								>Archive {sourceName}</Button
							>
						</div>
					{/if}
				{:else if record.isError}
					<ErrorState
						error={record.error}
						title="The outcome of each stack could not be loaded."
						onretry={() => record.refetch()}
						bare
						compact
					/>
				{:else}
					<div aria-busy="true"><Skeleton lines={3} /></div>
				{/if}
			{/if}
		</div>
	{/if}
{/snippet}

{#if envs.isPending || stacks.isPending}
	<div aria-busy="true"><Skeleton lines={5} /></div>
{:else if envs.isError && !envs.data}
	<ErrorState
		error={envs.error}
		title="The environments could not be loaded."
		onretry={() => envs.refetch()}
		bare
	/>
{:else if stacks.isError && !stacks.data}
	<ErrorState
		error={stacks.error}
		title="The stacks of {sourceName} could not be loaded."
		onretry={() => stacks.refetch()}
		bare
	/>
{:else if others.length === 0 && !jobId}
	<EmptyState
		icon={ArrowRightLeft}
		color="blue"
		title="Moving an environment needs a second environment."
		description="{sourceName} is the only one. Add another first: run the Docker Agent on another Docker host and enroll it."
		level={3}
		compact
	>
		{#snippet actions()}
			<Button variant="primary" href={routes.addEnvironment()}>Add environment</Button>
			<Button href={routes.environment(source)}>Back to {sourceName}</Button>
		{/snippet}
	</EmptyState>
{:else if choices.length === 0 && !jobId}
	<EmptyState
		icon={ArrowRightLeft}
		color="blue"
		title="No stacks on {sourceName} to migrate."
		description="Stacks you create or import on {sourceName} can be moved from here."
		level={3}
		compact
	>
		{#snippet actions()}
			<Button href={routes.environment(source)}>Back to {sourceName}</Button>
		{/snippet}
	</EmptyState>
{:else}
	<StepWizard
		label="Migrate {sourceName}"
		{steps}
		bind:current
		{step}
		{onnext}
		{canAdvance}
		canGoBack={!jobId}
		{nextLabel}
		finishLabel="Back to {sourceName}"
	/>
{/if}

<DestructiveConfirm
	bind:open={removing}
	title="Remove the old copies from {sourceName}?"
	consequences={[
		`Removes the stopped containers and networks of ${count(copies.length, 'stack')} on ${sourceName}.`,
		`Deletes their migrated volumes and their project folders there.`,
		`Backups of ${sourceName} stay in their repository and remain restorable.`
	]}
	affected={copies.map((c) => ({ label: c.title, detail: 'stopped copy' }))}
	confirmText={sourceName}
	confirmLabel="Remove old copies"
	onconfirm={removeCopies}
/>

<style>
	.form,
	.check,
	.confirm,
	.move {
		display: grid;
		gap: var(--space-5);
	}

	.stacks {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		border: 0;
	}

	.subsection-title {
		margin-bottom: var(--space-2);
	}

	.plain {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding-left: 18px;
	}

	.result {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.result li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
	}

	.again {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-3);
	}

	.warn-line {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--warn);
	}
</style>
