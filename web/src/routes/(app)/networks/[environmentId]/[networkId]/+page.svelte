<script lang="ts">
	// Network detail (#6): driver, subnets and flags, the attached
	// containers, its stack and labels, and what removing it would do (the
	// server refuses predefined, in-use, stack-managed and DockYard's own
	// networks with the reason).
	import { createQuery } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import Layers from '@lucide/svelte/icons/layers';
	import NetworkIcon from '@lucide/svelte/icons/network';
	import Server from '@lucide/svelte/icons/server';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { ApiRequestError } from '$lib/api/client';
	import { networkQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		ErrorState,
		JobProgress,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		StatusBadge,
		formatRelative,
		type MetaItem
	} from '$lib/ui';
	import Facts, { type Fact } from '$lib/features/resources/Facts.svelte';
	import ObjectRemoveHost from '$lib/features/resources/ObjectRemoveHost.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import { activeJobs, resourceKey } from '$lib/features/resources/jobs.svelte';
	import { can } from '$lib/features/resources/permissions';
	import { protectionLabel, sentence } from '$lib/features/resources/refusals';
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
	const meta = $derived<MetaItem[]>(
		n
			? [
					{ icon: Server, label: envName },
					...(n.stack ? [{ icon: Layers, label: n.stack.project }] : []),
					...(n.createdAt
						? [
								{
									icon: Clock,
									label: `Created ${formatRelative(n.createdAt)}`,
									title: n.createdAt
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
		<PageHeader title={n.name} icon={NetworkIcon} color="indigo" {meta}>
			{#snippet status()}
				{#if n.builtin}<Badge>Predefined</Badge>{/if}
				{#if n.protection}<ProtectionBadge protection={n.protection} />{/if}
			{/snippet}
			{#snippet actions()}
				{#if can(n.actions, 'network.remove')}
					<Button
						variant="danger-soft"
						icon={Trash2}
						onclick={() =>
							remover?.request({
								kind: 'network',
								environmentId: env,
								name: n.name,
								protection: n.protection
							})}>Remove</Button
					>
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
				{sentence(n.protection.reason)} DockYard never removes it.
			</Notice>
		{/if}
		{#if job}<JobProgress watcher={job} variant="inline" notices={null} />{/if}

		<div class="grid">
			<Card title="Details">
				{#if n.view === 'full'}<Facts items={facts} label="Details of {n.name}" />
				{:else}<p class="muted">Its details need the network read permission.</p>{/if}
			</Card>
			<Card title="Attached containers">
				{#if n.containers?.length}
					<ul class="list" role="list">
						{#each n.containers as c (c.id)}
							<li>
								<a href={routes.container(env, c.name)}>{c.name}</a>
								{#if c.state}<StatusBadge status={c.state} />{/if}
							</li>
						{/each}
					</ul>
				{:else}<p class="muted">No container is attached.</p>{/if}
			</Card>
			{#if n.removal}
				<Card title="If you remove it">
					{#if n.removal.blockers.length}
						<p class="lead">Removal is refused now:</p>
						<ul class="bullets">
							{#each n.removal.blockers as b, i (i)}<li>
									{sentence(b.message)}
								</li>{/each}
						</ul>
						<p class="then">Once nothing blocks it, removing it:</p>
					{/if}
					<ul class="bullets">
						{#each n.removal.consequences as c (c)}<li>{sentence(c)}</li>{/each}
					</ul>
				</Card>
			{/if}
			<Card title="Labels">
				{#if n.labels && Object.keys(n.labels).length}
					<Facts
						items={Object.entries(n.labels)
							.sort(([a], [b]) => a.localeCompare(b))
							.map(([k, v]) => ({ label: k, value: v, mono: true }))}
						label="Labels of {n.name}"
					/>
				{:else}<p class="muted">No labels.</p>{/if}
			</Card>
		</div>
	{/if}
</Page>

<style>
	.loading {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
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

	.lead {
		margin-bottom: var(--space-2);
		color: var(--warn);
	}

	.then {
		margin: var(--space-3) 0 var(--space-2);
		color: var(--text-muted);
	}

	.bullets {
		display: grid;
		gap: 4px;
		padding-left: 18px;
		margin-bottom: var(--space-2);
	}

	@media (max-width: 1023px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
