<script lang="ts">
	// Notification channels (#142; owner only, a Settings tab): where
	// Docker Manager sends messages, what each channel sends and whether
	// its last message arrived. The owner adds ("Add channel",
	// ?create=1), edits, tests and deletes them; addresses stay sealed on
	// the manager and are only shown in the edit dialog on request.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Plus from '@lucide/svelte/icons/plus';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		DeniedState,
		DestructiveConfirm,
		EmptyState,
		IconButton,
		Menu,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import { runTest } from '$lib/features/notifications/actions';
	import ChannelDialog from '$lib/features/notifications/ChannelDialog.svelte';
	import {
		channelStatus,
		sendsSummary,
		type NotificationChannel
	} from '$lib/features/notifications/model';
	import {
		deleteChannel,
		notificationChannelsQuery,
		notificationKeys
	} from '$lib/features/notifications/queries';
	import { serviceLabel } from '$lib/features/notifications/services';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';

	usePage({
		title: 'Notifications',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Notifications' }]
	});

	const queryClient = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const list = createQuery(() => ({ ...notificationChannelsQuery(), enabled: owner }));
	const envs = createQuery(() => ({ ...environmentsQuery(), enabled: owner }));
	const envName = (id: string) => envs.data?.find((e) => e.id === id)?.name;

	const createDialog = urlDialog('create');
	let editOpen = $state(false);
	let editing = $state<NotificationChannel | null>(null);
	let deleteOpen = $state(false);
	let deleting = $state<NotificationChannel | null>(null);
	let testing = $state<string | null>(null);

	async function test(c: NotificationChannel) {
		testing = c.id;
		try {
			await runTest(c, queryClient);
		} finally {
			testing = null;
		}
	}

	async function remove() {
		const c = deleting!;
		await deleteChannel(c);
		toast.success(`Deleted ${c.name}`);
		void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
	}

	function menu(c: NotificationChannel): MenuEntry[] {
		return [
			{
				label: 'Send test',
				description: testing === c.id ? 'Sending…' : undefined,
				disabled: testing === c.id,
				onSelect: () => void test(c)
			},
			{ label: 'Edit', onSelect: () => ((editing = c), (editOpen = true)) },
			{ separator: true },
			{
				label: 'Delete',
				tone: 'danger',
				onSelect: () => ((deleting = c), (deleteOpen = true))
			}
		];
	}

	const subLine = (c: NotificationChannel) =>
		c.target ? `${serviceLabel(c.service)}, ${c.target}` : serviceLabel(c.service);

	const columns: Column<NotificationChannel>[] = [
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (c) => c.name,
			maxWidth: '320px',
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (c) => channelStatus(c).label,
			width: '150px',
			stack: 'status'
		},
		{
			id: 'sends',
			header: 'Sends',
			cell: sendsCell,
			sortValue: (c) => sendsSummary(c, envName),
			maxWidth: '360px',
			stack: 'meta'
		},
		{
			id: 'sent',
			header: 'Last sent',
			cell: sentCell,
			sortValue: (c) => c.lastAttemptAt ?? '',
			width: '140px',
			stack: 'hidden'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '56px',
			align: 'end',
			pin: 'end',
			stack: 'head'
		}
	];
</script>

{#snippet nameCell(c: NotificationChannel)}<NameCell
		icon="notificationChannel"
		name={c.name}
		sub={subLine(c)}
	/>{/snippet}
{#snippet statusCell(c: NotificationChannel)}
	{@const s = channelStatus(c)}
	<StatusBadge status={s.status} label={s.label} title={s.reason} />
{/snippet}
{#snippet sendsCell(c: NotificationChannel)}
	<span class="sends">{sendsSummary(c, envName)}</span>
	{#if c.sendResolved}<span class="sub">and when resolved</span>{/if}
{/snippet}
{#snippet sentCell(c: NotificationChannel)}
	{#if c.lastAttemptAt}<span class="muted" title={formatDateTime(c.lastAttemptAt)}
			>{formatRelative(c.lastAttemptAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet actionsCell(c: NotificationChannel)}
	<Menu items={menu(c)} label="Actions for {c.name}" align="end">
		{#snippet trigger(props)}
			<IconButton {...props} icon={Ellipsis} label="Actions for {c.name}" size="sm" />
		{/snippet}
	</Menu>
{/snippet}

<Page>
	<SettingsHeader
		title="Notifications"
		description="Where Docker Manager sends messages: chat, email, push or a webhook of your own."
	>
		{#snippet actions()}
			{#if owner}
				<Button variant="primary" icon={Plus} onclick={() => (createDialog.open = true)}
					>Add channel</Button
				>
			{/if}
		{/snippet}
	</SettingsHeader>

	{#if perms.data && !owner}
		<DeniedState level={2} title="Only the owner manages notification channels." />
	{:else}
		<Card title="Notification channels" padding="none">
			<QueryView query={list} errorTitle="The notification channels could not be loaded.">
				{#snippet children(rows)}
					<Table
						label="Notification channels"
						{rows}
						{columns}
						rowKey={(c) => c.id}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							<EmptyState
								{...resourceIcon('notificationChannel')}
								title="No notification channels yet."
								description="Add a channel to get messages in Discord, Slack, Teams, Telegram, email, ntfy or any other service."
								level={3}
								compact
							>
								{#snippet actions()}
									<Button
										variant="primary"
										icon={Plus}
										onclick={() => (createDialog.open = true)}
										>Add channel</Button
									>
								{/snippet}
							</EmptyState>
						{/snippet}
					</Table>
				{/snippet}
			</QueryView>
		</Card>
	{/if}
</Page>

{#if owner}
	<ChannelDialog bind:open={() => createDialog.open, (v) => (createDialog.open = v)} />
	<ChannelDialog bind:open={editOpen} channel={editing} />
	{#if deleting}
		<DestructiveConfirm
			bind:open={deleteOpen}
			title="Delete {deleting.name}?"
			consequences={[
				'The channel and its stored address are deleted.',
				'Docker Manager sends nothing to it anymore.'
			]}
			confirmText={deleting.name}
			confirmLabel="Delete channel"
			onconfirm={remove}
		/>
	{/if}
{/if}

<style>
	.sends {
		display: block;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.sub,
	.muted {
		color: var(--text-muted);
	}

	.sub {
		display: block;
		font-size: var(--text-caption);
	}
</style>
