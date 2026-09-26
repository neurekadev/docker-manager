<script lang="ts">
	// Create stack (#7): name, environment, compose.yaml and an optional
	// .env, validated on the environment's agent (warnings include the
	// obsolete top-level version: key and unsupported features). Creating
	// writes the files into a new project directory of the stacks volume and
	// never overwrites anything; deploying afterwards is a separate choice.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { onDestroy } from 'svelte';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { createStack, deployStack, validateStack } from '$lib/features/stacks/actions';
	import { canInEnvironment, nameError } from '$lib/features/stacks/model';
	import { stackKeys } from '$lib/features/stacks/queries';
	import ValidationResult from '$lib/features/stacks/ValidationResult.svelte';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		Checkbox,
		CodeEditor,
		EmptyState,
		ErrorState,
		Notice,
		Select,
		Skeleton,
		TextField,
		errorView,
		toast
	} from '$lib/ui';
	import type { Schema } from '$lib/api/client';
	import Layers from '@lucide/svelte/icons/layers';

	usePage({
		title: 'Create stack',
		crumbs: [{ label: 'Stacks', href: routes.stacks() }, { label: 'Create stack' }],
		environmentScoped: true
	});

	const queryClient = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const allowed = $derived(
		(envs.data ?? []).filter((e) => canInEnvironment(perms.data, 'stack.create', e.id))
	);

	const STARTER = `services:
  web:
    image: nginx:1.27
    restart: unless-stopped
    ports:
      - "8080:80"
`;

	let environmentId = $state(
		page.url.searchParams.get('environment') ?? environmentSelection.id ?? ''
	);
	$effect(() => {
		if (!environmentId && allowed.length === 1) environmentId = allowed[0].id;
	});
	const env = $derived(allowed.find((e) => e.id === environmentId));
	let name = $state('');
	let displayName = $state('');
	let description = $state('');
	let compose = $state(STARTER);
	let envFile = $state('');
	let deployAfter = $state(false);
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
	onDestroy(() => release?.());

	const nameMsg = $derived(touched ? nameError(name) : undefined);
	const ready = $derived(!!env && !nameError(name) && compose.trim().length > 0);

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

	async function create() {
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
			release?.();
			release = null;
			validation = out.validation;
			void queryClient.invalidateQueries({ queryKey: stackKeys.all });
			const title = out.stack.displayName || out.stack.name;
			toast.success(`Created ${title}`, {
				body: out.validation.warnings.length
					? `${out.validation.warnings.length} validation warnings; see the stack's files.`
					: undefined
			});
			let job = '';
			if (deployAfter) {
				try {
					job = (await deployStack(out.stack.id, 'deploy')).id;
				} catch (e) {
					toast.error(`${title} was not deployed`, { body: errorView(e).message });
				}
			}
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

<div class="page">
	<header class="head">
		<h1>Create stack</h1>
		<p class="muted">
			Writes compose.yaml (and .env) into a new project directory in the environment's stacks
			volume. Nothing existing is overwritten.
		</p>
	</header>

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
			description="Stacks run on an environment. Add one first: run the Docker Agent on a Docker host and enroll it."
			level={2}
		>
			{#snippet actions()}<Button variant="primary" href={routes.environments()}
					>Open environments</Button
				>{/snippet}
		</EmptyState>
	{:else if allowed.length === 0}
		<EmptyState
			icon={Layers}
			color="blue"
			title="You can't create stacks in any environment."
			description="Ask the owner of this Docker Manager for the permission to create stacks."
			level={2}
		/>
	{:else}
		<form
			class="grid"
			onsubmit={(e) => {
				e.preventDefault();
				void create();
			}}
		>
			<Card title="Details" level={2}>
				<div class="fields">
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
						description="The Compose project name and its directory: lower-case letters, digits, dashes and underscores."
						error={nameConflict ?? nameMsg}
						oninput={() => (nameConflict = null)}
						onblur={() => (touched = true)}
					/>
					<TextField
						label="Display name"
						bind:value={displayName}
						description="Optional."
					/>
					<TextField
						label="Description"
						bind:value={description}
						description="Optional. Stored in Docker Manager, not in the Compose file."
					/>
				</div>
			</Card>

			<Card title="compose.yaml" level={2}>
				<CodeEditor
					value={compose}
					label="compose.yaml"
					height="360px"
					onchange={(t) => {
						compose = t;
						edited();
					}}
				/>
			</Card>

			<Card
				title=".env"
				level={2}
				subtitle="Optional. May hold secrets: stored sealed, never logged"
			>
				<CodeEditor
					value={envFile}
					label=".env"
					height="140px"
					onchange={(t) => {
						envFile = t;
						edited();
					}}
				/>
			</Card>

			<Card title="Check and create" level={2}>
				<div class="final">
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
							<ErrorState
								error={failure}
								title="The stack was not created."
								compact
							/>
						{/if}
					{/if}
					<Checkbox
						bind:checked={deployAfter}
						label="Deploy after creating"
						description="Starts a deploy right away; otherwise deploy it from the stack page."
					/>
					<div class="buttons">
						<Button variant="ghost" href={routes.stacks()}>Cancel</Button>
						<Button onclick={validate} loading={validating} disabled={!ready}
							>Validate</Button
						>
						<Button
							variant="primary"
							type="submit"
							loading={creating}
							disabled={!ready || (env && !env.online)}
						>
							{deployAfter ? 'Create and deploy' : 'Create stack'}
						</Button>
					</div>
				</div>
			</Card>
		</form>
	{/if}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		max-width: 1100px;
	}

	h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.head p {
		margin-top: 2px;
		font-size: var(--text-control);
	}

	.grid {
		display: grid;
		gap: var(--space-4);
	}

	.fields {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
		gap: var(--space-4);
		align-items: start;
	}

	.final {
		display: grid;
		gap: var(--space-4);
	}

	.buttons {
		display: flex;
		justify-content: flex-end;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.issues {
		margin: var(--space-2) 0 0;
		padding-left: 18px;
	}
</style>
