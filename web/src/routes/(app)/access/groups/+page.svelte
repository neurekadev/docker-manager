<script lang="ts">
	// Groups (#17, #233): a user can be in several groups; new users are in
	// none (denied everything until added to one or given overrides). The list is the
	// groups' priority order: for a member of several groups the first group
	// with a rule for an action decides. Drag a group's grip (or use the
	// arrow keys on it) to reorder; the new order is saved at once.
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
		DragHandle,
		EmptyState,
		Notice,
		Sortable,
		TextField,
		moveItem,
		toast
	} from '$lib/ui';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import AccessHeader from '$lib/features/access/AccessHeader.svelte';
	import { membersText } from '$lib/features/access/model';
	import {
		accessKeys,
		groupsQuery,
		saveGroupOrder,
		type Group
	} from '$lib/features/access/queries';

	usePage({
		title: 'Groups',
		crumbs: [{ label: 'Access', href: routes.access() }, { label: 'Groups' }]
	});

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const groups = createQuery(() => ({ ...groupsQuery(), enabled: owner }));

	// The order shown while a reorder is saved (the server's afterwards).
	let pending = $state<Group[] | null>(null);
	let saving = $state(false);
	const order = $derived(pending ?? groups.data ?? []);

	// Handles stay enabled while a move is saved (a disabled handle would
	// lose the keyboard focus Sortable returns to it); moves wait instead.
	const sort = new Sortable({
		onmove: (from, to) => void reorder(from, to),
		canMove: () => !saving
	});

	async function reorder(from: number, to: number) {
		const before = order;
		const next = moveItem(before, from, to);
		pending = next;
		saving = true;
		try {
			const saved = await withStepUp(() =>
				saveGroupOrder(
					before.map((g) => g.id),
					next.map((g) => g.id)
				)
			);
			qc.setQueryData(accessKeys.groups(), saved);
			toast.success(`Moved ${next[to].name} to priority ${to + 1}`);
		} catch (err) {
			toast.error('The order was not saved', {
				body: actionError(err, {
					precondition_failed:
						'The groups changed meanwhile. Check the order and try again.'
				})
			});
		} finally {
			pending = null;
			saving = false;
			await qc.invalidateQueries({ queryKey: accessKeys.groups() });
		}
	}

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
</script>

<Page>
	{#if perms.data && !owner}
		<DeniedState level={1} title="Only the owner manages groups." />
	{:else}
		<AccessHeader>
			{#snippet actions()}
				<Button variant="primary" icon={Plus} onclick={() => (createOpen = true)}
					>Create Group</Button
				>
			{/snippet}
		</AccessHeader>
		<Card
			title="Groups"
			info="For members of several groups, the highest group with a rule for an action decides. Drag a group to change its priority."
			padding="none"
		>
			<QueryView query={groups} errorTitle="The groups could not be loaded.">
				{#snippet children()}
					{#if !order.length}
						<div class="empty">
							<EmptyState
								icon={resourceIcon('group').icon}
								title="No Groups Yet"
								description="New users can't do anything until you add them to a group. Create one and give it permissions."
								level={3}
								compact
							>
								{#snippet actions()}
									<Button
										variant="primary"
										icon={Plus}
										onclick={() => (createOpen = true)}>Create Group</Button
									>
								{/snippet}
							</EmptyState>
						</div>
					{:else}
						<ol class="groups" aria-label="Groups by Priority" aria-busy={saving}>
							{#each order as g, i (g.id)}
								<li class="row" {@attach sort.item(i)}>
									<DragHandle
										sortable={sort}
										index={i}
										name={g.name}
										disabled={order.length < 2}
									/>
									<span class="rank" title="Priority {i + 1}">{i + 1}</span>
									<div class="name">
										<NameCell
											icon="group"
											name={g.name}
											href={routes.accessGroup(g.id)}
										/>
									</div>
									<span class="access">
										{#if g.grantsAccess}<Badge tone="ok" dot
												>Grants Access</Badge
											>{:else}<Badge dot>No Access</Badge>{/if}
									</span>
									<span class="count">{membersText(g.memberCount)}</span>
									<span class="count"
										>{g.ruleCount} {g.ruleCount === 1 ? 'rule' : 'rules'}</span
									>
								</li>
							{/each}
						</ol>
					{/if}
				{/snippet}
			</QueryView>
		</Card>
	{/if}
</Page>

<Dialog
	bind:open={createOpen}
	title="Create a Group"
	description="New groups start without access."
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
			disabled={!name.trim()}>Create Group</Button
		>
	{/snippet}
</Dialog>

<style>
	.empty {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.groups {
		margin: 0;
		padding: 0 0 var(--space-2);
		list-style: none;
	}

	.row {
		display: grid;
		grid-template-columns: auto 1.5rem minmax(0, 1fr) 150px 110px 90px;
		align-items: center;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-5) var(--space-2) var(--space-3);
		border-top: 1px solid var(--border-subtle);
	}

	.row:first-child {
		border-top: none;
	}

	.rank {
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-variant-numeric: tabular-nums;
		text-align: center;
	}

	.name {
		min-width: 0;
	}

	.count {
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-variant-numeric: tabular-nums;
		text-align: right;
		white-space: nowrap;
	}

	@media (max-width: 767px) {
		.row {
			grid-template-columns: auto 1.25rem minmax(0, 1fr) auto;
			padding: var(--space-2) var(--space-4) var(--space-2) var(--space-2);
		}

		.count {
			display: none;
		}
	}
</style>
