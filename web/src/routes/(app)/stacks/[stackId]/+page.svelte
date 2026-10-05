<script lang="ts">
	// Stack overview (#22 mockup): the KPI row and the services table.
	// Everything refreshes live: stack events, container events (services)
	// and metrics (#23 keys in $lib/features/stacks/queries); CPU and memory
	// are the live values (about every second while the stream is open, else
	// the newest 10 s samples), the CPU sparkline the last hour, and uptimes
	// tick every second.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { operateStack, type StackOperation } from '$lib/features/stacks/actions';
	import { useStackPage } from '$lib/features/stacks/context';
	import { startDeploy } from '$lib/features/stacks/deploy.svelte';
	import {
		deployFailure,
		driftNotes,
		recreateConsequences,
		stackUsage,
		stackTitle,
		upSince
	} from '$lib/features/stacks/model';
	import {
		capacityQuery,
		stackMetricsQuery,
		stackImageStatusQuery,
		stackServicesQuery
	} from '$lib/features/stacks/queries';
	import ServicesTable from '$lib/features/stacks/ServicesTable.svelte';
	import StackKpis from '$lib/features/stacks/StackKpis.svelte';
	import { latestContainerMetricsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Card,
		ConfirmDialog,
		ErrorState,
		Notice,
		Skeleton,
		errorMessage,
		formatRelative,
		toast
	} from '$lib/ui';

	const ctx = useStackPage();
	const stack = $derived(ctx.stack!);
	const full = $derived(stack.view === 'full');
	const title = $derived(stackTitle(stack));

	const services = createQuery(() => ({ ...stackServicesQuery(ctx.id), enabled: full }));
	const imageStatuses = createQuery(() => ({ ...stackImageStatusQuery(ctx.id), enabled: full }));
	const containerNames = $derived(
		(services.data?.services ?? [])
			.flatMap((s) => s.containers.map((c) => c.name ?? ''))
			.filter(Boolean)
			.sort()
	);
	const metrics = createQuery(() => stackMetricsQuery(stack.environmentId, containerNames));
	const capacity = createQuery(() => ({ ...capacityQuery(stack.environmentId), enabled: full }));
	const latest = createQuery(() => ({
		...latestContainerMetricsQuery(stack.environmentId),
		enabled: full
	}));
	// The newest samples of this stack's containers (undefined until the
	// first answer, or when it failed: the minute buckets are used then).
	const stackLatest = $derived.by(() => {
		const byName = latest.data;
		if (!byName) return undefined;
		return containerNames.flatMap((n) => (byName[n] ? [byName[n]] : []));
	});
	const usage = $derived(
		metrics.data || stackLatest ? stackUsage(metrics.data ?? [], stackLatest) : null
	);
	const since = $derived(upSince(services.data?.services ?? []));
	const readOnly = $derived(stack.readOnly || stack.environmentOnline === false);
	// Services in the order of the definition (as the Compose file lists them).
	const ordered = $derived.by(() => {
		const order = new Map((stack.services ?? []).map((s, i) => [s.name, i]));
		return [...(services.data?.services ?? [])].sort(
			(a, b) =>
				(order.get(a.name) ?? 1e6) - (order.get(b.name) ?? 1e6) ||
				a.name.localeCompare(b.name)
		);
	});

	// Drift, one sentence per finding with its remedy. Orphans (services
	// removed from the Compose file whose containers are still there) are
	// not repaired by a plain deploy: offer the deploy that removes them.
	const notes = $derived(driftNotes(services.data?.services));
	const onlyOrphans = $derived(notes.length > 0 && notes.every((n) => n.orphan));
	const hasOrphans = $derived(notes.some((n) => n.orphan));
	const canDeploy = $derived(stack.actions.includes('stack.deploy') && !readOnly);
	const queryClient = useQueryClient();
	let deploying = $state(false);
	async function deployNow() {
		deploying = true;
		try {
			await startDeploy(stack, {}, ctx.tray, queryClient);
		} catch (e) {
			toast.error(`${title} was not deployed`, { body: errorMessage(e) });
		} finally {
			deploying = false;
		}
	}

	// One service's start, stop or restart. Only Stop confirms (it ends
	// what runs); Start and Restart run at once, like a container's.
	let op = $state<{ service: string; action: StackOperation } | null>(null);
	let confirming = $state(false);
	function ask(service: string, action: StackOperation) {
		op = { service, action };
		if (action === 'stop' || action === 'down') {
			confirming = true;
			return;
		}
		run().catch((e) =>
			toast.error(`${service} was not ${VERB[action][2]}`, { body: errorMessage(e) })
		);
	}
	const VERB: Record<StackOperation, [string, string, string]> = {
		start: ['Start', 'Started', 'started'],
		stop: ['Stop', 'Stopped', 'stopped'],
		restart: ['Restart', 'Restarted', 'restarted'],
		down: ['Stop', 'Stopped', 'stopped']
	};
	async function run() {
		if (!op) return;
		const [verb, done, failed] = VERB[op.action];
		const job = await operateStack(stack.id, op.action, [op.service]);
		ctx.tray.add(job, {
			title: `${verb} ${op.service}`,
			success: `${done} ${op.service}`,
			failure: `${op.service} was not ${failed}`
		});
	}
	const consequence = $derived(
		op
			? [
					`Stops ${op.service} and the services that need it, those first.`,
					'Containers, volumes and files are kept; Start brings them back.'
				]
			: []
	);

	// One service's force recreate: a deploy of that service that replaces
	// its containers, confirmed first.
	let recreate = $state<string | null>(null);
	let recreating = $state(false);
	function askRecreate(service: string) {
		recreate = service;
		recreating = true;
	}
	async function runRecreate() {
		if (!recreate) return;
		const choice = { forceRecreate: true, services: [recreate] };
		try {
			await startDeploy(stack, choice, ctx.tray, queryClient);
		} catch (e) {
			toast.error(deployFailure(title, choice), { body: errorMessage(e) });
		}
	}
</script>

{#if !full}
	<Card>
		<p class="muted">
			You can't see the services and usage of {title}. Ask the owner for access.
		</p>
	</Card>
{:else}
	<StackKpis
		{stack}
		{usage}
		capacity={capacity.data}
		{since}
		revisionsHref={stack.actions.includes('stack.definition.read')
			? routes.stack(stack.id, 'revisions')
			: undefined}
	/>

	{#if notes.length}
		<Notice
			tone="warn"
			title={onlyOrphans
				? 'Containers of removed services are still on the host.'
				: 'Some services differ from the last deploy.'}
		>
			<ul class="drift">
				{#each notes as n, i (i)}<li>{n.text}</li>{/each}
			</ul>
			{#snippet actions()}
				{#if canDeploy && hasOrphans}
					<Button size="sm" onclick={() => ctx.removeOrphans.request()}
						>Remove Old Containers…</Button
					>
				{/if}
				{#if canDeploy && !onlyOrphans}
					<Button size="sm" loading={deploying} onclick={deployNow}>Deploy</Button>
				{/if}
			{/snippet}
		</Notice>
	{/if}

	<Card
		title="Services"
		padding="none"
		id="services"
		subtitle={services.data && !services.data.live && services.data.observedAt
			? `Last observed ${formatRelative(services.data.observedAt)}`
			: undefined}
	>
		{#if services.isPending}
			<div class="loading" aria-busy="true"><Skeleton lines={5} height="20px" /></div>
		{:else if services.isError}
			<div class="loading">
				<ErrorState
					error={services.error}
					title="The services could not be loaded."
					onretry={() => services.refetch()}
					compact
					bare
				/>
			</div>
		{:else}
			<ServicesTable
				{stack}
				services={ordered}
				imageStatuses={imageStatuses.data}
				{usage}
				serviceAddress={ctx.environment?.serviceAddress}
				onoperate={ask}
				onrecreate={askRecreate}
				ondeploy={canDeploy ? deployNow : undefined}
				{deploying}
				{readOnly}
			/>
		{/if}
	</Card>
{/if}

{#if op}
	<ConfirmDialog
		bind:open={confirming}
		title="{VERB[op.action][0]} {op.service}?"
		consequences={consequence}
		confirmLabel={VERB[op.action][0]}
		tone="danger"
		onconfirm={run}
	/>
{/if}

{#if recreate}
	<ConfirmDialog
		bind:open={recreating}
		title="Force Recreate {recreate}?"
		consequences={recreateConsequences(title, recreate)}
		confirmLabel="Force Recreate"
		tone="danger"
		onconfirm={runRecreate}
	/>
{/if}

<style>
	.loading {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.drift {
		margin: 0 0 var(--space-2);
		padding-left: 18px;
	}
</style>
