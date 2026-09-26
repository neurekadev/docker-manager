<script lang="ts">
	// Discovery and import (#7): Compose projects the Engine runs that
	// Docker Manager does not manage, read-only, from their container labels.
	// Labels never reconstruct a Compose file, so a project is either
	// adopted in place (its real files lie under a verified stack root) or
	// imported with a source the user pastes. Import never overwrites: name
	// and directory conflicts are refused and explained.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import FolderSearch from '@lucide/svelte/icons/folder-search';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { importStack } from '$lib/features/stacks/actions';
	import { canInEnvironment } from '$lib/features/stacks/model';
	import { discoveredQuery, stackKeys, type DiscoveredStack } from '$lib/features/stacks/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		CodeEditor,
		ConfirmDialog,
		Dialog,
		EmptyState,
		ErrorState,
		Notice,
		OfflineEnvironment,
		Select,
		Skeleton,
		StatusBadge,
		errorView,
		toast
	} from '$lib/ui';

	usePage({
		title: 'Import project',
		crumbs: [{ label: 'Stacks', href: routes.stacks() }, { label: 'Import project' }],
		environmentScoped: true
	});

	const queryClient = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const allowed = $derived(
		(envs.data ?? []).filter((e) => canInEnvironment(perms.data, 'stack.import', e.id))
	);
	let environmentId = $state(
		page.url.searchParams.get('environment') ?? environmentSelection.id ?? ''
	);
	$effect(() => {
		if ((!environmentId || !allowed.some((e) => e.id === environmentId)) && allowed.length)
			environmentId = allowed[0].id;
	});
	const env = $derived(allowed.find((e) => e.id === environmentId));
	const projects = createQuery(() => ({
		...discoveredQuery(environmentId),
		enabled: !!env && env.online
	}));
	const unmanaged = $derived((projects.data ?? []).filter((p) => !p.stackId));
	const managed = $derived((projects.data ?? []).filter((p) => p.stackId));

	// Adopt in place.
	let adopting = $state<DiscoveredStack | null>(null);
	let adoptOpen = $state(false);
	async function adopt() {
		const p = adopting;
		if (!p) return;
		try {
			const st = await importStack(environmentId, { projectName: p.name });
			done(st.id, st.displayName || st.name);
		} catch (e) {
			throw new Error(conflictMessage(e, p.name), { cause: e });
		}
	}

	// Import with an explicit source.
	let sourceFor = $state<DiscoveredStack | null>(null);
	let sourceOpen = $state(false);
	let compose = $state('');
	let envFile = $state('');
	let importing = $state(false);
	let importError = $state<string | null>(null);
	function openSource(p: DiscoveredStack) {
		sourceFor = p;
		compose = '';
		envFile = '';
		importError = null;
		sourceOpen = true;
	}
	async function importWithSource() {
		const p = sourceFor;
		if (!p) return;
		importing = true;
		importError = null;
		try {
			const st = await importStack(environmentId, {
				projectName: p.name,
				source: { compose, env: envFile.trim() ? envFile : undefined }
			});
			sourceOpen = false;
			done(st.id, st.displayName || st.name);
		} catch (e) {
			importError = conflictMessage(e, p.name);
		} finally {
			importing = false;
		}
	}

	function done(id: string, title: string) {
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
		toast.success(`Imported ${title}`);
		void goto(routes.stack(id));
	}

	function conflictMessage(e: unknown, name: string): string {
		const v = errorView(e);
		switch (v.code) {
			case 'stack_name_taken':
				return `Docker Manager already manages a stack named ${name} here. Nothing was changed.`;
			case 'stack_directory_exists':
				return `A directory ${name} already exists in the stacks volume. Nothing was overwritten: move it away or adopt the project in place.`;
			case 'stack_not_adoptable':
				return `${name} cannot be adopted in place: its files are outside the stacks volume and the registered stack roots. Import it with its Compose source instead.`;
			case 'invalid_definition':
				return `The Compose source is not valid: ${v.fields.map((f) => f.message).join('; ') || v.message}`;
		}
		return v.message;
	}
</script>

{#snippet project(p: DiscoveredStack)}
	{@const running = p.services.reduce((n, s) => n + s.running, 0)}
	{@const total = p.services.reduce((n, s) => n + s.containers, 0)}
	<li class="item">
		<div class="row">
			<div class="title">
				<span class="name mono">{p.name}</span>
				<StatusBadge
					status={running === 0 ? 'stopped' : running < total ? 'partial' : 'running'}
					label="{running} / {total} running"
				/>
				{#if p.stackId}<Badge tone="accent">Managed</Badge>{:else if p.adoptable}<Badge
						tone="ok">Can be adopted in place</Badge
					>{:else}<Badge>Needs its Compose source</Badge>{/if}
			</div>
			<div class="actions">
				{#if p.stackId}
					<Button size="sm" href={routes.stack(p.stackId)}>Open stack</Button>
				{:else}
					{#if p.adoptable}
						<Button
							size="sm"
							variant="primary"
							onclick={() => {
								adopting = p;
								adoptOpen = true;
							}}>Adopt in place</Button
						>
					{/if}
					<Button size="sm" onclick={() => openSource(p)}>Import with source</Button>
				{/if}
			</div>
		</div>
		{#if p.workingDir}
			<p class="dir">
				<span class="muted">Directory</span> <span class="mono">{p.workingDir}</span>
			</p>
		{/if}
		{#if !p.stackId && !p.adoptable && p.reason}<p class="muted">{p.reason}</p>{/if}
		<ul class="services" role="list" aria-label="Services of {p.name}">
			{#each p.services as s (s.name)}
				<li>
					<span class="mono svc">{s.name}</span>
					<span class="mono muted">{s.image}</span>
					<span class="num muted">{s.running} / {s.containers}</span>
				</li>
			{/each}
		</ul>
	</li>
{/snippet}

<div class="page">
	<header class="head">
		<div>
			<h1>Import a Compose project</h1>
			<p class="muted">
				Projects running on the Engine that Docker Manager does not manage yet. Container
				labels only name them: Docker Manager needs their real Compose files to manage them.
			</p>
		</div>
		<div class="tools">
			{#if allowed.length > 1}
				<Select
					label="Environment"
					hideLabel
					bind:value={environmentId}
					options={allowed.map((e) => ({
						value: e.id,
						label: e.online ? e.name : `${e.name} (offline)`
					}))}
				/>
			{/if}
			<Button
				icon={RefreshCw}
				onclick={() => projects.refetch()}
				disabled={!env?.online}
				loading={projects.isFetching}>Refresh</Button
			>
		</div>
	</header>

	{#if envs.isPending || perms.isPending}
		<div aria-busy="true"><Skeleton lines={5} /></div>
	{:else if (envs.data ?? []).length === 0}
		<EmptyState
			icon={FolderSearch}
			color="blue"
			title="No environments yet."
			description="Compose projects are found on an environment's Docker Engine. Add an environment first."
			level={2}
		>
			{#snippet actions()}<Button variant="primary" href={routes.environments()}
					>Open environments</Button
				>{/snippet}
		</EmptyState>
	{:else if allowed.length === 0}
		<EmptyState
			icon={FolderSearch}
			color="blue"
			title="You can't import projects in any environment."
			description="Ask the owner of this Docker Manager for the permission to import stacks."
			level={2}
		/>
	{:else if env && !env.online}
		<OfflineEnvironment name={env.name} since={env.connectionChangedAt} />
	{:else if projects.isPending}
		<div aria-busy="true"><Skeleton lines={6} /></div>
	{:else if projects.isError}
		<ErrorState
			error={projects.error}
			title="The projects on {env?.name} could not be listed."
			onretry={() => projects.refetch()}
		/>
	{:else}
		<Card title="Not managed yet" id="unmanaged" subtitle={env?.name}>
			{#if unmanaged.length === 0}
				<EmptyState
					icon={FolderSearch}
					color="blue"
					title="Every Compose project on {env?.name} is managed."
					description="Create a new stack, or refresh after starting a project with Compose."
					level={3}
					compact
				>
					{#snippet actions()}<Button
							variant="primary"
							href={routes.newStack(environmentId)}>Create stack</Button
						>{/snippet}
				</EmptyState>
			{:else}
				<ul class="list" role="list">
					{#each unmanaged as p (p.name)}{@render project(p)}{/each}
				</ul>
			{/if}
		</Card>
		{#if managed.length}
			<Card title="Already managed" id="managed">
				<ul class="list" role="list">
					{#each managed as p (p.name)}{@render project(p)}{/each}
				</ul>
			</Card>
		{/if}
	{/if}
</div>

{#if adopting}
	<ConfirmDialog
		bind:open={adoptOpen}
		title="Adopt {adopting.name} in place?"
		consequences={[
			`Reads the Compose files in ${adopting.workingDir ?? 'its project directory'} as the stack's first revision.`,
			'Nothing is written, pulled or restarted; the containers keep running.',
			'Docker Manager manages the project from then on: deploys use these files.'
		]}
		confirmLabel="Adopt in place"
		onconfirm={adopt}
	/>
{/if}

{#if sourceFor}
	<Dialog
		bind:open={sourceOpen}
		title="Import {sourceFor.name} with its source"
		description="Paste the project's compose.yaml (and .env). They are written into a new directory {sourceFor.name} of the stacks volume; nothing existing is overwritten."
		size="lg"
		dismissible={!importing}
	>
		<div class="source">
			<Notice tone="info" title="The running containers are not touched now.">
				Your next deploy of this stack recreates what differs from this source, under the
				same project name.
			</Notice>
			<CodeEditor
				value=""
				label="compose.yaml of {sourceFor.name}"
				height="280px"
				onchange={(t) => (compose = t)}
			/>
			<CodeEditor
				value=""
				label=".env of {sourceFor.name} (optional)"
				height="100px"
				onchange={(t) => (envFile = t)}
			/>
			{#if importError}<p class="error" role="alert">{importError}</p>{/if}
		</div>
		{#snippet footer()}
			<Button variant="ghost" onclick={() => (sourceOpen = false)} disabled={importing}
				>Cancel</Button
			>
			<Button
				variant="primary"
				loading={importing}
				disabled={!compose.trim()}
				onclick={importWithSource}>Import project</Button
			>
		{/snippet}
	</Dialog>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-4);
	}

	h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.head p {
		max-width: 72ch;
		margin-top: 2px;
		font-size: var(--text-control);
	}

	.tools {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	.list {
		display: grid;
		gap: var(--space-3);
	}

	.item {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.title {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.actions {
		display: flex;
		gap: var(--space-2);
	}

	.dir {
		overflow-wrap: anywhere;
	}

	.services {
		display: grid;
		gap: 2px;
		margin: 0;
		padding-left: 18px;
	}

	.services li {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-3);
	}

	.svc {
		color: var(--text-default);
	}

	.source {
		display: grid;
		gap: var(--space-3);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
