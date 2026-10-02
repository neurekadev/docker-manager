<script lang="ts">
	// One group (#17): its members first (add accounts from other groups),
	// then its allow/deny rules in the permission editor (no rule = deny),
	// rename, make it the default for new users (with a warning when it
	// grants access) and delete.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Star from '@lucide/svelte/icons/star';
	import Trash2 from '@lucide/svelte/icons/trash-2';
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
	import RulesSaveBar from '$lib/features/access/RulesSaveBar.svelte';
	import {
		accountStatus,
		displayName,
		groupMembers,
		memberCandidates,
		membersText,
		secondaryName
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
	// The owner's account may be in the group too; group rules never apply
	// to it, so it is neither listed nor counted, and deleting the group
	// moves it to the default group.
	const ownerHere = $derived((users.data ?? []).some((u) => u.owner && u.groupId === id));
	const groupName = (gid: string) =>
		groups.data?.find((g) => g.id === gid)?.name ?? 'another group';
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
	let defaultOpen = $state(false);
	let deleteOpen = $state(false);

	// Adding members moves accounts from their current group (every
	// account belongs to exactly one), one PATCH per account.
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
							body: { groupId: g.id }
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
		{ id: 'status', header: 'Status', cell: memberStatusCell, width: '170px', stack: 'status' }
	];

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

	async function makeDefault(g: Group) {
		try {
			const out = await withStepUp(() =>
				unwrap(
					api.POST('/api/v1/groups/{groupId}/default-selection', {
						params: { path: { groupId: g.id } }
					})
				)
			);
			if (out.warning)
				toast.warn(`${g.name} is now the default group`, { body: out.warning, timeout: 0 });
			else toast.success(`${g.name} is now the default group`);
			await qc.invalidateQueries({ queryKey: accessKeys.groups() });
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
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
					default_group_protected:
						'This is the default group. Choose another default first.',
					group_not_empty:
						'Move its members to another group first; Docker Manager never moves users on its own.'
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
		if (!g.default)
			items.push({
				label: 'Make Default for New Users',
				icon: Star,
				onSelect: () => (defaultOpen = true)
			});
		if (!g.default && g.memberCount === 0) {
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
					description={g.default
						? 'New users join this group when they redeem an invitation.'
						: 'Members get these rules; their own overrides win over them.'}
					meta={[
						{ label: membersText(g.memberCount) },
						{ label: `${g.ruleCount} ${g.ruleCount === 1 ? 'rule' : 'rules'}` }
					]}
				>
					{#snippet status()}
						{#if g.default}<Badge tone="accent">Default for New Users</Badge>{/if}
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

				<Card
					title="Members"
					subtitle={g.memberCount ? membersText(g.memberCount) : undefined}
					padding="none"
				>
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
									No members yet. {anyCandidate
										? 'Add accounts from other groups'
										: 'Invite someone'}{g.default
										? '; new users join this group when they redeem an invitation.'
										: '.'}
								</p>
							{/if}
						{/snippet}
					</QueryView>
				</Card>

				<Card
					title="Permissions"
					subtitle="Allow grants an action at a scope; Deny blocks it there even if a broader rule allows it. Without a rule, an action is denied."
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
					ondiscard={() => (draft = base)}
					onsave={saveRules}
				/>

				<Dialog
					bind:open={addOpen}
					title="Add Members to {g.name}"
					description="Every account belongs to exactly one group: the ones you pick move here from their current group, and their access changes at once."
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
					<ul class="pick" role="list" aria-label="Users in Other Groups">
						{#each candidates as u (u.id)}
							<li>
								<Checkbox
									label={displayName(u)}
									description="Now in {groupName(u.groupId)}{u.status ===
									'disabled'
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
							title="Not every account was moved"
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
				<ConfirmDialog
					bind:open={defaultOpen}
					title="Make {g.name} the default group?"
					consequences={[
						'Everyone who redeems an invitation from now on joins this group.',
						g.grantsAccess
							? `${g.name} grants access: every new user gets it before you look at their account.`
							: `${g.name} grants no access, so new users start with none.`,
						'Existing members of other groups are not moved.'
					]}
					confirmLabel="Make Default"
					tone={g.grantsAccess ? 'danger' : 'default'}
					onconfirm={() => makeDefault(g)}
				/>
				<DestructiveConfirm
					bind:open={deleteOpen}
					title="Delete Group {g.name}"
					consequences={[
						'The group and its rules are removed.',
						'It has no members, so nobody’s access changes.',
						...(ownerHere
							? [
									'Your own account moves to the default group; as the owner you keep every permission.'
								]
							: [])
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
