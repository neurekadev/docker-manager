<script lang="ts">
	// Header of the Backups section: title, description, the section's
	// primary action and route tabs (Overview with the policies, Backups,
	// Repositories). The primary action is the same on every tab: "Create
	// backup policy" (it opens the wizard in a dialog, ?create=1), or "Add
	// backup repository" while no repository is ready to hold backups.
	// restic's raw snapshots open from a repository's page, not a tab.
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import Plus from '@lucide/svelte/icons/plus';
	import { page } from '$app/state';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { Button, PageHeader, TabNav } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import BackupPolicyDialog from './BackupPolicyDialog.svelte';
	import { repositoriesQuery } from './queries';

	/** `actions`: secondary actions of a tab, before the primary one. */
	let { actions: outerActions }: { actions?: Snippet } = $props();

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const repos = createQuery(() => repositoriesQuery());
	const createDialog = urlDialog('create');
	const ready = $derived((repos.data ?? []).some((r) => r.state === 'ready'));
	const canCreatePolicy = $derived(can(access, 'backup_policy.manage'));
	const canAddRepository = $derived(access.owner || can(access, 'backup_repository.manage'));
</script>

<PageHeader
	title="Backups"
	description="Encrypted backups of the manager, stacks and volumes."
	info="Every backup opens with your Recovery Key."
>
	{#snippet actions()}
		{@render outerActions?.()}
		{#if canCreatePolicy && ready}
			<Button variant="primary" icon={Plus} onclick={() => (createDialog.open = true)}
				>Create Backup Policy</Button
			>
		{:else if canAddRepository && repos.isSuccess && !ready}
			<Button variant="primary" icon={Plus} href={routes.backupRepositoryNew()}
				>Add Backup Repository</Button
			>
		{/if}
	{/snippet}
</PageHeader>
<TabNav
	label="Backups Sections"
	current={page.url.pathname}
	items={[
		{ href: routes.backups(), label: 'Overview' },
		{ href: routes.backupList(), label: 'Backups' },
		{ href: routes.backupRepositories(), label: 'Repositories' }
	]}
/>

{#if createDialog.open}
	<BackupPolicyDialog bind:open={createDialog.open} owner={!!perms.data?.owner} />
{/if}
