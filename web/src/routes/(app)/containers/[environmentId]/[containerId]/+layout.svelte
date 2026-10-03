<script lang="ts">
	// Container detail (#6): header with status (and the image's update
	// state, #20), image and lifecycle actions (LifecycleButton: Stop while
	// it runs, is paused or restarts, Start otherwise; its menu has Start,
	// Restart and Stop as the state allows), Pause or Unpause right of it
	// and Settings (restart policy and limits), what Docker Manager refuses
	// on this container and why (#32 protection, managed stacks), its
	// running jobs (from the running list: they come back after a reload),
	// and the tabs: Overview here, Logs and Terminal (#8) as
	// child routes. Removal is the last entry of the "More Actions" menu.
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Layers from '@lucide/svelte/icons/layers';
	import Pause from '@lucide/svelte/icons/pause';
	import Play from '@lucide/svelte/icons/play';
	import Server from '@lucide/svelte/icons/server';
	import Settings from '@lucide/svelte/icons/settings';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { ApiRequestError } from '$lib/api/client';
	import { containerQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import LifecycleButton from '$lib/features/common/LifecycleButton.svelte';
	import type { LifecycleActions, LifecycleVerb } from '$lib/features/common/lifecycle';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		StatusBadge,
		TabNav,
		formatDateTime,
		formatRelative,
		type MenuEntry,
		type MetaItem,
		type TabLink
	} from '$lib/ui';
	import ContainerActionHost from '$lib/features/resources/ContainerActionHost.svelte';
	import EditContainerDialog from '$lib/features/resources/EditContainerDialog.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import {
		containerActions,
		type ContainerVerb
	} from '$lib/features/resources/container-actions';
	import { containerJobs } from '$lib/features/resources/object-jobs';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { containerStatus } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { protectionLabel, sentence } from '$lib/features/resources/refusals';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';
	import UpdateStatusBadge from '$lib/features/updates/UpdateStatusBadge.svelte';
	import { onlyOneEnvironment } from '$lib/features/common/data';

	let { children }: { children: Snippet } = $props();

	const env = $derived(page.params.environmentId ?? '');
	const name = $derived(page.params.containerId ?? '');
	const scope = useEnvironmentScope();
	const envName = $derived(scope.name(env));
	const q = createQuery(() => containerQuery(env, name));
	const c = $derived(q.data);

	// The address may hold the Engine ID: the crumb shows the name once loaded.
	usePage(() => ({
		title: c?.name ?? name,
		crumbs: [
			{ label: 'Containers', href: routes.containers() },
			{ label: envName },
			{ label: c?.name ?? name }
		]
	}));

	let host = $state<ContainerActionHost>();
	let editOpen = $state(false);

	const offline = $derived(
		q.error instanceof ApiRequestError && q.error.apiError?.code === 'environment_offline'
	);
	const notFound = $derived(q.error instanceof ApiRequestError && q.error.status === 404);
	const jobs = useTrackedJobs(() => (env && name ? containerJobs(env, name) : null));
	const actions = $derived(c ? containerActions(c) : []);
	const has = (verb: ContainerVerb) => actions.some((a) => a.verb === verb);

	// Start, Restart and Stop as one split button: each held one is listed,
	// off when the state does not allow it (containerActions). Docker
	// Manager's own containers keep them: the server refuses with its reason
	// (the notice below says up front what is refused).
	const running = $derived(
		c?.state === 'running' || c?.state === 'paused' || c?.state === 'restarting'
	);
	const lifecycle = $derived.by((): LifecycleActions => {
		const out: LifecycleActions = {};
		const target = c;
		if (!target) return out;
		for (const verb of ['start', 'restart', 'stop'] as LifecycleVerb[])
			if (can(target.actions, `container.${verb}`))
				out[verb] = { run: () => void host?.request(target, verb), disabled: !has(verb) };
		return out;
	});

	const tabs = $derived<TabLink[]>([
		{ href: routes.container(env, name), label: 'Overview' },
		...(c && can(c.actions, 'container.logs.read')
			? [{ href: routes.container(env, name, 'logs'), label: 'Logs' }]
			: []),
		...(c && can(c.actions, 'container.exec')
			? [{ href: routes.container(env, name, 'terminal'), label: 'Terminal' }]
			: [])
	]);

	const meta = $derived.by<MetaItem[]>(() => {
		if (!c) return [];
		const out: MetaItem[] = onlyOneEnvironment(scope.envs.data)
			? []
			: [{ icon: Server, label: envName }];
		if (c.stack)
			out.push({
				icon: Layers,
				label: c.stack.service
					? `${c.stack.project} / ${c.stack.service}`
					: c.stack.project,
				title: c.stack.managed ? 'Managed Stack' : 'Compose Project'
			});
		const started = c.details?.startedAt;
		if (c.state === 'running' && started)
			out.push({
				icon: Clock,
				label: `Started ${formatRelative(started)}`,
				title: formatDateTime(started)
			});
		else if (c.createdAt)
			out.push({
				icon: Clock,
				label: `Created ${formatRelative(c.createdAt)}`,
				title: formatDateTime(c.createdAt)
			});
		return out;
	});

	const editable = $derived(!!c && c.view === 'full' && can(c.actions, 'container.update'));

	const overflow = $derived.by<MenuEntry[]>(() => {
		if (!c) return [];
		const out: MenuEntry[] = [];
		if (c.imageId && c.view === 'full')
			out.push({ label: 'Open Image', href: routes.image(env, c.imageId) });
		// Docker Manager's own containers are never removed: no entry (the notice says why).
		if (has('remove') && !c.protection) {
			if (out.length) out.push({ separator: true });
			out.push({
				label: 'Remove…',
				tone: 'danger',
				onSelect: () => host?.request(c, 'remove')
			});
		}
		return out;
	});
</script>

<ContainerActionHost
	bind:this={host}
	environmentName={() => envName}
	onremoved={() => goto(routes.containers())}
	onstarted={(j) => jobs.add(j)}
/>
{#if c && c.view === 'full'}
	<EditContainerDialog
		bind:open={editOpen}
		container={c}
		environmentName={envName}
		onstarted={(j) => jobs.add(j)}
	/>
{/if}

<Page>
	{#if q.isPending}
		<div aria-busy="true" class="loading">
			<Skeleton height="56px" radius="lg" />
			<Skeleton lines={4} height="20px" />
		</div>
	{:else if offline}
		<PageHeader title={name} {...resourceIcon('container')} />
		<OfflineEnvironment name={envName} since={scope.environment(env)?.connectionChangedAt} />
	{:else if notFound}
		<EmptyState
			icon={resourceIcon('container').icon}
			color="slate"
			title="No container named {name} on {envName}."
			description="It may have been removed, or you don't have access to it."
			level={1}
		>
			{#snippet actions()}
				<Button variant="secondary" href={routes.containers()}>Back to Containers</Button>
			{/snippet}
		</EmptyState>
	{:else if q.isError}
		<ErrorState
			error={q.error}
			title="The container could not be loaded."
			onretry={() => q.refetch()}
		/>
	{:else if c}
		<PageHeader
			title={c.name}
			truncate
			description={c.image}
			icon={resourceIcon('container').icon}
			color={c.protection ? 'violet' : 'blue'}
			{meta}
		>
			{#snippet status()}
				<StatusBadge status={containerStatus(c)} />
				{#if c.update}<UpdateStatusBadge status={c.update} />{/if}
				{#if c.protection}<ProtectionBadge
						protection={c.protection}
						label="Part of Docker Manager"
					/>{/if}
			{/snippet}
			{#snippet actions()}
				{#if c.state !== 'removing'}
					<LifecycleButton {running} actions={lifecycle} />
				{/if}
				{#if has('pause')}
					<Button icon={Pause} onclick={() => host?.request(c, 'pause')}>Pause</Button>
				{:else if has('unpause')}
					<Button icon={Play} onclick={() => host?.request(c, 'unpause')}>Unpause</Button>
				{/if}
				{#if editable}
					<Button icon={Settings} iconOnPhones onclick={() => (editOpen = true)}
						>Settings</Button
					>
				{/if}
				{#if overflow.length}
					<Menu items={overflow} label="More Actions for {c.name}" align="end">
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

		{#if c.protection}
			<Notice
				tone="info"
				icon={ShieldCheck}
				title={protectionLabel(c.protection)}
				live="none"
			>
				{sentence(c.protection.reason)} Docker Manager refuses to stop, pause, change or remove
				it, for everyone including the owner{c.protection.restartAllowed
					? '; a restart needs an explicit confirmation because the UI disconnects'
					: c.protection.role === 'agent'
						? ", and doesn't restart it either"
						: ''}. Use Docker on the host if you really need to.
			</Notice>
		{:else if c.stack?.managed}
			<Notice
				tone="info"
				icon={Layers}
				title="Part of the Stack {c.stack.project}"
				live="none"
			>
				Change or remove it through its stack.
				{#if c.stack.stackId}<a href={routes.stack(c.stack.stackId)}>Open the stack</a
					>.{/if}
			</Notice>
		{/if}

		<ActiveJobs {jobs} variant="inline" label="Running Jobs of {c.name}" />

		<TabNav items={tabs} current={page.url.pathname} label="Container Sections" />
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
