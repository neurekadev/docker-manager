<script lang="ts">
	// Import project (#7) as a dialog over the stack list: the Compose
	// projects an environment's Engine runs, read from container labels,
	// each with one Import action. Docker Manager picks the safe way: a
	// project whose files already lie in a stack root is adopted in place
	// (nothing restarts); a project the agent reads through an import mount
	// (below /import) is copied with its whole directory into the stacks
	// volume by a job that stops it, copies and verifies the files,
	// recreates it from the copy and starts what ran before (confirmed
	// first, with its consequences); Docker Manager's own project
	// (protected) is copied while it runs and moves onto the copy at its
	// next deploy. Each row says in one line what happens, the rest (and
	// the host folder) waits behind Details; the job's progress shows in
	// the row. Nothing existing is ever overwritten. Projects Docker
	// Manager manages already are hidden unless the switch shows them.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import FolderSearch from '@lucide/svelte/icons/folder-search';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import type { Job } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		ConfirmDialog,
		Dialog,
		EmptyState,
		ErrorState,
		JobProgress,
		OfflineEnvironment,
		Select,
		Skeleton,
		StatusBadge,
		Switch,
		errorView,
		toast
	} from '$lib/ui';
	import { importStack, importStackByCopy } from './actions';
	import {
		containerCounts,
		importConsequences,
		importDetails,
		importFailure,
		importHow,
		importMode,
		importNeedsConfirm,
		jobStackId,
		projectStatus
	} from './importing';
	import { canInEnvironment, importCandidates } from './model';
	import { discoveredQuery, stackKeys, type DiscoveredStack } from './queries';

	let {
		open = $bindable(false),
		environmentId: suggested = null
	}: { open?: boolean; environmentId?: string | null } = $props();

	const queryClient = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const allowed = $derived(
		(envs.data ?? []).filter((e) => canInEnvironment(perms.data, 'stack.import', e.id))
	);
	let environmentId = $state('');
	$effect(() => {
		if (!open) return;
		if (!environmentId || !allowed.some((e) => e.id === environmentId)) {
			const pick = allowed.find((e) => e.id === suggested) ?? allowed[0];
			if (pick) environmentId = pick.id;
		}
	});
	const env = $derived(allowed.find((e) => e.id === environmentId));
	const projects = createQuery(() => ({
		...discoveredQuery(environmentId),
		enabled: open && !!env && env.online
	}));
	// Per project (by environment and name): the running import job, the
	// in-place request in flight, the last error, and whether an import
	// started here (its row stays when managed projects are hidden).
	let jobs = $state<Record<string, string>>({});
	let busy = $state<Record<string, boolean>>({});
	let errors = $state<Record<string, string>>({});
	let started = $state<Record<string, boolean>>({});
	const keyOf = (p: DiscoveredStack) => `${environmentId}/${p.name}`;
	let hideManaged = $state(true);
	const list = $derived(
		importCandidates([...(projects.data ?? [])], hideManaged, (p) => !!started[keyOf(p)])
	);
	// The project whose copy would stop running services, while its
	// confirmation is open.
	let confirming = $state<DiscoveredStack | null>(null);
	let confirmOpen = $state(false);

	function refresh() {
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
	}

	function openStack(id: string) {
		open = false;
		void goto(routes.stack(id));
	}

	function imported(name: string, stackId: string | undefined) {
		toast.success(`Imported ${name}`, {
			action: stackId ? { label: 'Open stack', onclick: () => openStack(stackId) } : undefined
		});
	}

	function importProject(p: DiscoveredStack) {
		if (importNeedsConfirm(p)) {
			confirming = p;
			confirmOpen = true;
			return;
		}
		return start(p);
	}

	async function start(p: DiscoveredStack) {
		const k = keyOf(p);
		started = { ...started, [k]: true };
		busy = { ...busy, [k]: true };
		errors = { ...errors, [k]: '' };
		try {
			if (p.adoptable) {
				const st = await importStack(environmentId, { projectName: p.name });
				imported(st.displayName || st.name, st.id);
				refresh();
			} else {
				const job: Job = await importStackByCopy(environmentId, { projectName: p.name });
				jobs = { ...jobs, [k]: job.id };
			}
		} catch (e) {
			errors = { ...errors, [k]: importFailure(errorView(e), p.name) };
		} finally {
			busy = { ...busy, [k]: false };
		}
	}

	function finished(p: DiscoveredStack, j: Job) {
		refresh();
		if (j.state === 'succeeded') imported(p.name, jobStackId(j));
	}
</script>

<Dialog
	bind:open
	title="Import project"
	description="Compose projects on the environment, running or stopped, that Docker Manager does not manage yet."
	size="lg"
>
	<div class="body">
		<div class="tools">
			<Switch bind:checked={hideManaged} label="Hide managed stacks" />
			<span class="spacer"></span>
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
				size="sm"
				onclick={() => projects.refetch()}
				disabled={!env?.online}
				loading={projects.isFetching}>Refresh</Button
			>
		</div>

		{#if envs.isPending || perms.isPending}
			<div aria-busy="true"><Skeleton lines={4} /></div>
		{:else if allowed.length === 0}
			<EmptyState
				icon={FolderSearch}
				color="blue"
				title="You can't import projects in any environment."
				description="Ask the owner of this Docker Manager for the permission to import stacks."
				level={3}
				compact
			/>
		{:else if env && !env.online}
			<OfflineEnvironment name={env.name} since={env.connectionChangedAt} />
		{:else if projects.isPending}
			<div aria-busy="true"><Skeleton lines={5} /></div>
		{:else if projects.isError}
			<ErrorState
				error={projects.error}
				title="The projects on {env?.name} could not be listed."
				onretry={() => projects.refetch()}
			/>
		{:else if list.length === 0 && (projects.data ?? []).length > 0}
			<EmptyState
				icon={FolderSearch}
				color="blue"
				title="Every Compose project on {env?.name} is already managed."
				description="Turn off “Hide managed stacks” to see them."
				level={3}
				compact
			/>
		{:else if list.length === 0}
			<EmptyState
				icon={FolderSearch}
				color="blue"
				title="No Compose projects run on {env?.name}."
				description="Start a project with Compose on the host, then refresh."
				level={3}
				compact
			/>
		{:else}
			<ul class="list" role="list" aria-label="Compose projects on {env?.name}">
				{#each list as p (p.name)}
					{@const k = keyOf(p)}
					{@const c = containerCounts(p)}
					{@const mode = importMode(p)}
					{@const details = importDetails(p)}
					<li class="item">
						<div class="row">
							<div class="title">
								<span class="name">{p.name}</span>
								<StatusBadge
									status={projectStatus(c)}
									label="{c.up} of {c.total} running"
								/>
								{#if p.stackId}<Badge tone="accent">Managed</Badge>{/if}
							</div>
							<div class="actions">
								{#if p.stackId}
									<Button size="sm" href={routes.stack(p.stackId)}
										>Open stack</Button
									>
								{:else if mode !== 'blocked' && !jobs[k]}
									<Button
										size="sm"
										variant="primary"
										loading={busy[k]}
										onclick={() => importProject(p)}>Import</Button
									>
								{/if}
							</div>
						</div>
						<p class="services" title={p.services.map((s) => s.name).join(', ')}>
							{p.services.map((s) => s.name).join(', ')}
						</p>
						{#if jobs[k]}
							<JobProgress
								jobId={jobs[k]}
								title="Import {p.name}"
								onfinish={(j) => finished(p, j)}
							/>
						{:else if !p.stackId}
							<p class="how" class:blocked={mode === 'blocked'}>{importHow(p)}</p>
							{#if details.length || p.sourceDir || p.workingDir}
								<Disclosure
									summary={mode === 'blocked'
										? 'Why and how to fix it'
										: 'Details'}
								>
									<ul class="details" role="list">
										{#each details as line (line)}<li>{line}</li>{/each}
									</ul>
									{#if p.sourceDir || p.workingDir}
										<p class="folder">
											<span class="muted">Folder on the host</span>
											<span class="mono">{p.sourceDir || p.workingDir}</span>
										</p>
									{/if}
								</Disclosure>
							{/if}
						{/if}
						{#if errors[k] && !p.stackId}<p class="error" role="alert">
								{errors[k]}
							</p>{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</div>
</Dialog>

<ConfirmDialog
	bind:open={confirmOpen}
	title="Import {confirming?.name ?? 'the project'}?"
	consequences={confirming ? importConsequences(confirming) : []}
	confirmLabel="Stop and import"
	onconfirm={() => (confirming ? start(confirming) : undefined)}
/>

<style>
	.body {
		display: grid;
		gap: var(--space-3);
	}

	.tools {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.spacer {
		flex: 1;
	}

	.list {
		display: grid;
		gap: var(--space-2);
		max-height: 60vh;
		overflow-y: auto;
	}

	.item {
		display: grid;
		gap: var(--space-1);
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
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		overflow-wrap: anywhere;
	}

	.actions {
		display: flex;
		gap: var(--space-2);
	}

	.services {
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.how {
		color: var(--text-default);
	}

	.how.blocked {
		color: var(--text-muted);
	}

	.details {
		display: grid;
		gap: var(--space-1);
		margin: 0;
		padding-left: 18px;
		color: var(--text-default);
		font-size: var(--text-caption);
		list-style: disc;
	}

	.folder {
		display: grid;
		gap: 2px;
		overflow-wrap: anywhere;
		font-size: var(--text-caption);
	}

	.error {
		margin-top: var(--space-1);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
