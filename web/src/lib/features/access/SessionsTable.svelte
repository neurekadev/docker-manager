<script lang="ts">
	// Signed-in devices (#16): one row per browser session, with the device
	// in words (User-Agent), its address, when it signed in, its last
	// activity and whether it stays signed in. Signing a device out ends it
	// and its open streams at once; it is not damaging (the device can sign
	// in again), so it runs without a confirmation. Without `userId` the
	// caller's own devices (Profile), where this device signs out like the
	// user menu does; with it a user's devices (owner only).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, unwrapEmpty } from '$lib/api/client';
	import { signOut } from '$lib/auth/session';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { notices } from '$lib/shell/notices.svelte';
	import {
		Badge,
		Button,
		EmptyState,
		Table,
		clock,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import {
		accessKeys,
		mySessionsQuery,
		userSessionsQuery,
		type UserSession
	} from '$lib/features/access/queries';
	import { describeUserAgent } from '$lib/features/access/useragent';

	interface Props {
		/** A user's devices (owner only); omitted: my own devices. */
		userId?: string;
		/** Accessible name of the table. */
		label?: string;
	}

	let { userId, label = 'Signed-In Devices' }: Props = $props();
	const qc = useQueryClient();
	const sessions = createQuery(() => (userId ? userSessionsQuery(userId) : mySessionsQuery()));
	const now = $derived(new Date(clock.now));
	let busy = $state<string | null>(null);

	const deviceOf = (s: UserSession) => describeUserAgent(s.userAgent).label;

	async function signOutDevice(s: UserSession) {
		const device = deviceOf(s);
		busy = s.id;
		try {
			if (!userId && s.current) {
				await signOut({
					queryClient: qc,
					navigate: (url) => goto(url, { replaceState: true }),
					currentPath: () => page.url.pathname,
					resetShell: () => {
						environmentSelection.reset();
						notices.clear();
					}
				});
				return;
			}
			if (userId)
				await unwrapEmpty(
					api.DELETE('/api/v1/users/{userId}/sessions/{sessionId}', {
						params: { path: { userId, sessionId: s.id } }
					})
				);
			else
				await unwrapEmpty(
					api.DELETE('/api/v1/me/sessions/{sessionId}', {
						params: { path: { sessionId: s.id } }
					})
				);
			toast.success(`Signed out ${device}`);
		} catch (e) {
			toast.error(`${device} was not signed out`, { body: actionError(e) });
		} finally {
			busy = null;
		}
		await qc.invalidateQueries({
			queryKey: userId ? accessKeys.userSessions(userId) : accessKeys.mySessions()
		});
	}

	const columns: Column<UserSession>[] = [
		{
			id: 'device',
			header: 'Device',
			cell: deviceCell,
			sortValue: (s) => deviceOf(s),
			stack: 'title'
		},
		{
			id: 'ip',
			header: 'IP',
			cell: ipCell,
			width: '160px',
			sortValue: (s) => s.ip ?? ''
		},
		{
			id: 'created',
			header: 'Signed In',
			cell: createdCell,
			width: '140px',
			sortValue: (s) => s.createdAt
		},
		{
			id: 'seen',
			header: 'Last Active',
			cell: seenCell,
			width: '140px',
			sortValue: (s) => s.lastSeenAt
		},
		{
			id: 'stay',
			header: 'Stay Signed In',
			cell: stayCell,
			width: '140px',
			sortValue: (s) => (s.staySignedIn ? 1 : 0)
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '120px',
			pin: 'end',
			stack: 'actions'
		}
	];
</script>

{#snippet thisDevice()}<Badge tone="accent">This Device</Badge>{/snippet}
{#snippet deviceCell(s: UserSession)}
	<NameCell icon="session" name={deviceOf(s)} extra={s.current ? thisDevice : undefined} />
{/snippet}
{#snippet ipCell(s: UserSession)}
	{#if s.ip}<span class="mono">{s.ip}</span>{:else}<span class="muted">Unknown</span>{/if}
{/snippet}
{#snippet createdCell(s: UserSession)}
	<span title={formatDateTime(s.createdAt)}>{formatRelative(s.createdAt, now)}</span>
{/snippet}
{#snippet seenCell(s: UserSession)}
	<span title={formatDateTime(s.lastSeenAt)}>{formatRelative(s.lastSeenAt, now)}</span>
{/snippet}
{#snippet stayCell(s: UserSession)}
	<span
		title="Signed out after {formatDateTime(s.idleExpiresAt)} without activity, {formatDateTime(
			s.expiresAt
		)} at the latest">{s.staySignedIn ? 'Yes' : 'No'}</span
	>
{/snippet}
{#snippet actionsCell(s: UserSession)}
	<Button
		size="sm"
		variant="ghost"
		loading={busy === s.id}
		disabled={!!busy}
		aria-label="Sign Out {deviceOf(s)}{s.current ? ' (This Device)' : ''}"
		onclick={() => signOutDevice(s)}>Sign Out</Button
	>
{/snippet}

<QueryView query={sessions} errorTitle="The signed-in devices could not be loaded.">
	{#snippet children(rows)}
		{#if rows.length}
			<Table
				{label}
				{rows}
				{columns}
				rowKey={(s) => s.id}
				sort={{ column: 'seen', direction: 'desc' }}
			/>
		{:else}
			<EmptyState
				{...resourceIcon('session')}
				title="No signed-in devices."
				description={userId
					? 'Devices appear here when this user signs in.'
					: 'Devices appear here when you sign in.'}
				level={3}
				compact
			/>
		{/if}
	{/snippet}
</QueryView>
