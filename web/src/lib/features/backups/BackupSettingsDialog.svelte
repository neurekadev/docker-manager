<script lang="ts">
	// Edit the backup settings (#10, #246) in a dialog, laid out like the
	// update settings: what to back up on the left (the environments
	// covered, every one unless left out, also ones added later; every
	// managed stack and volume, included until unchecked; anonymous and
	// buildx volumes only when turned on; container shutdown, off by
	// default), where and when on the right (a Primary repository and an
	// optional Secondary one, each run backs up to both; Back Up
	// Automatically, #13, its schedule shown only while on; retention),
	// and the scope preview below both. The manager state is always backed
	// up. Nothing is saved before Save Changes. Render it only while open
	// ({#if}): each opening starts from `settings`.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { Button, CronField, Dialog, Notice, Select, Skeleton, Switch, toast } from '$lib/ui';
	import CoverageList from '$lib/features/common/CoverageList.svelte';
	import FieldGroup from '$lib/features/common/FieldGroup.svelte';
	import { ifMatch, stacksQuery } from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import RetentionEditor from './RetentionEditor.svelte';
	import ScopePreviewView from './ScopePreviewView.svelte';
	import VolumeCoverage from './VolumeCoverage.svelte';
	import {
		repositoryLocation,
		type BackupRetention,
		type BackupSettings,
		type ScopePreview
	} from './model';
	import { backupKeys, repositoriesQuery } from './queries';

	let {
		open = $bindable(true),
		settings
	}: {
		open?: boolean;
		settings: BackupSettings;
	} = $props();

	const qc = useQueryClient();
	const st = untrack(() => settings);
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const stacks = createQuery(() => stacksQuery());

	let enabled = $state(st.enabled);
	let primary = $state(st.primaryRepositoryId);
	let secondary = $state(st.secondaryRepositoryId);
	let excludeEnvironments = $state<string[]>([...st.excludeEnvironments]);
	let excludeStacks = $state<string[]>([...st.excludeStacks]);
	let excludeVolumes = $state<string[]>([...st.excludeVolumes]);
	let anonymousVolumes = $state(st.anonymousVolumes);
	let buildxVolumes = $state(st.buildxVolumes);
	let externalBinds = $state(st.externalBinds);
	let includeMetrics = $state(st.includeMetrics);
	let shutdown = $state(st.shutdown);
	let cron = $state(st.schedule.cron);
	let zone = $state(st.schedule.timeZone);
	let retention = $state<BackupRetention>({ ...st.retention });
	let touched = $state(false);
	let busy = $state(false);
	let error = $state<unknown>(null);
	let scope = $state<ScopePreview | null>(null);
	// The shown preview no longer matches the selection (kept until previewed again).
	let scopeStale = $state(false);
	let scopeLoading = $state(false);
	let scopeError = $state<string | null>(null);

	useUnsaved(
		() => 'Backup settings',
		() => touched && !busy
	);

	const allRepos = $derived(repos.data ?? []);
	const ready = (id: string) => allRepos.find((r) => r.id === id)?.state === 'ready';
	const repoOptions = $derived(
		allRepos.map((r) => ({
			value: r.id,
			label: r.state === 'ready' ? r.name : `${r.name} (Recovery Key not confirmed)`
		}))
	);
	const primaryRepo = $derived(allRepos.find((r) => r.id === primary));
	const secondaryRepo = $derived(allRepos.find((r) => r.id === secondary));
	const activeEnvs = $derived((envs.data ?? []).filter((e) => e.status !== 'archived'));
	const coveredEnvs = $derived(activeEnvs.filter((e) => !excludeEnvironments.includes(e.id)));
	const stacksList = $derived((stacks.data ?? []).filter((s) => s.view === 'full'));
	const stacksByEnv = $derived.by(() => {
		const m: Record<string, typeof stacksList> = {};
		for (const s of stacksList) (m[s.environmentId] ??= []).push(s);
		return m;
	});
	const fields = $derived(fieldErrors(error));
	// Field errors shown next to their field (the schedule's by the schedule
	// field, only while on); any other one shows in the notice.
	const inline = $derived([
		'body.primaryRepositoryId',
		'body.secondaryRepositoryId',
		...(enabled ? ['body.schedule.cron', 'body.schedule.timeZone'] : [])
	]);
	const unshown = $derived(
		Object.entries(fields)
			.filter(([f]) => !inline.includes(f))
			.map(([, message]) => message)
	);
	const showError = $derived(!!error && (Object.keys(fields).length === 0 || unshown.length > 0));
	const problem = $derived(
		enabled && !primary
			? 'Choose a Primary repository before turning backups on.'
			: secondary && secondary === primary
				? 'The Secondary repository must differ from the Primary one.'
				: enabled && ((primary && !ready(primary)) || (secondary && !ready(secondary)))
					? 'Confirm the Recovery Key of the chosen repositories before turning backups on.'
					: enabled && !cron.trim()
						? 'Choose when backups run.'
						: null
	);

	function body() {
		return {
			enabled,
			primaryRepositoryId: primary,
			secondaryRepositoryId: secondary,
			excludeEnvironments,
			excludeStacks,
			excludeVolumes,
			anonymousVolumes,
			buildxVolumes,
			externalBinds,
			includeMetrics,
			shutdown,
			// An emptied schedule (only possible while off) keeps the saved one.
			...(cron.trim() ? { schedule: { cron, timeZone: zone } } : {}),
			retention
		};
	}

	async function previewScope() {
		scopeLoading = true;
		scopeError = null;
		try {
			const draft = {
				primaryRepositoryId: primary,
				secondaryRepositoryId: secondary,
				excludeEnvironments,
				excludeStacks,
				excludeVolumes,
				anonymousVolumes,
				buildxVolumes,
				externalBinds,
				includeMetrics,
				shutdown
			};
			scope = await unwrap(
				api.POST('/api/v1/backup-settings/scope-previews', { body: { draft } })
			);
			scopeStale = false;
		} catch (e) {
			scopeError = actionError(e);
		} finally {
			scopeLoading = false;
		}
	}

	// A scope preview goes out of date when the selection changes: it stays
	// visible, marked, until it is previewed again.
	$effect(() => {
		void excludeEnvironments;
		void excludeStacks;
		void excludeVolumes;
		void anonymousVolumes;
		void buildxVolumes;
		void externalBinds;
		void shutdown;
		untrack(() => {
			if (scope) scopeStale = true;
		});
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (problem) return;
		busy = true;
		error = null;
		try {
			const saved = await unwrap(
				api.PATCH('/api/v1/backup-settings', {
					params: { header: { 'If-Match': ifMatch(st.revision) } },
					body: body()
				})
			);
			toast.success('Saved the backup settings', {
				body: saved.enabled
					? undefined
					: 'Backups are off: nothing is backed up until you start a backup or turn them on.'
			});
			touched = false;
			qc.setQueryData(backupKeys.settings(), saved);
			await qc.invalidateQueries({ queryKey: ['policies'] });
			await qc.invalidateQueries({ queryKey: backupKeys.repositories() });
			open = false;
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}

	function changed() {
		touched = true;
	}
</script>

<Dialog
	bind:open
	title="Edit Backups"
	description="Every run backs up to the Primary repository, then to the Secondary one."
	size="xl"
	dismissible={!busy}
>
	<form id="backup-settings-form" onsubmit={submit} oninput={changed} novalidate>
		{#if showError}
			<div class="error">
				<Notice tone="danger" title="The settings were not saved" live="alert">
					{actionError(error, {
						recovery_key_not_confirmed:
							'Confirm the Recovery Key of the chosen repositories before turning backups on.'
					})}
					{unshown.join(' ')}
				</Notice>
			</div>
		{/if}
		<div class="editor">
			<section class="column" aria-labelledby="backups-what">
				<h3 class="subsection-title" id="backups-what">What to Back Up</h3>
				<FieldGroup
					legend="Environments"
					hint="Uncheck an environment to leave it out. Environments added later are covered."
				>
					{#if envs.isPending}
						<Skeleton lines={2} height="20px" />
					{:else if !activeEnvs.length}
						<p class="muted">No environments yet.</p>
					{:else}
						<CoverageList
							label="Environments Covered"
							min="180px"
							items={activeEnvs.map((e) => ({
								key: e.id,
								label: e.name,
								description: e.online ? undefined : 'Offline'
							}))}
							excluded={excludeEnvironments}
							onchange={(v) => {
								excludeEnvironments = v;
								changed();
							}}
						/>
					{/if}
				</FieldGroup>
				<FieldGroup
					legend="Manager"
					hint="Always backed up: needed to recover Docker Manager."
				>
					<Switch
						label="Include the Metrics Database"
						description="Charts history is large and can be rebuilt."
						bind:checked={includeMetrics}
						onchange={changed}
					/>
				</FieldGroup>
				<FieldGroup
					legend="Stacks"
					hint="Uncheck a stack to leave it out. New stacks are included."
				>
					{#if stacks.isPending}
						<Skeleton lines={3} height="20px" />
					{:else if stacksList.length === 0}
						<p class="muted">No stacks you can back up.</p>
					{/if}
					{#each coveredEnvs.filter((e) => stacksByEnv[e.id]?.length) as e (e.id)}
						<div class="env-group">
							<p class="env">{e.name}</p>
							<CoverageList
								label="Stacks on {e.name}"
								min="200px"
								items={stacksByEnv[e.id].map((s) => ({
									key: s.id,
									label: s.displayName || s.name
								}))}
								excluded={excludeStacks}
								onchange={(v) => {
									excludeStacks = v;
									changed();
								}}
							/>
						</div>
					{/each}
					<Switch
						label="Back Up Allowed Folders Outside Stacks"
						description="Folders a stack mounts from outside its own folder, such as /srv/media, if the server allows them."
						bind:checked={externalBinds}
						onchange={changed}
					/>
				</FieldGroup>
				<FieldGroup legend="Volumes" hint="Uncheck a volume to leave it out.">
					<Switch
						label="Back Up Anonymous Volumes"
						description="They usually hold caches and scratch data that containers recreate."
						bind:checked={anonymousVolumes}
						onchange={changed}
					/>
					<Switch
						label="Back Up buildx Builder Volumes"
						description="Build cache, rebuilt when needed."
						bind:checked={buildxVolumes}
						onchange={changed}
					/>
					{#each coveredEnvs.filter((e) => e.online) as e (e.id)}
						<VolumeCoverage
							environmentId={e.id}
							environmentName={e.name}
							excluded={excludeVolumes}
							excludedStacks={excludeStacks}
							anonymous={anonymousVolumes}
							buildx={buildxVolumes}
							onchange={(v) => {
								excludeVolumes = v;
								changed();
							}}
						/>
					{/each}
					{#each coveredEnvs.filter((e) => !e.online) as e (e.id)}
						<p class="muted small">
							{e.name} is offline: its volumes can't be listed. Saved exclusions are kept.
						</p>
					{/each}
				</FieldGroup>
				<FieldGroup
					legend="Running Containers"
					hint="Live backups are crash-consistent: fine for most data; databases are safe this way only if they survive a power loss."
				>
					<Switch
						label="Stop Containers During Backups"
						description="Containers using the data stop during each copy and start again afterwards."
						bind:checked={shutdown}
						onchange={changed}
					/>
					{#if shutdown}
						<Notice
							tone="warn"
							icon={TriangleAlert}
							title="Services Are Down While Their Data Is Backed Up"
							live="none"
						>
							With a Secondary repository, they stop once for each copy. Preview which
							containers stop and in which order.
						</Notice>
					{/if}
				</FieldGroup>
			</section>
			<section class="column" aria-labelledby="backups-where">
				<h3 class="subsection-title" id="backups-where">Where and When</h3>
				<FieldGroup legend="Repositories">
					{#if repos.isPending}
						<Skeleton lines={2} height="36px" />
					{:else if !allRepos.length}
						<Notice tone="warn" title="No Repository Yet" live="none">
							Add a backup repository and confirm its Recovery Key first.
							{#snippet actions()}<Button
									size="sm"
									href={routes.backupRepositoryNew()}>Add Repository</Button
								>{/snippet}
						</Notice>
					{:else}
						<Select
							label="Primary Repository"
							options={repoOptions}
							bind:value={primary}
							placeholder="Choose a repository"
							description={primaryRepo ? repositoryLocation(primaryRepo) : undefined}
							info="Every backup goes here first."
							error={fields['body.primaryRepositoryId']}
							onchange={changed}
						/>
						<Select
							label="Secondary Repository"
							options={[
								{ value: '', label: 'None' },
								...repoOptions.filter((o) => o.value !== primary)
							]}
							bind:value={secondary}
							description={secondaryRepo
								? repositoryLocation(secondaryRepo)
								: 'A second, independent copy of every backup.'}
							error={fields['body.secondaryRepositoryId']}
							onchange={changed}
						/>
					{/if}
				</FieldGroup>
				<FieldGroup
					legend="Schedule"
					info="Runs missed while Docker Manager was down are made up by one run when it is back."
				>
					<Switch
						label="Back Up Automatically"
						bind:checked={enabled}
						onchange={changed}
					/>
					{#if enabled}
						<CronField
							label="Backup Schedule"
							kind="backup"
							bind:cron
							bind:timeZone={zone}
						/>
					{/if}
				</FieldGroup>
				<FieldGroup legend="Retention">
					<RetentionEditor bind:value={retention} preview onchange={changed} />
				</FieldGroup>
			</section>
		</div>
		<div class="scope">
			<div class="preview-bar">
				<Button onclick={previewScope} loading={scopeLoading}
					>{scope && scopeStale
						? 'Preview Again'
						: shutdown
							? 'Preview What Gets Backed Up and Stopped'
							: 'Preview What Gets Backed Up'}</Button
				>
			</div>
			{#if scopeError}<Notice
					tone="danger"
					title="The preview could not be computed"
					live="alert">{scopeError}</Notice
				>{/if}
			{#if scope}
				{#if scopeStale}
					<Notice tone="info" title="Out of Date" live="polite">
						Press <strong>Preview Again</strong> to include your latest changes.
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
		</div>
		{#if problem && touched}
			<div class="problem">
				<Notice tone="warn" title="Not Ready to Save" live="polite">{problem}</Notice>
			</div>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
		<Button
			type="submit"
			form="backup-settings-form"
			variant="primary"
			loading={busy}
			disabled={!!problem}
		>
			Save Changes
		</Button>
	{/snippet}
</Dialog>

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

	.error {
		margin-bottom: var(--space-4);
	}

	.problem {
		margin-top: var(--space-4);
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

	.column {
		display: grid;
		gap: var(--space-4);
		min-width: 0;
	}

	/* The preview spans both columns, below them. */
	.scope {
		display: grid;
		gap: var(--space-4);
		margin-top: var(--space-6);
		padding-top: var(--space-5);
		border-top: 1px solid var(--border-subtle);
	}

	@media (max-width: 1023px) {
		.editor {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
