<script lang="ts">
	// Stack detail, Terminal tab (#8, #22): a terminal in one of the stack's
	// service containers (?container=<name> preselects one, e.g. from the
	// services table). Renders inside the stack layout when there is one.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import SquareTerminal from '@lucide/svelte/icons/square-terminal';
	import { fillViewport } from '$lib/features/files/fill';
	import { stackQuery, stackServicesQuery } from '$lib/features/files/resources';
	import TerminalPanel, {
		type TerminalChoice
	} from '$lib/features/terminal/TerminalPanel.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { EmptyState, ErrorState, Skeleton } from '$lib/ui';

	const stackId = $derived(page.params.stackId ?? '');
	const stack = createQuery(() => stackQuery(stackId));
	const services = createQuery(() => stackServicesQuery(stackId));
	const name = $derived(stack.data?.displayName || stack.data?.name || 'Stack');

	usePage(() => ({
		title: `${name} terminal`,
		crumbs: [
			{ label: 'Stacks', href: routes.stacks() },
			{ label: name, href: routes.stack(stackId) },
			{ label: 'Terminal' }
		]
	}));

	const choices = $derived<TerminalChoice[]>(
		stack.data && services.data
			? services.data.services.flatMap((svc) =>
					svc.containers
						.filter((c) => c.name)
						.map((c) => ({
							environmentId: stack.data!.environmentId,
							containerId: c.name!,
							label: svc.containers.length > 1 ? `${svc.name} (${c.name})` : svc.name,
							unavailable: c.state === 'running' ? null : c.state
						}))
				)
			: []
	);
	const error = $derived(stack.error ?? services.error);
</script>

<div class="page" use:fillViewport={{ bottom: 24, min: 420 }}>
	{#if stack.isPending || services.isPending}
		<div aria-busy="true"><Skeleton lines={10} /></div>
	{:else if error}
		<ErrorState
			{error}
			title="The services of {name} could not be loaded."
			onretry={() => {
				void stack.refetch();
				void services.refetch();
			}}
		/>
	{:else if choices.length === 0}
		<EmptyState
			icon={SquareTerminal}
			title="{name} has no containers"
			description="Deploy {name} to open a terminal in one of its services."
		/>
	{:else}
		<TerminalPanel
			{choices}
			initial={page.url.searchParams.get('container')}
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
