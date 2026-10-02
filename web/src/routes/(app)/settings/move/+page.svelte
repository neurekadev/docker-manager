<script lang="ts">
	// Settings → Move to a New Server (owner only): move Docker Manager and
	// the apps next to it to another server (ManagerMoveWizard;
	// docs/internal/architecture/manager-move.md). On a manager that arrived
	// by a move it shows Move complete until everything is done.
	import { createQuery } from '@tanstack/svelte-query';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Card, DeniedState, Skeleton } from '$lib/ui';
	import Page from '$lib/features/common/Page.svelte';
	import ManagerMoveWizard from '$lib/features/managermove/ManagerMoveWizard.svelte';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';

	usePage({
		title: 'Move to a New Server',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Move to a New Server' }]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
</script>

<Page>
	<SettingsHeader
		title="Move to a New Server"
		description="Move Docker Manager and the apps on its server to another server."
	/>
	{#if !perms.data}
		<Card><Skeleton lines={4} /></Card>
	{:else if !access.owner}
		<DeniedState level={2} title="Moving Docker Manager is for the owner." />
	{:else}
		<ManagerMoveWizard />
	{/if}
</Page>
