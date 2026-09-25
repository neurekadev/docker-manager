<script lang="ts">
	// Volume detail (#6): header with usage and actions (migrate #35,
	// remove with consequences), what DockYard refuses and why (#32, managed
	// stacks, non-local drivers #28), and the tabs: Overview here, Files
	// (#15 volume file manager) as a child route.
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import Clock from '@lucide/svelte/icons/clock';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Layers from '@lucide/svelte/icons/layers';
	import Server from '@lucide/svelte/icons/server';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { ApiRequestError } from '$lib/api/client';
	import { volumeQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		EmptyState,
		ErrorState,
		JobProgress,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		TabNav,
		formatRelative,
		type MetaItem,
		type TabLink
	} from '$lib/ui';
	import ObjectRemoveHost from '$lib/features/resources/ObjectRemoveHost.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import { activeJobs, resourceKey } from '$lib/features/resources/jobs.svelte';
	import { volumeAccess } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { protectionLabel, sentence } from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	let { children }: { children: Snippet } = $props();

	const env = $derived(page.params.environmentId ?? '');
	const name = $derived(page.params.volumeId ?? '');
	const scope = useEnvironmentScope();
	const envName = $derived(scope.name(env));
	const q = createQuery(() => volumeQuery(env, name));
	const v = $derived(q.data);
	let remover = $state<ObjectRemoveHost>();

	usePage(() => ({
		title: name,
		crumbs: [{ label: 'Volumes', href: routes.volumes() }, { label: envName }, { label: name }]
	}));

	const offline = $derived(
		q.error instanceof ApiRequestError && q.error.apiError?.code === 'environment_offline'
	);
	const notFound = $derived(q.error instanceof ApiRequestError && q.error.status === 404);
	const job = $derived(activeJobs.byKey[resourceKey('volume', env, name)]);
	const access = $derived(v ? volumeAccess(v) : { local: true });
	const filesOpen = $derived(
		!!v && access.local && !v.protection && can(v.actions, 'volume.files.read')
	);
	const migratable = $derived(!!v && !v.protection && can(v.actions, 'volume.migrate'));

	const tabs = $derived<TabLink[]>([
		{ href: routes.volume(env, name), label: 'Overview' },
		...(filesOpen ? [{ href: routes.volume(env, name, 'files'), label: 'Files' }] : []),
		...(migratable ? [{ href: routes.volume(env, name, 'migrate'), label: 'Migrate' }] : [])
	]);
	const meta = $derived<MetaItem[]>(
		v
			? [
					{ icon: Server, label: envName },
					...(v.driver ? [{ icon: HardDrive, label: `Driver ${v.driver}` }] : []),
					...(v.stack
						? [
								{
									icon: Layers,
									label: v.stack.project,
									title: v.stack.managed ? 'Managed stack' : 'Compose project'
								}
							]
						: []),
					...(v.createdAt
						? [
								{
									icon: Clock,
									label: `Created ${formatRelative(v.createdAt)}`,
									title: v.createdAt
								}
							]
						: [])
				]
			: []
	);
</script>

<ObjectRemoveHost
	bind:this={remover}
	environmentName={() => envName}
	onremoved={() => goto(routes.volumes())}
/>

<Page>
	{#if q.isPending}
		<div class="loading" aria-busy="true">
			<Skeleton height="56px" radius="lg" /><Skeleton lines={4} height="20px" />
		</div>
	{:else if offline}
		<PageHeader title={name} icon={HardDrive} color="teal" />
		<OfflineEnvironment name={envName} since={scope.environment(env)?.connectionChangedAt} />
	{:else if notFound}
		<EmptyState
			icon={HardDrive}
			color="slate"
			title="No volume named {name} on {envName}."
			description="It may have been removed, or you don't have access to it."
			level={1}
		>
			{#snippet actions()}<Button variant="secondary" href={routes.volumes()}
					>Back to volumes</Button
				>{/snippet}
		</EmptyState>
	{:else if q.isError}
		<ErrorState
			error={q.error}
			title="The volume could not be loaded."
			onretry={() => q.refetch()}
		/>
	{:else if v}
		<PageHeader title={v.name} icon={HardDrive} color="teal" {meta}>
			{#snippet status()}
				{#if v.inUse}<Badge tone="ok" dot>In use</Badge>{:else}<Badge>Unused</Badge>{/if}
				{#if !access.local}<Badge tone="warn">Read-only</Badge>{/if}
				{#if v.protection}<ProtectionBadge protection={v.protection} />{/if}
			{/snippet}
			{#snippet actions()}
				{#if migratable && !page.url.pathname.endsWith('/migrate')}
					<Button
						variant="secondary"
						icon={ArrowRightLeft}
						href={routes.volume(env, name, 'migrate')}>Migrate</Button
					>
				{/if}
				{#if can(v.actions, 'volume.remove')}
					<Button
						variant="danger-soft"
						icon={Trash2}
						onclick={() =>
							remover?.request({
								kind: 'volume',
								environmentId: env,
								name: v.name,
								protection: v.protection,
								usedBy: v.usedBy
							})}>Remove</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if v.protection}
			<Notice
				tone="info"
				icon={ShieldCheck}
				title={protectionLabel(v.protection)}
				live="none"
			>
				{sentence(v.protection.reason)} DockYard never removes or migrates it and keeps it out
				of the file manager, for everyone including the owner. Use Docker on the host if you really
				need to.
			</Notice>
		{:else if !access.local}
			<Notice tone="warn" icon={TriangleAlert} title="Read-only in DockYard" live="none">
				{access.reason} Containers can still use it; DockYard lists it and can remove it.
			</Notice>
		{/if}
		{#if job}<JobProgress watcher={job} variant="inline" notices={null} />{/if}

		<TabNav items={tabs} current={page.url.pathname} label="Volume sections" />
		{@render children()}
	{/if}
</Page>

<style>
	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}
</style>
