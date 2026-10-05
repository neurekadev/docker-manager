<script lang="ts">
	// One group (#17, #233): its members first (add accounts, remove them;
	// an account can be in several groups), then its allow/deny rules in the
	// permission editor (no rule = deny), rename and delete.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import UserMinus from '@lucide/svelte/icons/user-minus';
	import UserPlus from '@lucide/svelte/icons/user-plus';
	import { ApiRequestError, api, unwrap, unwrapEmpty, type Account } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { StepUpCancelledError, withStepUp } from '$lib/auth/stepup.svelte';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		Checkbox,
		ConfirmDialog,
		DestructiveConfirm,
		Dialog,
		IconButton,
		Menu,
		Notice,
		PageHeader,
		Table,
		TextField,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { environmentName, ifMatch } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import PermissionEditor from '$lib/features/access/PermissionEditor.svelte';
	import { cachedNames } from '$lib/features/access/tree';
	import RulesSaveBar from '$lib/features/access/RulesSaveBar.svelte';
	import {
		accountStatus,
		displayName,
		groupMembers,
		groupNames,
		memberCandidates,
		membersText,
		secondaryName,
		withGroup
	} from '$lib/features/access/model';
	import { diffRules, type Rule } from '$lib/features/access/permissions';
	import {
		accessKeys,
		catalogQuery,
		groupRulesQuery,
		groupsQuery,
		usersQuery,
		type Group
	} from '$lib/features/access/queries';

	const id = $derived(page.params.groupId ?? '');
	const qc = useQueryClient();
	const groups = createQuery(() => groupsQuery());
	const users = createQuery(() => usersQuery());
	const catalog = createQuery(() => catalogQuery());
	const doc = createQuery(() => ({ ...groupRulesQuery(id), refetchOnWindowFocus: false }));
	const envs = createQuery(() => environmentsQuery());
	const envName = (e: string) => environmentName(envs.data, e);

	const group = $derived(groups.data?.find((g) => g.id === id));
	// Its place in the priority order (the list is in that order).
	const rank = $derived((groups.data ?? []).findIndex((g) => g.id === id) + 1);
	// A group missing from the loaded list reads as the standard not-found state.
	const missing = new ApiRequestError('This group does not exist.', 404);
	const groupState = $derived({
		...groups,
		data: group,
		isError: groups.isError || (!!groups.data && !group),
		error: groups.error ?? (groups.data && !group ? missing : null)
	});

	usePage(() => ({
		title: group?.name ?? 'Group',
		crumbs: [
			{ label: 'Access', href: routes.access() },
			{ label: 'Groups', href: routes.accessGroups() },
			{ label: group?.name ?? 'Group' }
		]
	}));

	let draft = $state<Rule[] | null>(null);
	let base = $state<Rule[]>([]);
	let baseRevision = $state(0);
	$effect(() => {
		const d = doc.data;
		if (!d) return;
		untrack(() => {
			const dirty = draft !== null && diffRules(base, draft).length > 0;
			base = d.rules;
			baseRevision = d.revision;
			if (!dirty) draft = d.rules;
		});
	});
	const rules = $derived(draft ?? []);
	const dirty = $derived(draft !== null && diffRules(base, draft).length > 0);
	useUnsaved(
		() => `Permissions of ${group?.name ?? 'group'}`,
		() => dirty
	);

	let renameOpen = $state(false);
	let newName = $state('');
	let renameError = $state<string | null>(null);
	let deleteOpen = $state(false);

	// Adding members adds the group to each account's groups, one PATCH per
	// account; removing a member takes it out of this group only.
	let addOpen = $state(false);
	let addSearch = $state('');
	let picked = $state<string[]>([]);
	let adding = $state(false);
	let addError = $state<string | null>(null);
	const candidates = $derived(memberCandidates(users.data, id, addSearch));
	const anyCandidate = $derived(memberCandidates(users.data, id).length > 0);

	function openAdd() {
		addSearch = '';
		picked = [];
		addError = null;
		addOpen = true;
	}

	async function addMembers(g: Group) {
		adding = true;
		addError = null;
		const chosen = (users.data ?? []).filter((u) => picked.includes(u.id));
		const failed: string[] = [];
		const failedIds: string[] = [];
		let moved = 0;
		for (const u of chosen) {
			try {
				await withStepUp(() =>
					unwrap(
						api.PATCH('/api/v1/users/{userId}', {
							params: {
								path: { userId: u.id },
								header: { 'If-Match': ifMatch(u.revision) }
							},
							body: { groupIds: withGroup(u.groupIds, g.id, true) }
						})
					)
				);
				moved++;
			} catch (e) {
				failed.push(`${displayName(u)}: ${actionError(e)}`);
				failedIds.push(u.id);
				// The identity check was dismissed: stop, nothing more is moved.
				if (e instanceof StepUpCancelledError) break;
			}
		}
		adding = false;
		await qc.invalidateQueries({ queryKey: ['permissions'] });
		if (moved)
			toast.success(
				chosen.length === 1
					? `Added ${displayName(chosen[0])} to ${g.name}`
					: `Added ${moved} ${moved === 1 ? 'member' : 'members'} to ${g.name}`
			);
		if (failed.length) {
			addError = failed.join(' ');
			picked = failedIds;
		} else addOpen = false;
	}

	const memberColumns: Column<Account>[] = [
		{
			id: 'name',
			header: 'Member',
			cell: memberCell,
			sortValue: (u) => displayName(u),
			stack: 'title'
		},
		{ id: 'status', header: 'Status', cell: memberStatusCell, width: '170px', stack: 'status' },
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: memberActionsCell,
			width: '56px',
			align: 'end',
			stack: 'head'
		}
	];

	let removing = $state<Account | null>(null);
	let removeOpen = $state(false);

	async function removeMember(u: Account, g: Group) {
		try {
			await withStepUp(() =>
				unwrap(
					api.PATCH('/api/v1/users/{userId}', {
						params: {
							path: { userId: u.id },
							header: { 'If-Match': ifMatch(u.revision) }
						},
						body: { groupIds: withGroup(u.groupIds, g.id, false) }
					})
				)
			);
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		}
		toast.success(`Removed ${displayName(u)} from ${g.name}`);
		await qc.invalidateQueries({ queryKey: ['permissions'] });
	}

	async function saveRules() {
		try {
			const saved = await withStepUp(() =>
				unwrap(
					api.PUT('/api/v1/groups/{groupId}/permissions', {
						params: {
							path: { groupId: id },
							header: { 'If-Match': ifMatch(baseRevision) }
						},
						body: { rules }
					})
				)
			);
			base = saved.rules;
			baseRevision = saved.revision;
			draft = saved.rules;
			qc.setQueryData(accessKeys.groupRules(id), saved);
			await qc.invalidateQueries({ queryKey: accessKeys.groups() });
			toast.success(`Saved the permissions of ${group?.name ?? 'the group'}`);
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		}
	}

	async function rename(g: Group) {
		renameError = null;
		try {
			await withStepUp(() =>
				unwrap(
					api.PATCH('/api/v1/groups/{groupId}', {
						params: {
							path: { groupId: g.id },
							header: { 'If-Match': ifMatch(g.revision) }
						},
						body: { name: newName.trim() }
					})
				)
			);
			toast.success(`Renamed the group to ${newName.trim()}`);
			renameOpen = false;
			await qc.invalidateQueries({ queryKey: accessKeys.groups() });
		} catch (e) {
			renameError = actionError(e, { group_name_taken: 'Another group has this name.' });
		}
	}

	async function remove(g: Group) {
		try {
			await withStepUp(() =>
				unwrapEmpty(
					api.DELETE('/api/v1/groups/{groupId}', {
						params: {
							path: { groupId: g.id },
							header: { 'If-Match': ifMatch(g.revision) }
						}
					})
				)
			);
		} catch (e) {
			throw new Error(
				actionError(e, {
					group_not_empty:
						'Remove its members first; Docker Manager never changes memberships on its own.'
				}),
				{ cause: e }
			);
		}
		toast.success(`Deleted group ${g.name}`);
		await qc.invalidateQueries({ queryKey: ['permissions'] });
		await goto(routes.accessGroups());
	}

	function menuFor(g: Group): MenuEntry[] {
		const items: MenuEntry[] = [
			{
				label: 'Rename Group',
				icon: Pencil,
				onSelect: () => {
					newName = g.name;
					renameError = null;
					renameOpen = true;
				}
			}
		];
		if (g.memberCount === 0) {
			items.push({ separator: true });
			items.push({
				label: 'Delete Group',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleteOpen = true)
			});
		}
		return items;
	}
</script>

{#snippet memberCell(u: Account)}
	<NameCell
		icon="user"
		name={displayName(u)}
		href={routes.accessUser(u.id)}
		sub={secondaryName(u)}
	/>
{/snippet}
{#snippet memberStatusCell(u: Account)}
	{@const st = accountStatus(u)}
	<Badge tone={st.tone} dot>{st.label}</Badge>
{/snippet}
{#snippet memberActionsCell(u: Account)}
	<IconButton
		label="Remove {displayName(u)} from {group?.name ?? 'the group'}"
		icon={UserMinus}
		size="sm"
		onclick={() => {
			removing = u;
			removeOpen = true;
		}}
	/>
{/snippet}

<Page>
	<QueryView
		query={groupState}
		errorTitle="The group could not be loaded."
		notFoundTitle="This group does not exist."
		notFoundDescription="It was deleted. Open Groups to see the others."
	>
		{#snippet children(g: Group | undefined)}
			{#if g}
				<PageHeader
					title={g.name}
					{...resourceIcon('group')}
					meta={[
						{ label: `Priority ${rank} of ${groups.data?.length ?? rank}` },
						{ label: membersText(g.memberCount) },
						{ label: `${g.ruleCount} ${g.ruleCount === 1 ? 'rule' : 'rules'}` }
					]}
				>
					{#snippet status()}
						{#if g.grantsAccess}<Badge tone="ok" dot>Grants Access</Badge>{:else}<Badge
								dot>No Access</Badge
							>{/if}
					{/snippet}
					{#snippet actions()}
						<Menu items={menuFor(g)} label="Actions for {g.name}">
							{#snippet trigger(props)}
								<IconButton
									{...props}
									label="Group Actions"
									icon={Ellipsis}
									variant="secondary"
								/>
							{/snippet}
						</Menu>
					{/snippet}
				</PageHeader>

				<Card title="Members" padding="none">
					{#snippet actions()}
						{#if anyCandidate}
							<Button size="sm" icon={UserPlus} onclick={openAdd}>Add Members</Button>
						{/if}
					{/snippet}
					<QueryView query={users} errorTitle="The members could not be loaded.">
						{#snippet children(list: Account[])}
							{@const shown = groupMembers(list, g.id)}
							{#if shown.length}
								<Table
									label="Members of {g.name}"
									rows={shown}
									columns={memberColumns}
									rowKey={(u) => u.id}
									sort={{ column: 'name', direction: 'asc' }}
								/>
							{:else}
								<p class="none muted">
									No members yet. {anyCandidate ? 'Add some.' : 'Invite someone.'}
								</p>
							{/if}
						{/snippet}
					</QueryView>
				</Card>

				<Card
					title="Permissions"
					info="Allow grants an action at a scope; Deny blocks it even where a broader rule allows it. Without a rule, it is denied. For members of several groups, the highest group with a rule decides; their own overrides come first."
				>
					<QueryView
						query={catalog}
						errorTitle="The permission catalog could not be loaded."
					>
						{#snippet children(cat)}
							<QueryView
								query={doc}
								errorTitle="The group's rules could not be loaded."
							>
								{#snippet children(loaded)}
									{#if loaded}
										<PermissionEditor
											catalog={cat}
											mode="group"
											{rules}
											onchange={(r) => (draft = r)}
										/>
									{/if}
								{/snippet}
							</QueryView>
						{/snippet}
					</QueryView>
				</Card>

				<RulesSaveBar
					before={base}
					after={rules}
					catalog={catalog.data}
					subject={g.name}
					mode="group"
					environmentName={envName}
					resourceName={() => cachedNames(qc)}
					ondiscard={() => (draft = base)}
					onsave={saveRules}
				/>

				<Dialog
					bind:open={addOpen}
					title="Add Members to {g.name}"
					description="The accounts you pick join this group and keep their other groups. Their access changes at once."
				>
					{#if memberCandidates(users.data, g.id).length > 6}
						<TextField
							label="Find a User"
							hideLabel
							type="search"
							placeholder="Find a user"
							bind:value={addSearch}
						/>
					{/if}
					<ul class="pick" role="list" aria-label="Users Not in {g.name}">
						{#each candidates as u (u.id)}
							<li>
								<Checkbox
									label={displayName(u)}
									description="{u.groupIds.length
										? `In ${groupNames(u.groupIds, groups.data)}`
										: 'In no group'}{u.status === 'disabled'
										? ' (disabled)'
										: ''}"
									checked={picked.includes(u.id)}
									onchange={(e) =>
										(picked = e.currentTarget.checked
											? [...picked, u.id]
											: picked.filter((x) => x !== u.id))}
								/>
							</li>
						{:else}
							<li class="muted">No user matches.</li>
						{/each}
					</ul>
					{#if addError}<Notice
							tone="danger"
							title="Not every account was added"
							live="alert">{addError}</Notice
						>{/if}
					{#snippet footer()}
						<Button variant="ghost" onclick={() => (addOpen = false)}>Cancel</Button>
						<Button
							variant="primary"
							loading={adding}
							disabled={!picked.length}
							onclick={() => addMembers(g)}
							>{picked.length > 1
								? `Add ${picked.length} Members`
								: 'Add Member'}</Button
						>
					{/snippet}
				</Dialog>
				<Dialog bind:open={renameOpen} title="Rename {g.name}" size="sm">
					<TextField label="Name" bind:value={newName} required />
					{#if renameError}<Notice tone="danger" title="Not Renamed" live="alert"
							>{renameError}</Notice
						>{/if}
					{#snippet footer()}
						<Button variant="ghost" onclick={() => (renameOpen = false)}>Cancel</Button>
						<Button
							variant="primary"
							disabled={!newName.trim() || newName.trim() === g.name}
							onclick={() => rename(g)}>Rename Group</Button
						>
					{/snippet}
				</Dialog>
				{#if removing}
					{@const u = removing}
					<ConfirmDialog
						bind:open={removeOpen}
						title="Remove {displayName(u)} from {g.name}?"
						consequences={[
							u.groupIds.length > 1
								? `Their access becomes that of ${groupNames(withGroup(u.groupIds, g.id, false), groups.data)} plus their own overrides, at once.`
								: 'They are in no group then: only their own overrides grant access.',
							'Their open pages and streams restart.'
						]}
						confirmLabel="Remove Member"
						tone="danger"
						onconfirm={() => removeMember(u, g)}
					/>
				{/if}
				<DestructiveConfirm
					bind:open={deleteOpen}
					title="Delete Group {g.name}"
					consequences={[
						'The group and its rules are removed.',
						'It has no members, so nobody’s access changes.'
					]}
					confirmText={g.name}
					confirmLabel="Delete Group"
					onconfirm={() => remove(g)}
				/>
			{/if}
		{/snippet}
	</QueryView>
</Page>

<style>
	.none {
		padding: var(--space-4) var(--space-5);
	}

	.pick {
		display: grid;
		gap: var(--space-3);
		max-height: 360px;
		margin: var(--space-3) 0;
		overflow: auto;
	}

	@media (max-width: 767px) {
		.none {
			padding: var(--space-4);
		}
	}
</style>
