<script lang="ts">
	// My API tokens (#31, a Profile tab): tokens for scripts and
	// integrations, each with a subset of my permissions and an expiry.
	// Values are shown once, when created. Every user's tokens are the
	// owner's Settings tab (routes.allApiTokens).
	import { createQuery } from '@tanstack/svelte-query';
	import Plus from '@lucide/svelte/icons/plus';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, Card, EmptyState } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import Page from '$lib/features/common/Page.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { myTokensQuery } from '$lib/features/access/queries';
	import TokensTable from '$lib/features/access/TokensTable.svelte';
	import ProfileHeader from '$lib/features/profile/ProfileHeader.svelte';

	usePage({
		title: 'API Tokens',
		crumbs: [{ label: 'Profile', href: routes.profile() }, { label: 'API Tokens' }]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const tokens = createQuery(() => myTokensQuery());
</script>

<Page>
	<ProfileHeader
		title="API Tokens"
		description="Tokens let scripts call the Docker Manager API as you, with only the actions you grant them. A token never exceeds your current permissions."
	>
		{#snippet actions()}
			{#if can(access, 'api_tokens.create')}
				<Button variant="primary" icon={Plus} href={routes.apiTokenNew()}
					>Create Token</Button
				>
			{/if}
		{/snippet}
	</ProfileHeader>
	<Card title="Your Tokens" padding="none">
		<QueryView query={tokens} errorTitle="Your API tokens could not be loaded.">
			{#snippet children(rows)}
				{#if rows.length}
					<TokensTable tokens={rows} label="Your API Tokens" />
				{:else}
					<EmptyState
						{...resourceIcon('apiToken')}
						title="No API tokens yet."
						description={can(access, 'api_tokens.create')
							? 'Create one for a script or an integration, with just the actions it needs.'
							: 'Creating tokens needs the Create API tokens permission; ask the owner of this Docker Manager.'}
						level={3}
						compact
					>
						{#snippet actions()}
							{#if can(access, 'api_tokens.create')}
								<Button variant="primary" icon={Plus} href={routes.apiTokenNew()}
									>Create Token</Button
								>
							{/if}
						{/snippet}
					</EmptyState>
				{/if}
			{/snippet}
		</QueryView>
	</Card>
</Page>
