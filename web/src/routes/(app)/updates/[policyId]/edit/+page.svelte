<script lang="ts">
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { PageHeader } from '$lib/ui';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import EnvironmentPolicyForm from '$lib/features/updates/EnvironmentPolicyForm.svelte';
	import { environmentUpdatePolicyQuery } from '$lib/features/updates/queries';
	const id = $derived(page.params.policyId ?? '');
	const policy = createQuery(() => ({
		...environmentUpdatePolicyQuery(id),
		refetchOnWindowFocus: false
	}));
	usePage(() => ({
		title: `Edit ${policy.data?.name ?? 'update policy'}`,
		crumbs: [
			{ label: 'Updates', href: routes.updates() },
			{ label: policy.data?.name ?? 'Update policy', href: routes.updatePolicy(id) },
			{ label: 'Edit' }
		]
	}));
</script>

<Page narrow>
	<PageHeader
		title="Edit update policy"
		description="Changes apply to the next check or update."
	/>
	<QueryView query={policy} errorTitle="The update policy could not be loaded.">
		{#snippet children(p)}{#key p.id}<EnvironmentPolicyForm policy={p} />{/key}{/snippet}
	</QueryView>
</Page>
