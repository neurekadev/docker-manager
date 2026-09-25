<script lang="ts">
	// Registries and Git credentials (#19, #33): header and tabs. Credential
	// changes ask for a step-up through withStepUp (the dialog is mounted by
	// the signed-in layout).
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { routes } from '$lib/routes';
	import { DeniedState, PageHeader, TabNav } from '$lib/ui';
	import Page from '$lib/features/resources/Page.svelte';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	let { children }: { children: Snippet } = $props();
	const scope = useEnvironmentScope();
	const allowed = $derived(scope.hasAny('registry.', 'git_credential.'));
</script>

{#if scope.perms.data && !allowed}
	<DeniedState
		level={1}
		title="You don't have access to registry credentials."
		description="The owner of this DockYard manages them. Pulls and builds use them without anyone seeing the secrets."
	/>
{:else}
	<Page>
		<PageHeader
			title="Registries"
			description="Credentials DockYard uses to pull private images and build from private repositories. Secrets are entered once and never shown again."
		/>
		<TabNav
			label="Registry sections"
			current={page.url.pathname}
			items={[
				{ href: routes.registries(), label: 'Registry connections' },
				{ href: routes.gitCredentials(), label: 'Git credentials' },
				{ href: routes.registryMatches(), label: 'Which connection?' }
			]}
		/>
		{@render children()}
	</Page>
{/if}
