<script lang="ts">
	// Users (#16): every account with its group, status and sign-in
	// factors. Owner only; new users join through invitations.
	import { createQuery } from '@tanstack/svelte-query';
	import UserPlus from '@lucide/svelte/icons/user-plus';
	import Users from '@lucide/svelte/icons/users';
	import { myPermissionsQuery } from '$lib/api/queries';
	import type { Account } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		DeniedState,
		EmptyState,
		Table,
		formatRelative,
		type Column
	} from '$lib/ui';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import AccessHeader from '$lib/features/access/AccessHeader.svelte';
	import InviteDialog from '$lib/features/access/InviteDialog.svelte';
	import { accountStatus, displayName, factorsText } from '$lib/features/access/model';
	import { groupsQuery, usersQuery } from '$lib/features/access/queries';

	usePage({ title: 'Access', crumbs: [{ label: 'Access' }] });

	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const users = createQuery(() => ({ ...usersQuery(), enabled: owner }));
	const groups = createQuery(() => ({ ...groupsQuery(), enabled: owner }));
	const groupName = (id: string) =>
		groups.data?.find((g) => g.id === id)?.name ?? 'Unknown group';
	const defaultGroup = $derived(groups.data?.find((g) => g.default));
	let inviteOpen = $state(false);

	const columns: Column<Account>[] = [
		{
			id: 'name',
			header: 'User',
			cell: nameCell,
			sortValue: (u) => displayName(u),
			stack: 'title'
		},
		{ id: 'status', header: 'Status', cell: statusCell, width: '170px', stack: 'status' },
		{
			id: 'group',
			header: 'Group',
			cell: groupCell,
			sortValue: (u) => groupName(u.groupId),
			width: '170px'
		},
		{ id: 'factors', header: 'Signs in with', cell: factorsCell, width: '200px' },
		{
			id: 'last',
			header: 'Last sign-in',
			cell: lastCell,
			sortValue: (u) => u.lastSignInAt ?? '',
			width: '140px'
		}
	];
</script>

{#snippet nameCell(u: Account)}
	<NameCell
		name={displayName(u)}
		href={routes.accessUser(u.id)}
		sub={u.displayName ? u.username : u.email}
	>
		{#snippet extra()}{#if u.owner}<Badge tone="accent">Owner</Badge>{/if}{/snippet}
	</NameCell>
{/snippet}
{#snippet statusCell(u: Account)}
	{@const s = accountStatus(u)}
	<Badge tone={s.tone} dot>{s.label}</Badge>
{/snippet}
{#snippet groupCell(u: Account)}{u.owner
		? 'Owner (every permission)'
		: groupName(u.groupId)}{/snippet}
{#snippet factorsCell(u: Account)}<span class="muted">{factorsText(u.factors)}</span>{/snippet}
{#snippet lastCell(u: Account)}
	{#if u.lastSignInAt}<span class="num" title={u.lastSignInAt}
			>{formatRelative(u.lastSignInAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}

<Page>
	{#if perms.data && !owner}
		<DeniedState
			level={1}
			title="Only the owner manages access."
			description="Users, groups and invitations are administered by the owner of this DockYard."
		/>
	{:else}
		<AccessHeader>
			{#snippet actions()}
				<Button variant="primary" icon={UserPlus} onclick={() => (inviteOpen = true)}
					>Invite user</Button
				>
			{/snippet}
		</AccessHeader>
		<Card title="Users" padding="none">
			<QueryView query={users} errorTitle="The users could not be loaded.">
				{#snippet children(rows)}
					<Table
						label="Users"
						{rows}
						{columns}
						rowKey={(u) => u.id}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							<EmptyState
								icon={Users}
								title="Only you so far."
								description="Invite someone: they join {defaultGroup?.name ??
									'the default group'}, which starts without access."
								level={3}
								compact
							/>
						{/snippet}
					</Table>
				{/snippet}
			</QueryView>
		</Card>
		<InviteDialog bind:open={inviteOpen} defaultGroupName={defaultGroup?.name} />
	{/if}
</Page>
