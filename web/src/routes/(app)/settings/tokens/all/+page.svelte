<script lang="ts">
	// Every user's API tokens (#31, owner only; a Settings tab): see and
	// revoke them. The caller's own tokens are a Profile tab.
	import { createQuery } from '@tanstack/svelte-query';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Card, DeniedState, EmptyState } from '$lib/ui';
	import Page from '$lib/features/common/Page.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { allTokensQuery } from '$lib/features/access/queries';
	import TokensTable from '$lib/features/access/TokensTable.svelte';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';

	usePage({
		title: 'All API Tokens',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'API Tokens' }]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const tokens = createQuery(() => ({ ...allTokensQuery(), enabled: owner }));
</script>

<Page>
	<SettingsHeader title="All API Tokens" info="Token values are never shown." />
	{#if perms.data && !owner}
		<DeniedState level={2} title="Only the owner sees every token." />
	{:else}
		<Card title="Tokens" padding="none">
			<QueryView query={tokens} errorTitle="The API tokens could not be loaded.">
				{#snippet children(rows)}
					{#if rows.length}
						<TokensTable tokens={rows} label="All API Tokens" all />
					{:else}
						<EmptyState
							{...resourceIcon('apiToken')}
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
