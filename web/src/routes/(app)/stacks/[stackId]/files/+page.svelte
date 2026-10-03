<script lang="ts">
	// Stack detail, Files tab (#15, #22): the scoped file manager over the
	// stack's project directory, full width, with the logs as an optional
	// bottom drawer that never narrows the list or the editor. The editor
	// deploys saved Compose files through the stack page's job tray (a
	// toast outside the stack layout). Renders inside the stack layout when
	// there is one, and on its own otherwise.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Layers from '@lucide/svelte/icons/layers';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import { ApiRequestError } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import type { StackFiles } from '$lib/features/files/definition';
	import FileManager from '$lib/features/files/FileManager.svelte';
	import { fillViewport } from '$lib/features/files/fill';
	import { stackQuery } from '$lib/features/files/resources';
	import LogDock from '$lib/features/logs/LogDock.svelte';
	import { deployStackWith, validateStackFiles } from '$lib/features/stacks/actions';
	import { useStackPage, type StackPage } from '$lib/features/stacks/context';
	import { startDeploy } from '$lib/features/stacks/deploy.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, EmptyState, ErrorState, Skeleton, toast } from '$lib/ui';

	const stackId = $derived(page.params.stackId ?? '');
	const stack = createQuery(() => stackQuery(stackId));
	const envs = createQuery(() => environmentsQuery());
	const s = $derived(stack.data);
	const env = $derived(envs.data?.find((e) => e.id === s?.environmentId));
	const name = $derived(s?.displayName || s?.name || 'Stack');
	const canLogs = $derived(!!s?.actions.includes('stack.read'));
	const canDeploy = $derived(!!s?.actions.includes('stack.deploy') && !s?.readOnly);
	// Validating the saved definition needs the permission that edits it.
	const canValidate = $derived(!!s?.actions.includes('stack.definition.write') && !s?.readOnly);

	// The stack layout's job tray reports deploys under the header; on its
	// own (no layout) a toast says the deploy started.
	let stackPage: StackPage | null = null;
	try {
		stackPage = useStackPage();
	} catch {
		stackPage = null;
	}
	const queryClient = useQueryClient();

	async function deploy() {
		const cur = s;
		if (!cur) return;
		if (stackPage) {
			await startDeploy(cur, {}, stackPage.tray, queryClient);
			return;
		}
		await deployStackWith(cur.id, {});
		toast.info(`Deploying ${name}`, {
			body: 'The stack page shows its progress.',
			action: { label: 'Open Stack', onclick: () => void goto(routes.stack(cur.id)) }
		});
	}

	const stackFiles = $derived<StackFiles | null>(
		s
			? {
					name,
					configFiles: s.configFiles ?? [],
					revisionsHref: routes.stack(s.id, 'revisions'),
					validate: canValidate ? () => validateStackFiles(s.id) : undefined,
					undeployed: !!s.undeployedChanges,
					deploy: canDeploy ? deploy : undefined
				}
			: null
	);

	usePage(() => ({
		title: `${name} Files`,
		crumbs: [
			{ label: 'Stacks', href: routes.stacks() },
			{ label: name, href: routes.stack(stackId) },
			{ label: 'Files' }
		]
	}));

	let logsOpen = $state(false);
</script>

<div class="page" use:fillViewport={{ min: 520 }}>
	{#if stack.isPending}
		<div aria-busy="true" class="loading"><Skeleton lines={10} /></div>
	{:else if stack.error}
		{#if stack.error instanceof ApiRequestError && stack.error.status === 404}
			<EmptyState
				icon={Layers}
				title="This stack doesn't exist"
				description="It was deleted, or you can't see it."
			>
				{#snippet actions()}<Button href={routes.stacks()}>Open Stacks</Button>{/snippet}
			</EmptyState>
		{:else}
			<ErrorState
				error={stack.error}
				title="The stack could not be loaded."
				onretry={() => stack.refetch()}
			/>
		{/if}
	{:else if s}
		{#key s.id}
			<FileManager
				scope={{ kind: 'stack', stackId: s.id, environmentId: s.environmentId }}
				rootLabel={s.name}
				capabilities={s.actions}
				stack={stackFiles}
				environmentOnline={s.environmentOnline ?? env?.online ?? true}
				environmentName={env?.name ?? 'The environment'}
				label="Files of {name}"
				title="Stack Files"
			>
				{#snippet actions()}
					{#if canLogs}
						<Button
							size="sm"
							variant={logsOpen ? 'secondary' : 'ghost'}
							icon={ScrollText}
							aria-pressed={logsOpen}
							onclick={() => (logsOpen = !logsOpen)}
							>{logsOpen ? 'Hide Logs' : 'Show Logs'}</Button
						>
					{/if}
				{/snippet}
			</FileManager>
		{/key}
		{#if canLogs && logsOpen}
			<LogDock stackId={s.id} {name} onclose={() => (logsOpen = false)} />
		{/if}
	{/if}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		min-height: 520px;
	}

	.loading {
		padding: var(--space-4);
	}
</style>
