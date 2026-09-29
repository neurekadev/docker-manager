<script lang="ts">
	// Header of the personal Profile (#16, #31): the page title and the
	// caller's own pages (account and sign-in factors, API tokens, signed-in
	// devices). Instance administration lives in Settings.
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { PageHeader, TabNav } from '$lib/ui';
	import { profileTabs } from './tabs';

	let {
		title,
		description,
		actions
	}: { title: string; description?: string; actions?: Snippet } = $props();
</script>

<PageHeader {title} {description} {actions} />
<TabNav label="Profile sections" current={page.url.pathname} items={profileTabs()} />
