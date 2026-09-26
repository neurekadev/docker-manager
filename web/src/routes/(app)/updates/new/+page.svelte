<script lang="ts">
	import { createQuery } from '@tanstack/svelte-query';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { DeniedState, PageHeader, Skeleton } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import Page from '$lib/features/common/Page.svelte';
	import EnvironmentPolicyForm from '$lib/features/updates/EnvironmentPolicyForm.svelte';
	usePage({
		title: 'Create update policy',
		crumbs: [{ label: 'Updates', href: routes.updates() }, { label: 'New policy' }]
	});
	const perms = createQuery(() => myPermissionsQuery());
</script>

<Page narrow>
	<PageHeader
		title="Create update policy"
		description="Choose All Environments or one environment, then exclude individual stacks and containers as needed."
	/>
	{#if perms.isPending}<Skeleton lines={6} height="36px" />
	{:else if !can(accessOf(perms.data), 'update_policy.manage')}<DeniedState
			title="You can't create update policies."
			level={2}
		/>
	{:else}<EnvironmentPolicyForm
			environmentId={environmentSelection.id}
			allowAll={can(accessOf(perms.data), 'update_policy.manage_all')}
		/>{/if}
</Page>
