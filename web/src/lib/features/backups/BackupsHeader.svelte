<script lang="ts">
	// Header of the Backups section: title, description, the section's
	// actions and route tabs (Backups, Policies, Repositories).
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { routes } from '$lib/routes';
	import { PageHeader, TabNav } from '$lib/ui';

	let { actions }: { actions?: Snippet } = $props();
</script>

<PageHeader
	title="Backups"
	description="Encrypted restic backups of the manager, stacks and volumes, to local disks or S3. Every backup opens with your Recovery Key."
	{actions}
/>
<TabNav
	label="Backups sections"
	current={page.url.pathname}
	items={[
		{ href: routes.backups(), label: 'Backups' },
		{ href: routes.backupPolicies(), label: 'Policies' },
		{ href: routes.backupRepositories(), label: 'Repositories' }
	]}
/>
