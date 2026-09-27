<script lang="ts">
	// The Backups tab of a stack or volume (#10): its backups, newest first,
	// each restorable whole or file by file. Browsing opens the file picker
	// (a lazily listed tree); every restore is previewed and confirmed with
	// a danger button that says what is replaced.
	import { createQuery } from '@tanstack/svelte-query';
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import FolderSearch from '@lucide/svelte/icons/folder-search';
	import History from '@lucide/svelte/icons/history';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		formatBytes,
		formatDateTime,
		formatRelative,
		Table,
		type Column
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { canAnywhere } from '$lib/features/stacks/model';
	import FilePickerDialog from './FilePickerDialog.svelte';
	import RestoreDialog from './RestoreDialog.svelte';
	import { CONSISTENCY_LABEL, type Backup } from './model';
	import { backupsQuery, type BackupFilter } from './queries';
	import type { RestorePlan } from './restore';

	interface Props {
		filter: BackupFilter;
		/** The stack's or volume's name. */
		subject: string;
		/** A volume's page: only this volume of stack backups. */
		volume?: string;
		/** The user may deploy the stack (offers the redeploy). */
		canDeploy?: boolean;
	}

	let { filter, subject, volume, canDeploy = false }: Props = $props();

	const backups = createQuery(() => backupsQuery(filter));
	const perms = createQuery(() => myPermissionsQuery());
	const canCreatePolicy = $derived(canAnywhere(perms.data, 'backup_policy.manage'));
	const rows = $derived(
		[...(backups.data ?? [])]
			.filter((b) => b.kind !== 'manager_state')
			.sort((a, b) => b.snapshotTime.localeCompare(a.snapshotTime))
	);

	let picking = $state<Backup | null>(null);
	let pickerOpen = $state(false);
	let restoring = $state<{ backup: Backup; plan: RestorePlan } | null>(null);
	let restoreOpen = $state(false);

	function browse(b: Backup) {
		picking = b;
		pickerOpen = true;
	}

	function restoreAll(b: Backup) {
		restoring = { backup: b, plan: { kind: 'full' } };
		restoreOpen = true;
	}

	const canRestore = (b: Backup) => has(b, 'backup.restore');
	const canBrowse = (b: Backup) => canRestore(b) && has(b, 'backup.contents.read');

	const columns: Column<Backup>[] = [
		{
			id: 'time',
			header: 'Backup',
			cell: timeCell,
			sortValue: (b) => b.snapshotTime,
			stack: 'title'
		},
		{
			id: 'consistency',
			header: 'Consistency',
			cell: consistencyCell,
			width: '160px'
		},
		{ id: 'state', header: 'State', cell: stateCell, width: '120px', stack: 'status' },
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			numeric: true,
			width: '100px',
			sortValue: (b) => b.bytes ?? 0
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '300px',
			stack: 'actions'
		}
	];
</script>

{#snippet timeCell(b: Backup)}
	<a class="when" href={routes.backup(b.id)}>
		<span>{formatDateTime(b.snapshotTime)}</span>
		<span class="ago">{formatRelative(b.snapshotTime)}</span>
	</a>
{/snippet}
{#snippet consistencyCell(b: Backup)}{b.consistency
		? CONSISTENCY_LABEL[b.consistency]
		: '—'}{/snippet}
{#snippet stateCell(b: Backup)}
	{#if b.state === 'complete'}<Badge tone="ok" dot>Complete</Badge>{:else}<Badge tone="warn" dot
			>Partial</Badge
		>{/if}
{/snippet}
{#snippet sizeCell(b: Backup)}<span class="num">{b.bytes ? formatBytes(b.bytes) : '—'}</span
	>{/snippet}
{#snippet actionsCell(b: Backup)}
	<span class="acts">
		{#if canBrowse(b)}
			<Button size="sm" icon={FolderSearch} onclick={() => browse(b)}>Choose files</Button>
		{/if}
		{#if canRestore(b)}
			<Button size="sm" variant="danger-soft" icon={History} onclick={() => restoreAll(b)}
				>Restore all</Button
			>
		{/if}
	</span>
{/snippet}

<Card
	title="Backups"
	subtitle="Restoring stops the containers that use the data and starts the ones that were running again afterwards."
	padding="none"
>
	<QueryView query={backups} errorTitle="The backups could not be loaded.">
		{#if rows.length === 0}
			<EmptyState
				icon={DatabaseBackup}
				color="teal"
				level={3}
				title="No backups of {subject} yet"
				description="A backup policy that covers {subject} creates them on its schedule or when you run it."
			>
				{#snippet actions()}
					{#if canCreatePolicy}<Button href={routes.backupPolicyNew()} variant="primary"
							>Create a backup policy</Button
						>{/if}
				{/snippet}
			</EmptyState>
		{:else}
			<Table
				label="Backups of {subject}"
				{rows}
				{columns}
				rowKey={(b) => b.id}
				sort={{ column: 'time', direction: 'desc' }}
			/>
		{/if}
	</QueryView>
</Card>

{#if picking}
	<FilePickerDialog
		bind:open={pickerOpen}
		backup={picking}
		{volume}
		onnext={(paths) => {
			if (!picking) return;
			restoring = { backup: picking, plan: { kind: 'paths', paths } };
			restoreOpen = true;
		}}
	/>
{/if}
{#if restoring}
	<RestoreDialog
		bind:open={restoreOpen}
		backup={restoring.backup}
		plan={restoring.plan}
		{subject}
		{volume}
		{canDeploy}
	/>
{/if}

<style>
	.when {
		display: grid;
		color: var(--text-strong);
	}

	.ago {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.acts {
		display: inline-flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: var(--space-2);
	}
</style>
