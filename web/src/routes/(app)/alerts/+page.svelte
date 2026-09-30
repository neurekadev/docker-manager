<script lang="ts">
	// Alerts (#159): problems Docker Manager found across the environments
	// the user can see (AlertsView), scoped by the environment switcher. A
	// Restricted user sees the denied state (#17).
	import { createQuery } from '@tanstack/svelte-query';
	import { myPermissionsQuery } from '$lib/api/queries';
	import AlertsView from '$lib/features/alerts/AlertsView.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf, isRestricted } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { DeniedState } from '$lib/ui';

	usePage({ title: 'Alerts', crumbs: [{ label: 'Alerts' }], environmentScoped: true });

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
</script>

{#if perms.data && isRestricted(access)}
	<DeniedState level={1} />
{:else}
	<Page>
		<AlertsView environmentId={environmentSelection.id} owner={access.owner} />
	</Page>
{/if}
