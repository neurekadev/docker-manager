<script lang="ts">
	// Container detail (#6): header with status (and the image's update
	// state, #20), image and lifecycle actions, what Docker Manager refuses
	// on this container and why (#32 protection, managed stacks), the
	// running job, and the tabs: Overview here, Logs and Terminal (#8) as
	// child routes. Removal is the last entry of the "More actions" menu.
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import ContainerIcon from '@lucide/svelte/icons/container';
	import Clock from '@lucide/svelte/icons/clock';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Layers from '@lucide/svelte/icons/layers';
	import Pause from '@lucide/svelte/icons/pause';
	import Play from '@lucide/svelte/icons/play';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Server from '@lucide/svelte/icons/server';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import Square from '@lucide/svelte/icons/square';
	import { ApiRequestError } from '$lib/api/client';
	import { containerQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		EmptyState,
		ErrorState,
		IconButton,
		JobProgress,
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
	import { activeJobs, resourceKey } from '$lib/features/resources/jobs.svelte';
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

	usePage(() => ({
		title: name,
		crumbs: [
			{ label: 'Containers', href: routes.containers() },
			{ label: envName },
			{ label: name }
		]
	}));

	let host = $state<ContainerActionHost>();
	let editOpen = $state(false);

	const offline = $derived(
		q.error instanceof ApiRequestError && q.error.apiError?.code === 'environment_offline'
	);
	const notFound = $derived(q.error instanceof ApiRequestError && q.error.status === 404);
	const job = $derived(activeJobs.byKey[resourceKey('container', env, name)]);
	const actions = $derived(c ? containerActions(c) : []);
	const has = (verb: ContainerVerb) => actions.some((a) => a.verb === verb);

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
				title: c.stack.managed ? 'Managed stack' : 'Compose project'
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

	const overflow = $derived.by<MenuEntry[]>(() => {
		if (!c) return [];
		const out: MenuEntry[] = [];
		if (has('pause'))
			out.push({ label: 'Pause', icon: Pause, onSelect: () => host?.request(c, 'pause') });
		if (has('unpause'))
			out.push({ label: 'Unpause', icon: Play, onSelect: () => host?.request(c, 'unpause') });
		if (c.view === 'full' && can(c.actions, 'container.update'))
			out.push({
				label: 'Change restart policy and limits…',
				onSelect: () => (editOpen = true)
			});
		if (c.imageId && c.view === 'full')
			out.push({ label: 'Open image', href: routes.image(env, c.imageId) });
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
/>
{#if c && c.view === 'full'}
	<EditContainerDialog bind:open={editOpen} container={c} environmentName={envName} />
{/if}

<Page>
	{#if q.isPending}
		<div aria-busy="true" class="loading">
			<Skeleton height="56px" radius="lg" />
			<Skeleton lines={4} height="20px" />
		</div>
	{:else if offline}
		<PageHeader title={name} icon={ContainerIcon} color="blue" />
		<OfflineEnvironment name={envName} since={scope.environment(env)?.connectionChangedAt} />
	{:else if notFound}
		<EmptyState
			icon={ContainerIcon}
			color="slate"
			title="No container named {name} on {envName}."
			description="It may have been removed, or you don't have access to it."
			level={1}
		>
			{#snippet actions()}
				<Button variant="secondary" href={routes.containers()}>Back to containers</Button>
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
			icon={ContainerIcon}
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
				{#if has('start')}
					<Button variant="primary" icon={Play} onclick={() => host?.request(c, 'start')}
						>Start</Button
					>
				{/if}
				{#if has('restart')}
					<Button
						variant="secondary"
						icon={RotateCw}
						onclick={() => host?.request(c, 'restart')}>Restart</Button
					>
				{/if}
				{#if has('stop')}
					<Button
						variant="danger-soft"
						icon={Square}
						onclick={() => host?.request(c, 'stop')}>Stop</Button
					>
				{/if}
				{#if overflow.length}
					<Menu items={overflow} label="More actions for {c.name}" align="end">
						{#snippet trigger(props)}
							<IconButton
								{...props}
								icon={Ellipsis}
								label="More actions"
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
				title="Part of the stack {c.stack.project}"
				live="none"
			>
				Change this container through its stack: settings changes and removal here are
				refused.
				{#if c.stack.stackId}<a href={routes.stack(c.stack.stackId)}>Open the stack</a
					>.{/if}
			</Notice>
		{/if}

		{#if job}
			<JobProgress watcher={job} variant="inline" notices={null} />
		{/if}

		<TabNav items={tabs} current={page.url.pathname} label="Container sections" />
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
