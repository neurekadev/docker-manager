<script lang="ts">
	// Create an update policy (#20). Nothing is checked or updated until its
	// schedules are turned on or a user starts a check.
	import { createQuery } from '@tanstack/svelte-query';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { DeniedState, PageHeader, Skeleton } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import Page from '$lib/features/common/Page.svelte';
	import PolicyForm from '$lib/features/updates/PolicyForm.svelte';

	usePage({
		title: 'Create update policy',
		crumbs: [{ label: 'Updates', href: routes.updates() }, { label: 'New policy' }]
	});
	const perms = createQuery(() => myPermissionsQuery());
</script>

<Page narrow>
	<PageHeader
		title="Create update policy"
		description="Choose what follows its tags. Checks and updates stay off until you turn them on."
	/>
	{#if perms.isPending}
		<Skeleton lines={6} height="36px" />
	{:else if !can(accessOf(perms.data), 'update_policy.manage')}
		<DeniedState
			title="You can't create update policies."
			description="Ask the owner of this DockYard for the Manage update policies permission."
			level={2}
		/>
	{:else}
		<PolicyForm environmentId={environmentSelection.id} />
	{/if}
</Page>
