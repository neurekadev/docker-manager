<script lang="ts">
	// Stack detail, Logs tab (#8, #22): every service container's logs,
	// merged by time and coloured by service, full height. Renders inside the
	// stack layout when there is one, and on its own otherwise.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import LockKeyhole from '@lucide/svelte/icons/lock-keyhole';
	import { fillViewport } from '$lib/features/files/fill';
	import { stackQuery } from '$lib/features/files/resources';
	import LogPanel from '$lib/features/logs/LogPanel.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { EmptyState, ErrorState, Skeleton } from '$lib/ui';

	const stackId = $derived(page.params.stackId ?? '');
	const stack = createQuery(() => stackQuery(stackId));
	const name = $derived(stack.data?.displayName || stack.data?.name || 'Stack');

	usePage(() => ({
		title: `${name} logs`,
		crumbs: [
			{ label: 'Stacks', href: routes.stacks() },
			{ label: name, href: routes.stack(stackId) },
			{ label: 'Logs' }
		]
	}));
</script>

<div class="page" use:fillViewport={{ bottom: 24, min: 420 }}>
	{#if stack.isPending}
		<div aria-busy="true"><Skeleton lines={10} /></div>
	{:else if stack.error}
		<ErrorState
			error={stack.error}
			title="The stack could not be loaded."
			onretry={() => stack.refetch()}
		/>
	{:else if stack.data && !stack.data.actions.includes('stack.read')}
		<EmptyState
			icon={LockKeyhole}
			title="You can't see this stack's services"
			description="Ask the owner of this DockYard for access to {name}."
		/>
	{:else if stack.data}
		{#key stackId}
			<LogPanel target={{ kind: 'stack', stackId }} {name} />
		{/key}
	{/if}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		min-height: 420px;
	}
</style>
