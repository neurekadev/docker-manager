<script lang="ts">
	// Stack migration wizard (#35, #22 high-risk flow): destination, the
	// check computed before anything stops (problems vs warnings, platform
	// and images, conflicts, bind paths, data size against free space,
	// downtime, who gains or loses access, unencrypted transfer), then
	// confirmation and the move's progress with each part's checksum.
	// Changing which images or volumes are copied runs the check again
	// (debounced) and Next waits for it, so the confirmation and the
	// "Remove from source" list always match what is copied. Afterwards the
	// source stays stopped: remove it, or start it again. Nothing is
	// deleted automatically. A migration needs a second environment; with
	// only one the wizard says so instead of showing its steps.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import Play from '@lucide/svelte/icons/play';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { Environment, Job } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Checkbox,
		ConfirmDialog,
		DestructiveConfirm,
		EmptyState,
		ErrorState,
		JobProgress,
		Notice,
		RadioGroup,
		Skeleton,
		StepWizard,
		Table,
		errorView,
		formatBytes,
		shortId,
		toast,
		type Column,
		type WizardStep
	} from '$lib/ui';
	import {
		previewMigration,
		removeMigrationSource,
		restartSource,
		startMigration,
		type SourceRestart
	} from './actions';
	import {
		accessChangeText,
		canCopyImage,
		checkHeadline,
		copiedVolumes,
		count,
		imageActionLabel,
		migrationBody,
		migrationTargets,
		selectionChanged,
		selectionKey,
		sentence,
		toggled,
		volumeActionLabel,
		volumeChoice,
		type MigrationPreview,
		type MigrationSelection,
		type ServicePlan,
		type VolumePlan
	} from './migration';
	import {
		dependencyOrder,
		downtimeText,
		findingTitle,
		spaceCheck,
		stackTitle,
		type MigrationFinding
	} from './model';
	import { stackKeys, type Stack } from './queries';
	import type { JobTray } from './tray.svelte';

	interface Props {
		stack: Stack;
		tray: JobTray;
	}

	let { stack, tray }: Props = $props();
	const queryClient = useQueryClient();
	// The stack moves: keep what it was when the wizard opened.
	const opened = untrack(() => ({
		title: stackTitle(stack),
		source: stack.environmentId,
		projectName: stack.name,
		order: dependencyOrder(stack.services ?? [])
	}));
	const { title, source, projectName, order } = opened;

	const envs = createQuery(() => environmentsQuery());
	const sourceEnv = $derived(envs.data?.find((e) => e.id === source));
	const targets = $derived(migrationTargets(envs.data, source));
	const envName = (id: string) => envs.data?.find((e) => e.id === id)?.name ?? 'the environment';

	let current = $state(0);
	let target = $state('');
	let preview = $state<MigrationPreview | null>(null);
	let excluded = $state<string[]>([]);
	let anonymous = $state<string[]>([]);
	let transfer = $state<string[]>([]);
	let acknowledged = $state(false);
	let jobId = $state<string | null>(null);
	let finished = $state<Job | null>(null);
	// The selection the check on screen was run for, a check in flight and
	// the last automatic check's failure.
	let checkedKey = $state<string | null>(null);
	let checking = $state(false);
	let checkError = $state<unknown>(null);

	// One destination: nothing to choose.
	$effect(() => {
		if (!target && targets.length === 1) target = targets[0].id;
	});

	const steps: WizardStep[] = [
		{
			id: 'destination',
			label: 'Destination',
			description: 'Where the stack and its data go.'
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
				'The stack stops here, its files and data are copied and checked, then it starts on the destination.'
		}
	];

	function selection(): MigrationSelection {
		return {
			target,
			excluded: [...excluded],
			anonymous: [...anonymous],
			transfer: [...transfer]
		};
	}
	const key = $derived(selectionKey({ target, excluded, anonymous, transfer }));
	const changed = $derived(
		selectionChanged(checkedKey, { target, excluded, anonymous, transfer })
	);

	// Only the latest check's answer is shown.
	let seq = 0;
	async function runCheck() {
		const mine = ++seq;
		const s = selection();
		checking = true;
		checkError = null;
		try {
			const p = await previewMigration(stack.id, migrationBody(s));
			if (mine !== seq) return;
			preview = p;
			checkedKey = selectionKey(s);
		} finally {
			if (mine === seq) checking = false;
		}
	}

	function autoCheck() {
		runCheck().catch((e) => (checkError = e));
	}

	// A changed selection on the check step runs the check again.
	$effect(() => {
		const k = key;
		if (steps[current].id !== 'check' || !preview) return;
		if (k === checkedKey) {
			checkError = null;
			return;
		}
		const t = setTimeout(autoCheck, 600);
		return () => clearTimeout(t);
	});

	let release: (() => void) | null = null;
	async function onnext(step: WizardStep) {
		switch (step.id) {
			case 'destination':
				if (!target) throw new Error('Choose the destination environment.');
				await runCheck();
				return;
			case 'check':
				if (changed || checking)
					throw new Error('Wait for the check to finish with your changes.');
				if (!preview?.allowed)
					throw new Error(
						'Fix the problems first, then check again. Nothing was stopped.'
					);
				return;
			case 'confirm': {
				try {
					const job = await startMigration(stack.id, migrationBody(selection()));
					jobId = job.id;
					release = criticalWork.register('other', `Migration of ${title}`);
				} catch (e) {
					const v = errorView(e);
					if (v.code === 'migration_blocked') {
						await runCheck().catch(() => undefined);
						current = 1;
						throw new Error(
							'Something changed: the check found problems again. Review them.',
							{ cause: e }
						);
					}
					throw e;
				}
				return;
			}
			case 'move':
				void goto(routes.stack(stack.id));
				return false;
		}
	}

	function done(job: Job) {
		finished = job;
		release?.();
		release = null;
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
		if (job.state === 'succeeded') toast.success(`Migrated ${title} to ${envName(target)}`);
		else
			toast.error(`${title} was not migrated`, {
				body: `It stays on ${envName(source)}. The migration page says what failed.`
			});
	}

	// After a failed or cancelled migration: back to the start.
	function startOver() {
		jobId = null;
		finished = null;
		acknowledged = false;
		preview = null;
		checkedKey = null;
		checkError = null;
		current = 0;
	}

	// After a completed migration.
	let removing = $state(false);
	let restarting = $state(false);
	let restartResult = $state<SourceRestart | null>(null);
	const copied = $derived(copiedVolumes(preview?.volumes ?? [], { excluded, anonymous }));

	async function removeSource() {
		if (!jobId) return;
		const job = await removeMigrationSource(stack.id, jobId);
		tray.add(job, {
			title: `Remove ${title} from ${envName(source)}`,
			success: `Removed ${title} from ${envName(source)}`,
			failure: `${title} was not removed from ${envName(source)}`
		});
	}

	async function startSource() {
		restartResult = await restartSource(source, projectName, order);
		if (restartResult.failed.length === 0)
			toast.success(`Started ${title} on ${envName(source)} again`);
		else toast.error(`Some containers of ${title} did not start on ${envName(source)}`);
	}

	const canAdvance = $derived.by(() => {
		switch (steps[current].id) {
			case 'destination':
				return !!target;
			case 'check':
				return !!preview?.allowed && !checking && !changed;
			case 'confirm':
				return acknowledged;
			case 'move':
				return !!finished;
		}
		return true;
	});
	const nextLabel = $derived(
		steps[current].id === 'destination'
			? 'Check destination'
			: steps[current].id === 'confirm'
				? 'Start migration'
				: 'Next'
	);

	const serviceColumns: Column<ServicePlan>[] = [
		{ id: 'name', header: 'Service', cell: svcName, stack: 'title', width: '160px' },
		{
			id: 'image',
			header: 'Image',
			cell: svcImage,
			mono: true,
			maxWidth: '280px',
			truncate: true,
			title: (s) => s.image
		},
		{
			id: 'action',
			header: 'On the destination',
			cell: svcAction,
			title: (s) => (s.reason ? sentence(s.reason) : undefined)
		},
		{ id: 'copy', header: 'Copy image', cell: svcCopy, width: '110px', stack: 'actions' }
	];
	const volumeColumns: Column<VolumePlan>[] = [
		{ id: 'name', header: 'Volume', cell: volName, stack: 'title' },
		{ id: 'size', header: 'Size', cell: volSize, numeric: true, width: '110px' },
		{
			id: 'action',
			header: 'What happens',
			cell: volAction,
			title: (v) => (v.reason ? sentence(v.reason) : undefined)
		},
		{ id: 'copy', header: 'Copy data', cell: volCopy, width: '110px', stack: 'actions' }
	];

	const findingKey = (f: MigrationFinding, i: number) =>
		`${f.code}/${f.service ?? ''}/${f.resource ?? ''}/${i}`;
</script>

{#snippet svcName(s: ServicePlan)}<span class="strong">{s.name}</span>{/snippet}
{#snippet svcImage(s: ServicePlan)}{s.image}{/snippet}
{#snippet svcCopy(s: ServicePlan)}
	{#if canCopyImage(s, transfer)}
		<Checkbox
			label="Copy {s.image} through Docker Manager instead of downloading it"
			hideLabel
			checked={transfer.includes(s.image)}
			onchange={(e) =>
				(transfer = toggled(
					transfer,
					s.image,
					(e.currentTarget as HTMLInputElement).checked
				))}
		/>
	{:else}<span class="muted" aria-hidden="true">—</span>
	{/if}
{/snippet}
{#snippet svcAction(s: ServicePlan)}{imageActionLabel(s.action)}{/snippet}
{#snippet volCopy(v: VolumePlan)}
	{@const choice = volumeChoice(v, excluded)}
	{#if choice === 'anonymous'}
		<Checkbox
			label="Copy the data of anonymous volume {shortId(v.source)}"
			hideLabel
			checked={anonymous.includes(v.source)}
			onchange={(e) =>
				(anonymous = toggled(
					anonymous,
					v.source,
					(e.currentTarget as HTMLInputElement).checked
				))}
		/>
	{:else if choice === 'named'}
		<Checkbox
			label="Copy the data of volume {v.key ?? v.source}"
			hideLabel
			checked={!excluded.includes(v.source)}
			onchange={(e) =>
				(excluded = toggled(
					excluded,
					v.source,
					!(e.currentTarget as HTMLInputElement).checked
				))}
		/>
	{:else}<span class="muted" aria-hidden="true">—</span>
	{/if}
{/snippet}
{#snippet volName(v: VolumePlan)}
	{#if v.anonymous}
		<span class="strong">Anonymous volume</span>
		<span class="muted mono" title={v.source}>{shortId(v.source)}</span>
	{:else}
		<span class="strong" title={v.source}>{v.key ?? v.source}</span>
	{/if}
{/snippet}
{#snippet volSize(v: VolumePlan)}{formatBytes(v.bytes)}{v.truncated ? '+' : ''}{/snippet}
{#snippet volAction(v: VolumePlan)}{volumeActionLabel(v.action)}{/snippet}

{#snippet findings(list: MigrationFinding[], tone: 'danger' | 'warn')}
	<ul class="findings {tone}" role="list">
		{#each list as f, i (findingKey(f, i))}
			<li>
				{#if tone === 'danger'}<CircleAlert
						size={16}
						strokeWidth={1.75}
						aria-hidden="true"
					/>{:else}<TriangleAlert size={16} strokeWidth={1.75} aria-hidden="true" />{/if}
				<div>
					<span class="f-title">{findingTitle(f.code)}</span>
					{#if f.service}<span class="muted"> · {f.service}</span>{/if}
					<p class="f-msg">{sentence(f.message)}</p>
				</div>
			</li>
		{/each}
	</ul>
{/snippet}

{#snippet step(s: WizardStep)}
	{#if s.id === 'destination'}
		{#if targets.length === 1}
			{@const only = targets[0]}
			<p>
				{title} moves from {sourceEnv?.name ?? 'the current environment'} to
				<span class="strong">{only.name}</span>, the only other environment.
			</p>
			{#if !only.online}
				<Notice tone="offline" title="{only.name} is offline.">
					The check needs it online. Try again when it has reconnected.
				</Notice>
			{/if}
		{:else}
			<RadioGroup
				label="Destination environment"
				bind:value={target}
				options={targets.map((e: Environment) => ({
					value: e.id,
					label: e.name,
					description: e.online ? 'Online' : 'Offline: it must be online for the check'
				}))}
			/>
			<p class="muted">From {sourceEnv?.name ?? 'the current environment'}.</p>
		{/if}
		<p class="muted">
			The stack keeps its name, history and details; its update and backup policies follow it.
		</p>
	{:else if s.id === 'check' && preview}
		{@const head = checkHeadline(preview)}
		{@const space = spaceCheck(preview.data)}
		<div class="check" aria-busy={checking}>
			<Notice tone={head.tone} title={head.title} live="none" />
			{#if checking || changed}
				<p class="muted" role="status">Checking again with your changes…</p>
			{/if}
			{#if checkError}
				<ErrorState
					error={checkError}
					title="The check could not run again with your changes."
					onretry={autoCheck}
					retrying={checking}
					bare
					compact
				/>
			{/if}

			{#if preview.blockers.length}
				<section aria-labelledby="blockers-title">
					<h3 id="blockers-title" class="subsection-title">To fix before moving</h3>
					{@render findings(preview.blockers, 'danger')}
				</section>
			{/if}

			<dl class="facts">
				<div>
					<dt>Data to copy</dt>
					<dd class="num">
						{formatBytes(preview.data.totalBytes)}{preview.data.truncated
							? ' or more'
							: ''}
					</dd>
				</div>
				<div>
					<dt>Free on {envName(target)}</dt>
					<dd class="num">
						{#if space === 'unknown'}Unknown{:else}
							{formatBytes(
								Math.min(
									preview.data.destinationStacksFree,
									preview.data.destinationVolumesFree
								)
							)}
							{#if space === 'short'}<Badge tone="danger" dot>Not enough</Badge
								>{:else}<Badge tone="ok" dot>Enough</Badge>{/if}
						{/if}
					</dd>
				</div>
				<div>
					<dt>Downtime</dt>
					<dd>{downtimeText(preview.downtime.estimatedSeconds)}</dd>
				</div>
			</dl>

			{#if preview.warnings.length}
				<Disclosure
					summary="Show {count(preview.warnings.length, 'warning')}"
					open={preview.warnings.length <= 2}
				>
					{@render findings(preview.warnings, 'warn')}
				</Disclosure>
			{/if}

			{#if preview.volumes.length}
				<section aria-labelledby="volumes-title">
					<h3 id="volumes-title" class="subsection-title">Data</h3>
					<div class="table">
						<Table
							label="Volumes"
							rows={preview.volumes}
							columns={volumeColumns}
							rowKey={(r) => r.source}
						/>
					</div>
					<p class="muted hint">
						Untick a volume to start it empty on {envName(target)} instead of copying its
						data.
					</p>
				</section>
			{/if}

			{#if preview.access.changes.length}
				<section aria-labelledby="access-title">
					<h3 id="access-title" class="subsection-title">Access changes</h3>
					<ul class="plain" role="list">
						{#each preview.access.changes as c (c.userId)}
							<li><span class="strong">{c.username}</span> {accessChangeText(c)}</li>
						{/each}
					</ul>
				</section>
			{/if}

			<Disclosure summary="Images of {count(preview.services.length, 'service')}">
				<div class="table">
					<Table
						label="Images on the destination"
						rows={preview.services}
						columns={serviceColumns}
						rowKey={(r) => r.name}
					/>
				</div>
				<p class="muted hint">
					Tick an image to copy it through Docker Manager instead of downloading it on {envName(
						target
					)}.
				</p>
			</Disclosure>

			<Disclosure summary="More details">
				<dl class="details">
					<div>
						<dt>Data to copy</dt>
						<dd>
							Project files {formatBytes(preview.data.projectBytes)}, volumes {formatBytes(
								preview.data.volumeBytes
							)}, images {formatBytes(preview.data.imageBytes)}
						</dd>
					</div>
					<div>
						<dt>How the downtime was estimated</dt>
						<dd>{sentence(preview.downtime.basis)}</dd>
					</div>
					<div>
						<dt>New folder on {envName(target)}</dt>
						<dd class="mono">
							{preview.targetDirectory ?? preview.projectName ?? projectName}
						</dd>
					</div>
					{#if preview.transport.bandwidthLimitBytesPerSecond > 0}
						<div>
							<dt>Bandwidth limit</dt>
							<dd class="num">
								{formatBytes(preview.transport.bandwidthLimitBytesPerSecond)}/s
							</dd>
						</div>
					{/if}
					<div>
						<dt>Access</dt>
						<dd>
							{#if preview.access.unavailable}
								{sentence(preview.access.unavailable)}
							{:else if preview.access.changes.length === 0}
								Nobody gains or loses access: rules on the stack move with it.
							{:else}
								{count(preview.access.changes.length, 'user')} listed above.
							{/if}
							{#if !preview.access.complete && preview.access.othersAffected > 0}
								{count(preview.access.othersAffected, 'other user')} affected; only the
								owner sees who.
							{/if}
						</dd>
					</div>
					{#if preview.excluded.length}
						<div>
							<dt>Not migrated</dt>
							<dd>
								<ul class="plain" role="list">
									{#each preview.excluded as x (x.name)}<li>
											<span class="mono">{x.name}</span>: {x.reason}
										</li>{/each}
								</ul>
							</dd>
						</div>
					{/if}
					{#if preview.leftovers.length}
						<div>
							<dt>Earlier attempts</dt>
							<dd>
								Partial data of {count(
									preview.leftovers.length,
									'earlier unsuccessful migration'
								)} on {envName(target)} is removed first.
							</dd>
						</div>
					{/if}
				</dl>
			</Disclosure>
		</div>
	{:else if s.id === 'confirm' && preview}
		<div class="confirm">
			<ul class="plain" role="list">
				<li>
					Stops {title} on {envName(source)}. It is unavailable for
					{downtimeText(preview.downtime.estimatedSeconds).toLowerCase()}.
				</li>
				<li>
					Copies its files and {count(copied.length, 'volume')} ({formatBytes(
						preview.data.totalBytes
					)}) to {envName(target)} and checks every part on both sides.
				</li>
				<li>Starts {title} on {envName(target)} and waits for it to be healthy.</li>
				<li>
					Leaves everything on {envName(source)} stopped and untouched. You remove it or start
					it again afterwards; nothing is deleted automatically.
				</li>
				<li>
					If anything fails before the end, {title} goes back to {envName(source)} and starts
					again.
				</li>
			</ul>
			{#if preview.warnings.length}
				<p class="warn-line">
					<TriangleAlert size={16} strokeWidth={1.75} aria-hidden="true" />
					Starting accepts {count(preview.warnings.length, 'warning')} from the check.
				</p>
			{/if}
			<Checkbox
				bind:checked={acknowledged}
				label="I understand that {title} is unavailable during the migration."
			/>
		</div>
	{:else if s.id === 'move' && jobId}
		<div class="move">
			<JobProgress {jobId} title="Migrate {title} to {envName(target)}" onfinish={done} />
			{#if finished?.state === 'succeeded'}
				<Notice tone="info" title="{title} runs on {envName(target)} now.">
					Its containers, volumes and files on {envName(source)} are stopped and kept. Remove
					them once you are sure, or start them again. To move the stack back later, migrate
					it back.
					{#snippet actions()}
						<Button size="sm" icon={Play} onclick={() => (restarting = true)}
							>Start again on {envName(source)}</Button
						>
						<Button
							size="sm"
							variant="danger-soft"
							icon={Trash2}
							onclick={() => (removing = true)}>Remove from {envName(source)}</Button
						>
					{/snippet}
				</Notice>
				{#if restartResult}
					<p class="muted">
						Started {count(restartResult.started.length, 'container')} on {envName(
							source
						)}.
						{#each restartResult.failed as f (f.container)}<br /><span class="err"
								>{f.container}: {f.message}</span
							>{/each}
					</p>
				{/if}
			{:else if finished}
				<div class="again">
					<p class="muted">
						Nothing was deleted: {title} stays on {envName(source)}. Fix the cause
						above, then try again.
					</p>
					<Button onclick={startOver}>Start over</Button>
				</div>
			{/if}
		</div>
	{/if}
{/snippet}

{#if envs.isPending}
	<div aria-busy="true"><Skeleton lines={5} /></div>
{:else if envs.isError && !envs.data}
	<ErrorState
		error={envs.error}
		title="The environments could not be loaded."
		onretry={() => envs.refetch()}
		bare
	/>
{:else if targets.length === 0 && !jobId}
	<EmptyState
		icon={ArrowRightLeft}
		color="blue"
		title="Moving a stack needs a second environment."
		description="{sourceEnv?.name ??
			'This environment'} is the only one. Add another first: run the Docker Agent on another Docker host and enroll it."
		level={3}
		compact
	>
		{#snippet actions()}
			<Button variant="primary" href={routes.addEnvironment()}>Add environment</Button>
			<Button href={routes.stack(stack.id)}>Back to {title}</Button>
		{/snippet}
	</EmptyState>
{:else}
	<StepWizard
		label="Migrate {title}"
		{steps}
		bind:current
		{step}
		{onnext}
		{canAdvance}
		canGoBack={!jobId}
		{nextLabel}
		finishLabel="Open the stack"
	/>
{/if}

<ConfirmDialog
	bind:open={restarting}
	title="Start {title} on {envName(source)} again?"
	consequences={[
		`Starts the stopped containers of ${projectName} on ${envName(source)}, dependencies first.`,
		`${title} keeps running on ${envName(target)} too: both copies run and their data drifts apart.`,
		'To move the stack back, migrate it back instead.'
	]}
	confirmLabel="Start again on {envName(source)}"
	onconfirm={startSource}
/>

<DestructiveConfirm
	bind:open={removing}
	title="Remove {title} from {envName(source)}?"
	consequences={[
		`Removes the stopped containers and networks of ${projectName} on ${envName(source)}.`,
		`Deletes the migrated volumes there and the project folder ${projectName}.`,
		`Backups of ${envName(source)} stay in their repository and remain restorable.`
	]}
	affected={copied.map((v) => ({ label: v.source, detail: `volume, ${formatBytes(v.bytes)}` }))}
	confirmText={projectName}
	confirmLabel="Remove from {envName(source)}"
	onconfirm={removeSource}
/>

<style>
	.check,
	.confirm,
	.move {
		display: grid;
		gap: var(--space-5);
	}

	.subsection-title {
		margin-bottom: var(--space-2);
	}

	.findings {
		display: grid;
		gap: var(--space-2);
		margin: 0;
	}

	.findings li {
		display: flex;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
	}

	.findings.danger :global(svg) {
		flex: none;
		margin-top: 2px;
		color: var(--danger);
	}

	.findings.warn :global(svg) {
		flex: none;
		margin-top: 2px;
		color: var(--warn);
	}

	.f-title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.f-msg {
		color: var(--text-default);
	}

	.again {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-3);
	}

	.hint {
		margin-top: var(--space-2);
	}

	.facts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: var(--space-3);
		margin: 0;
	}

	.facts div {
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	.details {
		display: grid;
		gap: var(--space-3);
		margin: 0;
	}

	dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	dd {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin: 2px 0 0;
		color: var(--text-strong);
	}

	.details dd {
		display: block;
		color: var(--text-default);
	}

	.table {
		overflow: hidden;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.plain {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding-left: 18px;
	}

	.strong {
		color: var(--text-strong);
	}

	.warn-line {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--warn);
	}

	.err {
		color: var(--danger);
	}
</style>
