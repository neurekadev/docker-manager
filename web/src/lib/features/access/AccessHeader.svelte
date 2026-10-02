<script lang="ts">
	// Header of the Access section (#16, #17): users, groups and invitations
	// are administered by the instance owner only.
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { routes } from '$lib/routes';
	import { PageHeader, TabNav } from '$lib/ui';

	let { actions }: { actions?: Snippet } = $props();
</script>

<PageHeader
	title="Access"
	description="Who can sign in and what they can do. New accounts join through invitations and start in the default group."
	{actions}
/>
<TabNav
	label="Access Sections"
	current={page.url.pathname}
	items={[
		{ href: routes.access(), label: 'Users' },
		{ href: routes.accessGroups(), label: 'Groups' },
		{ href: routes.accessInvitations(), label: 'Invitations' }
	]}
/>
