<script lang="ts">
	// Invitations (#16): single-use codes the owner hands out; codes are
	// never listed again. Revoking stops a pending code at once.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import MailPlus from '@lucide/svelte/icons/mail-plus';
	import UserPlus from '@lucide/svelte/icons/user-plus';
	import { api, unwrapEmpty } from '$lib/api/client';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		DeniedState,
		EmptyState,
		Table,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import AccessHeader from '$lib/features/access/AccessHeader.svelte';
	import InviteDialog from '$lib/features/access/InviteDialog.svelte';
	import { displayName, invitationStatus } from '$lib/features/access/model';
	import {
		accessKeys,
		groupsQuery,
		invitationsQuery,
		usersQuery,
		type Invitation
	} from '$lib/features/access/queries';

	usePage({
		title: 'Invitations',
		crumbs: [{ label: 'Access', href: routes.access() }, { label: 'Invitations' }]
	});

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const invitations = createQuery(() => ({ ...invitationsQuery(), enabled: owner }));
	const users = createQuery(() => ({ ...usersQuery(), enabled: owner }));
	const groups = createQuery(() => ({ ...groupsQuery(), enabled: owner }));
	const defaultGroup = $derived(groups.data?.find((g) => g.default));
	let inviteOpen = $state(false);
	let revoking = $state<Invitation | null>(null);
	let revokeOpen = $state(false);

	function who(id?: string) {
		const u = users.data?.find((x) => x.id === id);
		return u ? displayName(u) : undefined;
	}

	async function revoke(inv: Invitation) {
		try {
			await unwrapEmpty(
				api.DELETE('/api/v1/invitations/{invitationId}', {
					params: { path: { invitationId: inv.id } }
				})
			);
		} catch (e) {
			throw new Error(
				actionError(e, {
					invitation_redeemed: 'It was redeemed already; manage the account instead.'
				}),
				{ cause: e }
			);
		}
		toast.success('Revoked the invitation');
		await qc.invalidateQueries({ queryKey: accessKeys.invitations() });
	}

	const columns: Column<Invitation>[] = [
		{
			id: 'created',
			header: 'Invitation',
			cell: createdCell,
			sortValue: (i) => i.createdAt,
			stack: 'title'
		},
		{ id: 'status', header: 'Status', cell: statusCell, width: '130px', stack: 'status' },
		{
			id: 'expires',
			header: 'Expires',
			cell: expiresCell,
			sortValue: (i) => i.expiresAt,
			width: '170px'
		},
		{
			id: 'used',
			header: 'Redeemed by',
			cell: usedCell,
			width: '190px',
			maxWidth: '190px',
			truncate: true
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '110px',
			pin: 'end',
			stack: 'actions'
		}
	];
</script>

{#snippet createdCell(i: Invitation)}
	<NameCell
		name={i.email ?? 'Anyone with the link'}
		sub="Created {formatDateTime(i.createdAt)}{who(i.createdBy)
			? ` by ${who(i.createdBy)}`
			: ''}"
	/>
{/snippet}
{#snippet statusCell(i: Invitation)}
	{@const s = invitationStatus(i.status)}
	<Badge tone={s.tone} dot>{s.label}</Badge>
{/snippet}
{#snippet expiresCell(i: Invitation)}
	{#if i.status === 'pending'}
		<span class="num" title={formatDateTime(i.expiresAt)}>{formatRelative(i.expiresAt)}</span>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet usedCell(i: Invitation)}
	{#if i.redeemedUserId}
		<a href={routes.accessUser(i.redeemedUserId)}>{who(i.redeemedUserId) ?? 'An account'}</a>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionsCell(i: Invitation)}
	{#if i.status === 'pending'}
		<Button
			size="sm"
			variant="danger-soft"
			onclick={() => {
				revoking = i;
				revokeOpen = true;
			}}>Revoke</Button
		>
	{/if}
{/snippet}

<Page>
	{#if perms.data && !owner}
		<DeniedState
			level={1}
			title="Only the owner invites users."
			description="Invitations are issued by the owner of this Docker Manager."
		/>
	{:else}
		<AccessHeader>
			{#snippet actions()}
				<Button variant="primary" icon={UserPlus} onclick={() => (inviteOpen = true)}
					>Invite user</Button
				>
			{/snippet}
		</AccessHeader>
		<Card title="Invitations" padding="none">
			<QueryView query={invitations} errorTitle="The invitations could not be loaded.">
				{#snippet children(rows)}
					<Table
						label="Invitations"
						{rows}
						{columns}
						rowKey={(i) => i.id}
						sort={{ column: 'created', direction: 'desc' }}
					>
						{#snippet empty()}
							<EmptyState
								icon={MailPlus}
								title="No invitations yet."
								description="Invite someone to create their own account. They join {defaultGroup?.name ??
									'the default group'}."
								level={3}
								compact
							>
								{#snippet actions()}
									<Button
										variant="primary"
										icon={UserPlus}
										onclick={() => (inviteOpen = true)}>Invite user</Button
									>
								{/snippet}
							</EmptyState>
						{/snippet}
					</Table>
				{/snippet}
			</QueryView>
		</Card>
		<InviteDialog bind:open={inviteOpen} defaultGroupName={defaultGroup?.name} />
		<ConfirmDialog
			bind:open={revokeOpen}
			title="Revoke this invitation?"
			consequences={['The link stops working at once. Nobody can create an account with it.']}
			confirmLabel="Revoke invitation"
			tone="danger"
			onconfirm={() => (revoking ? revoke(revoking) : undefined)}
		/>
	{/if}
</Page>
