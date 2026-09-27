<script lang="ts">
	// Import project (#7) as a dialog over the stack list: the Compose
	// projects an environment's Engine runs, read from container labels,
	// each with one Import action. Docker Manager picks the safe way: a
	// project whose files already lie in a stack root is adopted in place
	// (nothing restarts); a project the agent reads through an import mount
	// (below /import) is copied with its whole directory into the stacks
	// volume by a job that stops it, copies and verifies the files,
	// recreates it from the copy and starts what ran before. The job's
	// progress shows in the row. Anything else explains how to make it
	// importable. Nothing existing is ever overwritten.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import FolderSearch from '@lucide/svelte/icons/folder-search';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import type { Job } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Dialog,
		EmptyState,
		ErrorState,
		JobProgress,
		OfflineEnvironment,
		Select,
		Skeleton,
		StatusBadge,
		errorView,
		toast
	} from '$lib/ui';
	import { importStack, importStackByCopy } from './actions';
	import { canInEnvironment } from './model';
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
	const list = $derived(
		[...(projects.data ?? [])].sort(
			(a, b) => Number(!!a.stackId) - Number(!!b.stackId) || a.name.localeCompare(b.name)
		)
	);

	// Per project (by environment and name): the running import job, the
	// in-place request in flight, the last error.
	let jobs = $state<Record<string, string>>({});
	let busy = $state<Record<string, boolean>>({});
	let errors = $state<Record<string, string>>({});
	const keyOf = (p: DiscoveredStack) => `${environmentId}/${p.name}`;
	const running = (p: DiscoveredStack) => p.services.filter((s) => s.running > 0).length;
	const containers = (p: DiscoveredStack) => p.services.reduce((n, s) => n + s.containers, 0);
	const runningContainers = (p: DiscoveredStack) => p.services.reduce((n, s) => n + s.running, 0);

	function refresh() {
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
	}

	async function start(p: DiscoveredStack) {
		const k = keyOf(p);
		busy = { ...busy, [k]: true };
		errors = { ...errors, [k]: '' };
		try {
			if (p.adoptable) {
				const st = await importStack(environmentId, { projectName: p.name });
				toast.success(`Imported ${st.displayName || st.name}`);
				refresh();
			} else {
				const job: Job = await importStackByCopy(environmentId, { projectName: p.name });
				jobs = { ...jobs, [k]: job.id };
			}
		} catch (e) {
			errors = { ...errors, [k]: failure(e, p.name) };
		} finally {
			busy = { ...busy, [k]: false };
		}
	}

	function finished(p: DiscoveredStack, j: Job) {
		refresh();
		if (j.state === 'succeeded') toast.success(`Imported ${p.name}`);
	}

	function failure(e: unknown, name: string): string {
		const v = errorView(e);
		switch (v.code) {
			case 'stack_name_taken':
				return `Docker Manager already manages a stack named ${name} here. Nothing was changed.`;
			case 'stack_directory_exists':
				return `A directory ${name} already exists in the stacks volume. Nothing was overwritten: move it away and import again.`;
			case 'stack_not_adoptable':
			case 'stack_not_copyable':
				return `${name} cannot be imported: ${v.message}`;
			case 'protected':
				return `${name} is Docker Manager's own project and is never stopped. Keep it in a stack root to import it in place.`;
			case 'agent_unsupported':
				return 'The agent of this environment cannot import projects by copy yet. Update the agent, then import again.';
		}
		return v.message;
	}

	/** The agent's reason as a sentence. */
	function sentence(s: string): string {
		const t = s.trim();
		return t ? `${t[0].toUpperCase()}${t.slice(1)}${/[.!?]$/.test(t) ? '' : '.'}` : t;
	}

	function how(p: DiscoveredStack): string {
		if (p.adoptable)
			return 'Its files already lie in a stack root: they are adopted in place and nothing restarts.';
		const n = running(p);
		return n
			? `Stops its ${n} running ${n === 1 ? 'service' : 'services'}, copies its whole directory (data folders included, owners and permissions kept) into the stacks volume, checks the copy and starts ${n === 1 ? 'it' : 'them'} again from there. The original directory is left untouched.`
			: 'Copies its whole directory (data folders included, owners and permissions kept) into the stacks volume and recreates its containers from the copy; it stays stopped. The original directory is left untouched.';
	}
</script>

<Dialog
	bind:open
	title="Import project"
	description="Compose projects running on the environment that Docker Manager does not manage yet."
	size="lg"
>
	<div class="body">
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
					{@const total = containers(p)}
					{@const up = runningContainers(p)}
					<li class="item">
						<div class="row">
							<div class="title">
								<span class="name mono">{p.name}</span>
								<StatusBadge
									status={up === 0
										? 'stopped'
										: up < total
											? 'partial'
											: 'running'}
									label="{up} / {total} running"
								/>
								{#if p.stackId}<Badge tone="accent">Managed</Badge>{/if}
							</div>
							<div class="actions">
								{#if p.stackId}
									<Button size="sm" href={routes.stack(p.stackId)}
										>Open stack</Button
									>
								{:else if (p.adoptable || p.copyable) && !jobs[k]}
									<Button
										size="sm"
										variant="primary"
										loading={busy[k]}
										onclick={() => start(p)}>Import</Button
									>
								{/if}
							</div>
						</div>
						{#if p.workingDir}
							<p class="dir mono">{p.workingDir}</p>
						{/if}
						{#if p.sourceDir && p.sourceDir !== p.workingDir}
							<p class="dir">
								<span class="muted">Files on the host</span>
								<span class="mono">{p.sourceDir}</span>
							</p>
						{/if}
						<p class="muted services">
							{p.services.map((s) => s.name).join(', ')}
						</p>
						{#if !p.stackId}
							{#if jobs[k]}
								<JobProgress
									jobId={jobs[k]}
									title="Import {p.name}"
									onfinish={(j) => finished(p, j)}
								/>
							{:else if p.adoptable || p.copyable}
								<p class="how">{how(p)}</p>
							{:else}
								<p class="muted">
									{sentence(
										p.reason ??
											'Docker Manager cannot read this project directory.'
									)}
								</p>
							{/if}
							{#if errors[k]}<p class="error" role="alert">{errors[k]}</p>{/if}
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</div>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)}>Close</Button>
	{/snippet}
</Dialog>

<style>
	.body {
		display: grid;
		gap: var(--space-3);
	}

	.tools {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: var(--space-2);
	}

	.list {
		display: grid;
		gap: var(--space-3);
		max-height: 60vh;
		overflow-y: auto;
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

	.dir,
	.services {
		overflow-wrap: anywhere;
		font-size: var(--text-caption);
	}

	.how {
		font-size: var(--text-caption);
		color: var(--text-default);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
