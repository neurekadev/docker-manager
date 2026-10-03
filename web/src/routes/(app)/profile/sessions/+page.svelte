<script lang="ts">
	// My signed-in devices (#16, a Profile tab): every browser I signed in
	// with, its address and last activity, and whether it stays signed in.
	// I can sign out one device, or every device but this one. A user's
	// devices are the owner's (routes.accessUser).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import LogOut from '@lucide/svelte/icons/log-out';
	import { api, unwrap } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, Card, ConfirmDialog, toast } from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import Page from '$lib/features/common/Page.svelte';
	import { accessKeys, mySessionsQuery } from '$lib/features/access/queries';
	import SessionsTable from '$lib/features/access/SessionsTable.svelte';
	import ProfileHeader from '$lib/features/profile/ProfileHeader.svelte';

	usePage({
		title: 'Sessions',
		crumbs: [{ label: 'Profile', href: routes.profile() }, { label: 'Sessions' }]
	});

	const qc = useQueryClient();
	const sessions = createQuery(() => mySessionsQuery());
	const others = $derived((sessions.data ?? []).filter((s) => !s.current).length);
	let confirmOpen = $state(false);

	async function signOutOthers() {
		try {
			await unwrap(api.POST('/api/v1/me/session-revocations'));
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		}
		toast.success('Signed out your other devices');
		await qc.invalidateQueries({ queryKey: accessKeys.mySessions() });
	}
</script>

<Page>
	<ProfileHeader title="Sessions" />
	<Card title="Signed-In Devices" padding="none">
		{#snippet actions()}
			{#if others > 0}
				<Button size="sm" icon={LogOut} onclick={() => (confirmOpen = true)}
					>Sign Out Other Devices</Button
				>
			{/if}
		{/snippet}
		<SessionsTable label="Your Signed-In Devices" />
	</Card>
</Page>

<ConfirmDialog
	bind:open={confirmOpen}
	title="Sign out your other devices?"
	consequences={[
		`${others === 1 ? '1 other device is' : `${others} other devices are`} signed out now, and their open pages stop.`,
		'This device stays signed in. You can sign in on the others again.'
	]}
	confirmLabel="Sign Out Other Devices"
	tone="danger"
	onconfirm={signOutOthers}
/>
