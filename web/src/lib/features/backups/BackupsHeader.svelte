<script lang="ts">
	// Header of the Backups section: title, description, the tab's actions
	// and route tabs (Overview with the settings, Backups, Repositories).
	// While no repository is ready to hold backups, every tab offers "Add
	// Backup Repository". restic's raw snapshots open from a repository's
	// page, not a tab.
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import Plus from '@lucide/svelte/icons/plus';
	import { page } from '$app/state';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { Button, PageHeader, TabNav } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { repositoriesQuery } from './queries';

	/** `actions`: the tab's actions. */
	let { actions: outerActions }: { actions?: Snippet } = $props();

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const repos = createQuery(() => repositoriesQuery());
	const ready = $derived((repos.data ?? []).some((r) => r.state === 'ready'));
	const canAddRepository = $derived(access.owner || can(access, 'backup_repository.manage'));
</script>

<PageHeader
	title="Backups"
	description="Encrypted backups of the manager, stacks and volumes."
	info="Every backup opens with your Recovery Key."
>
	{#snippet actions()}
		{@render outerActions?.()}
		{#if canAddRepository && repos.isSuccess && !ready}
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
