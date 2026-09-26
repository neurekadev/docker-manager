<script lang="ts">
	// One group (#17): its members, allow/deny rules in the permission
	// editor (no rule = deny), rename, make it the default for new users
	// (with a warning when it grants access) and delete.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Star from '@lucide/svelte/icons/star';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import UsersRound from '@lucide/svelte/icons/users-round';
	import { api, unwrap, unwrapEmpty } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		DestructiveConfirm,
		Dialog,
		IconButton,
		Menu,
		Notice,
		PageHeader,
		TextField,
		toast,
		type MenuEntry
	} from '$lib/ui';
	import { environmentName, ifMatch } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import PermissionEditor from '$lib/features/access/PermissionEditor.svelte';
	import RulesSaveBar from '$lib/features/access/RulesSaveBar.svelte';
	import { displayName } from '$lib/features/access/model';
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
	const members = $derived((users.data ?? []).filter((u) => u.groupId === id && !u.owner));
	const groupState = $derived({
		...groups,
		data: groups.data ? (group ?? null) : undefined,
		isError: groups.isError
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
		await qc.invalidateQueries({ queryKey: accessKeys.groups() });
		await goto(routes.accessGroups());
	}

	function menuFor(g: Group): MenuEntry[] {
		const items: MenuEntry[] = [
			{
				label: 'Rename group',
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
				label: 'Make default for new users',
				icon: Star,
				onSelect: () => (defaultOpen = true)
			});
		if (!g.default && g.memberCount === 0) {
			items.push({ separator: true });
			items.push({
				label: 'Delete group',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleteOpen = true)
			});
		}
		return items;
	}
</script>

<Page>
	<QueryView query={groupState} errorTitle="The group could not be loaded.">
		{#snippet children(g: Group | null)}
			{#if !g}
				<Notice tone="info" title="This group does not exist" live="none"
					>It was deleted. <a href={routes.accessGroups()}>Open groups</a></Notice
				>
			{:else}
				<PageHeader
					title={g.name}
					icon={UsersRound}
					color="indigo"
					description={g.default
						? 'New users join this group when they redeem an invitation.'
						: 'Members get these rules; their own overrides win over them.'}
					meta={[
						{ label: `${g.memberCount} ${g.memberCount === 1 ? 'member' : 'members'}` },
						{ label: `${g.ruleCount} ${g.ruleCount === 1 ? 'rule' : 'rules'}` }
					]}
				>
					{#snippet status()}
						{#if g.default}<Badge tone="accent">Default for new users</Badge>{/if}
						{#if g.grantsAccess}<Badge tone="ok" dot>Grants access</Badge>{:else}<Badge
								dot>No access</Badge
							>{/if}
					{/snippet}
					{#snippet actions()}
						<Menu items={menuFor(g)} label="Actions for {g.name}">
							{#snippet trigger(props)}
								<IconButton
									{...props}
									label="Group actions"
									icon={Ellipsis}
									variant="secondary"
								/>
							{/snippet}
						</Menu>
					{/snippet}
				</PageHeader>

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

				<Card title="Members">
					{#if members.length}
						<ul class="members" role="list">
							{#each members as m (m.id)}
								<li>
									<a href={routes.accessUser(m.id)}>{displayName(m)}</a>
									<span class="muted">{m.username}</span>
								</li>
							{/each}
						</ul>
					{:else}
						<p class="muted">
							No members. Move users here from their page{g.default
								? ', or invite someone'
								: ''}.
						</p>
					{/if}
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

				<Dialog bind:open={renameOpen} title="Rename {g.name}" size="sm">
					<TextField label="Name" bind:value={newName} required />
					{#if renameError}<Notice tone="danger" title="Not renamed" live="alert"
							>{renameError}</Notice
						>{/if}
					{#snippet footer()}
						<Button variant="ghost" onclick={() => (renameOpen = false)}>Cancel</Button>
						<Button
							variant="primary"
							disabled={!newName.trim() || newName.trim() === g.name}
							onclick={() => rename(g)}>Rename group</Button
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
					confirmLabel="Make default"
					tone={g.grantsAccess ? 'danger' : 'default'}
					onconfirm={() => makeDefault(g)}
				/>
				<DestructiveConfirm
					bind:open={deleteOpen}
					title="Delete group {g.name}"
					consequences={[
						'The group and its rules are removed.',
						'It has no members, so nobody’s access changes.'
					]}
					confirmText={g.name}
					confirmLabel="Delete group"
					onconfirm={() => remove(g)}
				/>
			{/if}
		{/snippet}
	</QueryView>
</Page>

<style>
	.members {
		display: grid;
		gap: var(--space-1);
	}
</style>
