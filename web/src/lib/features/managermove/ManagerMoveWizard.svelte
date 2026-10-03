<script lang="ts">
	// Settings → Move to a new server (the old manager, owner;
	// docs/internal/architecture/manager-move.md, "The flow"). Three steps:
	// New server (both addresses → "Create setup files": the new server's
	// compose.yaml and .env, and the same as one command to paste, shown
	// once and kept only in this component's memory, then a live checklist
	// until its agent connected and its Docker Manager waits; files that are
	// gone or expired are replaced with "Create new setup files"), Check (the
	// environment migration's check of the apps next to Docker Manager,
	// EnvironmentMigrationCheck, and the reminders) and Move ("Move
	// everything": the apps move, then Docker Manager hands itself over).
	// Everything is read from the move, which the live stream keeps current
	// (topic manager), so a reload or coming back opens where the move
	// stands; the manager.move job is tracked too (the running list brings a
	// running one back). After the handoff the moved panel says where to
	// point DNS and offers "Resume on this server" (type-to-confirm); ending
	// a move that restarts Docker Manager waits until it answers again and
	// reloads the page. On a manager that arrived by a move, Move complete
	// shows until everything is done.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { onDestroy, tick, untrack } from 'svelte';
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
		AGENT_KEPT,
		cancelConsequences,
		BEFORE_YOU_START,
		MOVE_STEPS,
		ONLY_MANAGER_MOVES,
		SETUP_FOLDER,
		canMoveEverything,
		isActive,
		isHandedOver,
		moveButtonLabel,
		moveCompleteDone,
		moveStatus,
		movedPanel,
		newServerChecklist,
		newServerReady,
		onlyManagerMoves,
		restartsWhenEnded,
		runView,
		setupFilesConsequences,
		setupFilesNotice,
		setupFilesReason,
		setupScript,
		stepOf,
		type CreatedManagerMove,
		type ManagerMove,
		type MoveStepId,
		type RunPhase
	} from './model';
	import {
		cancelMove,
		createMove,
		createSetupFiles,
		managerAnswers,
		managerMoveDefaultsQuery,
		managerMoveJobMatch,
		managerMoveKeys,
		managerMoveQuery,
		startMoveRun
	} from './queries';
	import { waitForRestart } from './restart';

	const qc = useQueryClient();
	// Live events of the move (topic manager) keep it current: no polling.
	const move = createQuery(() => managerMoveQuery());
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
	const script = $derived(files ? setupScript(files) : '');
	// Files gone (a reload) or expired: offer new ones.
	const reason = $derived(active ? setupFilesReason(active, !!files) : null);
	const reasonNotice = $derived(reason ? setupFilesNotice(reason) : null);
	let renewing = $state(false);

	// Focus: after a step changes by itself (the move went on elsewhere or
	// ended) or new files appear, the step's heading (or the files' one)
	// takes the focus, unless the person is busy elsewhere on the page (a
	// dialog, another control).
	let root = $state<HTMLElement>();
	async function focusIn(selector: string) {
		await tick();
		const now = document.activeElement;
		if (now && now !== document.body && !root?.contains(now)) return;
		root?.querySelector<HTMLElement>(selector)?.focus();
	}
	const focusStep = () => focusIn('#wizard-step-title');
	let focusedFiles: CreatedManagerMove | null = null;
	$effect(() => {
		const f = files;
		if (!f || f === untrack(() => focusedFiles)) return;
		focusedFiles = f;
		void focusIn('#files-title');
	});

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
			} else if (!isActive(data) && current !== 0) {
				current = 0;
				void focusStep();
			} else if (isActive(data) && data.state !== 'open' && current < want) {
				current = want;
				void focusStep();
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

	// New setup files: a new pairing code (the old files stop working) and,
	// unless the new server's agent enrolled, a new enrollment token.
	async function renewFiles() {
		const c = await createSetupFiles();
		created = c;
		void qc.invalidateQueries({ queryKey: managerMoveKeys.current });
		toast.success('Created new setup files');
	}

	async function start() {
		const job = await startMoveRun();
		runs.add(job, 'Move Everything');
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
	// Why the main button is off (its tooltip).
	const disabledReason = $derived.by(() => {
		switch (stepId) {
			case 'server':
				return "Wait until the new server's agent is connected and its Docker Manager is waiting.";
			case 'check':
				if (checking) return 'Wait for the check to finish.';
				return preview
					? 'Fix the problems the check found, then check again.'
					: 'Run the check again.';
			case 'move':
				return 'Your apps are moving. Wait until the move finishes.';
		}
		return undefined;
	});
	const nextLabel = $derived(stepId === 'server' && !active ? 'Create Setup Files' : 'Next');
	const canStop = $derived(!!active && moveStatus(active).stop === 'cancel');

	// Cancel (before the handoff) and resume here (after it).
	let cancelling = $state(false);
	let resuming = $state(false);
	const instanceName = $derived(instance.data?.name ?? '');

	// Ending a move after agents heard the new address restarts Docker
	// Manager: wait until it answers again, then reload the page.
	let restarting = $state<'no' | 'waiting' | 'slow'>('no');
	const leaving = new AbortController();
	onDestroy(() => leaving.abort());
	async function followRestart() {
		restarting = 'waiting';
		const outcome = await waitForRestart({
			probe: () => managerAnswers(leaving.signal),
			sleep: (ms) => new Promise((done) => setTimeout(done, ms)),
			now: () => Date.now(),
			signal: leaving.signal
		});
		if (outcome === 'up') location.reload();
		else if (outcome === 'timeout') restarting = 'slow';
	}

	async function cancel() {
		const restarts = !!active && restartsWhenEnded(active);
		const m = await cancelMove();
		created = null;
		preview = null;
		own = [];
		submitted = false;
		if (restarts) {
			toast.success('Cancelled the move', {
				body: 'Docker Manager restarts now. This page reloads by itself when it is back.'
			});
			void followRestart();
			return;
		}
		qc.setQueryData(managerMoveKeys.current, m);
		void qc.invalidateQueries({ queryKey: managerMoveKeys.current });
		toast.success('Cancelled the move');
		void focusStep();
	}

	async function resume() {
		await cancelMove({ resumeHere: true, instanceName });
		toast.success('Resumed Docker Manager on this server', {
			body: 'It restarts now. This page reloads by itself when it is back.'
		});
		void followRestart();
	}
</script>

{#snippet serverStep()}
	{#if !active}
		<div class="form">
			<TextField
				label="This Server's Address"
				bind:value={thisAddr}
				required
				autocomplete="off"
				description="IP address or host name. The new server connects to it."
				error={submitted && !thisAddr.trim()
					? "Enter this server's address."
					: fieldError(createError, 'body.thisServerAddress')}
			/>
			<TextField
				label="New Server's Address"
				bind:value={newAddr}
				required
				autocomplete="off"
				placeholder="192.168.1.20"
				description="IP address or host name."
				error={submitted && !newAddr.trim()
					? "Enter the new server's address."
					: fieldError(createError, 'body.newServerAddress')}
			/>
			<TextField
				label="Name for the New Server"
				bind:value={newName}
				autocomplete="off"
				optional
				description="Defaults to its address."
				error={fieldError(createError, 'body.newEnvironmentName')}
			/>
		</div>
	{:else}
		<div class="server">
			{#if reason && reasonNotice}
				<Notice
					tone={reason === 'expired' ? 'warn' : 'info'}
					title={reasonNotice.title}
					live="none"
				>
					{reasonNotice.body}
					{#snippet actions()}
						<Button size="sm" icon={RotateCw} onclick={() => (renewing = true)}
							>Create New Setup Files</Button
						>
					{/snippet}
				</Notice>
			{/if}
			{#if files}
				<Notice
					tone="warn"
					icon={KeyRound}
					title="These files are shown once. They contain a one-time pairing code."
					live="none"
				>
					Save them now. If you lose them, you can create new ones here.
				</Notice>
				{#if files.agentEnrolled}
					<Notice
						tone="info"
						title="The agent on the new server stays connected."
						live="none">{AGENT_KEPT}</Notice
					>
				{/if}
				<section class="files" aria-labelledby="files-title">
					<h3 id="files-title" class="subsection-title" tabindex="-1">
						Set Up the New Server
					</h3>
					<InstallCommand
						title="compose.yaml"
						description="Save this as compose.yaml in an empty folder on the new server."
						command={files.composeYaml}
						what="compose.yaml"
						filename="compose.yaml"
					/>
					<InstallCommand
						title=".env"
						description="Save this as .env in the same folder. If your browser drops the dot from the downloaded file's name, rename it to .env."
						command={files.env}
						what=".env"
						filename=".env"
					/>
					<InstallCommand
						title="Start Command"
						description="Then run this in that folder:"
						command="docker compose up -d"
					/>
				</section>
				<section class="files" aria-labelledby="script-title">
					<h3 id="script-title" class="subsection-title">
						Or Paste This on the New Server
					</h3>
					<InstallCommand
						title="One Command"
						description="Creates the folder {SETUP_FOLDER} with both files and starts Docker Manager."
						command={script}
						what="command"
					/>
				</section>
			{/if}
			<section aria-labelledby="checklist-title">
				<h3 id="checklist-title" class="subsection-title">On the New Server</h3>
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
							>Check Again</Button
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
				<h3 id="before-title" class="subsection-title">Before You Start</h3>
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
						label="Apps Moved"
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
					Try Again moves what is left. Apps that already moved stay on {destName}.
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

<div class="wizard-root" bind:this={root}>
	{#if restarting !== 'no'}
		<Card>
			{#if restarting === 'waiting'}
				<div class="restarting" role="status" aria-busy="true">
					<p class="busy strong"><Spinner /> Restarting Docker Manager…</p>
					<p class="muted">
						This page reloads by itself as soon as Docker Manager answers again.
					</p>
				</div>
			{:else}
				<Notice tone="warn" title="Docker Manager does not answer yet." live="status">
					It may still be starting. Reload the page in a moment.
					{#snippet actions()}
						<Button size="sm" icon={RotateCw} onclick={() => location.reload()}
							>Reload</Button
						>
					{/snippet}
				</Notice>
			{/if}
		</Card>
	{:else if move.isPending}
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
						Follow it at <a
							href={panel.statusUrl}
							target="_blank"
							rel="noopener noreferrer">{panel.statusUrl}</a
						>.
					{/if}
				</Notice>
				<p class="busy">
					{#if handedOver.state === 'handed_off'}
						<Spinner /> Handed over. Waiting for the new Docker Manager to confirm.
					{:else}
						<CircleCheck size={16} strokeWidth={1.75} aria-hidden="true" /> Handed over. The
						new Docker Manager confirmed.
					{/if}
				</p>
				{#if handedOver.state === 'handed_off' && instanceName}
					<section class="resume" aria-labelledby="resume-title">
						<h3 id="resume-title" class="subsection-title">
							The new server did not start?
						</h3>
						<p class="muted">
							Only if Docker Manager on the new server never started with the copy,
							you can go back to this server.
						</p>
						<div>
							<Button
								variant="danger-soft"
								icon={Undo2}
								onclick={() => (resuming = true)}>Resume on This Server</Button
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
				label="Move to a New Server"
				{steps}
				bind:current
				{step}
				{onnext}
				{canAdvance}
				{disabledReason}
				canGoBack={phase === 'idle' || phase === 'failed'}
				{nextLabel}
				finishLabel={moveButtonLabel(phase)}
				oncancel={canStop ? () => (cancelling = true) : undefined}
				cancelLabel="Cancel the Move"
			/>
		</Card>
	{/if}
</div>

{#if active}
	<ConfirmDialog
		bind:open={renewing}
		title="Create new setup files?"
		consequences={setupFilesConsequences(active)}
		confirmLabel="Create New Setup Files"
		onconfirm={renewFiles}
	/>
{/if}

<ConfirmDialog
	bind:open={cancelling}
	title="Cancel the move?"
	consequences={cancelConsequences(active)}
	confirmLabel="Cancel the Move"
	cancelLabel="Keep the Move"
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
	confirmLabel="Resume on This Server"
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

	.files,
	.restarting {
		display: grid;
		gap: var(--space-3);
	}

	.files h3 {
		outline: none;
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
