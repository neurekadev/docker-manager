<script lang="ts">
	// Groups (#17): every user belongs to one group; new users join the
	// default group (initially Restricted, without access). New groups start
	// without access too.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, unwrap } from '$lib/api/client';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		DeniedState,
		Dialog,
		Notice,
		Table,
		TextField,
		toast,
		type Column
	} from '$lib/ui';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import AccessHeader from '$lib/features/access/AccessHeader.svelte';
	import { accessKeys, groupsQuery, type Group } from '$lib/features/access/queries';

	usePage({
		title: 'Groups',
		crumbs: [{ label: 'Access', href: routes.access() }, { label: 'Groups' }]
	});

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const groups = createQuery(() => ({ ...groupsQuery(), enabled: owner }));

	let createOpen = $state(false);
	let name = $state('');
	let busy = $state(false);
	let error = $state<unknown>(null);

	async function create(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		try {
			const g = await withStepUp(() =>
				unwrap(api.POST('/api/v1/groups', { body: { name: name.trim() } }))
			);
			toast.success(`Created group ${g.name}`, {
				body: 'It has no access yet: choose its permissions.'
			});
			await qc.invalidateQueries({ queryKey: accessKeys.groups() });
			createOpen = false;
			name = '';
			await goto(routes.accessGroup(g.id));
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}

	const columns: Column<Group>[] = [
		{ id: 'name', header: 'Group', cell: nameCell, sortValue: (g) => g.name, stack: 'title' },
		{ id: 'access', header: 'Access', cell: accessCell, width: '170px', stack: 'status' },
		{
			id: 'members',
			header: 'Members',
			cell: membersCell,
			numeric: true,
			sortValue: (g) => g.memberCount,
			width: '110px'
		},
		{
			id: 'rules',
			header: 'Rules',
			cell: rulesCell,
			numeric: true,
			sortValue: (g) => g.ruleCount,
			width: '100px'
		}
	];
</script>

{#snippet nameCell(g: Group)}
	<NameCell name={g.name} href={routes.accessGroup(g.id)}>
		{#snippet extra()}{#if g.default}<Badge tone="accent">Default for new users</Badge
				>{/if}{/snippet}
	</NameCell>
{/snippet}
{#snippet accessCell(g: Group)}
	{#if g.grantsAccess}<Badge tone="ok" dot>Grants access</Badge>{:else}<Badge dot>No access</Badge
		>{/if}
{/snippet}
{#snippet membersCell(g: Group)}<span class="num">{g.memberCount}</span>{/snippet}
{#snippet rulesCell(g: Group)}<span class="num">{g.ruleCount}</span>{/snippet}

<Page>
	{#if perms.data && !owner}
		<DeniedState
			level={1}
			title="Only the owner manages groups."
			description="Groups and permissions are administered by the owner of this Docker Manager."
		/>
	{:else}
		<AccessHeader>
			{#snippet actions()}
				<Button variant="primary" icon={Plus} onclick={() => (createOpen = true)}
					>Create group</Button
				>
			{/snippet}
		</AccessHeader>
		<Card title="Groups" padding="none">
			<QueryView query={groups} errorTitle="The groups could not be loaded.">
				{#snippet children(rows)}
					<Table
						label="Groups"
						{rows}
						{columns}
						rowKey={(g) => g.id}
						sort={{ column: 'name', direction: 'asc' }}
					/>
				{/snippet}
			</QueryView>
		</Card>
	{/if}
</Page>

<Dialog
	bind:open={createOpen}
	title="Create a group"
	description="New groups start without access; choose their permissions next."
	size="sm"
>
	<form id="group-form" onsubmit={create} novalidate>
		<TextField
			label="Name"
			bind:value={name}
			required
			error={fieldErrors(error)['body.name']}
			placeholder="Operators"
		/>
		{#if error && !Object.keys(fieldErrors(error)).length}
			<Notice tone="danger" title="The group was not created" live="alert"
				>{actionError(error, {
					group_name_taken: 'Another group has this name. Choose a different name.'
				})}</Notice
			>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (createOpen = false)}>Cancel</Button>
		<Button
			variant="primary"
			type="submit"
			form="group-form"
			loading={busy}
			disabled={!name.trim()}>Create group</Button
		>
	{/snippet}
</Dialog>
