<script lang="ts">
	// One user (#16, #17, #233): account and status (disable, sessions,
	// factor and password resets, delete), their signed-in devices, their
	// groups in priority order (add, remove), user overrides (Inherit /
	// Allow / Deny per action and scope, reset to inherit), the effective
	// access with its reasons and a "view as" preview of unsaved changes.
	import { untrack } from 'svelte';
	import { createQueries, createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import LogOut from '@lucide/svelte/icons/log-out';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import UserCheck from '@lucide/svelte/icons/user-check';
	import UserX from '@lucide/svelte/icons/user-x';
	import X from '@lucide/svelte/icons/x';
	import { api, unwrap, unwrapEmpty, type Account, type Schema } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
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
		SecretReveal,
		Select,
		formatDateTime,
		toast,
		type MenuEntry
	} from '$lib/ui';
	import { environmentName, ifMatch, newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import EffectiveTable from '$lib/features/access/EffectiveTable.svelte';
	import PermissionEditor from '$lib/features/access/PermissionEditor.svelte';
	import RulesSaveBar from '$lib/features/access/RulesSaveBar.svelte';
	import SessionsTable from '$lib/features/access/SessionsTable.svelte';
	import {
		accountStatus,
		displayName,
		factorsText,
		groupNames,
		withGroup
	} from '$lib/features/access/model';
	import { diffRules, type InheritedGroup, type Rule } from '$lib/features/access/permissions';
	import {
		accessKeys,
		catalogQuery,
		effectiveQuery,
		groupRulesQuery,
		groupsQuery,
		userQuery,
		userRulesQuery
	} from '$lib/features/access/queries';

	const id = $derived(page.params.userId ?? '');
	const qc = useQueryClient();
	const user = createQuery(() => userQuery(id));
	const groups = createQuery(() => groupsQuery());
	const catalog = createQuery(() => catalogQuery());
	const doc = createQuery(() => ({ ...userRulesQuery(id), refetchOnWindowFocus: false }));
	const effective = createQuery(() => effectiveQuery(id));
	// The user's groups come in priority order, highest first.
	const groupDocs = createQueries(() => ({
		queries: (user.data?.groupIds ?? []).map((gid) => groupRulesQuery(gid))
	}));
	const envs = createQuery(() => environmentsQuery());
	const envName = (e: string) => environmentName(envs.data, e);

	const name = $derived(user.data ? displayName(user.data) : 'User');
	const memberOf = $derived(
		(user.data?.groupIds ?? []).flatMap((gid) => groups.data?.find((g) => g.id === gid) ?? [])
	);
	const inheritedGroups = $derived<InheritedGroup[]>(
		(user.data?.groupIds ?? []).map((gid, i) => ({
			name: groups.data?.find((g) => g.id === gid)?.name ?? 'a group',
			rules: groupDocs[i]?.data?.rules ?? []
		}))
	);
	const addable = $derived(
		(groups.data ?? []).filter((g) => !(user.data?.groupIds ?? []).includes(g.id))
	);
	usePage(() => ({
		title: name,
		crumbs: [{ label: 'Access', href: routes.access() }, { label: name }]
	}));

	// Overrides being edited (kept when the saved document refreshes).
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
		() => `Permissions of ${name}`,
		() => dirty
	);

	let preview = $state<Schema<'PermissionPreview'> | null>(null);
	let previewing = $state(false);
	let addTo = $state('');
	// The group being joined or left (confirm 'join' / 'leave').
	let changing = $state('');
	let confirm = $state<
		null | 'disable' | 'enable' | 'sessions' | 'factors' | 'password' | 'join' | 'leave'
	>(null);
	let confirmOpen = $state(false);
	let deleteOpen = $state(false);
	let revokeTokens = $state(false);
	let resetLink = $state<Schema<'IssuedCode'> | null>(null);

	function ask(kind: typeof confirm, groupId = '') {
		confirm = kind;
		changing = groupId;
		revokeTokens = false;
		confirmOpen = true;
	}

	async function refresh() {
		await qc.invalidateQueries({ queryKey: ['permissions'] });
	}

	async function patch(u: Account, body: Schema<'PatchUserInputBody'>) {
		return withStepUp(() =>
			unwrap(
				api.PATCH('/api/v1/users/{userId}', {
					params: { path: { userId: u.id }, header: { 'If-Match': ifMatch(u.revision) } },
					body
				})
			)
		);
	}

	async function run(u: Account) {
		try {
			switch (confirm) {
				case 'disable':
					await patch(u, { status: 'disabled' });
					toast.success(`Disabled ${displayName(u)}`, {
						body: 'Their sessions and API tokens stopped working.'
					});
					break;
				case 'enable':
					await patch(u, { status: 'active' });
					toast.success(`Enabled ${displayName(u)}`);
					break;
				case 'join':
				case 'leave': {
					await patch(u, {
						groupIds: withGroup(u.groupIds, changing, confirm === 'join')
					});
					const g = groups.data?.find((x) => x.id === changing)?.name ?? 'the group';
					toast.success(
						confirm === 'join'
							? `Added ${displayName(u)} to ${g}`
							: `Removed ${displayName(u)} from ${g}`
					);
					if (confirm === 'join') addTo = '';
					break;
				}
				case 'sessions':
					await unwrapEmpty(
						api.POST('/api/v1/users/{userId}/session-revocations', {
							params: { path: { userId: u.id } }
						})
					);
					toast.success(`Signed out ${displayName(u)} everywhere`);
					break;
				case 'factors':
					await withStepUp(() =>
						unwrap(
							api.POST('/api/v1/users/{userId}/factor-resets', {
								params: { path: { userId: u.id } },
								body: { revokeApiTokens: revokeTokens }
							})
						)
					);
					toast.success(`Reset the sign-in factors of ${displayName(u)}`);
					break;
				case 'password': {
					const key = newIdempotencyKey();
					resetLink = await withStepUp(() =>
						unwrap(
							api.POST('/api/v1/users/{userId}/password-resets', {
								params: {
									path: { userId: u.id },
									header: { 'Idempotency-Key': key }
								},
								body: { revokeApiTokens: revokeTokens }
							})
						)
					);
					break;
				}
			}
			await refresh();
		} catch (e) {
			throw new Error(
				actionError(e, {
					owner_protected:
						'The owner account cannot be changed this way. The owner uses owner recovery.'
				}),
				{ cause: e }
			);
		}
	}

	async function remove(u: Account) {
		try {
			await unwrapEmpty(
				api.DELETE('/api/v1/users/{userId}', {
					params: { path: { userId: u.id }, header: { 'If-Match': ifMatch(u.revision) } }
				})
			);
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		}
		toast.success(`Deleted ${displayName(u)}`);
		await refresh();
		await goto(routes.access());
	}

	async function saveRules() {
		try {
			const saved = await withStepUp(() =>
				unwrap(
					api.PUT('/api/v1/users/{userId}/permissions', {
						params: {
							path: { userId: id },
							header: { 'If-Match': ifMatch(baseRevision) }
						},
						body: { rules }
					})
				)
			);
			base = saved.rules;
			baseRevision = saved.revision;
			draft = saved.rules;
			preview = null;
			qc.setQueryData(accessKeys.userRules(id), saved);
			await qc.invalidateQueries({ queryKey: accessKeys.effective(id) });
			toast.success(`Saved the permissions of ${name}`);
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		}
	}

	async function runPreview() {
		previewing = true;
		try {
			preview = await unwrap(
				api.POST('/api/v1/permission-previews', { body: { userId: id, userRules: rules } })
			);
		} catch (e) {
			toast.error('The preview could not be computed', { body: actionError(e) });
		} finally {
			previewing = false;
		}
	}

	function menuFor(u: Account): MenuEntry[] {
		if (u.owner) return [];
		return [
			u.status === 'active'
				? { label: 'Disable Account', icon: UserX, onSelect: () => ask('disable') }
				: { label: 'Enable Account', icon: UserCheck, onSelect: () => ask('enable') },
			{ label: 'Sign Out Everywhere', icon: LogOut, onSelect: () => ask('sessions') },
			{ label: 'Reset Sign-In Factors', icon: RotateCcw, onSelect: () => ask('factors') },
			{
				label: 'Create Password Reset Link',
				icon: KeyRound,
				onSelect: () => ask('password')
			},
			{ separator: true },
			{
				label: 'Delete Account',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleteOpen = true)
			}
		];
	}

	const CONFIRM = $derived.by(() => {
		const n = name;
		const g = groups.data?.find((x) => x.id === changing);
		const ids = user.data?.groupIds ?? [];
		const after = (join: boolean) => withGroup(ids, changing, join);
		return {
			disable: {
				title: `Disable ${n}?`,
				label: 'Disable Account',
				lines: [
					'Every session and open page of the account ends at once.',
					'Its API tokens stop working.',
					'Nothing it created is removed; enable it again any time.'
				]
			},
			enable: {
				title: `Enable ${n}?`,
				label: 'Enable Account',
				lines: ['They can sign in again with their existing factors.']
			},
			sessions: {
				title: `Sign out ${n} everywhere?`,
				label: 'Sign Out Everywhere',
				lines: ['Every session and open page ends now. They can sign in again.']
			},
			factors: {
				title: `Reset the sign-in factors of ${n}?`,
				label: 'Reset Factors',
				lines: [
					'Removes their authenticator app (TOTP), passkeys and recovery codes.',
					'Ends their sessions; they sign in with their password and enroll again.',
					'Accounts that only had passkeys also need a password reset link.'
				]
			},
			password: {
				title: `Create a password reset link for ${n}?`,
				label: 'Create Link',
				lines: [
					'The link works once and replaces earlier unused links.',
					'Redeeming it ends all of their sessions.'
				]
			},
			join: {
				title: `Add ${n} to ${g?.name ?? 'the group'}?`,
				label: 'Add to Group',
				lines: [
					`Their access becomes that of ${groupNames(after(true), groups.data)}, in this priority order, plus their own overrides, at once.`,
					'Their open pages and streams restart.'
				]
			},
			leave: {
				title: `Remove ${n} from ${g?.name ?? 'the group'}?`,
				label: 'Remove from Group',
				lines: [
					after(false).length
						? `Their access becomes that of ${groupNames(after(false), groups.data)} plus their own overrides, at once.`
						: 'They are in no group then: only their own overrides grant access.',
					'Their open pages and streams restart.'
				]
			}
		};
	});
</script>

<Page>
	<QueryView
		query={user}
		errorTitle="The user could not be loaded."
		notFoundTitle="This user does not exist."
	>
		{#snippet children(u: Account)}
			{@const s = accountStatus(u)}
			{@const menu = menuFor(u)}
			<PageHeader
				title={displayName(u)}
				{...resourceIcon('user')}
				meta={[{ label: u.username, mono: true }, ...(u.email ? [{ label: u.email }] : [])]}
			>
				{#snippet status()}
					<Badge tone={s.tone} dot>{s.label}</Badge>
					{#if u.owner}<Badge tone="accent">Owner</Badge>{/if}
				{/snippet}
				{#snippet actions()}
					{#if menu.length}
						<Menu items={menu} label="Actions for {displayName(u)}">
							{#snippet trigger(props)}
								<IconButton
									{...props}
									label="Account Actions"
									icon={Ellipsis}
									variant="secondary"
								/>
							{/snippet}
						</Menu>
					{/if}
				{/snippet}
			</PageHeader>

			<Card title="Account">
				<Facts
					columns={3}
					items={[
						{ label: 'Signs In With', value: factorsText(u.factors) },
						{ label: 'Recovery Codes Left', value: u.factors.recoveryCodesRemaining },
						{
							label: 'Last Sign-In',
							value: u.lastSignInAt ? formatDateTime(u.lastSignInAt) : 'Never'
						},
						{ label: 'Created', value: formatDateTime(u.createdAt) },
						{
							label: 'Enrollment Open Until',
							value: u.enrollmentDeadline
								? formatDateTime(u.enrollmentDeadline)
								: undefined
						},
						{
							label: 'Disabled',
							value: u.disabledAt ? formatDateTime(u.disabledAt) : undefined
						}
					]}
				/>
			</Card>

			{#if u.owner}
				<Notice tone="info" title="The owner holds every permission" live="none">
					Group rules and overrides never apply to the owner, and the owner cannot be
					disabled or deleted.
				</Notice>
			{:else}
				<Card
					title="Sessions"
					subtitle="Signing a device out ends it and its open pages at once."
					padding="none"
				>
					<SessionsTable userId={u.id} label="Signed-In Devices of {displayName(u)}" />
				</Card>

				<Card
					title="Groups"
					info="In priority order: the highest group with a rule for an action decides. Change the order under Groups."
				>
					{#if memberOf.length}
						<ol class="memberships" aria-label="Groups of {displayName(u)}">
							{#each memberOf as g, i (g.id)}
								<li>
									<span class="rank" title="Priority {i + 1}">{i + 1}</span>
									<a href={routes.accessGroup(g.id)}>{g.name}</a>
									{#if g.default}<Badge tone="accent">Default for New Users</Badge
										>{/if}
									<IconButton
										label="Remove {displayName(u)} from {g.name}"
										icon={X}
										size="sm"
										onclick={() => ask('leave', g.id)}
									/>
								</li>
							{/each}
						</ol>
					{:else}
						<p class="muted">In no group: only their own overrides grant access.</p>
					{/if}
					{#if addable.length}
						<div class="add">
							<Select
								label="Add to Group"
								hideLabel
								placeholder="Add to a group"
								options={addable.map((g) => ({ value: g.id, label: g.name }))}
								value={addTo}
								onchange={(v) => (addTo = v)}
							/>
							<Button disabled={!addTo} onclick={() => ask('join', addTo)}
								>Add to Group</Button
							>
						</div>
					{/if}
				</Card>

				<Card
					title="Overrides"
					info="Allow or Deny here beats every group rule. Inherit uses the decision of their groups."
				>
					{#snippet actions()}
						{#if base.length}
							<Button
								size="sm"
								variant="ghost"
								icon={RotateCcw}
								onclick={() => (draft = [])}>Reset All to Inherit</Button
							>
						{/if}
					{/snippet}
					<QueryView
						query={catalog}
						errorTitle="The permission catalog could not be loaded."
					>
						{#snippet children(cat)}
							<QueryView
								query={doc}
								errorTitle="The overrides of {displayName(u)} could not be loaded."
							>
								{#snippet children(loaded)}
									{#if loaded}
										<PermissionEditor
											catalog={cat}
											mode="user"
											{rules}
											groups={inheritedGroups}
											onchange={(r) => (draft = r)}
										/>
									{/if}
								{/snippet}
							</QueryView>
						{/snippet}
					</QueryView>
				</Card>

				<Card title="Effective Access" subtitle="Anything not listed is denied.">
					{#snippet actions()}
						<Button size="sm" loading={previewing} onclick={runPreview}
							>{dirty ? 'Preview with Unsaved Changes' : 'View as This User'}</Button
						>
					{/snippet}
					{#if preview}
						<Notice
							tone="info"
							title={dirty
								? 'Preview of the Unsaved Overrides'
								: 'Access as Evaluated Now'}
							live="status"
						>
							{preview.effective.entries.length} decisions. Nothing is saved.
							{#snippet actions()}<Button
									size="sm"
									variant="ghost"
									onclick={() => (preview = null)}>Close Preview</Button
								>{/snippet}
						</Notice>
						<div class="spaced">
							<EffectiveTable
								entries={preview.effective.entries}
								catalog={catalog.data}
								label="Previewed Access of {displayName(u)}"
							/>
						</div>
					{:else}
						<QueryView
							query={effective}
							errorTitle="The effective access could not be loaded."
						>
							{#snippet children(eff)}
								{#if eff.entries.length}
									<EffectiveTable
										entries={eff.entries}
										catalog={catalog.data}
										label="Effective Access of {displayName(u)}"
									/>
								{:else}
									<p class="muted">
										No rule applies: {displayName(u)} sees nothing yet.
									</p>
								{/if}
							{/snippet}
						</QueryView>
					{/if}
				</Card>

				<RulesSaveBar
					before={base}
					after={rules}
					catalog={catalog.data}
					subject={displayName(u)}
					mode="user"
					environmentName={envName}
					ondiscard={() => (draft = base)}
					onsave={saveRules}
				/>
			{/if}

			{#if confirm}
				<ConfirmDialog
					bind:open={confirmOpen}
					title={CONFIRM[confirm].title}
					consequences={CONFIRM[confirm].lines}
					confirmLabel={CONFIRM[confirm].label}
					tone={confirm === 'disable' || confirm === 'factors' ? 'danger' : 'default'}
					onconfirm={() => run(u)}
				>
					{#if confirm === 'factors' || confirm === 'password'}
						<Checkbox
							bind:checked={revokeTokens}
							label="Also Revoke Their API Tokens"
						/>
					{/if}
				</ConfirmDialog>
			{/if}
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete {displayName(u)}"
				consequences={[
					'The account is removed and every session, open page and API token ends.',
					'Their overrides go with it. Jobs, backups and policies they created stay, and so does the audit history.',
					'They can only come back with a new invitation.'
				]}
				confirmText={u.username}
				confirmLabel="Delete Account"
				onconfirm={() => remove(u)}
			/>
			<Dialog
				open={!!resetLink}
				title="Password Reset Link for {displayName(u)}"
				dismissible={false}
				onclose={() => (resetLink = null)}
			>
				{#if resetLink}
					<SecretReveal
						secret={resetLink.url}
						label="password reset link"
						filename="docker-manager-password-reset.txt"
						description="Send it to {displayName(
							u
						)}. It works once and expires {formatDateTime(resetLink.expiresAt)}."
						confirmLabel="Done"
						onconfirm={() => {
							resetLink = null;
							toast.success(`Created a password reset link for ${displayName(u)}`);
						}}
					/>
				{/if}
			</Dialog>
		{/snippet}
	</QueryView>
</Page>

<style>
	.memberships {
		display: grid;
		gap: var(--space-2);
		margin: 0 0 var(--space-4);
		padding: 0;
		list-style: none;
	}

	.memberships li {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-height: var(--control-height);
	}

	.memberships a {
		color: var(--text-strong);
		font-weight: 500;
	}

	.memberships :global(button) {
		margin-left: auto;
	}

	.rank {
		min-width: 1.25rem;
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-variant-numeric: tabular-nums;
		text-align: center;
	}

	.add {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
	}

	.add > :global(:first-child) {
		flex: 0 1 320px;
	}

	.spaced {
		margin-top: var(--space-3);
	}
</style>
