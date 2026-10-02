<script lang="ts">
	// RAID arrays of one environment (#143), on the System tab: Linux
	// software RAID (md) and ZFS pools, problems first, one line each: the
	// name (md arrays and members by device path, /dev/md0) and level, the
	// state, the member disks (failed ones in red) and a running rebuild or
	// check with its progress and the kernel's finish estimate. The info
	// button opens the array's details (RaidDetailsDialog, #206). "Check
	// RAID Now" reads the state again (never a scrub).
	// An array with a firing alert (#159) has a mark beside its name that
	// opens Alerts. Every array shows the RAID tile
	// (RESOURCE_ICONS).
	// Shown only when the host has arrays (the Disk Health card says "No
	// RAID arrays found" otherwise) or the state could not be read.
	import { useQueryClient } from '@tanstack/svelte-query';
	import Info from '@lucide/svelte/icons/info';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { api, unwrap, type Environment } from '$lib/api/client';
	import { queryKeys } from '$lib/api/queries';
	import AlertMark from '$lib/features/alerts/AlertMark.svelte';
	import { alertsByArray, arrayAlertKey, type Alert } from '$lib/features/alerts/model';
	import IconCell from '$lib/features/common/IconCell.svelte';
	import { actionError } from '$lib/features/common/errors';
	import {
		Button,
		Card,
		IconButton,
		Meter,
		Notice,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import RaidDetailsDialog from './RaidDetailsDialog.svelte';
	import {
		arrayName,
		memberLabel,
		raidBadge,
		raidCheckedToast,
		raidDisks,
		raidLevel,
		raidProgress,
		sortArrays,
		sortMembers,
		type DiskDevice,
		type RaidArray,
		type RaidHealth
	} from './diskHealth';

	let {
		env,
		raid,
		online,
		devices = [],
		alerts = [],
		now
	}: {
		env: Environment;
		raid: RaidHealth;
		online: boolean;
		/** The environment's disks, for the members' disk health in the details. */
		devices?: DiskDevice[];
		/** The environment's firing alerts (arrays are matched by kind and name). */
		alerts?: Alert[];
		now?: Date;
	} = $props();

	const byArray = $derived(alertsByArray(alerts));

	const qc = useQueryClient();
	let busy = $state(false);
	const rows = $derived(sortArrays(raid.arrays));
	const showCheck = $derived(online && raid.status !== 'agent_outdated');

	// The array whose details are open, by key: live refreshes update it.
	const arrayKey = (a: RaidArray) => `${a.kind}/${a.name}`;
	let detailsOpen = $state(false);
	let selectedKey = $state('');
	const selected = $derived(rows.find((a) => arrayKey(a) === selectedKey));
	// A row that leaves (a notice replaced the list) closes its details, so
	// they never reopen by themselves when it comes back.
	$effect(() => {
		if (!selected) detailsOpen = false;
	});
	function showDetails(a: RaidArray) {
		selectedKey = arrayKey(a);
		detailsOpen = true;
	}

	async function check() {
		busy = true;
		try {
			const res = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/disk-health/checks', {
					params: { path: { environmentId: env.id } },
					body: { scope: 'raid' }
				})
			);
			void qc.invalidateQueries({ queryKey: queryKeys.environments.system(env.id) });
			toast.success(raidCheckedToast(res.raid, env.name));
		} catch (e) {
			toast.error(`Couldn’t check RAID on ${env.name}`, {
				body: actionError(e, {
					rate_limited: 'RAID was checked moments ago. Try again in a few seconds.'
				})
			});
		} finally {
			busy = false;
		}
	}

	const columns: Column<RaidArray>[] = [
		{
			id: 'array',
			header: 'Array',
			cell: arrayCell,
			sortValue: (a) => arrayName(a),
			maxWidth: '260px',
			truncate: true,
			title: (a) => `${arrayName(a)} · ${raidLevel(a)}`,
			stack: 'title'
		},
		{ id: 'state', header: 'State', cell: stateCell, width: '140px', stack: 'status' },
		{
			id: 'disks',
			header: 'Disks',
			cell: disksCell,
			maxWidth: '360px',
			truncate: true,
			// Every member in the tooltip; the cell names only those that are
			// not simply active (failed first).
			title: (a) =>
				[raidDisks(a), ...sortMembers(a.members).map((m) => memberLabel(m).text)]
					.filter(Boolean)
					.join(', ')
		},
		{ id: 'progress', header: 'Progress', cell: progressCell, width: '320px' },
		{
			id: 'details',
			header: 'Details',
			hideHeader: true,
			cell: detailsCell,
			width: '48px',
			pin: 'end',
			stack: 'head'
		}
	];
</script>

{#snippet arrayCell(a: RaidArray)}
	{@const alert = byArray.get(arrayAlertKey(a.kind, a.name))}
	<IconCell icon="raidArray"
		><span class="line"
			><span class="mono">{arrayName(a)}</span>{#if alert}<AlertMark {alert} />{/if}<span
				class="muted level">{raidLevel(a)}</span
			></span
		></IconCell
	>
{/snippet}
{#snippet stateCell(a: RaidArray)}
	{@const b = raidBadge(a)}
	<StatusBadge status={b.status} label={b.label} />
{/snippet}
{#snippet disksCell(a: RaidArray)}
	<span class="muted">{raidDisks(a)}</span
	>{#each sortMembers(a.members).filter((m) => m.state !== 'active') as m (m.name)}{@const l =
			memberLabel(m)}<span class="sep" aria-hidden="true">·</span><span
			class="member mono"
			class:failed={l.failed}
			title={l.title}>{l.text}</span
		>{/each}
{/snippet}
{#snippet progressCell(a: RaidArray)}
	{@const p = raidProgress(a)}
	{#if p}
		<div class="progress">
			{#if p.percent !== null}
				<div class="meter">
					<Meter
						value={p.percent}
						max={100}
						label="{arrayName(a)} progress"
						valueText={p.text}
						role="progressbar"
						tone="neutral"
						showPercent={false}
					/>
				</div>
			{/if}
			<span class="muted text">{p.text}</span>
		</div>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet detailsCell(a: RaidArray)}
	<IconButton
		icon={Info}
		label="Details of {arrayName(a)}"
		size="sm"
		onclick={() => showDetails(a)}
	/>
{/snippet}

<Card title="RAID" padding="none" id="raid">
	{#snippet actions()}
		<div class="head-actions">
			{#if raid.readAt}
				<span class="muted checked" title={formatDateTime(raid.readAt)}
					>Last checked {formatRelative(raid.readAt, now)}</span
				>
			{/if}
			{#if showCheck}
				<Button size="sm" icon={RefreshCw} loading={busy} onclick={check}
					>Check RAID Now</Button
				>
			{/if}
		</div>
	{/snippet}
	{#if raid.status === 'error'}
		<div class="notice">
			<Notice tone="warn" title="The RAID state could not be read." live="none">
				Docker Manager tries again with the next check.
			</Notice>
		</div>
	{/if}
	{#if rows.length}
		<Table label="RAID Arrays of {env.name}" {rows} {columns} rowKey={arrayKey} />
	{/if}
</Card>
{#if selected}
	<RaidDetailsDialog bind:open={detailsOpen} array={selected} {devices} />
{/if}

<style>
	/* The name line keeps the column's ellipsis beside the tile. */
	.line {
		display: block;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.head-actions {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-3);
	}

	.checked {
		font-size: var(--text-caption);
	}

	.level {
		margin-left: var(--space-2);
	}

	.sep {
		margin: 0 var(--space-2);
		color: var(--text-muted);
	}

	.member.failed {
		color: var(--danger);
		font-weight: var(--weight-medium);
	}

	.progress {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-3);
		min-width: 0;
	}

	.meter {
		flex: 0 0 96px;
	}

	.text {
		font-size: var(--text-caption);
	}

	.notice {
		padding: var(--space-4);
	}
</style>
