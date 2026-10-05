<script lang="ts">
	// Create stack from template (template registry), a dialog over the
	// stack list. Step one finds a template of any source (this instance's
	// and the added ones; search and tag chips; templates with a published
	// version you may use): clicking a card chooses it. Step two picks the
	// version, the
	// environment and the name, and shows the version's .env to edit. The
	// stack is created from the template's files; an edited .env is saved
	// to the new stack right after (through its file routes), then it is
	// deployed if asked. Nothing existing is overwritten.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { FilesApi } from '$lib/features/files/api';
	import { deployStack } from '$lib/features/stacks/actions';
	import { canInEnvironment, nameError } from '$lib/features/stacks/model';
	import { stackKeys } from '$lib/features/stacks/queries';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import {
		Button,
		Checkbox,
		Chip,
		CodeEditor,
		Dialog,
		EmptyState,
		ErrorState,
		InfoTip,
		Notice,
		Select,
		Skeleton,
		TextField,
		errorView,
		toast
	} from '$lib/ui';
	import { createStackFromTemplate } from './actions';
	import { catalogSearch, projectNameFor, tagCounts } from './model';
	import {
		catalogDefinitionQuery,
		templateCatalogQuery,
		templateDefinitionQuery,
		type TemplateCatalogItem
	} from './queries';
	import TemplateCard from './TemplateCard.svelte';
	import TemplateIcon from './TemplateIcon.svelte';

	interface Props {
		open?: boolean;
		environmentId?: string | null;
		/** Preselected template (from its page). */
		templateId?: string | null;
		/** The preselected template's registry (empty: this instance). */
		registry?: string | null;
	}

	let {
		open = $bindable(false),
		environmentId: suggested = null,
		templateId = null,
		registry = null
	}: Props = $props();

	const queryClient = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const templates = createQuery(() => ({ ...templateCatalogQuery(), enabled: open }));
	const allowed = $derived(
		(envs.data ?? []).filter((e) => canInEnvironment(perms.data, 'stack.create', e.id))
	);
	// Templates you can create stacks from: published and template.use.
	const usable = $derived(
		(templates.data ?? []).filter(
			(t) => t.versions.length > 0 && t.actions.includes('template.use')
		)
	);

	let picked = $state<TemplateCatalogItem | null>(null);
	let query = $state('');
	let tag = $state('');
	let version = $state('');
	let environmentId = $state('');
	let name = $state('');
	let displayName = $state('');
	let envFile = $state('');
	let envTouched = $state(false);
	let deployAfter = $state(false);
	// Deploying the new stack needs Deploy Stacks in its environment (#282).
	const mayDeploy = $derived(
		!!environmentId && canInEnvironment(perms.data, 'stack.deploy', environmentId)
	);
	const deploying = $derived(deployAfter && mayDeploy);
	let touched = $state(false);
	let creating = $state(false);
	let failure = $state<unknown>(null);
	let nameConflict = $state<string | null>(null);

	const versionNumber = $derived(Number(version) || 0);
	// This instance's versions are read locally; a registry's are downloaded
	// from it (checked against its digest).
	const ownDefinition = createQuery(() =>
		templateDefinitionQuery(picked?.own ? picked.templateId : '', versionNumber)
	);
	const remoteDefinition = createQuery(() =>
		catalogDefinitionQuery(
			picked && !picked.own ? picked.instanceId : '',
			picked?.templateId ?? '',
			versionNumber
		)
	);
	const definition = $derived(picked?.own ? ownDefinition : remoteDefinition);
	const templateEnv = $derived(definition.data?.files.find((f) => f.path === '.env'));
	const composeFile = $derived(
		definition.data?.files.find((f) => f.path !== '.env' && !f.path.includes('override'))
	);

	// Unsaved .env edits survive an app update prompt (#23 critical work).
	let release: (() => void) | null = null;
	function releaseWork() {
		release?.();
		release = null;
	}

	function pick(t: TemplateCatalogItem) {
		picked = t;
		version = String(t.versions[0]?.number ?? '');
		name = projectNameFor(t.name);
		displayName = t.name;
		envTouched = touched = false;
		failure = nameConflict = null;
	}

	// Every opening starts fresh (with the preselected template).
	$effect(() => {
		if (!open) return;
		untrack(() => {
			environmentId = suggested ?? '';
			picked = null;
			query = tag = '';
			deployAfter = false;
			failure = nameConflict = null;
		});
		return releaseWork;
	});
	$effect(() => {
		if (!open || picked || !templateId) return;
		const t = usable.find(
			(x) => x.templateId === templateId && (registry ? x.instanceId === registry : x.own)
		);
		if (t) untrack(() => pick(t));
	});
	$effect(() => {
		if (open && !environmentId && allowed.length === 1) environmentId = allowed[0].id;
	});
	// The version's .env becomes the starting point (until edited).
	$effect(() => {
		const content = templateEnv?.content ?? '';
		if (!envTouched) untrack(() => (envFile = content));
	});

	const shown = $derived.by(() => {
		const q = query.trim().toLowerCase();
		return usable
			.filter((t) => !tag || (t.tags ?? []).includes(tag))
			.filter((t) => !q || catalogSearch(t).some((s) => s?.toLowerCase().includes(q)))
			.sort((a, b) => a.name.localeCompare(b.name));
	});
	const tags = $derived(tagCounts(usable).slice(0, 12));

	const env = $derived(allowed.find((e) => e.id === environmentId));
	const nameMsg = $derived(touched ? nameError(name) : undefined);
	const ready = $derived(!!picked && !!env && !nameError(name) && versionNumber > 0);

	async function create(event?: SubmitEvent) {
		event?.preventDefault();
		touched = true;
		if (!ready || !picked) return;
		creating = true;
		failure = nameConflict = null;
		try {
			const out = await createStackFromTemplate({
				environmentId,
				name: name.trim(),
				displayName: displayName.trim() || undefined,
				instanceId: picked.own ? undefined : picked.instanceId,
				templateId: picked.templateId,
				version: versionNumber
			});
			const st = out.stack;
			const title = st.displayName || st.name;
			void queryClient.invalidateQueries({ queryKey: stackKeys.all });
			// Your .env replaces the template's before anything runs.
			if (envTouched && envFile !== (templateEnv?.content ?? '')) {
				try {
					await new FilesApi({
						kind: 'stack',
						stackId: st.id,
						environmentId: st.environmentId
					}).write('.env', envFile, templateEnv ? { ifMatch: '*' } : { create: true });
				} catch (e) {
					toast.error(`Your .env was not saved to ${title}`, {
						body: `${errorView(e).message} Edit it in the stack's Files tab before deploying.`
					});
					deployAfter = false;
				}
			}
			releaseWork();
			toast.success(`Created ${title} from ${picked.name}`);
			let job = '';
			if (deploying) {
				try {
					job = (await deployStack(st.id, 'deploy')).id;
				} catch (e) {
					toast.error(`${title} was not deployed`, { body: errorView(e).message });
				}
			}
			await goto(routes.stack(st.id) + (job ? `?job=${encodeURIComponent(job)}` : ''));
		} catch (e) {
			const v = errorView(e);
			switch (v.code) {
				case 'stack_name_taken':
					nameConflict = `${env?.name ?? 'This environment'} already has a stack named ${name.trim()}. Choose another name.`;
					break;
				case 'stack_directory_exists':
					nameConflict = `A project directory ${name.trim()} already exists in the stacks volume. Nothing was overwritten: import it, or choose another name.`;
					break;
				case 'compose_project_exists':
					nameConflict = `A Compose project ${name.trim()} already runs on ${env?.name ?? 'the Engine'}. Choose another name.`;
					break;
				default:
					failure = e;
			}
		} finally {
			creating = false;
		}
	}
</script>

<Dialog
	bind:open
	title={picked ? `Create Stack From ${picked.name}` : 'Create Stack From Template'}
	description={picked
		? undefined
		: 'Only templates with a published version you may use are listed.'}
	size="xl"
	dismissible={!creating}
>
	{#if envs.isPending || perms.isPending || templates.isPending}
		<div aria-busy="true"><Skeleton lines={6} /></div>
	{:else if templates.isError}
		<ErrorState
			error={templates.error}
			title="The templates could not be loaded."
			onretry={() => templates.refetch()}
		/>
	{:else if allowed.length === 0}
		<EmptyState
			icon={LayoutTemplate}
			color="violet"
			title="You can't create stacks in any environment."
			description="Ask the owner of this Docker Manager for the permission to create stacks."
			level={3}
		/>
	{:else if !picked}
		{#if usable.length === 0}
			<EmptyState
				icon={LayoutTemplate}
				color="violet"
				title="No templates to use yet."
				description="Templates appear here once they have a published version and you may use them."
				level={3}
			>
				{#snippet actions()}<Button href={routes.templates()}>Open Templates</Button
					>{/snippet}
			</EmptyState>
		{:else}
			<div class="picker">
				<TextField
					label="Search Templates"
					hideLabel
					type="search"
					placeholder="Search templates"
					bind:value={query}
				/>
				{#if tags.length}
					<div class="chips" role="group" aria-label="Filter by Tag">
						{#each tags as t (t.tag)}
							<Chip
								label={t.tag}
								count={t.count}
								selected={tag === t.tag}
								onclick={() => (tag = tag === t.tag ? '' : t.tag)}
							/>
						{/each}
					</div>
				{/if}
				{#if shown.length}
					<ul class="grid" aria-label="Templates">
						{#each shown as t (`${t.instanceId}/${t.templateId}`)}
							<li>
								<TemplateCard
									onselect={() => pick(t)}
									name={t.name}
									description={t.description}
									tags={t.tags}
									iconUrl={t.iconUrl}
									latest={t.versions[0]?.label}
									source={t.own ? 'This Instance' : t.registryName}
								/>
							</li>
						{/each}
					</ul>
				{:else}
					<p class="muted">No template matches. Clear the search or the tag.</p>
				{/if}
			</div>
		{/if}
	{:else}
		<form id="create-from-template" class="layout" onsubmit={create}>
			<div class="details">
				<div class="chosen">
					<TemplateIcon url={picked.iconUrl} />
					<div class="chosen-text">
						<strong>{picked.name}</strong>
						<span class="muted"
							>{picked.own ? 'This Instance' : picked.registryName}</span
						>
					</div>
					{#if !templateId}
						<Button
							size="sm"
							variant="ghost"
							icon={ArrowLeft}
							onclick={() => (picked = null)}>Change</Button
						>
					{/if}
				</div>
				<Select
					label="Version"
					bind:value={version}
					options={(picked?.versions ?? []).map((v, i) => ({
						value: String(v.number),
						label: i === 0 ? `${v.label} (latest)` : v.label
					}))}
				/>
				<Select
					label="Environment"
					bind:value={environmentId}
					placeholder="Choose an environment"
					options={allowed.map((e) => ({
						value: e.id,
						label: e.online ? e.name : `${e.name} (offline)`
					}))}
					error={env && !env.online
						? `${env.name} is offline. Stacks can be created when it is back.`
						: undefined}
				/>
				<TextField
					label="Name"
					bind:value={name}
					mono
					required
					description="Lower-case letters, digits, dashes and underscores."
					error={nameConflict ?? nameMsg}
					oninput={() => (nameConflict = null)}
					onblur={() => (touched = true)}
				/>
				<TextField label="Display Name" bind:value={displayName} optional />
				{#if mayDeploy}
					<Checkbox bind:checked={deployAfter} label="Deploy After Creating" />
				{/if}
				{#if failure}
					{@const v = errorView(failure)}
					{#if v.code === 'invalid_definition'}
						<Notice tone="danger" title="The template's definition is not valid here.">
							<ul class="issues">
								{#each v.fields as f, i (i)}<li>{f.message}</li>{/each}
							</ul>
						</Notice>
					{:else}
						<ErrorState error={failure} title="The stack was not created." compact />
					{/if}
				{/if}
			</div>
			<div class="editors">
				{#if definition.isError}
					<ErrorState
						error={definition.error}
						title="The version's files could not be loaded."
						compact
					/>
				{:else if definition.isPending}
					<Skeleton lines={8} />
				{:else}
					<div class="editor">
						<span class="editor-label"
							>.env <InfoTip text="Saved to the stack before anything runs." /></span
						>
						<CodeEditor
							value={envFile}
							label=".env"
							language="properties"
							height="180px"
							onchange={(t) => {
								envFile = t;
								envTouched = true;
								if (!release)
									release = criticalWork.register('unsaved-edit', 'New stack');
							}}
						/>
					</div>
					{#if composeFile}
						<div class="editor">
							<span class="editor-label"
								>{composeFile.path}
								<InfoTip text="Edit it later in the stack's files." /></span
							>
							<CodeEditor
								value={composeFile.content}
								label={composeFile.path}
								readOnly
								height="260px"
							/>
						</div>
					{/if}
				{/if}
			</div>
		</form>
	{/if}
	{#snippet footer()}
		<Button variant="ghost" disabled={creating} onclick={() => (open = false)}>Cancel</Button>
		{#if picked && allowed.length}
			<Button
				variant="primary"
				type="submit"
				form="create-from-template"
				loading={creating}
				disabled={!ready || (env && !env.online)}
			>
				{deploying ? 'Create and Deploy' : 'Create Stack'}
			</Button>
		{/if}
	{/snippet}
</Dialog>

<style>
	.picker {
		display: grid;
		gap: var(--space-3);
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
		gap: var(--space-3);
		max-height: 60vh;
		margin: 0;
		/* Room for the cards' focus ring inside the scroll box. */
		padding: 4px;
		overflow-y: auto;
		list-style: none;
	}

	.layout {
		display: grid;
		grid-template-columns: minmax(260px, 340px) minmax(0, 1fr);
		gap: var(--space-5);
		align-items: start;
	}

	.details,
	.editors {
		display: grid;
		gap: var(--space-4);
		min-width: 0;
	}

	.chosen {
		display: flex;
		align-items: center;
		gap: var(--space-3);
	}

	.chosen-text {
		display: flex;
		flex: 1;
		flex-direction: column;
		min-width: 0;
	}

	.editor {
		display: grid;
		gap: var(--space-2);
		min-width: 0;
	}

	.editor-label {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--text-default);
		font-weight: var(--weight-medium);
	}

	.issues {
		margin: var(--space-2) 0 0;
		padding-left: 18px;
	}

	@media (max-width: 1023px) {
		.layout {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
