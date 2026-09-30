<script lang="ts">
	// Backup policy form (#10), shown in BackupPolicyDialog. Creating is a
	// wizard: destination → what to back up (manager state; every managed
	// stack and volume in scope, included until unchecked, stack volumes
	// too; anonymous volumes only when turned on; container shutdown, off
	// by default, with the affected containers and stop order previewed) →
	// schedule (#13, off until turned on) → retention (a preset or custom
	// rules, preview, recovery floor). Cancel closes it on every step and
	// visited steps can be reopened; the body keeps one height. Editing is
	// one screen with the same sections in two columns and Cancel / Save
	// changes. Nothing is saved before Create policy or Save changes: a new
	// policy is created with every setting at once (optionally followed by
	// a first backup); previews of a new policy use the draft preview.
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
	import FormFooter from '$lib/features/common/FormFooter.svelte';
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
		DEFAULT_RETENTION,
		policyEdits,
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
		ondone,
		oncancel
	}: {
		policy?: BackupPolicy;
		owner: boolean;
		/** The policy was created or saved. */
		ondone: (policy: BackupPolicy) => unknown;
		/** Cancel: close without saving. */
		oncancel?: () => unknown;
	} = $props();

	const qc = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const policies = createQuery(() => backupPoliciesQuery());
	const stacks = createQuery(() => stacksQuery());
	const schedDefaults = createQuery(() => scheduleDefaultsQuery());
	const envName = (id: string) => environmentName(envs.data, id);

	const p0 = untrack(() => initial);
	const editing = !!p0;
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
	let buildxVolumes = $state(p0?.buildxVolumes ?? false);
	let externalBinds = $state(p0?.externalBinds ?? false);
	let shutdown = $state(p0?.shutdown ?? false);
	let enabled = $state(p0?.schedule?.enabled ?? false);
	let cron = $state(p0?.schedule?.cron ?? '');
	let zone = $state(p0?.schedule?.timeZone ?? '');
	let retention = $state<BackupRetention>(p0?.retention ?? { ...DEFAULT_RETENTION });
	let current = $state(0);
	let touched = $state(false);
	let error = $state<unknown>(null);
	let saveError = $state<string | null>(null);
	let saving = $state(false);
	let scope = $state<ScopePreview | null>(null);
	// The shown preview no longer matches the selection (kept until previewed again).
	let scopeStale = $state(false);
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
			buildxVolumes,
			externalBinds,
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
	const repoOptions = $derived(readyRepos.map((r) => ({ value: r.id, label: r.name })));
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

	/** Where a repository lives, as the field's description (the name is the option). */
	function repoDescription(r: (typeof readyRepos)[number]): string {
		return `${r.kind === 's3' ? 'S3 storage' : 'Local directory'}: ${repositoryLocation(r, envName)}`;
	}

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
				.map((r) => ({ value: r.id, label: r.name }))
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
			buildxVolumes,
			externalBinds,
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
			scopeStale = false;
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

	// Creates the policy or saves the edits: the only write of the form.
	async function save(): Promise<BackupPolicy> {
		const saved = policy
			? await unwrap(
					api.PATCH('/api/v1/backup-policies/{policyId}', {
						params: {
							path: { policyId: policy.id },
							header: { 'If-Match': ifMatch(policy.revision) }
						},
						body: policyEdits(draft())
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
			description: 'The manager state, stacks and volumes, and whether containers stop.'
		},
		{ id: 'schedule', label: 'Schedule', description: 'When backups run automatically.' },
		{
			id: 'retention',
			label: 'Retention',
			description: 'Which old backups are forgotten. Creating saves the policy.'
		}
	];

	const destinationOk = $derived(
		!!name.trim() && !!repositoryId && (scopeMode === 'all' || !!environmentId)
	);
	const scopeOk = $derived(involvedEnvs.every((e) => !needsEnvRepo(e)));
	const scheduleOk = $derived(!!cron.trim());
	const canAdvance = $derived(
		current === 0 ? destinationOk : current === 1 ? scopeOk : current === 2 ? scheduleOk : true
	);

	async function onnext(step: { id: string }) {
		error = null;
		if (step.id === 'destination' && overlapping) {
			throw new Error(
				`${overlapping.name} already covers ${overlapping.scope === 'all' ? 'all environments' : envName(overlapping.environmentId ?? '')}: policies can't overlap. Edit that policy, or choose another environment.`
			);
		}
		if (step.id === 'scope' && shutdown && (!scope?.shutdown || scopeStale)) {
			await previewScope();
			// Stay on the step: the error shows where the preview was asked for.
			if (scopeError) throw new Error(scopeError);
		}
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
		toast.success(`${editing ? 'Saved' : 'Created'} backup policy ${out.name}`, {
			body: enabled ? undefined : 'Its schedule is off: backups run only when you start them.'
		});
		await ondone(out);
	}

	// The one-screen editor: Save changes checks what the wizard's steps check.
	async function submit() {
		saveError = null;
		if (!destinationOk) return (saveError = 'Enter a name and choose a repository.');
		if (!scopeOk) return (saveError = 'Choose a repository for every environment below.');
		if (!scheduleOk) return (saveError = 'Choose when backups run.');
		saving = true;
		try {
			await finish();
		} catch (e) {
			saveError = e instanceof Error ? e.message : String(e);
		} finally {
			saving = false;
		}
	}

	// A scope preview goes out of date when the selection changes: it stays
	// visible, marked, until it is previewed again (asking every agent again
	// on each click would be slow and, with shutdown, recompute stop plans).
	$effect(() => {
		void excludeStacks;
		void excludeVolumes;
		void anonymousVolumes;
		void buildxVolumes;
		void externalBinds;
		void includeManager;
		void shutdown;
		void scopeMode;
		void environmentId;
		void JSON.stringify(envRepos);
		untrack(() => {
			if (scope) scopeStale = true;
		});
	});
</script>

{#snippet destinationSection()}
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
			description={policy
				? 'Fixed once the policy exists. Create another policy for other environments.'
				: undefined}
		/>
		{#if scopeMode === 'environment'}
			<Select
				label="Environment"
				options={activeEnvs.map((e) => ({ value: e.id, label: e.name }))}
				bind:value={environmentId}
				placeholder="Choose an environment"
				required
				disabled={!!policy}
				description={policy ? 'Fixed once the policy exists.' : undefined}
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
				description={primary
					? repoDescription(primary)
					: 'Backups of the manager state go here; environments can use their own.'}
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
				Nothing is saved until the last step. New policies start with their schedule off.
			</p>
		{/if}
	</Fields>
{/snippet}

{#snippet scopeSection()}
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
			<Switch
				label="Back up allowed folders outside stacks"
				description="Off by default. On: folders a stack mounts from outside its own folder (such as /srv/media) are backed up too, but only those the server allows (see Backups in the documentation)."
				bind:checked={externalBinds}
				onchange={() => (touched = true)}
			/>
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
			<Switch
				label="Back up buildx builder volumes"
				description="Off by default: they hold the build cache of buildx builders, which is rebuilt when needed. Volumes with the label docker-manager.backup.exclude=true, or used by a container with it, are never backed up."
				bind:checked={buildxVolumes}
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
					buildx={buildxVolumes}
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
		<FieldGroup
			legend="Running containers"
			hint="Live backups are crash-consistent: fine for most data; databases are safe this way only if they survive a power loss."
		>
			<Switch
				label="Stop containers during backups"
				description="Off by default. On: the containers using the data stop in reverse dependency order and the ones that were running start again afterwards, also after a failure. Docker Manager's own containers never stop."
				bind:checked={shutdown}
				onchange={() => (touched = true)}
			/>
			{#if shutdown}
				<Notice
					tone="warn"
					icon={TriangleAlert}
					title="Services are down while their data is backed up"
					live="none"
				>
					Preview which containers stop and in which order.
				</Notice>
			{/if}
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
						bind:value={() => envRepos[envId] ?? '', (v) => (envRepos[envId] = v)}
						error={needsEnvRepo(envId)
							? `${primary?.name ?? 'The policy’s repository'} can't hold ${envName(envId)}'s data. Choose a repository on ${envName(envId)} or an S3 repository.`
							: null}
					/>
				{/each}
			</FieldGroup>
		{/if}
		<div class="preview-bar">
			<Button onclick={previewScope} loading={scopeLoading}
				>{scope && scopeStale
					? 'Preview again'
					: shutdown
						? 'Preview what gets backed up and stopped'
						: 'Preview what gets backed up'}</Button
			>
			<span class="muted small"
				>Each environment's agent resolves the sources; nothing is stored.</span
			>
		</div>
		{#if scopeError}<Notice tone="danger" title="The preview could not be computed" live="alert"
				>{scopeError}</Notice
			>{/if}
		{#if scope}
			{#if scopeStale}
				<Notice tone="info" title="Out of date" live="polite">
					This preview doesn't include your latest changes. Press <strong
						>Preview again</strong
					> to update it.
				</Notice>
			{/if}
			<div class:stale={scopeStale}>
				<ScopePreviewView
					preview={scope}
					showShutdown={shutdown && !scopeStale}
					stackName={(id) => {
						const s = stacks.data?.find((x) => x.id === id);
						return s?.displayName || s?.name;
					}}
				/>
			</div>
		{/if}
	</Fields>
{/snippet}

{#snippet scheduleSection()}
	<Fields>
		<Switch
			label="Back up automatically"
			description="Off: backups run only when you start them. Turning it on needs every repository's Recovery Key confirmed."
			bind:checked={enabled}
			onchange={() => (touched = true)}
		/>
		{#if zone}<CronField label="Schedule" kind="backup" bind:cron bind:timeZone={zone} />{/if}
	</Fields>
{/snippet}

{#snippet retentionSection()}
	<Fields>
		<RetentionEditor
			bind:value={retention}
			policyId={policy?.id}
			onchange={() => (touched = true)}
		/>
		{#if !editing}
			<Checkbox
				label="Run the first backup after creating"
				description={shutdown
					? 'Checks that everything can be read and stored. The affected containers stop while it runs.'
					: 'Checks that everything can be read and stored.'}
				bind:checked={runFirst}
			/>
		{/if}
	</Fields>
{/snippet}

{#if editing}
	<div class="editor">
		<div class="column">
			<section aria-labelledby="policy-where">
				<h3 class="subsection-title" id="policy-where">Name and destination</h3>
				{@render destinationSection()}
			</section>
			<section aria-labelledby="policy-what">
				<h3 class="subsection-title" id="policy-what">What to back up</h3>
				{@render scopeSection()}
			</section>
		</div>
		<div class="column">
			<section aria-labelledby="policy-when">
				<h3 class="subsection-title" id="policy-when">Schedule</h3>
				{@render scheduleSection()}
			</section>
			<section aria-labelledby="policy-keep">
				<h3 class="subsection-title" id="policy-keep">Retention</h3>
				{@render retentionSection()}
			</section>
		</div>
	</div>
	{#if saveError}
		<Notice tone="danger" title="Not saved" live="alert">{saveError}</Notice>
	{/if}
	<FormFooter>
		<Button variant="ghost" disabled={saving} onclick={() => oncancel?.()}>Cancel</Button>
		<Button variant="primary" loading={saving} onclick={submit}>Save changes</Button>
	</FormFooter>
{:else}
	<StepWizard
		label="Create backup policy"
		{steps}
		bind:current
		{onnext}
		onfinish={finish}
		{canAdvance}
		finishLabel="Create policy"
		oncancel={oncancel ? () => oncancel?.() : undefined}
		stepsClickable
		minHeight="min(560px, 60vh)"
	>
		{#snippet step(s)}
			{#if s.id === 'destination'}
				{@render destinationSection()}
			{:else if s.id === 'scope'}
				{@render scopeSection()}
			{:else if s.id === 'schedule'}
				{@render scheduleSection()}
			{:else if s.id === 'retention'}
				{@render retentionSection()}
			{/if}
		{/snippet}
	</StepWizard>
{/if}

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

	.stale {
		opacity: 0.55;
	}

	.preview-bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
	}

	.editor {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		align-items: start;
		gap: var(--space-6);
	}

	.column,
	section {
		display: grid;
		gap: var(--space-4);
		min-width: 0;
	}

	.column {
		gap: var(--space-6);
	}

	@media (max-width: 1023px) {
		.editor {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
