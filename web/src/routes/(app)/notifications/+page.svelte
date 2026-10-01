<script lang="ts">
	// Notifications: what Docker Manager did and found, in two tabs. The
	// first, Notifications, is the history of finished backups and
	// restores, prunes and update runs (NotificationsView); the second,
	// Alerts (#159), the problems it found (AlertsView), its count the
	// active alerts. The tab lives in the URL (?tab=alerts, which
	// routes.alerts() builds). Both are scoped by the environment switcher; a
	// Restricted user sees the denied state (#17).
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createQuery } from '@tanstack/svelte-query';
	import { myPermissionsQuery } from '$lib/api/queries';
	import AlertsView from '$lib/features/alerts/AlertsView.svelte';
	import { activeAlertsQuery } from '$lib/features/alerts/queries';
	import Page from '$lib/features/common/Page.svelte';
	import NotificationsView from '$lib/features/notification-history/NotificationsView.svelte';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf, isRestricted } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { DeniedState, PageHeader, Tabs } from '$lib/ui';

	usePage({
		title: 'Notifications',
		crumbs: [{ label: 'Notifications' }],
		environmentScoped: true
	});

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	// The bell's query (no request of its own): the Alerts tab's count.
	const active = createQuery(() => ({ ...activeAlertsQuery(), enabled: !!perms.data }));
	const activeCount = $derived(
		(active.data ?? []).filter(
			(a) => !environmentSelection.id || a.environmentId === environmentSelection.id
		).length
	);

	const tab = $derived(page.url.searchParams.get('tab') === 'alerts' ? 'alerts' : 'history');
	const tabs = $derived([
		{ id: 'history', label: 'Notifications' },
		{ id: 'alerts', label: 'Alerts', count: activeCount || undefined }
	]);

	function selectTab(t: string) {
		const url = new URL(page.url);
		if (t === 'alerts') url.searchParams.set('tab', 'alerts');
		else url.searchParams.delete('tab');
		void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
	}
</script>

{#if perms.data && isRestricted(access)}
	<DeniedState level={1} />
{:else}
	<Page>
		<PageHeader
			title="Notifications"
			description="What Docker Manager did and what it found: finished backups, prunes and updates, and the problems that need a look."
		/>
		<Tabs items={tabs} value={tab} label="Notifications sections" onchange={selectTab}>
			{#snippet panel(t)}
				{#if t === 'alerts'}
					<AlertsView environmentId={environmentSelection.id} owner={access.owner} />
				{:else}
					<NotificationsView environmentId={environmentSelection.id} />
				{/if}
			{/snippet}
		</Tabs>
	</Page>
{/if}
