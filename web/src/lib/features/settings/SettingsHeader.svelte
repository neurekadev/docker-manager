<script lang="ts">
	// Header of the Settings section: the page title and the settings the
	// caller can open (hidden, not disabled, when they can't).
	import type { Snippet } from 'svelte';
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { PageHeader, TabNav, type TabLink } from '$lib/ui';
	import { can } from '$lib/features/common/access';

	let {
		title,
		description,
		actions
	}: { title: string; description?: string; actions?: Snippet } = $props();

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const tabs = $derived.by(() => {
		const t: TabLink[] = [
			{ href: routes.settings(), label: 'Overview' },
			{ href: routes.security(), label: 'Profile and security' },
			{ href: routes.apiTokens(), label: 'API tokens' }
		];
		if (access.owner) t.push({ href: routes.signInPolicy(), label: 'Sign-in policy' });
		if (can(access, 'settings.read'))
			t.push({ href: routes.scheduleDefaults(), label: 'Schedule defaults' });
		if (can(access, 'audit.read')) t.push({ href: routes.audit(), label: 'Audit log' });
		if (access.owner) t.push({ href: routes.diagnostics(), label: 'Diagnostics' });
		return t;
	});
</script>

<PageHeader {title} {description} {actions} />
<TabNav label="Settings sections" current={page.url.pathname} items={tabs} />
