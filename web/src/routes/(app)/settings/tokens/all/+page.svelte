<script lang="ts">
	// Every user's API tokens (#31, owner only): see and revoke them.
	import { createQuery } from '@tanstack/svelte-query';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Card, DeniedState, EmptyState } from '$lib/ui';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { allTokensQuery } from '$lib/features/access/queries';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';
	import TokensTable from '$lib/features/settings/TokensTable.svelte';

	usePage({
		title: 'All API tokens',
		crumbs: [
			{ label: 'Settings', href: routes.settings() },
			{ label: 'API tokens', href: routes.apiTokens() },
			{ label: 'All users' }
		]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const tokens = createQuery(() => ({ ...allTokensQuery(), enabled: owner }));
</script>

<Page>
	<SettingsHeader
		title="All API tokens"
		description="Every user's tokens. Revoke any of them; values are never shown."
	/>
	{#if perms.data && !owner}
		<DeniedState level={2} title="Only the owner sees every token." />
	{:else}
		<Card title="Tokens" padding="none">
			<QueryView query={tokens} errorTitle="The API tokens could not be loaded.">
				{#snippet children(rows)}
					{#if rows.length}
						<TokensTable tokens={rows} label="All API tokens" all />
					{:else}
						<EmptyState
							icon={KeyRound}
							color="violet"
							title="No API tokens exist."
							level={3}
							compact
						/>
					{/if}
				{/snippet}
			</QueryView>
		</Card>
	{/if}
</Page>
