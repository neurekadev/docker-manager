<script lang="ts">
	// Create a prune policy (#14). It starts from the maintenance defaults:
	// every rule and the schedule off.
	import { createQuery } from '@tanstack/svelte-query';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { DeniedState, PageHeader, Skeleton } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import Page from '$lib/features/common/Page.svelte';
	import PolicyForm from '$lib/features/maintenance/PolicyForm.svelte';

	usePage({
		title: 'Create maintenance policy',
		crumbs: [{ label: 'Maintenance', href: routes.maintenance() }, { label: 'New policy' }]
	});
	const perms = createQuery(() => myPermissionsQuery());
</script>

<Page narrow>
	<PageHeader
		title="Create maintenance policy"
		description="Turn on the rules you want. Preview the policy before its first run."
	/>
	{#if perms.isPending}
		<Skeleton lines={6} height="36px" />
	{:else if !can(accessOf(perms.data), 'maintenance_policy.manage')}
		<DeniedState
			title="You can't create maintenance policies."
			description="Ask the owner of this DockYard for the Manage maintenance policies permission."
			level={2}
		/>
	{:else}
		<PolicyForm environmentId={environmentSelection.id} />
	{/if}
</Page>
