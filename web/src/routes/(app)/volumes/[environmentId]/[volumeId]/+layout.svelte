<script lang="ts">
	// Volume detail (#6): header with usage, size and actions ("Browse
	// Files"; removal with the server's preview is the last entry of the
	// "More Actions" menu), what Docker Manager refuses and why (#32,
	// managed stacks, non-local drivers #28), and the tabs: Overview here,
	// Files (#15 volume file manager), Backups (#10) and Migrate (#35, only
	// with another environment to move to) as child routes. Its running
	// jobs come from the running list (also after a reload), except those
	// the open tab shows itself (files, backups, the migration).
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Layers from '@lucide/svelte/icons/layers';
	import Server from '@lucide/svelte/icons/server';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { ApiRequestError } from '$lib/api/client';
	import { myPermissionsQuery, volumeQuery, volumeUsageQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		TabNav,
		formatBytes,
		formatDateTime,
		formatRelative,
		type MenuEntry,
		type MetaItem,
		type TabLink
	} from '$lib/ui';
	import ObjectRemoveHost from '$lib/features/resources/ObjectRemoveHost.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import { volumeJobs, volumeTab } from '$lib/features/resources/object-jobs';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { volumeAccess } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { protectionLabel, sentence } from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';
	import { canAnywhere } from '$lib/features/stacks/model';
	import { onlyOneEnvironment } from '$lib/features/common/data';

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
	const tab = $derived(volumeTab(page.url.pathname));
	const jobs = useTrackedJobs(() => (env && name ? volumeJobs(env, name, tab) : null));
	const access = $derived(v ? volumeAccess(v) : { local: true });
	const filesOpen = $derived(
		!!v && access.local && !v.protection && can(v.actions, 'volume.files.read')
	);
	const oneEnvironment = $derived(onlyOneEnvironment(scope.envs.data));
	// Migrate shows once: as a tab, and only with another environment to move to.
	const migratable = $derived(
		!!v && !v.protection && !oneEnvironment && can(v.actions, 'volume.migrate')
	);
	// The size the list shows (the Engine's disk usage, reused for a minute).
	const usage = createQuery(() => ({ ...volumeUsageQuery(env), enabled: !!v }));
	const size = $derived(usage.data?.items.find((i) => i.name === name)?.sizeBytes);
	const perms = createQuery(() => myPermissionsQuery());
	const backedUp = $derived(!!v && !v.protection && canAnywhere(perms.data, 'backup.read'));

	const tabs = $derived<TabLink[]>([
		{ href: routes.volume(env, name), label: 'Overview' },
		...(filesOpen ? [{ href: routes.volume(env, name, 'files'), label: 'Files' }] : []),
		...(backedUp ? [{ href: routes.volume(env, name, 'backups'), label: 'Backups' }] : []),
		...(migratable ? [{ href: routes.volume(env, name, 'migrate'), label: 'Migrate' }] : [])
	]);
	const meta = $derived<MetaItem[]>(
		v
			? [
					...(oneEnvironment ? [] : [{ icon: Server, label: envName }]),
					...(size !== undefined ? [{ icon: HardDrive, label: formatBytes(size) }] : []),
					...(v.driver && v.driver !== 'local'
						? [{ icon: HardDrive, label: `Driver ${v.driver}` }]
						: []),
					...(v.stack
						? [
								{
									icon: Layers,
									label: v.stack.project,
									title: v.stack.managed ? 'Managed Stack' : 'Compose Project'
								}
							]
						: []),
					...(v.createdAt
						? [
								{
									icon: Clock,
									label: `Created ${formatRelative(v.createdAt)}`,
									title: formatDateTime(v.createdAt)
								}
							]
						: [])
				]
			: []
	);
	/** Removal last, after a separator (one rule on every resource page). */
	const overflow = $derived.by<MenuEntry[]>(() => {
		// Docker Manager's own volumes are never removed: no entry (the notice says why).
		if (!v || v.protection || !can(v.actions, 'volume.remove')) return [];
		return [
			{
				label: 'Remove',
				tone: 'danger',
				onSelect: () =>
					remover?.request({
						kind: 'volume',
						environmentId: env,
						name: v.name,
						protection: v.protection,
						usedBy: v.usedBy
					})
			}
		];
	});
</script>

<ObjectRemoveHost
	bind:this={remover}
	environmentName={() => envName}
	onremoved={() => goto(routes.volumes())}
	onstarted={(j) => jobs.add(j)}
/>

<Page>
	{#if q.isPending}
		<div class="loading" aria-busy="true">
			<Skeleton height="56px" radius="lg" /><Skeleton lines={4} height="20px" />
		</div>
	{:else if offline}
		<PageHeader title={name} {...resourceIcon('volume')} />
		<OfflineEnvironment name={envName} since={scope.environment(env)?.connectionChangedAt} />
	{:else if notFound}
		<EmptyState
			icon={resourceIcon('volume').icon}
			color="slate"
			title="No volume named {name} on {envName}."
			description="It may have been removed, or you don't have access to it."
			level={1}
		>
			{#snippet actions()}<Button variant="secondary" href={routes.volumes()}
					>Back to Volumes</Button
				>{/snippet}
		</EmptyState>
	{:else if q.isError}
		<ErrorState
			error={q.error}
			title="The volume could not be loaded."
			onretry={() => q.refetch()}
		/>
	{:else if v}
		<PageHeader title={v.name} truncate {...resourceIcon('volume')} {meta}>
			{#snippet status()}
				{#if v.inUse}<Badge tone="ok" dot>In Use</Badge>{:else}<Badge>Unused</Badge>{/if}
				{#if !access.local}<Badge tone="warn">Read-Only</Badge>{/if}
				{#if v.protection}<ProtectionBadge
						protection={v.protection}
						label="Used by Docker Manager"
					/>{/if}
			{/snippet}
			{#snippet actions()}
				{#if filesOpen && !page.url.pathname.endsWith('/files')}
					<Button
						variant="secondary"
						icon={FolderOpen}
						href={routes.volume(env, name, 'files')}>Browse Files</Button
					>
				{/if}
				{#if overflow.length}
					<Menu items={overflow} label="More Actions for {v.name}" align="end">
						{#snippet trigger(props)}
							<IconButton
								{...props}
								icon={Ellipsis}
								label="More Actions"
								variant="secondary"
							/>
						{/snippet}
					</Menu>
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
				{sentence(v.protection.reason)} Docker Manager never removes or migrates it and keeps
				it out of the file manager, for everyone including the owner. Use Docker on the host if
				you really need to.
			</Notice>
		{:else if !access.local}
			<Notice
				tone="warn"
				icon={TriangleAlert}
				title="Read-Only in Docker Manager"
				live="none"
			>
				{access.reason}
			</Notice>
		{/if}
		<ActiveJobs {jobs} variant="inline" label="Running Jobs of {name}" />

		<TabNav items={tabs} current={page.url.pathname} label="Volume Sections" />
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
