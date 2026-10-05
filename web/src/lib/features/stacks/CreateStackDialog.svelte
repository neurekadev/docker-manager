<script lang="ts">
	// Create stack (#7) as a dialog over the stack list: name, environment,
	// compose.yaml and an optional .env, validated on the environment's
	// agent (warnings include the obsolete top-level version: key and
	// unsupported features). Creating writes the files into a new project
	// directory of the stacks volume and never overwrites anything;
	// deploying afterwards is on by default. Details sit beside the editors
	// so the Compose file gets the width. With one environment the picker
	// is hidden; beside disabled buttons a hint says what is missing.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import Layers from '@lucide/svelte/icons/layers';
	import type { Schema } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import {
		Button,
		Checkbox,
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
	import { createStack, deployStack, validateStack } from './actions';
	import { createBlocker } from './creating';
	import { canInEnvironment, nameError } from './model';
	import { stackKeys } from './queries';
	import ValidationResult from './ValidationResult.svelte';

	let {
		open = $bindable(false),
		environmentId: suggested = null
	}: { open?: boolean; environmentId?: string | null } = $props();

	const STARTER = `services:
  web:
    image: nginx:1.27
    restart: unless-stopped
    ports:
      - "8080:80"
`;

	const queryClient = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const allowed = $derived(
		(envs.data ?? []).filter((e) => canInEnvironment(perms.data, 'stack.create', e.id))
	);

	let environmentId = $state('');
	let name = $state('');
	let displayName = $state('');
	let description = $state('');
	let compose = $state(STARTER);
	let envFile = $state('');
	let deployAfter = $state(true);
	// Deploying the new stack needs Deploy Stacks in its environment (#282).
	const mayDeploy = $derived(
		!!environmentId && canInEnvironment(perms.data, 'stack.deploy', environmentId)
	);
	const deploying = $derived(deployAfter && mayDeploy);
	let touched = $state(false);
	let validating = $state(false);
	let creating = $state(false);
	let validation = $state<Schema<'StackValidation'> | null>(null);
	let failure = $state<unknown>(null);
	let nameConflict = $state<string | null>(null);

	// Unsaved definitions survive an app update prompt (#23 critical work).
	let release: (() => void) | null = null;
	function edited() {
		validation = null;
		if (!release) release = criticalWork.register('unsaved-edit', 'New stack');
	}
	function releaseWork() {
		release?.();
		release = null;
	}

	// Every opening starts a fresh draft.
	$effect(() => {
		if (!open) return;
		untrack(() => {
			environmentId = suggested ?? '';
			name = displayName = description = envFile = '';
			compose = STARTER;
			deployAfter = true;
			touched = false;
			validation = failure = nameConflict = null;
		});
		return releaseWork;
	});
	$effect(() => {
		// The only environment the caller may create in (the picker is hidden).
		if (open && allowed.length === 1 && environmentId !== allowed[0].id)
			environmentId = allowed[0].id;
	});

	const env = $derived(allowed.find((e) => e.id === environmentId));
	const nameMsg = $derived(touched ? nameError(name) : undefined);
	const ready = $derived(!!env && !nameError(name) && compose.trim().length > 0);
	const busy = $derived(validating || creating);
	// One environment to choose from: no picker.
	const pickEnvironment = $derived(allowed.length > 1);
	const blocker = $derived(createBlocker({ environment: env, name, compose }));

	function body(): Schema<'ValidateStackInputBody'> {
		return {
			environmentId,
			name: name.trim(),
			compose,
			env: envFile.trim() ? envFile : undefined
		};
	}

	async function validate() {
		touched = true;
		if (!ready) return;
		validating = true;
		failure = null;
		try {
			validation = await validateStack(body());
		} catch (e) {
			failure = e;
		} finally {
			validating = false;
		}
	}

	async function create(event?: SubmitEvent) {
		event?.preventDefault();
		touched = true;
		if (!ready) return;
		creating = true;
		failure = null;
		nameConflict = null;
		try {
			const out = await createStack({
				...body(),
				displayName: displayName.trim() || undefined,
				description: description.trim() || undefined
			});
			releaseWork();
			void queryClient.invalidateQueries({ queryKey: stackKeys.all });
			const title = out.stack.displayName || out.stack.name;
			toast.success(`Created ${title}`, {
				body: out.validation.warnings.length
					? `${out.validation.warnings.length} validation warnings; see the stack's files.`
					: undefined
			});
			let job = '';
			if (deploying) {
				try {
					job = (await deployStack(out.stack.id, 'deploy')).id;
				} catch (e) {
					toast.error(`${title} was not deployed`, { body: errorView(e).message });
				}
			}
			// The new stack's page replaces the list (and this dialog).
			await goto(routes.stack(out.stack.id) + (job ? `?job=${encodeURIComponent(job)}` : ''));
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
					nameConflict = `A Compose project ${name.trim()} already runs on ${env?.name ?? 'the Engine'}. Import it instead of creating a new stack, or choose another name.`;
					break;
				default:
					failure = e;
			}
		} finally {
			creating = false;
		}
	}
</script>

<Dialog bind:open title="Create Stack" size="xl" dismissible={!busy}>
	{#if envs.isPending || perms.isPending}
		<div aria-busy="true"><Skeleton lines={6} /></div>
	{:else if envs.isError}
		<ErrorState
			error={envs.error}
			title="The environments could not be loaded."
			onretry={() => envs.refetch()}
		/>
	{:else if (envs.data ?? []).length === 0}
		<EmptyState
			icon={Layers}
			color="blue"
			title="No environments yet."
			description="Add one first: run the Docker Agent on a Docker host and enroll it."
			level={3}
		>
			{#snippet actions()}<Button variant="primary" href={routes.environments()}
					>Open Environments</Button
				>{/snippet}
		</EmptyState>
	{:else if allowed.length === 0}
		<EmptyState
			icon={Layers}
			color="blue"
			title="You can't create stacks in any environment."
			description="Ask the owner of this Docker Manager for the permission to create stacks."
			level={3}
		/>
	{:else}
		<form id="create-stack-form" class="layout" onsubmit={create}>
			<div class="details">
				{#if pickEnvironment}
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
				{:else if env && !env.online}
					<Notice tone="offline" title="{env.name} is offline.">
						Stacks can be created when it is back.
					</Notice>
				{/if}
				<TextField
					label="Name"
					bind:value={name}
					mono
					required
					description="Lower-case letters, digits, dashes and underscores."
					info="Also names its folder and containers."
					error={nameConflict ?? nameMsg}
					oninput={() => (nameConflict = null)}
					onblur={() => (touched = true)}
				/>
				<TextField label="Display Name" bind:value={displayName} optional />
				<TextField label="Description" bind:value={description} optional />
				{#if mayDeploy}
					<Checkbox bind:checked={deployAfter} label="Deploy After Creating" />
				{/if}
				{#if validation}<ValidationResult {validation} />{/if}
				{#if failure}
					{@const v = errorView(failure)}
					{#if v.code === 'invalid_definition'}
						<Notice tone="danger" title="The definition is not valid.">
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
				<div class="editor">
					<span class="editor-label">Compose File</span>
					<CodeEditor
						value={compose}
						label="compose.yaml"
						height="240px"
						onchange={(t) => {
							compose = t;
							edited();
						}}
					/>
				</div>
				<div class="editor">
					<span class="editor-label"
						>.env File <span class="editor-optional">Optional</span><InfoTip
							text="May hold passwords; they never show in logs."
						/></span
					>
					<CodeEditor
						value={envFile}
						label=".env"
						height="120px"
						onchange={(t) => {
							envFile = t;
							edited();
						}}
					/>
				</div>
			</div>
		</form>
	{/if}
	{#snippet footer()}
		{#if allowed.length && blocker}
			<p class="blocker" role="status">{blocker}</p>
		{/if}
		<Button variant="ghost" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
		{#if allowed.length}
			<Button onclick={validate} loading={validating} disabled={!ready || creating}
				>Validate</Button
			>
			<Button
				variant="primary"
				type="submit"
				form="create-stack-form"
				loading={creating}
				disabled={!ready || (env && !env.online) || validating}
			>
				{deploying ? 'Create and Deploy' : 'Create Stack'}
			</Button>
		{/if}
	{/snippet}
</Dialog>

<style>
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

	.editor-optional {
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-regular);
		line-height: var(--leading-caption);
	}

	.blocker {
		flex: 1 1 auto;
		min-width: 0;
		align-self: center;
		margin-right: auto;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
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
