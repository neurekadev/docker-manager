<script lang="ts">
	// Backup policies as one row each (#10): what and where, schedule, the
	// last set, the next run and retention, with Back up now, Details (a
	// drawer) and an overflow menu.
	import { useQueryClient } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Drawer,
		IconButton,
		Menu,
		Table,
		formatDateTime,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { runPolicy } from './actions';
	import PolicyDetail from './PolicyDetail.svelte';
	import {
		retentionText,
		scopeText,
		setState,
		type BackupPolicy,
		type BackupRepository
	} from './model';

	interface Props {
		policies: BackupPolicy[];
		repositories: BackupRepository[] | undefined;
		environmentName: (id: string) => string;
		/** Policy IDs with a backup running now. */
		running?: ReadonlySet<string>;
	}

	let { policies, repositories, environmentName, running = new Set() }: Props = $props();
	const qc = useQueryClient();
	let starting = $state<string | null>(null);
	let selectedId = $state<string | null>(null);
	let drawerOpen = $state(false);
	const selected = $derived(policies.find((p) => p.id === selectedId));

	function details(p: BackupPolicy) {
		selectedId = p.id;
		drawerOpen = true;
	}

	async function run(p: BackupPolicy) {
		starting = p.id;
		await runPolicy(qc, p);
		starting = null;
	}

	function where(p: BackupPolicy): string {
		const repo = repositories?.find((r) => r.id === p.repositoryId)?.name;
		const excluded = (p.excludeStacks ?? []).length + (p.excludeVolumes ?? []).length;
		return [
			`${scopeText(p, environmentName)}${repo ? ` to ${repo}` : ''}`,
			excluded ? `${excluded} left out` : undefined
		]
			.filter(Boolean)
			.join(' · ');
	}

	function menu(p: BackupPolicy): MenuEntry[] {
		const items: MenuEntry[] = [
			{ label: 'Open policy', icon: ExternalLink, href: routes.backupPolicy(p.id) }
		];
		if (has(p, 'backup_policy.manage'))
			items.push({ label: 'Edit policy', icon: Pencil, href: routes.backupPolicyEdit(p.id) });
		return items;
	}

	const columns: Column<BackupPolicy>[] = [
		{ id: 'name', header: 'Policy', cell: nameCell, sortValue: (p) => p.name, stack: 'title' },
		{ id: 'last', header: 'Last set', cell: lastCell, width: '190px', stack: 'status' },
		{ id: 'next', header: 'Next run', cell: nextCell, width: '170px', stack: 'meta' },
		{
			id: 'retention',
			header: 'Retention',
			cell: retentionCell,
			width: '220px',
			stack: 'hidden'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '250px',
			stack: 'actions'
		}
	];
</script>

{#snippet nameCell(p: BackupPolicy)}
	<NameCell name={p.name} href={routes.backupPolicy(p.id)} sub={where(p)} />
{/snippet}
{#snippet lastCell(p: BackupPolicy)}
	{@const s = p.recentSets?.[0]}
	{#if running.has(p.id)}
		<Badge tone="info" dot>Running</Badge>
	{:else if s}
		{@const st = setState(s.state)}
		<span class="cell">
			<Badge tone={st.tone} dot>{st.label}</Badge>
			<span class="muted num" title={formatDateTime(s.startedAt)}
				>{formatRelative(s.startedAt)}</span
			>
		</span>
	{:else}<span class="muted">Never run</span>{/if}
{/snippet}
{#snippet nextCell(p: BackupPolicy)}
	{#if p.schedule?.enabled && p.schedule.nextRun}
		<span class="num" title={formatDateTime(p.schedule.nextRun, p.schedule.timeZone)}
			>{formatRelative(p.schedule.nextRun)}</span
		>
	{:else}<span class="muted">Only when started</span>{/if}
{/snippet}
{#snippet retentionCell(p: BackupPolicy)}<span class="muted">{retentionText(p.retention)}</span
	>{/snippet}
{#snippet actionsCell(p: BackupPolicy)}
	<div class="row-actions">
		{#if has(p, 'backup.run')}
			<Button
				size="sm"
				variant="secondary"
				icon={Play}
				loading={starting === p.id}
				disabled={running.has(p.id)}
				onclick={() => run(p)}>Back up now</Button
			>
		{/if}
		<Button size="sm" variant="ghost" onclick={() => details(p)}>Details</Button>
		<Menu items={menu(p)} label="Actions for {p.name}" align="end">
			{#snippet trigger(props)}
				<IconButton {...props} icon={Ellipsis} label="Actions for {p.name}" size="sm" />
			{/snippet}
		</Menu>
	</div>
{/snippet}

<Table
	label="Backup policies"
	rows={policies}
	{columns}
	rowKey={(p) => p.id}
	sort={{ column: 'name', direction: 'asc' }}
/>

<Drawer bind:open={drawerOpen} title={selected?.name ?? 'Backup policy'} size="640px">
	{#if selected}
		{#key selected.id}
			<PolicyDetail policy={selected} {repositories} {environmentName} />
		{/key}
	{/if}
	{#snippet footer()}
		{#if selected}
			{#if has(selected, 'backup.run')}
				<Button
					icon={Play}
					loading={starting === selected.id}
					disabled={running.has(selected.id)}
					onclick={() => selected && run(selected)}>Back up now</Button
				>
			{/if}
			{#if has(selected, 'backup_policy.manage')}
				<Button variant="ghost" icon={Pencil} href={routes.backupPolicyEdit(selected.id)}
					>Edit</Button
				>
			{/if}
			<Button variant="ghost" href={routes.backupPolicy(selected.id)}>Open policy</Button>
		{/if}
	{/snippet}
</Drawer>

<style>
	.cell {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.row-actions {
		display: flex;
		justify-content: flex-end;
		align-items: center;
		gap: var(--space-1);
	}
</style>
