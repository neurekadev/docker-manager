<script lang="ts">
	// Container terminal (#8): a command (default /bin/sh) inside one
	// container. A child route of the container detail; works on its own.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { fillViewport } from '$lib/features/files/fill';
	import { environmentsQuery } from '$lib/api/queries';
	import { containerQuery } from '$lib/features/files/resources';
	import TerminalPanel from '$lib/features/terminal/TerminalPanel.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import LockKeyhole from '@lucide/svelte/icons/lock-keyhole';
	import { EmptyState, ErrorState, Skeleton } from '$lib/ui';

	const environmentId = $derived(page.params.environmentId ?? '');
	const containerId = $derived(page.params.containerId ?? '');
	const container = createQuery(() => containerQuery(environmentId, containerId));
	const name = $derived(container.data?.name ?? containerId);
	const envs = createQuery(() => environmentsQuery());
	const envName = $derived(envs.data?.find((e) => e.id === environmentId)?.name ?? environmentId);

	// The same trail as the container's other tabs: Containers / env / name / Terminal.
	usePage(() => ({
		title: `${name} terminal`,
		crumbs: [
			{ label: 'Containers', href: routes.containers() },
			{ label: envName },
			{ label: name, href: routes.container(environmentId, containerId) },
			{ label: 'Terminal' }
		]
	}));
</script>

<div class="page" use:fillViewport={{ bottom: 24, min: 420 }}>
	{#if container.isPending}
		<div aria-busy="true"><Skeleton lines={10} /></div>
	{:else if container.error}
		<ErrorState
			error={container.error}
			title="The container could not be loaded."
			onretry={() => container.refetch()}
		/>
	{:else if container.data && !container.data.actions.includes('container.exec')}
		<EmptyState
			icon={LockKeyhole}
			title="You can't open terminals in {name}"
			description="Ask the owner of this Docker Manager for the “Open terminal” permission on {name}."
		/>
	{:else if container.data}
		<TerminalPanel
			choices={[
				{
					environmentId,
					containerId,
					label: name,
					unavailable: container.data.state === 'running' ? null : container.data.state
				}
			]}
			label="Terminal of {name}"
		/>
	{/if}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		min-height: 420px;
	}
</style>
