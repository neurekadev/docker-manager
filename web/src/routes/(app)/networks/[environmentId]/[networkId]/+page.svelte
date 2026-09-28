<script lang="ts">
	// Network detail (#6): driver, subnets and flags, the attached
	// containers (by name, with their address on it, from the containers
	// list), its stack and labels (system labels folded). Removal is the
	// last entry of the "More actions" menu; its dialog shows the server's
	// preview (it refuses predefined, in-use, stack-managed and Docker
	// Manager's own networks with the reason).
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Layers from '@lucide/svelte/icons/layers';
	import NetworkIcon from '@lucide/svelte/icons/network';
	import Server from '@lucide/svelte/icons/server';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { ApiRequestError } from '$lib/api/client';
	import { containersQuery, networkQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
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
		formatDateTime,
		formatRelative,
		type MenuEntry,
		type MetaItem
	} from '$lib/ui';
	import Columns from '$lib/features/common/Columns.svelte';
	import { onlyOneEnvironment } from '$lib/features/common/data';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import ObjectRemoveHost from '$lib/features/resources/ObjectRemoveHost.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import LabelsCard from '$lib/features/resources/LabelsCard.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import { activeJobs, resourceKey } from '$lib/features/resources/jobs.svelte';
	import { can } from '$lib/features/resources/permissions';
	import { protectionLabel, sentence } from '$lib/features/resources/refusals';
	import { attachedContainers } from '$lib/features/resources/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	const env = $derived(page.params.environmentId ?? '');
	const name = $derived(page.params.networkId ?? '');
	const scope = useEnvironmentScope();
	const envName = $derived(scope.name(env));
	const q = createQuery(() => networkQuery(env, name));
	const n = $derived(q.data);
	let remover = $state<ObjectRemoveHost>();

	usePage(() => ({
		title: name,
		crumbs: [
			{ label: 'Networks', href: routes.networks() },
			{ label: envName },
			{ label: name }
		]
	}));

	const offline = $derived(
		q.error instanceof ApiRequestError && q.error.apiError?.code === 'environment_offline'
	);
	const notFound = $derived(q.error instanceof ApiRequestError && q.error.status === 404);
	const job = $derived(activeJobs.byKey[resourceKey('network', env, name)]);
	// The containers' addresses on this network come from the containers
	// list (the network's own answer names the containers only).
	const envTarget = $derived.by(() => {
		const e = scope.environment(env);
		return e
			? [
					{
						id: e.id,
						name: e.name,
						online: e.online,
						connectionChangedAt: e.connectionChangedAt
					}
				]
			: [];
	});
	const containers = createQuery(() => ({
		...containersQuery(envTarget),
		enabled:
			envTarget.length > 0 &&
			!!n?.containers?.length &&
			scope.hasAny('container.details.read')
	}));
	const attached = $derived(
		n ? attachedContainers(n.name, n.containers ?? [], containers.data?.items ?? []) : []
	);
	const meta = $derived<MetaItem[]>(
		n
			? [
					...(onlyOneEnvironment(scope.envs.data)
						? []
						: [{ icon: Server, label: envName }]),
					...(n.stack ? [{ icon: Layers, label: n.stack.project }] : []),
					...(n.createdAt
						? [
								{
									icon: Clock,
									label: `Created ${formatRelative(n.createdAt)}`,
									title: formatDateTime(n.createdAt)
								}
							]
						: [])
				]
			: []
	);
	const facts = $derived<Fact[]>(
		n
			? [
					{ label: 'Driver', value: n.driver, mono: true },
					{ label: 'Scope', value: n.scope },
					{ label: 'Subnets', value: n.subnets?.join(', '), mono: true },
					{ label: 'Gateways', value: n.gateways?.join(', '), mono: true },
					{
						label: 'Internal',
						value: n.internal ? 'Yes: no traffic to or from outside' : 'No'
					},
					{ label: 'Attachable', value: n.attachable ? 'Yes' : 'No' },
					{ label: 'IPv6', value: n.enableIpv6 ? 'Enabled' : 'Off' },
					{ label: 'Network ID', value: n.id.slice(0, 12), mono: true, title: n.id }
				]
			: []
	);
	/** Removal last, after a separator (one rule on every resource page). */
	const overflow = $derived.by<MenuEntry[]>(() => {
		// Predefined and Docker Manager's own networks are never removed:
		// no entry at all (the badge and notice say why).
		if (!n || n.builtin || n.protection || !can(n.actions, 'network.remove')) return [];
		return [
			{
				label: 'Remove…',
				tone: 'danger',
				onSelect: () =>
					remover?.request({
						kind: 'network',
						environmentId: env,
						name: n.name,
						protection: n.protection
					})
			}
		];
	});
</script>

<ObjectRemoveHost
	bind:this={remover}
	environmentName={() => envName}
	onremoved={() => goto(routes.networks())}
/>

<Page>
	{#if q.isPending}
		<div class="loading" aria-busy="true">
			<Skeleton height="56px" radius="lg" /><Skeleton lines={4} height="20px" />
		</div>
	{:else if offline}
		<PageHeader title={name} icon={NetworkIcon} color="indigo" />
		<OfflineEnvironment name={envName} since={scope.environment(env)?.connectionChangedAt} />
	{:else if notFound}
		<EmptyState
			icon={NetworkIcon}
			color="slate"
			title="No network named {name} on {envName}."
			description="It may have been removed, or you don't have access to it."
			level={1}
		>
			{#snippet actions()}<Button variant="secondary" href={routes.networks()}
					>Back to networks</Button
				>{/snippet}
		</EmptyState>
	{:else if q.isError}
		<ErrorState
			error={q.error}
			title="The network could not be loaded."
			onretry={() => q.refetch()}
		/>
	{:else if n}
		<PageHeader title={n.name} truncate icon={NetworkIcon} color="indigo" {meta}>
			{#snippet status()}
				{#if n.builtin}<Badge>Predefined</Badge>{/if}
				{#if n.protection}<ProtectionBadge
						protection={n.protection}
						label="Used by Docker Manager"
					/>{/if}
			{/snippet}
			{#snippet actions()}
				{#if overflow.length}
					<Menu items={overflow} label="More actions for {n.name}" align="end">
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
		{#if n.protection}
			<Notice
				tone="info"
				icon={ShieldCheck}
				title={protectionLabel(n.protection)}
				live="none"
			>
				{sentence(n.protection.reason)} Docker Manager never removes it.
			</Notice>
		{/if}
		{#if job}<JobProgress watcher={job} variant="inline" notices={null} />{/if}

		<Columns>
			<Card title="Details">
				{#if n.view === 'full'}<Facts items={facts} label="Details of {n.name}" />
				{:else}<p class="muted">Its details need the network read permission.</p>{/if}
			</Card>
			<Card title="Attached containers">
				{#if attached.length}
					<ul class="list" role="list">
						{#each attached as c (c.id)}
							<li>
								<a href={routes.container(env, c.name)}>{c.name}</a>
								<span class="end">
									{#if c.addresses.length}<span
											class="mono addr"
											title={c.addresses.join('\n')}>{c.addresses[0]}</span
										>{/if}
									{#if c.state}<StatusBadge status={c.state} />{/if}
								</span>
							</li>
						{/each}
					</ul>
				{:else}<p class="muted">No container is attached.</p>{/if}
			</Card>
		</Columns>
		<LabelsCard labels={n.labels} label="Labels of {n.name}" />
	{/if}
</Page>

<style>
	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.list {
		display: grid;
		gap: var(--space-2);
	}

	.list li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}

	a {
		color: var(--accent-text);
		text-decoration: none;
	}

	.end {
		display: inline-flex;
		align-items: center;
		gap: var(--space-3);
	}

	.addr {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}
</style>
