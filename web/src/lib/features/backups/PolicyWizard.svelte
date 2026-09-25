<script lang="ts">
	// Backup policy wizard (#10): destination → scope (manager state, stacks
	// with volume and path rules, standalone volumes; previewed by the
	// agents) → container shutdown (off by default; affected containers,
	// stop order and downtime previewed) → schedule (#13, off until turned
	// on) → retention (preview, recovery floor) → a first test backup.
	// The policy is saved disabled after the destination step, so previews
	// can run; nothing is backed up before the last step or its schedule.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Checkbox,
		CronField,
		JobProgress,
		Notice,
		Select,
		Skeleton,
		StepWizard,
		Switch,
		TextField,
		toast
	} from '$lib/ui';
	import ChoiceGrid from '$lib/features/common/ChoiceGrid.svelte';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import {
		environmentName,
		ifMatch,
		newIdempotencyKey,
		stacksQuery
	} from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import { defaultSchedule, scheduleDefaultsQuery } from '$lib/features/common/schedules';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import RetentionEditor from './RetentionEditor.svelte';
	import ScopePreviewView from './ScopePreviewView.svelte';
	import StackScopeEditor from './StackScopeEditor.svelte';
	import VolumePicker from './VolumePicker.svelte';
	import {
		repositoryLocation,
		type BackupPolicy,
		type BackupRetention,
		type PolicyInput,
		type ScopePreview,
		type StackSelection,
		type VolumeSelection
	} from './model';
	import { backupKeys, repositoriesQuery } from './queries';

	let { policy: initial, owner }: { policy?: BackupPolicy; owner: boolean } = $props();

	const qc = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const stacks = createQuery(() => stacksQuery());
	const schedDefaults = createQuery(() => scheduleDefaultsQuery());
	const envName = (id: string) => environmentName(envs.data, id);

	const p0 = untrack(() => initial);
	let policy = $state<BackupPolicy | null>(p0 ?? null);
	let name = $state(p0?.name ?? '');
	let repositoryId = $state(p0?.repositoryId ?? '');
	let envRepos = $state<Record<string, string>>({ ...(p0?.environmentRepositories ?? {}) });
	let includeManager = $state(p0?.includeManagerState ?? untrack(() => owner));
	let includeMetrics = $state(p0?.includeMetrics ?? false);
	let selStacks = $state<StackSelection[]>(p0?.stacks ?? []);
	let selVolumes = $state<VolumeSelection[]>(p0?.volumes ?? []);
	let shutdown = $state(p0?.shutdown ?? false);
	let enabled = $state(p0?.schedule?.enabled ?? false);
	let cron = $state(p0?.schedule?.cron ?? '');
	let zone = $state(p0?.schedule?.timeZone ?? '');
	let retention = $state<BackupRetention>(
		p0?.retention ?? { daily: 7, weekly: 4, monthly: 6, minKeep: 3 }
	);
	let current = $state(0);
	let touched = $state(false);
	let error = $state<unknown>(null);
	let scope = $state<ScopePreview | null>(null);
	let scopeLoading = $state(false);
	let scopeError = $state<string | null>(null);
	let saved = $state(false);
	let runJobs = $state<Job[]>([]);
	let running = $state(false);

	$effect(() => {
		if (zone || schedDefaults.isPending) return;
		const d = defaultSchedule('backup', schedDefaults.data);
		untrack(() => {
			if (!cron) cron = d.cron;
			zone = d.timeZone;
		});
	});

	// Any change of the draft counts as an unsaved edit until the policy is saved.
	let baseline: string | null = null;
	$effect(() => {
		const now = JSON.stringify([
			name,
			repositoryId,
			envRepos,
			includeManager,
			includeMetrics,
			selStacks,
			selVolumes,
			shutdown,
			enabled,
			cron,
			zone,
			retention
		]);
		untrack(() => {
			if (baseline === null || saved) baseline = now;
			else if (now !== baseline) touched = true;
		});
	});

	useUnsaved(
		() => `Backup policy ${name || 'draft'}`,
		() => touched && !saved
	);

	const readyRepos = $derived((repos.data ?? []).filter((r) => r.state === 'ready'));
	const repoOptions = $derived(
		readyRepos.map((r) => ({
			value: r.id,
			label: `${r.name} (${repositoryLocation(r, envName)})`
		}))
	);
	const primary = $derived(readyRepos.find((r) => r.id === repositoryId));
	const pendingRepos = $derived((repos.data ?? []).filter((r) => r.state !== 'ready'));

	const stacksList = $derived((stacks.data ?? []).filter((s) => s.view === 'full'));
	const stacksByEnv = $derived.by(() => {
		const m: Record<string, typeof stacksList> = {};
		for (const s of stacksList) (m[s.environmentId] ??= []).push(s);
		return m;
	});
	const activeEnvs = $derived((envs.data ?? []).filter((e) => e.status !== 'archived'));
	const involvedEnvs = $derived([
		...new Set([
			...selStacks
				.map((s) => stacksList.find((x) => x.id === s.stackId)?.environmentId)
				.filter((x): x is string => !!x),
			...selVolumes.map((v) => v.environmentId)
		])
	]);

	function envRepoOptions(envId: string) {
		return [
			{
				value: '',
				label:
					primary?.kind === 's3'
						? `Same as the policy (${primary.name})`
						: 'Choose a repository'
			},
			...readyRepos
				.filter((r) => r.kind === 's3' || r.executor === envId)
				.map((r) => ({
					value: r.id,
					label: `${r.name} (${repositoryLocation(r, envName)})`
				}))
		];
	}
	function needsEnvRepo(envId: string): boolean {
		const chosen = envRepos[envId];
		if (chosen) return false;
		return !primary || (primary.kind === 'local' && primary.executor !== envId);
	}

	function isSelected(id: string) {
		return selStacks.some((s) => s.stackId === id);
	}
	function toggleStack(id: string, on: boolean) {
		touched = true;
		selStacks = on
			? [...selStacks, { stackId: id, anonymousVolumes: false }]
			: selStacks.filter((s) => s.stackId !== id);
		scope = null;
	}
	function setOptIn(stackId: string, path: string, on: boolean) {
		touched = true;
		selStacks = selStacks.map((s) =>
			s.stackId !== stackId
				? s
				: {
						...s,
						externalPaths: on
							? [...new Set([...(s.externalPaths ?? []), path])]
							: (s.externalPaths ?? []).filter((x) => x !== path)
					}
		);
	}

	function draft(): PolicyInput {
		const er: Record<string, string> = {};
		for (const [k, v] of Object.entries(envRepos)) if (v) er[k] = v;
		return {
			name: name.trim(),
			repositoryId,
			environmentRepositories: er,
			includeManagerState: includeManager,
			includeMetrics: includeManager && includeMetrics,
			shutdown,
			stacks: selStacks,
			volumes: selVolumes,
			schedule: { cron, timeZone: zone, enabled },
			retention
		};
	}

	async function previewScope() {
		if (!policy) return;
		scopeLoading = true;
		scopeError = null;
		try {
			scope = await unwrap(
				api.POST('/api/v1/backup-policies/{policyId}/scope-previews', {
					params: { path: { policyId: policy.id } },
					body: { draft: draft() }
				})
			);
		} catch (e) {
			scopeError = actionError(e);
		} finally {
			scopeLoading = false;
		}
	}

	async function save(fields?: Partial<PolicyInput>) {
		if (!policy) return;
		const body = fields ?? draft();
		policy = await unwrap(
			api.PATCH('/api/v1/backup-policies/{policyId}', {
				params: {
					path: { policyId: policy.id },
					header: { 'If-Match': ifMatch(policy.revision) }
				},
				body
			})
		);
		qc.setQueryData(backupKeys.policy(policy.id), policy);
		await qc.invalidateQueries({ queryKey: ['policies', 'list'] });
	}

	const steps = [
		{
			id: 'destination',
			label: 'Destination',
			description: 'Name the policy and choose where its backups go.'
		},
		{
			id: 'scope',
			label: 'What to back up',
			description: 'The manager state, stacks and volumes, with their rules.'
		},
		{
			id: 'shutdown',
			label: 'Consistency',
			description: 'Back up live, or stop the affected containers meanwhile.'
		},
		{ id: 'schedule', label: 'Schedule', description: 'When backups run automatically.' },
		{ id: 'retention', label: 'Retention', description: 'Which old backups are forgotten.' },
		{ id: 'test', label: 'First backup', description: 'Save the policy and try it once.' }
	];

	const canAdvance = $derived(
		current === 0
			? !!name.trim() && !!repositoryId
			: current === 1
				? (includeManager || selStacks.length > 0 || selVolumes.length > 0) &&
					involvedEnvs.every((e) => !needsEnvRepo(e))
				: current === 3
					? !!cron.trim()
					: current === 4
						? !(
								retention.minKeep !== undefined &&
								retention.minKeep < 1 &&
								Object.keys(retention).length > 1
							)
						: true
	);

	async function onnext(step: { id: string }) {
		error = null;
		try {
			if (step.id === 'destination') {
				if (!policy) {
					policy = await unwrap(
						api.POST('/api/v1/backup-policies', {
							body: {
								name: name.trim(),
								repositoryId,
								includeManagerState: includeManager
							}
						})
					);
					await qc.invalidateQueries({ queryKey: ['policies', 'list'] });
				} else {
					await save({ name: name.trim(), repositoryId });
				}
			}
			if (step.id === 'shutdown' && shutdown && !scope?.shutdown) await previewScope();
			if (step.id === 'retention') {
				await save();
				saved = true;
				touched = false;
			}
		} catch (e) {
			error = e;
			throw new Error(
				Object.keys(fieldErrors(e)).length
					? Object.values(fieldErrors(e)).join(' ')
					: actionError(e, {
							backup_policy_name_taken:
								'Another backup policy has this name. Choose a different name.',
							recovery_key_not_confirmed:
								'Confirm the Recovery Key of every repository this policy uses before turning its schedule on.'
						}),
				{ cause: e }
			);
		}
	}

	async function runFirst() {
		if (!policy) return;
		running = true;
		try {
			const run = await unwrap(
				api.POST('/api/v1/backup-policies/{policyId}/runs', {
					params: {
						path: { policyId: policy.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: {}
				})
			);
			runJobs = run.jobs;
			toast.info(`Started a backup of ${policy.name}`);
		} catch (e) {
			toast.error('The backup did not start', { body: actionError(e) });
		} finally {
			running = false;
		}
	}

	async function finish() {
		if (!policy) return;
		toast.success(`Saved backup policy ${policy.name}`, {
			body: enabled ? undefined : 'Its schedule is off: backups run only when you start them.'
		});
		await goto(routes.backupPolicy(policy.id));
	}

	// Scope previews go stale when the selection changes.
	$effect(() => {
		void selStacks;
		void selVolumes;
		void includeManager;
		untrack(() => (scope = null));
	});
</script>

<StepWizard
	label={initial ? 'Edit backup policy' : 'Create backup policy'}
	{steps}
	bind:current
	{onnext}
	onfinish={finish}
	{canAdvance}
	nextLabel={current === 0 && !policy ? 'Create policy' : current === 4 ? 'Save policy' : 'Next'}
	finishLabel="Done"
>
	{#snippet step(s)}
		{#if s.id === 'destination'}
			<Fields>
				<TextField
					label="Name"
					bind:value={name}
					required
					placeholder="Nightly"
					error={fieldErrors(error)['body.name']}
				/>
				{#if repos.isPending}
					<Skeleton lines={1} height="36px" />
				{:else if readyRepos.length === 0}
					<Notice tone="warn" title="No repository is ready" live="none">
						Add a backup repository and confirm its Recovery Key first.
						{#snippet actions()}<Button size="sm" href={routes.backupRepositoryNew()}
								>Add repository</Button
							>{/snippet}
					</Notice>
				{:else}
					<Select
						label="Repository"
						options={repoOptions}
						bind:value={repositoryId}
						placeholder="Choose a repository"
						description="Backups of the manager state go here; environments can use their own below."
						required
					/>
				{/if}
				{#each pendingRepos as r (r.id)}
					<p class="muted small">
						{r.name} is not listed: its Recovery Key is not confirmed yet.
					</p>
				{/each}
				{#if !policy}
					<p class="muted small">
						Creating saves the policy with its schedule off. Nothing is backed up yet.
					</p>
				{/if}
			</Fields>
		{:else if s.id === 'scope'}
			<Fields>
				{#if owner}
					<FieldGroup
						legend="Manager"
						hint="The manager's database and settings, needed to recover DockYard itself."
					>
						<Switch label="Back up the manager state" bind:checked={includeManager} />
						{#if includeManager}
							<Switch
								label="Include the metrics database"
								description="Off by default: charts history is large and can be rebuilt."
								bind:checked={includeMetrics}
							/>
						{/if}
					</FieldGroup>
				{/if}
				<FieldGroup
					legend="Stacks"
					hint="The project directory with its Compose files, .env and relative bind data, plus the stack's named volumes."
				>
					{#if stacks.isPending}
						<Skeleton lines={3} height="20px" />
					{:else if stacksList.length === 0}
						<p class="muted">No stacks you can back up.</p>
					{/if}
					{#each Object.entries(stacksByEnv) as [envId, list] (envId)}
						<p class="env">{envName(envId)}</p>
						<ChoiceGrid min="200px">
							{#each list as st (st.id)}
								<Checkbox
									label={st.displayName || st.name}
									checked={isSelected(st.id)}
									onchange={(e) => toggleStack(st.id, e.currentTarget.checked)}
								/>
							{/each}
						</ChoiceGrid>
					{/each}
				</FieldGroup>
				{#each selStacks as sel, i (sel.stackId)}
					{@const st = stacksList.find((x) => x.id === sel.stackId)}
					{#if st}
						<StackScopeEditor
							bind:value={selStacks[i]}
							stack={st}
							onchange={() => (touched = true)}
						/>
					{/if}
				{/each}
				<FieldGroup
					legend="Standalone volumes"
					hint="Named volumes that belong to no stack."
				>
					{#each activeEnvs.filter((e) => e.online) as e (e.id)}
						<VolumePicker
							environmentId={e.id}
							environmentName={e.name}
							selected={selVolumes}
							onchange={(v) => {
								selVolumes = v;
								touched = true;
							}}
						/>
					{/each}
				</FieldGroup>
				{#if involvedEnvs.length}
					<FieldGroup
						legend="Repositories per environment"
						hint="A local repository only holds data of its own host; S3 repositories hold everything."
					>
						{#each involvedEnvs as envId (envId)}
							<Select
								label="Repository for {envName(envId)}"
								options={envRepoOptions(envId)}
								bind:value={envRepos[envId]}
								error={needsEnvRepo(envId)
									? `${primary?.name ?? 'The policy’s repository'} can't hold ${envName(envId)}'s data. Choose a repository on ${envName(envId)} or an S3 repository.`
									: null}
							/>
						{/each}
					</FieldGroup>
				{/if}
				<div class="preview-bar">
					<Button onclick={previewScope} loading={scopeLoading} disabled={!policy}
						>Preview what gets backed up</Button
					>
					<span class="muted small"
						>Each environment's agent resolves the sources; nothing is stored.</span
					>
				</div>
				{#if scopeError}<Notice
						tone="danger"
						title="The preview could not be computed"
						live="alert">{scopeError}</Notice
					>{/if}
				{#if scope}
					<ScopePreviewView
						preview={scope}
						showShutdown={false}
						optedIn={(id, path) =>
							!!selStacks
								.find((x) => x.stackId === id)
								?.externalPaths?.includes(path)}
						onOptIn={setOptIn}
					/>
				{/if}
			</Fields>
		{:else if s.id === 'shutdown'}
			<Fields>
				<Switch
					label="Stop containers during backups"
					description="Off by default. On: the containers using the data stop in reverse dependency order and the ones that were running start again afterwards, also after a failure. DockYard's own containers never stop."
					bind:checked={shutdown}
					onchange={() => {
						touched = true;
						scope = null;
					}}
				/>
				{#if shutdown}
					<Notice
						tone="warn"
						icon={TriangleAlert}
						title="Services are down while their data is backed up"
						live="none"
					>
						Review the containers and the order below.
					</Notice>
					<div class="preview-bar">
						<Button onclick={previewScope} loading={scopeLoading}
							>Preview the shutdown</Button
						>
					</div>
					{#if scopeError}<Notice
							tone="danger"
							title="The preview could not be computed"
							live="alert">{scopeError}</Notice
						>{/if}
					{#if scope?.shutdown}<ScopePreviewView preview={scope} />{/if}
				{:else}
					<p class="muted">
						Backups run while the containers keep running (crash-consistent). Databases
						are usually safe to back up this way only if they tolerate a power loss;
						stop them for a consistent copy.
					</p>
				{/if}
			</Fields>
		{:else if s.id === 'schedule'}
			<Fields>
				<Switch
					label="Back up automatically"
					description="Off: backups run only when you start them. Turning it on needs every repository's Recovery Key confirmed."
					bind:checked={enabled}
					onchange={() => (touched = true)}
				/>
				{#if zone}<CronField
						label="Schedule"
						kind="backup"
						bind:cron
						bind:timeZone={zone}
					/>{/if}
			</Fields>
		{:else if s.id === 'retention'}
			<RetentionEditor
				bind:value={retention}
				policyId={policy?.id}
				onchange={() => (touched = true)}
			/>
		{:else if s.id === 'test'}
			<Fields>
				<Notice tone="info" title="The policy is saved" live="status">
					{enabled
						? 'Its schedule is on.'
						: 'Its schedule is off: backups run only when you start them.'}
					Run it once now to check that everything can be read and stored.
				</Notice>
				{#if runJobs.length === 0}
					<div>
						<Button variant="primary" loading={running} onclick={runFirst}
							>Run first backup</Button
						>
					</div>
				{/if}
				{#each runJobs as j (j.id)}
					<JobProgress
						jobId={j.id}
						title="Back up {j.environmentId ? envName(j.environmentId) : 'the manager'}"
					/>
				{/each}
			</Fields>
		{/if}
	{/snippet}
</StepWizard>

<style>
	.small {
		font-size: var(--text-caption);
	}

	.env {
		color: var(--text-muted);
		font-size: var(--text-caption);
		margin-top: var(--space-1);
	}

	.preview-bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
	}
</style>
