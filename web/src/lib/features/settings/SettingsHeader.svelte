<script lang="ts">
	// Header of the Settings section: the page title and the instance
	// settings the caller can open (hidden, not disabled, when they can't).
	// The caller's own account and API tokens are the Profile.
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { accessOf } from '$lib/shell/nav';
	import { PageHeader, TabNav } from '$lib/ui';
	import { settingsTabs } from './tabs';

	let {
		title,
		description,
		actions
	}: { title: string; description?: string; actions?: Snippet } = $props();

	const perms = createQuery(() => myPermissionsQuery());
	const tabs = $derived(settingsTabs(accessOf(perms.data)));
</script>

<PageHeader {title} {description} {actions} />
<TabNav label="Settings sections" current={page.url.pathname} items={tabs} />
