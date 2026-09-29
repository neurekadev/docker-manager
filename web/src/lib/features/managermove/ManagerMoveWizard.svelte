<script lang="ts">
	// Settings → Move to a new server (the old manager, owner;
	// docs/internal/architecture/manager-move.md, "The flow"). Three steps:
	// New server (both addresses → "Create setup files": the new server's
	// compose.yaml and .env, shown once and kept only in this component's
	// memory, then a live checklist until its agent connected and its Docker
	// Manager waits), Check (the environment migration's check of the apps
	// next to Docker Manager, EnvironmentMigrationCheck, and the reminders)
	// and Move ("Move everything": the apps move, then Docker Manager hands
	// itself over). Everything is read from the move (polled every 3 s while
	// it changes), so a reload or coming back opens where the move stands;
	// the manager.move job is tracked too (the running list brings a running
	// one back). After the handoff the moved panel says where to point DNS
	// and offers "Resume on this server" (type-to-confirm); on a manager that
	// arrived by a move, Move complete shows until everything is done.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Truck from '@lucide/svelte/icons/truck';
	import Undo2 from '@lucide/svelte/icons/undo-2';
	import EnvironmentMigrationCheck from '$lib/features/environments/EnvironmentMigrationCheck.svelte';
	import InstallCommand from '$lib/features/environments/InstallCommand.svelte';
	import {
		environmentCheckHeadline,
		withOwnFromCheck,
		type EnvironmentMigrationPreview,
		type TitleOf
	} from '$lib/features/environments/environment-migration';
	import { previewEnvironmentMigration } from '$lib/features/environments/migration-actions';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { instanceSettingsQuery } from '$lib/features/settings/queries';
	import {
		Button,
		Card,
		ConfirmDialog,
		DestructiveConfirm,
		ErrorState,
		Meter,
		Notice,
		Skeleton,
		Spinner,
		StepWizard,
		TextField,
		fieldError,
		toast,
		type WizardStep
	} from '$lib/ui';
	import MoveCompleteCard from './MoveCompleteCard.svelte';
	import {
		BEFORE_YOU_START,
		MOVE_STEPS,
		ONLY_MANAGER_MOVES,
		canMoveEverything,
		isActive,
		isHandedOver,
		isPolled,
		moveButtonLabel,
		moveCompleteDone,
		moveStatus,
		movedPanel,
		newServerChecklist,
		newServerReady,
		oldManagerSettled,
		onlyManagerMoves,
		runView,
		stepOf,
		type CreatedManagerMove,
		type ManagerMove,
		type MoveStepId,
		type RunPhase
	} from './model';
	import {
		MOVE_POLL_MS,
		cancelMove,
		createMove,
		managerMoveDefaultsQuery,
		managerMoveJobMatch,
		managerMoveKeys,
		managerMoveQuery,
		startMoveRun
	} from './queries';

	const qc = useQueryClient();
	const move = createQuery(() => ({
		...managerMoveQuery(),
		refetchInterval: (q) => {
			const m = q.state.data;
			if (isPolled(m?.state)) return MOVE_POLL_MS;
			return m?.state === 'arrived' && !oldManagerSettled(m) ? 15_000 : false;
		}
	}));
	const active = $derived(isActive(move.data) ? move.data : null);
	const arrived = $derived(
		move.data?.state === 'arrived' && !moveCompleteDone(move.data) ? move.data : null
	);
	const handedOver = $derived(isHandedOver(move.data) ? (move.data as ManagerMove) : null);
	const defaults = createQuery(() => ({
		...managerMoveDefaultsQuery(),
		enabled: move.data !== undefined && !active
	}));
	const instance = createQuery(() => instanceSettingsQuery());
	const publicUrl = $derived(instance.data?.deployment.publicUrl);

	// A running Move everything (after a reload the running list has it).
	const runs = useTrackedJobs(() => managerMoveJobMatch);
	const jobRunning = $derived(runs.running.length > 0);
	// The job started or ended: read the move at once.
	let seenRunning = -1;
	$effect(() => {
		const n = runs.running.length;
		untrack(() => {
			if (seenRunning >= 0 && n !== seenRunning)
				void qc.invalidateQueries({ queryKey: managerMoveKeys.current });
			seenRunning = n;
		});
	});

	// The form.
	let thisAddr = $state('');
	let newAddr = $state('');
	let newName = $state('');
	let submitted = $state(false);
	let createError = $state<unknown>(null);
	$effect(() => {
		const d = defaults.data?.thisServerAddress;
		if (d && !untrack(() => thisAddr)) thisAddr = d;
	});

	// The new server's files: shown once, only in this component's memory.
	let created = $state<CreatedManagerMove | null>(null);
	const files = $derived(created && active && created.move.id === active.id ? created : null);

	const steps: WizardStep[] = MOVE_STEPS.map((s) => ({ ...s }));
	const indexOf = (id: MoveStepId) => steps.findIndex((s) => s.id === id);
	let current = $state(0);
	const stepId = $derived(steps[current].id as MoveStepId);

	// Open where the move stands; follow a run started elsewhere; go back
	// to the start when the move ended.
	let placed = false;
	$effect(() => {
		const data = move.data;
		if (data === undefined) return;
		untrack(() => {
			const want = indexOf(stepOf(data));
			if (!placed) {
				placed = true;
				current = want;
			} else if (!isActive(data)) {
				current = 0;
			} else if (data.state !== 'open' && current < want) {
				current = want;
			}
		});
	});

	// The check: the apps next to Docker Manager → the new server.
	const source = $derived(active?.sourceEnvironment);
	const target = $derived(active?.newServer?.environmentId);
	const sourceName = $derived(source?.name ?? 'this server');
	const destName = $derived(active?.newServer?.environmentName || 'the new server');
	const titleOf: TitleOf = (_id, name) => name;
	let preview = $state<EnvironmentMigrationPreview | null>(null);
	let checking = $state(false);
	let checkError = $state<unknown>(null);
	let own = $state<string[]>([]);
	const onlyManager = $derived(!!active && onlyManagerMoves(active, preview));

	let seq = 0;
	async function runCheck() {
		const from = source;
		const to = target;
		if (!from || from.stackCount === 0 || !to) return;
		const mine = ++seq;
		checking = true;
		checkError = null;
		try {
			const p = await previewEnvironmentMigration(from.environmentId, {
				targetEnvironmentId: to
			});
			if (mine !== seq) return;
			preview = p;
			own = withOwnFromCheck(own, p);
		} finally {
			if (mine === seq) checking = false;
		}
	}

	function checkAgain() {
		runCheck().catch((e) => (checkError = e));
	}

	// Coming back on the check step (Back from Move) without an answer: check.
	$effect(() => {
		if (stepId !== 'check') return;
		untrack(() => {
			if (!preview && !checking && !checkError) checkAgain();
		});
	});

	const run = $derived(active ? runView(active) : null);
	const phase = $derived.by((): RunPhase => {
		const p = run?.phase ?? 'idle';
		return jobRunning && (p === 'idle' || p === 'failed') ? 'moving' : p;
	});

	async function create() {
		submitted = true;
		createError = null;
		if (!thisAddr.trim() || !newAddr.trim())
			throw new Error('Enter both addresses, then create the setup files.');
		try {
			const c = await createMove({
				thisServerAddress: thisAddr.trim(),
				newServerAddress: newAddr.trim(),
				newEnvironmentName: newName.trim() || undefined
			});
			created = c;
			preview = null;
			own = [];
			qc.setQueryData(managerMoveKeys.current, c.move);
			void qc.invalidateQueries({ queryKey: managerMoveKeys.current });
		} catch (e) {
			createError = e;
			throw e;
		}
	}

	async function start() {
		const job = await startMoveRun();
		runs.add(job, 'Move everything');
		await qc.invalidateQueries({ queryKey: managerMoveKeys.current });
	}

	async function onnext(step: WizardStep) {
		switch (step.id as MoveStepId) {
			case 'server':
				if (!active) {
					await create();
					return false;
				}
				if (!newServerReady(active)) throw new Error('Wait until the new server is ready.');
				if (!preview) checkAgain();
				return;
			case 'check':
				if (checking) throw new Error('Wait for the check to finish.');
				if (!active || !canMoveEverything(active, preview))
					throw new Error(
						'Fix the problems first, then check again. Nothing was stopped.'
					);
				return;
			case 'move':
				await start();
				return false;
		}
	}

	const canAdvance = $derived.by(() => {
		switch (stepId) {
			case 'server':
				return !active || newServerReady(active);
			case 'check':
				return !!active && !checking && canMoveEverything(active, preview);
			case 'move':
				return phase === 'idle' || phase === 'failed';
		}
		return true;
	});
	const nextLabel = $derived(stepId === 'server' && !active ? 'Create setup files' : 'Next');
	const canStop = $derived(!!active && moveStatus(active).stop === 'cancel');

	// Cancel (before the handoff) and resume here (after it).
	let cancelling = $state(false);
	let resuming = $state(false);
	const instanceName = $derived(instance.data?.name ?? '');

	async function cancel() {
		const m = await cancelMove();
		created = null;
		preview = null;
		own = [];
		submitted = false;
		qc.setQueryData(managerMoveKeys.current, m);
		void qc.invalidateQueries({ queryKey: managerMoveKeys.current });
		toast.success('Cancelled the move');
	}

	async function resume() {
		const m = await cancelMove({ resumeHere: true, instanceName });
		qc.setQueryData(managerMoveKeys.current, m);
		toast.success('Resumed Docker Manager on this server', {
			body: 'It restarts now. Reload the page in a minute.'
		});
	}
</script>

{#snippet serverStep()}
	{#if !active}
		<div class="form">
			<TextField
				label="This server's address"
				bind:value={thisAddr}
				required
				autocomplete="off"
				description="This server's IP address or host name on your network. The new server connects to it."
				error={submitted && !thisAddr.trim()
					? "Enter this server's address."
					: fieldError(createError, 'body.thisServerAddress')}
			/>
			<TextField
				label="New server's address"
				bind:value={newAddr}
				required
				autocomplete="off"
				placeholder="192.168.1.20"
				description="The new server's IP address on your network"
				error={submitted && !newAddr.trim()
					? "Enter the new server's address."
					: fieldError(createError, 'body.newServerAddress')}
			/>
			<TextField
				label="Name for the new server"
				bind:value={newName}
				autocomplete="off"
				description="Optional. How the new server shows in Docker Manager. Leave it empty to use its address."
				error={fieldError(createError, 'body.newEnvironmentName')}
			/>
		</div>
	{:else}
		<div class="server">
			{#if files}
				<Notice
					tone="warn"
					icon={KeyRound}
					title="These files are shown once. They contain a one-time pairing code."
					live="none"
				/>
				<InstallCommand
					title="compose.yaml"
					description=""
					command={files.composeYaml}
					what="compose.yaml"
					filename="compose.yaml"
				/>
				<InstallCommand
					title=".env"
					description=""
					command={files.env}
					what=".env"
					filename=".env"
				/>
				<InstallCommand
					title="Start command"
					description="On the new server, save both files in an empty folder and run:"
					command="docker compose up -d"
				/>
			{:else}
				<Notice
					tone="info"
					title="The setup files were shown when you created the move."
					live="none"
				>
					If you no longer have them, cancel the move and start again.
				</Notice>
			{/if}
			<section aria-labelledby="checklist-title">
				<h3 id="checklist-title" class="subsection-title">On the new server</h3>
				<ul class="checklist" role="list">
					{#each newServerChecklist(active) as item (item.label)}
						<li class:done={item.done}>
							<span class="mark" aria-hidden="true">
								{#if item.done}<CircleCheck
										size={18}
										strokeWidth={1.75}
									/>{:else}<Spinner size={16} />{/if}
							</span>
							<span>
								{item.label}<span class="sr-only"
									>{item.done ? ': done' : ': waiting'}</span
								>
								{#if item.detail}<span class="detail">{item.detail}</span>{/if}
							</span>
						</li>
					{/each}
				</ul>
			</section>
		</div>
	{/if}
{/snippet}

{#snippet checkStep()}
	{#if active}
		<div class="check" aria-busy={checking}>
			{#if onlyManager}
				<Notice tone="info" title={ONLY_MANAGER_MOVES} live="none" />
			{:else if !preview}
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
				<EnvironmentMigrationCheck {preview} {sourceName} {destName} {titleOf} {own} />
			{/if}
			<section class="before" aria-labelledby="before-title">
				<h3 id="before-title" class="subsection-title">Before you start</h3>
				<ul class="plain" role="list">
					{#each BEFORE_YOU_START as line (line)}<li>{line}</li>{/each}
					{#if active.statusUrl}
						<li>
							Follow the move at <a
								href={active.statusUrl}
								target="_blank"
								rel="noopener noreferrer">{active.statusUrl}</a
							> meanwhile.
						</li>
					{/if}
				</ul>
			</section>
		</div>
	{/if}
{/snippet}

{#snippet moveStep()}
	{#if active && run}
		<div class="move">
			{#if phase === 'idle'}
				<p>
					{#if onlyManager}{ONLY_MANAGER_MOVES}{:else}Your apps move to {destName} first, group
						by group. Each group is unavailable while it moves.{/if}
					Then this Docker Manager becomes read-only and hands itself over to the new server.
				</p>
			{:else if phase === 'moving'}
				{#if run.total > 0}
					<Meter
						role="progressbar"
						label="Apps moved"
						value={run.moved}
						max={run.total}
						valueText="{run.moved} of {run.total}"
						tone="neutral"
						size="md"
					/>
				{/if}
				<div role="status">
					<p class="strong">{run.title}</p>
					{#if run.detail}<p class="muted">{run.detail}</p>{/if}
				</div>
			{:else if phase === 'handing_over'}
				<div role="status">
					<p class="busy strong"><Spinner /> {run.title}</p>
					{#if run.detail}<p class="muted">{run.detail}</p>{/if}
				</div>
			{:else if phase === 'failed'}
				<Notice tone="danger" title={run.title} live="alert">{run.recovery}</Notice>
				<p class="muted">
					Try again moves what is left. Apps that already moved stay on {destName}.
				</p>
			{/if}
		</div>
	{/if}
{/snippet}

{#snippet step(s: WizardStep)}
	{#if s.id === 'server'}
		{@render serverStep()}
	{:else if s.id === 'check'}
		{@render checkStep()}
	{:else if s.id === 'move'}
		{@render moveStep()}
	{/if}
{/snippet}

{#if move.isPending}
	<Card><div aria-busy="true"><Skeleton lines={5} /></div></Card>
{:else if move.isError && move.data === undefined}
	<ErrorState
		error={move.error}
		title="The move could not be loaded."
		onretry={() => move.refetch()}
	/>
{:else if handedOver}
	{@const panel = movedPanel(handedOver, publicUrl)}
	<Card>
		<div class="moved">
			<Notice tone="info" icon={Truck} title={panel.title} live="status">
				{panel.body}
				{#if panel.statusUrl}
					Follow it at <a href={panel.statusUrl} target="_blank" rel="noopener noreferrer"
						>{panel.statusUrl}</a
					>.
				{/if}
			</Notice>
			<p class="busy">
				{#if handedOver.state === 'handed_off'}
					<Spinner /> Handed over. Waiting for the new Docker Manager to confirm.
				{:else}
					<CircleCheck size={16} strokeWidth={1.75} aria-hidden="true" /> Handed over. The new
					Docker Manager confirmed.
				{/if}
			</p>
			{#if handedOver.state === 'handed_off' && instanceName}
				<section class="resume" aria-labelledby="resume-title">
					<h3 id="resume-title" class="subsection-title">
						The new server did not start?
					</h3>
					<p class="muted">
						Only if Docker Manager on the new server never started with the copy, you
						can go back to this server.
					</p>
					<div>
						<Button variant="danger-soft" icon={Undo2} onclick={() => (resuming = true)}
							>Resume on this server</Button
						>
					</div>
				</section>
			{/if}
		</div>
	</Card>
{:else if arrived}
	<MoveCompleteCard move={arrived} />
{:else}
	<Card>
		<StepWizard
			label="Move to a new server"
			{steps}
			bind:current
			{step}
			{onnext}
			{canAdvance}
			canGoBack={phase === 'idle' || phase === 'failed'}
			{nextLabel}
			finishLabel={moveButtonLabel(phase)}
			oncancel={canStop ? () => (cancelling = true) : undefined}
			cancelLabel="Cancel the move"
		/>
	</Card>
{/if}

<ConfirmDialog
	bind:open={cancelling}
	title="Cancel the move?"
	consequences={[
		'Docker Manager stays on this server and works as before.',
		'Apps that already moved stay on the new server.',
		'The setup files stop working. Stop Docker Manager on the new server and remove its folder there.'
	]}
	confirmLabel="Cancel the move"
	cancelLabel="Keep the move"
	tone="danger"
	onconfirm={cancel}
/>

<DestructiveConfirm
	bind:open={resuming}
	title="Resume Docker Manager on this server?"
	consequences={[
		'Only do this if Docker Manager on the new server never started with the copy. Two copies must never manage the same servers.',
		'This Docker Manager unlocks and restarts. Agents that already follow the new server come back here.',
		'Apps that moved stay on the new server.'
	]}
	confirmText={instanceName}
	confirmLabel="Resume on this server"
	onconfirm={resume}
/>

<style>
	.form,
	.server,
	.check,
	.move,
	.moved {
		display: grid;
		gap: var(--space-5);
	}

	.subsection-title {
		margin-bottom: var(--space-2);
	}

	.checklist {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.checklist li {
		display: flex;
		align-items: flex-start;
		gap: var(--space-2);
		color: var(--text-strong);
	}

	.mark {
		display: grid;
		flex: none;
		place-items: center;
		width: 18px;
		height: 20px;
		color: var(--text-muted);
	}

	.done .mark {
		color: var(--ok);
	}

	.detail {
		display: block;
		color: var(--text-muted);
	}

	.plain {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding-left: 18px;
	}

	.before {
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	.busy {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	.strong {
		color: var(--text-strong);
	}

	.resume {
		display: grid;
		gap: var(--space-2);
	}
</style>
