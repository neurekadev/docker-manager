<script lang="ts">
	// Edit a prune policy (#14): rules and schedule, saved with If-Match.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { DeniedState, PageHeader } from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import PolicyForm from '$lib/features/maintenance/PolicyForm.svelte';
	import { maintenancePolicyQuery } from '$lib/features/maintenance/queries';

	const id = $derived(page.params.policyId ?? '');
	const policy = createQuery(() => ({
		...maintenancePolicyQuery(id),
		refetchOnWindowFocus: false
	}));
	const name = $derived(policy.data?.name ?? 'Maintenance policy');

	usePage(() => ({
		title: `Edit ${name}`,
		crumbs: [
			{ label: 'Maintenance', href: routes.maintenance() },
			{ label: name, href: routes.maintenancePolicy(id) },
			{ label: 'Edit' }
		]
	}));
</script>

<Page narrow>
	<PageHeader title="Edit {name}" description="Changes apply to the next preview and run." />
	<QueryView query={policy} errorTitle="The maintenance policy could not be loaded." lines={6}>
		{#snippet children(p)}
			{#if has(p, 'maintenance_policy.manage')}
				{#key p.id}<PolicyForm policy={p} />{/key}
			{:else}
				<DeniedState
					title="You can't edit this policy."
					description="Ask the owner of this Docker Manager for the Manage maintenance policies permission."
					level={2}
				/>
			{/if}
		{/snippet}
	</QueryView>
</Page>
