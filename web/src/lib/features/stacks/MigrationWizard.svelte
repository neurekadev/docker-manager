<script lang="ts">
	// Stack migration wizard (#35, #22 high-risk flow): destination, the
	// preflight computed before anything stops (blockers vs warnings,
	// platform and images, conflicts, bind paths, data size against free
	// space, downtime, who gains or loses access, unencrypted transfer),
	// confirmation, then the cold migration's progress with each part's
	// checksum. Afterwards the source stays stopped: remove it, or start it
	// again. Nothing is deleted automatically.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import Play from '@lucide/svelte/icons/play';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { Environment, Job, Schema } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Checkbox,
		ConfirmDialog,
		DestructiveConfirm,
		EmptyState,
		JobProgress,
		Notice,
		RadioGroup,
		StepWizard,
		Table,
		errorView,
		formatBytes,
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
		dependencyOrder,
		downtimeText,
		findingTitle,
		spaceCheck,
		stackTitle,
		type MigrationFinding
	} from './model';
	import { stackKeys, type Stack } from './queries';
	import type { JobTray } from './tray.svelte';

	type Preview = Schema<'MigrationPreview'>;
	type ServicePlan = Schema<'MigrationServicePlan'>;
	type VolumePlan = Schema<'MigrationVolumePlan'>;

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
	const targets = $derived(
		(envs.data ?? []).filter((e) => e.id !== source && e.status === 'active')
	);
	const envName = (id: string) => envs.data?.find((e) => e.id === id)?.name ?? 'the environment';

	let current = $state(0);
	let target = $state('');
	let preview = $state<Preview | null>(null);
	let excluded = $state<string[]>([]);
	let anonymous = $state<string[]>([]);
	let transfer = $state<string[]>([]);
	let acknowledged = $state(false);
	let jobId = $state<string | null>(null);
	let finished = $state<Job | null>(null);
	let rechecking = $state(false);

	const steps: WizardStep[] = [
		{
			id: 'destination',
			label: 'Destination',
			description: 'Where the stack and its volumes go.'
		},
		{
			id: 'preflight',
			label: 'Preflight',
			description:
				'Checked before anything stops. Blockers must be fixed; warnings are accepted by migrating.'
		},
		{ id: 'confirm', label: 'Confirm', description: 'What happens when the migration starts.' },
		{
			id: 'migrate',
			label: 'Migrate',
			description:
				'Cold migration: the source stops, data is copied and verified, the destination starts.'
		}
	];

	function selection(): Schema<'StackMigrationBody'> {
		return {
			targetEnvironmentId: target,
			excludeVolumes: excluded.length ? excluded : undefined,
			anonymousVolumes: anonymous.length ? anonymous : undefined,
			transferImages: transfer.length ? transfer : undefined
		};
	}

	async function runPreview() {
		preview = await previewMigration(stack.id, selection());
	}

	async function recheck() {
		rechecking = true;
		try {
			await runPreview();
		} catch (e) {
			toast.error('The preflight could not run', { body: errorView(e).message });
		} finally {
			rechecking = false;
		}
	}

	let release: (() => void) | null = null;
	async function onnext(step: WizardStep) {
		switch (step.id) {
			case 'destination':
				if (!target) throw new Error('Choose the destination environment.');
				await runPreview();
				return;
			case 'preflight':
				if (!preview?.allowed)
					throw new Error(
						'Fix the blockers first, then check again. Nothing was stopped.'
					);
				return;
			case 'confirm': {
				try {
					const job = await startMigration(stack.id, selection());
					jobId = job.id;
					release = criticalWork.register('other', `Migration of ${title}`);
				} catch (e) {
					const v = errorView(e);
					if (v.code === 'migration_blocked') {
						await runPreview().catch(() => undefined);
						current = 1;
						throw new Error(
							'Something changed: the preflight found blockers again. Review them.',
							{
								cause: e
							}
						);
					}
					throw e;
				}
				return;
			}
			case 'migrate':
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

	// After a failed or cancelled migration: back to the preflight.
	function startOver() {
		jobId = null;
		finished = null;
		acknowledged = false;
		preview = null;
		current = 0;
	}

	// After a completed migration.
	let removing = $state(false);
	let restarting = $state(false);
	let restartResult = $state<SourceRestart | null>(null);
	const copied = $derived((preview?.volumes ?? []).filter((v) => v.action === 'copy'));

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
			case 'preflight':
				return !!preview?.allowed && !rechecking;
			case 'confirm':
				return acknowledged;
			case 'migrate':
				return !!finished;
		}
		return true;
	});
	const nextLabel = $derived(
		steps[current].id === 'destination'
			? 'Run preflight'
			: steps[current].id === 'confirm'
				? 'Start migration'
				: 'Next'
	);

	const ACTION: Record<string, string> = {
		pull: 'Pulled on the destination',
		rebuild: 'Rebuilt from its build section',
		transfer: 'Copied through the manager',
		present: 'Already there'
	};
	const VOLUME: Record<string, string> = {
		copy: 'Copied',
		skip: 'Not copied',
		definition_only: 'Definition only',
		external: 'External'
	};

	const serviceColumns: Column<ServicePlan>[] = [
		{
			id: 'copy',
			header: 'Copy through the manager',
			cell: svcCopy,
			hideHeader: true,
			width: '48px',
			stack: 'actions'
		},
		{ id: 'name', header: 'Service', cell: svcName, stack: 'title', width: '160px' },
		{ id: 'image', header: 'Image', cell: svcImage, mono: true },
		{ id: 'platform', header: 'Platform', cell: svcPlatform, width: '130px' },
		{ id: 'action', header: 'On the destination', cell: svcAction }
	];
	const volumeColumns: Column<VolumePlan>[] = [
		{
			id: 'copy',
			header: 'Copy',
			cell: volCopy,
			hideHeader: true,
			width: '48px',
			stack: 'actions'
		},
		{ id: 'name', header: 'Volume', cell: volName, stack: 'title' },
		{ id: 'size', header: 'Size', cell: volSize, numeric: true, width: '110px' },
		{ id: 'action', header: 'Plan', cell: volAction }
	];

	const sentence = (t: string) => (t ? t.charAt(0).toUpperCase() + t.slice(1) : t);
	const findingKey = (f: MigrationFinding, i: number) =>
		`${f.code}/${f.service ?? ''}/${f.resource ?? ''}/${i}`;
</script>

{#snippet svcName(s: ServicePlan)}<span class="mono strong">{s.name}</span>{/snippet}
{#snippet svcImage(s: ServicePlan)}<span class="ellipsis" title={s.image}>{s.image}</span>{/snippet}
{#snippet svcPlatform(s: ServicePlan)}{s.platform ?? '—'}{/snippet}
{#snippet svcCopy(s: ServicePlan)}
	{#if s.action === 'pull' || transfer.includes(s.image) || (s.action !== 'rebuild' && s.action !== 'present' && s.action !== 'transfer')}
		<Checkbox
			label="Copy {s.image} through the manager instead of pulling it"
			hideLabel
			checked={transfer.includes(s.image)}
			onchange={(e) => {
				const on = (e.currentTarget as HTMLInputElement).checked;
				transfer = on ? [...transfer, s.image] : transfer.filter((x) => x !== s.image);
			}}
		/>
	{/if}
{/snippet}
{#snippet svcAction(s: ServicePlan)}
	{ACTION[s.action] ?? s.action}{#if s.reason}<span class="muted">: {s.reason}</span>{/if}
{/snippet}
{#snippet volCopy(v: VolumePlan)}
	{#if v.anonymous}
		<Checkbox
			label="Copy anonymous volume {v.source}"
			hideLabel
			checked={anonymous.includes(v.source)}
			onchange={(e) => {
				const on = (e.currentTarget as HTMLInputElement).checked;
				anonymous = on ? [...anonymous, v.source] : anonymous.filter((x) => x !== v.source);
			}}
		/>
	{:else if v.action === 'copy' || excluded.includes(v.source)}
		<Checkbox
			label="Copy volume {v.source}"
			hideLabel
			checked={!excluded.includes(v.source)}
			onchange={(e) => {
				const on = (e.currentTarget as HTMLInputElement).checked;
				excluded = on ? excluded.filter((x) => x !== v.source) : [...excluded, v.source];
			}}
		/>
	{/if}
{/snippet}
{#snippet volName(v: VolumePlan)}
	<span class="mono strong">{v.key ?? v.source}</span>
	{#if v.anonymous}<Badge>Anonymous</Badge>{/if}
	<span class="muted mono"> {v.source} → {v.target}</span>
{/snippet}
{#snippet volSize(v: VolumePlan)}{formatBytes(v.bytes)}{v.truncated ? '+' : ''}{/snippet}
{#snippet volAction(v: VolumePlan)}
	{VOLUME[v.action] ?? v.action}{#if v.reason}<span class="muted">: {v.reason}</span>{/if}
{/snippet}

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
					{#if f.service}<span class="mono muted"> {f.service}</span>{/if}
					{#if f.resource}<span class="mono muted"> {f.resource}</span>{/if}
					<p class="f-msg">{sentence(f.message)}</p>
				</div>
			</li>
		{/each}
	</ul>
{/snippet}

{#snippet step(s: WizardStep)}
	{#if s.id === 'destination'}
		{#if targets.length === 0}
			<EmptyState
				icon={ArrowRightLeft}
				color="blue"
				title="There is no other environment to migrate to."
				description="Add an environment first: run the Docker Agent on another Docker host and enroll it."
				level={3}
				compact
			/>
		{:else}
			<RadioGroup
				label="Destination environment"
				bind:value={target}
				options={targets.map((e: Environment) => ({
					value: e.id,
					label: e.name,
					description: e.online ? 'Online' : 'Offline: the preflight needs it online'
				}))}
			/>
			<p class="muted">
				From {sourceEnv?.name ?? 'the current environment'}. The stack keeps its identity,
				revisions and details; its update and backup policies follow it.
			</p>
		{/if}
	{:else if s.id === 'preflight' && preview}
		{@const space = spaceCheck(preview.data)}
		<div class="preflight">
			{#if preview.blockers.length}
				<section aria-labelledby="blockers-title">
					<h3 id="blockers-title">
						{preview.blockers.length}
						{preview.blockers.length === 1 ? 'blocker' : 'blockers'}
					</h3>
					{@render findings(preview.blockers, 'danger')}
				</section>
			{:else}
				<Notice tone="info" title="No blockers."
					>Warnings below are accepted by starting the migration.</Notice
				>
			{/if}

			{#if preview.warnings.length}
				<section aria-labelledby="warnings-title">
					<h3 id="warnings-title">
						{preview.warnings.length}
						{preview.warnings.length === 1 ? 'warning' : 'warnings'}
					</h3>
					{@render findings(preview.warnings, 'warn')}
				</section>
			{/if}

			<dl class="facts">
				<div>
					<dt>Data to copy</dt>
					<dd class="num">
						{formatBytes(preview.data.totalBytes)}{preview.data.truncated
							? ' or more'
							: ''}
						<span class="muted"
							>(project {formatBytes(preview.data.projectBytes)}, volumes {formatBytes(
								preview.data.volumeBytes
							)}, images {formatBytes(preview.data.imageBytes)})</span
						>
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
					<dd>
						{downtimeText(preview.downtime.estimatedSeconds)}
						<span class="muted basis">{sentence(preview.downtime.basis)}</span>
					</dd>
				</div>
				<div>
					<dt>New directory</dt>
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
			</dl>

			<section aria-labelledby="images-title">
				<h3 id="images-title">Services and images</h3>
				<div class="table">
					<Table
						label="Images on the destination"
						rows={preview.services}
						columns={serviceColumns}
						rowKey={(r) => r.name}
					/>
				</div>
				<p class="muted hint">
					Tick an image to copy it through the manager instead of pulling it on {envName(
						target
					)}.
				</p>
			</section>

			{#if preview.volumes.length}
				<section aria-labelledby="volumes-title">
					<h3 id="volumes-title">Volumes</h3>
					<div class="table">
						<Table
							label="Volumes"
							rows={preview.volumes}
							columns={volumeColumns}
							rowKey={(r) => r.source}
						/>
					</div>
				</section>
			{/if}

			<div class="recheck">
				<span class="muted"
					>Changed which images or volumes are copied? Check again to update the findings
					and sizes.</span
				>
				<Button size="sm" loading={rechecking} onclick={recheck}>Check again</Button>
			</div>

			{#if preview.excluded.length}
				<section aria-labelledby="excluded-title">
					<h3 id="excluded-title">Not migrated</h3>
					<ul class="plain" role="list">
						{#each preview.excluded as x (x.name)}<li>
								<span class="mono">{x.name}</span>: {x.reason}
							</li>{/each}
					</ul>
				</section>
			{/if}

			<section aria-labelledby="access-title">
				<h3 id="access-title">Access</h3>
				{#if preview.access.unavailable}
					<p class="muted">{preview.access.unavailable}</p>
				{:else if preview.access.changes.length === 0}
					<p class="muted">
						Nobody gains or loses access: rules on the stack move with it.
					</p>
				{:else}
					<ul class="plain" role="list">
						{#each preview.access.changes as c (c.userId)}
							<li>
								<span class="strong">{c.username}</span>
								{#if c.gained.length}gains {c.gained.join(', ')}{/if}
								{#if c.gained.length && c.lost.length};{/if}
								{#if c.lost.length}loses {c.lost.join(', ')}{/if}
							</li>
						{/each}
					</ul>
				{/if}
				{#if !preview.access.complete && preview.access.othersAffected > 0}
					<p class="muted">
						{preview.access.othersAffected} other users are affected; only the owner sees
						who.
					</p>
				{/if}
			</section>

			{#if preview.leftovers.length}
				<p class="muted">
					Partial data of {preview.leftovers.length} earlier unsuccessful migrations on {envName(
						target
					)} is removed first.
				</p>
			{/if}
		</div>
	{:else if s.id === 'confirm' && preview}
		<div class="confirm">
			<ul class="plain" role="list">
				<li>
					Stops {title} on {envName(source)} in reverse dependency order. It stays down for
					{downtimeText(preview.downtime.estimatedSeconds).toLowerCase()}.
				</li>
				<li>
					Copies the project directory and {copied.length}
					{copied.length === 1 ? 'volume' : 'volumes'} ({formatBytes(
						preview.data.totalBytes
					)}), verifying each part's checksum on both sides.
				</li>
				<li>
					Deploys {title} on {envName(target)} in dependency order and waits for it to be healthy.
				</li>
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
					{preview.warnings.length} preflight {preview.warnings.length === 1
						? 'warning is'
						: 'warnings are'} accepted by starting.
				</p>
			{/if}
			<Checkbox
				bind:checked={acknowledged}
				label="I understand that {title} is unavailable during the migration."
			/>
		</div>
	{:else if s.id === 'migrate' && jobId}
		<div class="migrate">
			<JobProgress {jobId} title="Migrate {title} to {envName(target)}" onfinish={done} />
			{#if finished?.state === 'succeeded'}
				<Notice tone="info" title="{title} runs on {envName(target)} now.">
					Its containers, volumes and files on {envName(source)} are stopped and kept. Remove
					them once you are sure, or start them again. To move the stack back later, migrate
					it back.
					{#snippet actions()}
						<Button
							size="sm"
							icon={Play}
							loading={restarting}
							onclick={() => (restarting = true)}>Restart source</Button
						>
						<Button
							size="sm"
							variant="danger-soft"
							icon={Trash2}
							onclick={() => (removing = true)}>Remove from source</Button
						>
					{/snippet}
				</Notice>
				{#if restartResult}
					<p class="muted">
						Started {restartResult.started.length} containers on {envName(source)}.
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

<ConfirmDialog
	bind:open={restarting}
	title="Start {title} on {envName(source)} again?"
	consequences={[
		`Starts the stopped containers of ${projectName} on ${envName(source)}, dependencies first.`,
		`${title} keeps running on ${envName(target)} too: both copies run and their data diverges.`,
		'To move the stack back, migrate it back instead.'
	]}
	confirmLabel="Restart source"
	onconfirm={startSource}
/>

<DestructiveConfirm
	bind:open={removing}
	title="Remove {title} from {envName(source)}?"
	consequences={[
		`Removes the stopped containers and networks of ${projectName} on ${envName(source)}.`,
		`Deletes the migrated volumes there and the project directory ${projectName}.`,
		`Backups of ${envName(source)} stay in their repository and remain restorable.`
	]}
	affected={copied.map((v) => ({ label: v.source, detail: `volume, ${formatBytes(v.bytes)}` }))}
	confirmText={projectName}
	confirmLabel="Remove from source"
	onconfirm={removeSource}
/>

<style>
	.preflight,
	.confirm,
	.migrate {
		display: grid;
		gap: var(--space-5);
	}

	h3 {
		margin-bottom: var(--space-2);
		font-size: var(--text-control);
		font-weight: var(--weight-semibold);
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

	.basis {
		flex-basis: 100%;
		font-size: var(--text-caption);
	}

	.hint {
		margin-top: var(--space-2);
	}

	.facts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: var(--space-3);
		margin: 0;
	}

	.facts div {
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
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

	.table {
		overflow: hidden;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.recheck {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-2);
		margin-top: var(--space-2);
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

	.ellipsis {
		display: block;
		max-width: 280px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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
