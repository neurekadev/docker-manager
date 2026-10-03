<script lang="ts">
	// Volume file manager (#15): the same file manager as a stack's Files
	// tab, rooted at a local volume's data directory. A child route of the
	// volume detail; works on its own too. Non-local drivers, the stacks
	// volume and Docker Manager's own volumes are refused by the agent
	// (volume_files_unsupported) and explained here.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import { ApiRequestError } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import FileManager from '$lib/features/files/FileManager.svelte';
	import { fillViewport } from '$lib/features/files/fill';
	import { volumeQuery } from '$lib/features/files/resources';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, EmptyState, ErrorState, Skeleton } from '$lib/ui';

	const environmentId = $derived(page.params.environmentId ?? '');
	const volumeId = $derived(page.params.volumeId ?? '');
	const volume = createQuery(() => volumeQuery(environmentId, volumeId));
	const envs = createQuery(() => environmentsQuery());
	const env = $derived(envs.data?.find((e) => e.id === environmentId));

	// The same trail as the volume's other tabs: Volumes / env / name / Files.
	usePage(() => ({
		title: `${volumeId} Files`,
		crumbs: [
			{ label: 'Volumes', href: routes.volumes() },
			{ label: env?.name ?? environmentId },
			{ label: volumeId, href: routes.volume(environmentId, volumeId) },
			{ label: 'Files' }
		]
	}));

	const unsupported = $derived(
		!!volume.data?.protection ||
			(volume.data?.driver !== undefined && volume.data.driver !== 'local')
	);
</script>

<div class="page" use:fillViewport={{ min: 520 }}>
	{#if volume.isPending}
		<div aria-busy="true"><Skeleton lines={10} /></div>
	{:else if volume.error}
		{#if volume.error instanceof ApiRequestError && volume.error.status === 404}
			<EmptyState
				icon={HardDrive}
				title="This volume doesn't exist"
				description="It may have been removed, or you don't have access to it."
			>
				{#snippet actions()}<Button href={routes.volumes()}>Open Volumes</Button>{/snippet}
			</EmptyState>
		{:else}
			<ErrorState
				error={volume.error}
				title="The volume could not be loaded."
				onretry={() => volume.refetch()}
			/>
		{/if}
	{:else if volume.data && unsupported}
		<EmptyState
			icon={HardDrive}
			title="{volumeId} can't be browsed here"
			description={volume.data.protection
				? "It holds Docker Manager's own data, which the file manager never opens."
				: `Only local volumes can be browsed. Its driver is ${volume.data.driver}.`}
		/>
	{:else if volume.data}
		{#key `${environmentId}/${volumeId}`}
			<FileManager
				scope={{ kind: 'volume', environmentId, volume: volumeId }}
				rootLabel={volumeId}
				capabilities={volume.data.actions}
				environmentOnline={env?.online ?? true}
				environmentName={env?.name ?? 'The environment'}
				label="Files of Volume {volumeId}"
			/>
		{/key}
	{/if}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		min-height: 520px;
	}
</style>
