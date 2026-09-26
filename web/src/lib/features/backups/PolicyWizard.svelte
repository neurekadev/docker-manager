<script lang="ts">
	// Backup policy wizard (#10), shown in BackupPolicyDialog: destination →
	// scope (manager state; every managed stack and volume in scope,
	// included until unchecked, stack volumes too; anonymous volumes only
	// when turned on; previewed by the agents) → container shutdown (off by
	// default; affected containers, stop order and downtime previewed) →
	// schedule (#13, off until turned on) → retention (preview, recovery
	// floor). Nothing is saved before the last step: a new policy is
	// created with every setting at once (optionally followed by a first
	// backup), an edited one is saved once; previews of a new policy use
	// the draft preview.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Checkbox,
		CronField,
		Notice,
		Select,
		Skeleton,
		StepWizard,
		Switch,
		TextField,
		toast
	} from '$lib/ui';
	import CoverageList from '$lib/features/common/CoverageList.svelte';
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
	import VolumeCoverage from './VolumeCoverage.svelte';
	import {
		repositoryLocation,
		type BackupPolicy,
		type BackupRetention,
		type PolicyInput,
		type ScopePreview
	} from './model';
	import { backupKeys, backupPoliciesQuery, repositoriesQuery } from './queries';

	let {
		policy: initial,
		owner,
		ondone
	}: {
		policy?: BackupPolicy;
		owner: boolean;
		/** The wizard finished (Done) with the saved policy. */
		ondone: (policy: BackupPolicy) => unknown;
	} = $props();

	const qc = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const policies = createQuery(() => backupPoliciesQuery());
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
	let scopeMode = $state<'all' | 'environment'>(p0?.scope ?? 'all');
	let environmentId = $state(p0?.environmentId ?? '');
	let excludeStacks = $state<string[]>(p0?.excludeStacks ?? []);
	let excludeVolumes = $state<string[]>(p0?.excludeVolumes ?? []);
	let anonymousVolumes = $state(p0?.anonymousVolumes ?? false);
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
	let runFirst = $state(false);

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
			scopeMode,
			environmentId,
			excludeStacks,
			excludeVolumes,
			anonymousVolumes,
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
	const involvedEnvs = $derived(
		activeEnvs.filter((e) => scopeMode === 'all' || e.id === environmentId).map((e) => e.id)
	);

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

	function draft(): PolicyInput {
		const er: Record<string, string> = {};
		for (const [k, v] of Object.entries(envRepos)) if (v) er[k] = v;
		return {
			name: name.trim(),
			scope: scopeMode,
			environmentId: scopeMode === 'all' ? undefined : environmentId,
			excludeStacks,
			excludeVolumes,
			anonymousVolumes,
			repositoryId,
			environmentRepositories: er,
			includeManagerState: includeManager,
			includeMetrics: includeManager && includeMetrics,
			shutdown,
			schedule: { cron, timeZone: zone, enabled },
			retention
		};
	}

	async function previewScope() {
		scopeLoading = true;
		scopeError = null;
		try {
			scope = await unwrap(
				policy
					? api.POST('/api/v1/backup-policies/{policyId}/scope-previews', {
							params: { path: { policyId: policy.id } },
							body: { draft: draft() }
						})
					: api.POST('/api/v1/backup-policy-scope-previews', { body: draft() })
			);
		} catch (e) {
			scopeError = actionError(e, {
				backup_scope_overlap:
					'Another backup policy already covers these environments. Edit that policy, or choose another environment.'
			});
		} finally {
			scopeLoading = false;
		}
	}

	/** An existing policy (other than this one) covering the chosen scope. */
	const overlapping = $derived(
		(policies.data ?? []).find(
			(p) =>
				p.id !== policy?.id &&
				(scopeMode === 'all' || p.scope === 'all' || p.environmentId === environmentId)
		)
	);

	// Creates the policy or saves the edits: the only write of the wizard.
	async function save(): Promise<BackupPolicy> {
		const saved = policy
			? await unwrap(
					api.PATCH('/api/v1/backup-policies/{policyId}', {
						params: {
							path: { policyId: policy.id },
							header: { 'If-Match': ifMatch(policy.revision) }
						},
						body: draft()
					})
				)
			: await unwrap(api.POST('/api/v1/backup-policies', { body: draft() }));
		qc.setQueryData(backupKeys.policy(saved.id), saved);
		await qc.invalidateQueries({ queryKey: ['policies', 'list'] });
		return saved;
	}

	function failure(e: unknown): Error {
		error = e;
		return new Error(
			Object.keys(fieldErrors(e)).length
				? Object.values(fieldErrors(e)).join(' ')
				: actionError(e, {
						backup_policy_name_taken:
							'Another backup policy has this name. Choose a different name.',
						backup_scope_overlap:
							'Another backup policy already covers these environments. Edit that policy, or choose another environment.',
						recovery_key_not_confirmed:
							'Confirm the Recovery Key of every repository this policy uses before turning its schedule on.'
					}),
			{ cause: e }
		);
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
		{
			id: 'retention',
			label: 'Retention',
			description: p0
				? 'Which old backups are forgotten. Saving applies every change.'
				: 'Which old backups are forgotten. Creating saves the policy.'
		}
	];

	const canAdvance = $derived(
		current === 0
			? !!name.trim() && !!repositoryId && (scopeMode === 'all' || !!environmentId)
			: current === 1
				? involvedEnvs.every((e) => !needsEnvRepo(e))
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
		if (step.id === 'destination' && overlapping) {
			throw new Error(
				`${overlapping.name} already covers ${overlapping.scope === 'all' ? 'all environments' : envName(overlapping.environmentId ?? '')}: policies can't overlap. Edit that policy, or choose another environment.`
			);
		}
		if (step.id === 'shutdown' && shutdown && !scope?.shutdown) await previewScope();
	}

	async function finish() {
		let out: BackupPolicy;
		try {
			out = await save();
		} catch (e) {
			throw failure(e);
		}
		policy = out;
		saved = true;
		touched = false;
		if (runFirst) {
			try {
				await unwrap(
					api.POST('/api/v1/backup-policies/{policyId}/runs', {
						params: {
							path: { policyId: out.id },
							header: { 'Idempotency-Key': newIdempotencyKey() }
						},
						body: {}
					})
				);
				toast.info(`Started the first backup of ${out.name}`);
			} catch (e) {
				toast.error('The first backup did not start', { body: actionError(e) });
			}
		}
		toast.success(`${initial ? 'Saved' : 'Created'} backup policy ${out.name}`, {
			body: enabled ? undefined : 'Its schedule is off: backups run only when you start them.'
		});
		await ondone(out);
	}

	// Scope previews go stale when the selection changes.
	$effect(() => {
		void excludeStacks;
		void excludeVolumes;
		void anonymousVolumes;
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
	finishLabel={initial ? 'Save changes' : 'Create policy'}
>
	{#snippet step(s)}
		{#if s.id === 'destination'}
			<Fields columns={2}>
				<TextField
					label="Name"
					bind:value={name}
					required
					placeholder="Nightly"
					error={fieldErrors(error)['body.name']}
				/>
				<Select
					label="Environments"
					options={[
						{ value: 'all', label: 'All environments' },
						{ value: 'environment', label: 'One environment' }
					]}
					bind:value={scopeMode}
					disabled={!!policy}
				/>
				{#if scopeMode === 'environment'}
					<Select
						label="Environment"
						options={activeEnvs.map((e) => ({ value: e.id, label: e.name }))}
						bind:value={environmentId}
						placeholder="Choose an environment"
						required
						disabled={!!policy}
					/>
				{/if}
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
						Nothing is saved until the last step. New policies start with their schedule
						off.
					</p>
				{/if}
			</Fields>
		{:else if s.id === 'scope'}
			<Fields>
				{#if owner}
					<FieldGroup
						legend="Manager"
						hint="The manager's database and settings, needed to recover Docker Manager itself."
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
					hint="Every managed stack in scope is backed up: its project directory and its volumes. Uncheck a stack to leave it out; stacks created later are included too."
				>
					{#if stacks.isPending}
						<Skeleton lines={3} height="20px" />
					{:else if stacksList.length === 0}
						<p class="muted">No stacks you can back up.</p>
					{/if}
					{#each Object.entries(stacksByEnv).filter( ([envId]) => involvedEnvs.includes(envId) ) as [envId, list] (envId)}
						<div class="env-group">
							<p class="env">{envName(envId)}</p>
							<CoverageList
								label="Stacks on {envName(envId)}"
								min="200px"
								items={list.map((st) => ({
									key: st.id,
									label: st.displayName || st.name
								}))}
								excluded={excludeStacks}
								onchange={(v) => {
									excludeStacks = v;
									touched = true;
								}}
							/>
						</div>
					{/each}
				</FieldGroup>
				<FieldGroup
					legend="Volumes"
					hint="The volumes of included stacks and every standalone volume are backed up. Uncheck a volume to leave it out."
				>
					<Switch
						label="Back up anonymous volumes"
						description="Off by default: anonymous volumes usually hold caches and scratch data that containers recreate. On: those of included stacks and standalone ones are backed up too."
						bind:checked={anonymousVolumes}
						onchange={() => (touched = true)}
					/>
					{#each activeEnvs.filter((e) => e.online && involvedEnvs.includes(e.id)) as e (e.id)}
						<VolumeCoverage
							environmentId={e.id}
							environmentName={e.name}
							all={scopeMode === 'all'}
							excluded={excludeVolumes}
							excludedStacks={excludeStacks}
							anonymous={anonymousVolumes}
							onchange={(v) => {
								excludeVolumes = v;
								touched = true;
							}}
						/>
					{/each}
					{#each activeEnvs.filter((e) => !e.online && involvedEnvs.includes(e.id)) as e (e.id)}
						<p class="muted small">
							{e.name} is offline: its volumes can't be listed. Saved exclusions are kept.
						</p>
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
								bind:value={
									() => envRepos[envId] ?? '', (v) => (envRepos[envId] = v)
								}
								error={needsEnvRepo(envId)
									? `${primary?.name ?? 'The policy’s repository'} can't hold ${envName(envId)}'s data. Choose a repository on ${envName(envId)} or an S3 repository.`
									: null}
							/>
						{/each}
					</FieldGroup>
				{/if}
				<div class="preview-bar">
					<Button onclick={previewScope} loading={scopeLoading}
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
					<ScopePreviewView preview={scope} showShutdown={false} />
				{/if}
			</Fields>
		{:else if s.id === 'shutdown'}
			<Fields>
				<Switch
					label="Stop containers during backups"
					description="Off by default. On: the containers using the data stop in reverse dependency order and the ones that were running start again afterwards, also after a failure. Docker Manager's own containers never stop."
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
			<Fields>
				<RetentionEditor
					bind:value={retention}
					policyId={policy?.id}
					onchange={() => (touched = true)}
				/>
				{#if !initial}
					<Checkbox
						label="Run the first backup after creating"
						description={shutdown
							? 'Checks that everything can be read and stored. The affected containers stop while it runs.'
							: 'Checks that everything can be read and stored.'}
						bind:checked={runFirst}
					/>
				{/if}
			</Fields>
		{/if}
	{/snippet}
</StepWizard>

<style>
	.small {
		font-size: var(--text-caption);
	}

	.env {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.env-group {
		display: grid;
		gap: var(--space-1);
	}

	.preview-bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
	}
</style>
