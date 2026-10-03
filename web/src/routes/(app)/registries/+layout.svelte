<script lang="ts">
	// Registries and Git credentials (#19, #33): header, its actions and the
	// tabs. The header's actions follow the tab: registry connections have
	// "Test an Image" (a dialog, ?test=1) and "Add Connection", Git
	// credentials "Add Credential"; adding opens the tab's dialog through
	// ?create=1. Credential changes ask for a step-up through withStepUp
	// (the dialog is mounted by the signed-in layout).
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import FlaskConical from '@lucide/svelte/icons/flask-conical';
	import Plus from '@lucide/svelte/icons/plus';
	import { routes } from '$lib/routes';
	import { Button, DeniedState, PageHeader, TabNav } from '$lib/ui';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import MatchTestDialog from '$lib/features/registries/MatchTestDialog.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	let { children }: { children: Snippet } = $props();
	const scope = useEnvironmentScope();
	const allowed = $derived(scope.hasAny('registry.', 'git_credential.'));
	const owner = $derived(!!scope.perms.data?.owner);
	const createDialog = urlDialog('create');
	const testDialog = urlDialog('test');
	const onGit = $derived(page.url.pathname.startsWith(routes.gitCredentials()));
</script>

{#if scope.perms.data && !allowed}
	<DeniedState
		level={1}
		title="You don't have access to registry credentials."
		description="The owner of this Docker Manager manages them."
	/>
{:else}
	<Page>
		<PageHeader
			title="Registries"
			info="Credentials for private images and repositories. Secrets are never shown again."
		>
			{#snippet actions()}
				{#if onGit}
					{#if owner}
						<Button
							variant="primary"
							icon={Plus}
							onclick={() => (createDialog.open = true)}>Add Credential</Button
						>
					{/if}
				{:else}
					{#if scope.hasAny('registry.')}
						<Button icon={FlaskConical} onclick={() => (testDialog.open = true)}
							>Test an Image</Button
						>
					{/if}
					{#if owner}
						<Button
							variant="primary"
							icon={Plus}
							onclick={() => (createDialog.open = true)}>Add Connection</Button
						>
					{/if}
				{/if}
			{/snippet}
		</PageHeader>
		<TabNav
			label="Registry Sections"
			current={page.url.pathname}
			items={[
				{ href: routes.registries(), label: 'Registry Connections' },
				{ href: routes.gitCredentials(), label: 'Git Credentials' }
			]}
		/>
		{@render children()}
	</Page>
	{#if testDialog.open}
		<MatchTestDialog bind:open={() => testDialog.open, (v) => (testDialog.open = v)} />
	{/if}
{/if}
