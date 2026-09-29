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
	// source until the user removes them (one request per stack, followed as
	// tracked jobs, one summary toast); what did not move can be migrated
	// again. A running migration of the environment (after a reload, or when
	// the user comes back) opens the wizard at the move step on its progress
	// (the running list, docs/internal/web.md "Job progress after reload");
	// the latest one, once it ended, opens on its result while old copies
	// wait for their removal or stacks it did not move are still on the
	// source (restoredMigration), with "Start a new migration".
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { onDestroy, tick, untrack } from 'svelte';
	import Archive from '@lucide/svelte/icons/archive';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import Plus from '@lucide/svelte/icons/plus';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { Environment, Job } from '$lib/api/client';
	import { containersQuery, environmentsQuery } from '$lib/api/queries';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { bulkSummary, type BulkOutcome } from '$lib/features/resources/bulk';
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
		formatRelative,
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
		jobStackIds,
		migrationEnded,
		migrationOutcome,
		moveState,
		oldCopies,
		oldCopyRemovalMatch,
		oldCopyState,
		ownStackIds,
		pendingCopies,
		restoredMigration,
		resultNotice,
		stackChoices,
		stacksLeft,
		warningCount,
		withOwnFromCheck,
		type EnvironmentMigration,
		type EnvironmentMigrationPreview,
		type EnvironmentMigrationSelection,
		type OldCopy,
		type TitleOf
	} from './environment-migration';
	import {
		previewEnvironmentMigration,
		startEnvironmentMigration,
		startOldCopyRemovals
	} from './migration-actions';
	import {
		environmentMigrationKeys,
		environmentMigrationQuery,
		environmentMigrationsQuery
	} from './queries';

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

	// The latest migrations away from this environment: the ended one the
	// page opens on, and the old copies of every ended one. Keyed under the
	// jobs list, so a removal or a stack migration ending refreshes it.
	const recent = createQuery(() => environmentMigrationsQuery(source));

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
	const onSource = (id: string) =>
		(stacks.data ?? []).some((s) => s.id === id && s.environmentId === source);

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
	// The run shown ended before the page opened: its result, no toast.
	let restored = $state(false);

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
		// A restored result gives way to a migration started since.
		if ((jobId && !restored) || starting || shown.has(job.id)) return;
		follow(job.id);
		current = steps.length - 1;
	}

	function follow(id: string) {
		shown.add(id);
		jobId = id;
		finished = null;
		restored = false;
		release?.();
		release = criticalWork.register('other', `Migration of ${environment.name}`);
	}

	onDestroy(() => release?.());

	// The ended run to open on, decided once when the list first answers
	// (a later refresh never takes the user away from a new migration).
	let decided = false;
	$effect.pre(() => {
		const items = recent.data;
		if (decided || !items || !stacks.data) return;
		untrack(() => {
			decided = true;
			if (jobId || starting || current !== 0) return;
			const r = restoredMigration(items, onSource);
			if (r) restore(r);
		});
	});

	function restore(r: EnvironmentMigration) {
		shown.add(r.id);
		queryClient.setQueryData(environmentMigrationKeys.record(r.id), r);
		jobId = r.id;
		finished = null;
		restored = true;
		current = steps.length - 1;
	}

	// The run's record: where it goes (after a reload) and each stack's
	// state. The list, refreshed by every job event, is the fresher copy
	// once it has the run ended (or while it runs).
	const record = createQuery(() => ({
		...environmentMigrationQuery(source, jobId ?? ''),
		enabled: !!jobId
	}));
	const rec = $derived.by<EnvironmentMigration | undefined>(() => {
		if (!jobId) return undefined;
		const listed = recent.data?.find((r) => r.id === jobId);
		if (listed && (migrationEnded(listed) || !finished)) return listed;
		return record.data?.id === jobId ? record.data : listed;
	});
	// A run goes where its record says (the picker may hold another choice).
	const dest = $derived((jobId && rec?.targetEnvironmentId) || target || '');
	const destName = $derived(dest ? envName(dest) : 'the other environment');
	// The run shown has ended (this page saw it end, or it had before).
	const ended = $derived(!!finished || restored);

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
		if (restored) return; // it ended before the page opened: no toast
		runs.markFinished(job);
		release?.();
		release = null;
		void queryClient.invalidateQueries({ queryKey: environmentMigrationKeys.list(source) });
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

	// Leaves the result for a new run (the run stays in the list).
	function leaveResult() {
		jobId = null;
		finished = null;
		restored = false;
		acknowledged = false;
		preview = null;
		checkError = null;
		deselected = [];
	}

	// The step the user was sent to gets the focus (the button they pressed
	// is gone).
	async function focusStep() {
		await tick();
		document.getElementById('wizard-step-title')?.focus();
	}

	// After a run that left stacks on the source: check again for the rest.
	async function migrateRest() {
		const to = dest;
		leaveResult();
		target = to;
		await queryClient.invalidateQueries({ queryKey: stackKeys.list(source) });
		current = 1;
		void focusStep();
		checkAgain();
	}

	// From a result: a new migration from the first step.
	function startNew() {
		leaveResult();
		target = '';
		current = 0;
		void queryClient.invalidateQueries({ queryKey: stackKeys.list(source) });
		void focusStep();
	}

	const outcome = $derived(rec ? migrationOutcome(rec) : null);
	// This run's stacks still on the source (not those moved since).
	const left = $derived(rec ? stacksLeft(rec, onSource) : []);

	// The old copies of the moved stacks, stopped on the source: this run's
	// and earlier runs' (one per stack). Their removals are tracked jobs, so
	// a reload finds them again; a copy being removed is not offered again.
	const removals = useTrackedJobs(() => oldCopyRemovalMatch(source));
	const beingRemoved = $derived(
		removals.entries.filter((e) => e.active).flatMap((e) => jobStackIds(e.job))
	);
	const allCopies = $derived.by<OldCopy[]>(() => {
		// This run as the wizard knows it (the list may not have its end yet).
		const mine = rec && ended ? oldCopies(rec, titleOf) : [];
		const earlier = pendingCopies(
			(recent.data ?? []).filter((r) => r.id !== rec?.id),
			titleOf
		).filter((c) => !mine.some((m) => m.stackId === c.stackId));
		return [...mine, ...earlier];
	});
	const copies = $derived(allCopies.filter((c) => !beingRemoved.includes(c.stackId)));
	const notice = $derived(
		outcome
			? resultNotice(outcome.moved.length, allCopies.length, {
					source: sourceName,
					destination: destName
				})
			: null
	);
	// Names of the stacks of the listed runs (a removal names its stack).
	const recordNames = $derived<Record<string, string>>(
		Object.fromEntries(
			(recent.data ?? []).flatMap((r) => r.stacks.map((st) => [st.stackId, st.name]))
		)
	);

	let removing = $state(false);
	// The removals started together, for their one summary toast (a reload
	// loses the toast, not the removals: they stay tracked jobs).
	interface RemovalBatch {
		/** Job IDs still running, each with its copy's title. */
		open: Record<string, string>;
		outcome: BulkOutcome;
	}
	let batch: RemovalBatch | null = null;

	async function removeCopies() {
		const { started, refused } = await startOldCopyRemovals(copies);
		for (const { copy, job } of started) removals.add(job, removalTitle(copy.title));
		batch = {
			open: Object.fromEntries(started.map((st) => [st.job.id, st.copy.title])),
			outcome: { succeeded: [], failed: refused.map((c) => c.title), refused: [], skipped: 0 }
		};
		if (!started.length) summarize();
		void queryClient.invalidateQueries({ queryKey: environmentMigrationKeys.list(source) });
	}

	const removalTitle = (title: string) => `Remove the old copy of ${title}`;

	function removalJobTitle(job: Job): string | undefined {
		const id = jobStackIds(job)[0];
		if (!id) return undefined;
		return removalTitle(titleOf(id, recordNames[id] ?? 'a stack'));
	}

	function removalEnded(job: Job) {
		void queryClient.invalidateQueries({ queryKey: environmentMigrationKeys.list(source) });
		if (jobId)
			void queryClient.invalidateQueries({
				queryKey: environmentMigrationKeys.record(jobId)
			});
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
		const title = batch?.open[job.id];
		if (!batch || title === undefined) return;
		(job.state === 'succeeded' ? batch.outcome.succeeded : batch.outcome.failed).push(title);
		delete batch.open[job.id];
		if (!Object.keys(batch.open).length) summarize();
	}

	function summarize() {
		if (!batch) return;
		const o = batch.outcome;
		batch = null;
		const s = bulkSummary('remove', { one: 'old copy', many: 'old copies' }, o);
		const title = o.succeeded.length ? `${s.title} from ${environment.name}` : s.title;
		toast[s.tone](title, s.body ? { body: s.body } : undefined);
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
				return ended;
		}
		return true;
	});
	// Why Next is off (its tooltip).
	const disabledReason = $derived.by(() => {
		switch (steps[current].id) {
			case 'destination':
				if (!environment.online)
					return `${sourceName} is offline: the check needs it online.`;
				if (!target) return 'Choose the destination environment first.';
				return 'Choose at least one stack to migrate.';
			case 'check':
				if (checking || !preview) return 'Wait for the check to finish.';
				return 'Fix the problems first, then check again.';
			case 'confirm':
				return 'Tick the box above to confirm first.';
			case 'move':
				return 'Wait for the migration to finish.';
		}
		return undefined;
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
			{#if restored && rec}
				<p class="muted">
					This migration ended {formatRelative(rec.finishedAt ?? rec.updatedAt)}. What it
					left to do is below.
				</p>
			{/if}
			{#key jobId}
				<JobProgress
					{jobId}
					title={dest ? `Migrate ${sourceName} to ${destName}` : `Migrate ${sourceName}`}
					onfinish={done}
					notices={restored ? null : undefined}
				/>
			{/key}
			{#if ended}
				{#if rec && outcome}
					<section aria-labelledby="result-title">
						<h3 id="result-title" class="subsection-title">Stacks</h3>
						<ul class="result" role="list">
							{#each rec.stacks as st (st.stackId)}
								{@const ms = moveState(st.state)}
								{@const copy = oldCopyState(st)}
								<li>
									<a href={routes.stack(st.stackId)}
										>{titleOf(st.stackId, st.name)}</a
									>
									<span class="outcome">
										{#if copy}<span class="muted caption">{copy}</span>{/if}
										<Badge tone={ms.tone} dot>{ms.label}</Badge>
									</span>
								</li>
							{/each}
						</ul>
					</section>
					{#if notice}
						<Notice tone="info" title={notice.title} live="none">
							{notice.body}
							{#snippet actions()}
								{#if copies.length && !removals.busy}
									<Button
										size="sm"
										variant="danger-soft"
										icon={Trash2}
										onclick={() => (removing = true)}
										>Remove old copies from {sourceName}</Button
									>
								{/if}
							{/snippet}
						</Notice>
					{/if}
					<ActiveJobs
						jobs={removals}
						variant="inline"
						titleOf={removalJobTitle}
						onfinish={removalEnded}
						label="Removing old copies"
					/>
					{#if left.length || everyStackMoved(rec) || movable}
						<div class="again">
							{#if left.length}
								<p class="muted">
									{count(left.length, 'stack')}
									{left.length === 1 ? 'is' : 'are'} still on {sourceName}. Fix
									the cause above, then migrate the rest.
								</p>
							{:else if everyStackMoved(rec) && !movable}
								<p class="muted">
									Every stack moved. Archive {sourceName} from its page once you no
									longer need it.
								</p>
							{:else}
								<p class="muted">
									{sourceName} still has {count(movable, 'stack')} you can migrate.
								</p>
							{/if}
							<div class="buttons">
								{#if left.length}
									<Button icon={ArrowRightLeft} onclick={migrateRest}
										>Migrate the rest</Button
									>
								{:else if everyStackMoved(rec) && !movable}
									<Button icon={Archive} href={routes.environment(source)}
										>Archive {sourceName}</Button
									>
								{/if}
								{#if movable}
									<Button icon={Plus} onclick={startNew}
										>Start a new migration</Button
									>
								{/if}
							</div>
						</div>
					{/if}
				{:else if record.isError && !rec}
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

{#if envs.isPending || stacks.isPending || recent.isPending}
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
		{disabledReason}
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

	.outcome {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: var(--space-3);
	}

	.caption {
		font-size: var(--text-caption);
	}

	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
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
