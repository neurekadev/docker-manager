<script lang="ts">
	// Edit an update policy (#20): name, services, schedules, window. The
	// target cannot change. Saves with If-Match (a concurrent edit is refused).
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { DeniedState, PageHeader } from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import PolicyForm from '$lib/features/updates/PolicyForm.svelte';
	import { updatePolicyQuery } from '$lib/features/updates/queries';

	const id = $derived(page.params.policyId ?? '');
	const policy = createQuery(() => ({ ...updatePolicyQuery(id), refetchOnWindowFocus: false }));
	const name = $derived(policy.data?.name ?? 'Update policy');

	usePage(() => ({
		title: `Edit ${name}`,
		crumbs: [
			{ label: 'Updates', href: routes.updates() },
			{ label: name, href: routes.updatePolicy(id) },
			{ label: 'Edit' }
		]
	}));
</script>

<Page narrow>
	<PageHeader title="Edit {name}" description="Changes apply to the next check and update." />
	<QueryView query={policy} errorTitle="The update policy could not be loaded." lines={6}>
		{#snippet children(p)}
			{#if has(p, 'update_policy.manage')}
				{#key p.id}<PolicyForm policy={p} />{/key}
			{:else}
				<DeniedState
					title="You can't edit this policy."
					description="Ask the owner of this DockYard for the Manage update policies permission."
					level={2}
				/>
			{/if}
		{/snippet}
	</QueryView>
</Page>
