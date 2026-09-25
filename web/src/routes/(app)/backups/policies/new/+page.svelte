<script lang="ts">
	// Create a backup policy (#10) with the setup wizard.
	import { createQuery } from '@tanstack/svelte-query';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Card, DeniedState, PageHeader, Skeleton } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import Page from '$lib/features/common/Page.svelte';
	import PolicyWizard from '$lib/features/backups/PolicyWizard.svelte';

	usePage({
		title: 'Create backup policy',
		crumbs: [
			{ label: 'Backups', href: routes.backups() },
			{ label: 'Policies', href: routes.backupPolicies() },
			{ label: 'New policy' }
		]
	});
	const perms = createQuery(() => myPermissionsQuery());
</script>

<Page narrow>
	<PageHeader
		title="Create backup policy"
		description="Choose what to back up, how, when and for how long. Nothing runs until you start it or turn its schedule on."
	/>
	{#if perms.isPending}
		<Skeleton lines={6} height="36px" />
	{:else if !can(accessOf(perms.data), 'backup_policy.manage')}
		<DeniedState
			title="You can't create backup policies."
			description="Ask the owner of this DockYard for the Manage backup policies permission."
			level={2}
		/>
	{:else}
		<Card><PolicyWizard owner={!!perms.data?.owner} /></Card>
	{/if}
</Page>
