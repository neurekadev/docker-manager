<script lang="ts">
	// Disk health of one environment (#143), on the System tab: one line
	// per disk (device and model, health, temperature, power-on time and
	// what is wrong in words), problems first; serial numbers, firmware and
	// sizes wait under "Advanced". "Check disks now" asks the agent for a
	// fresh SMART read (never a self-test; a disk in standby is not woken)
	// and toasts the result, also when the read outlasts the request (the
	// live stream brings the new report). Notices replace the list when the
	// agent can't read the disks, is too old, has disk health turned off,
	// or the disks report no SMART data. "No RAID arrays found" tells the
	// host has none (the RAID card only shows with arrays).
	import { useQueryClient } from '@tanstack/svelte-query';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { api, unwrap, type Environment } from '$lib/api/client';
	import { queryKeys } from '$lib/api/queries';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { actionError } from '$lib/features/common/errors';
	import {
		Button,
		Card,
		Notice,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		formatTemperature,
		toast,
		type Column
	} from '$lib/ui';
	import {
		canCheckDisks,
		capacity,
		checkedToast,
		diskBadge,
		diskKind,
		diskNotice,
		diskSummary,
		issuesText,
		noRaidText,
		poweredOn,
		sortDisks,
		type DiskDevice,
		type DiskHealth,
		type RaidHealth
	} from './diskHealth';

	let {
		env,
		health,
		raid,
		online,
		now
	}: {
		env: Environment;
		health: DiskHealth;
		raid?: RaidHealth;
		online: boolean;
		now?: Date;
	} = $props();

	const qc = useQueryClient();
	let busy = $state(false);
	// A check the agent is still running: toast once a newer report ends it.
	let awaiting = $state<{ before: string | undefined } | null>(null);

	const notice = $derived(diskNotice(health));
	const rows = $derived(notice?.replacesList ? [] : sortDisks(health.devices));
	const checking = $derived(busy || health.checking);
	const showCheck = $derived(online && canCheckDisks(health));
	const noRaid = $derived(noRaidText(raid));

	$effect(() => {
		if (awaiting && !health.checking && health.checkedAt !== awaiting.before) {
			awaiting = null;
			toast.success(checkedToast(health, env.name));
		}
	});

	async function check() {
		busy = true;
		const before = health.checkedAt;
		try {
			const res = await unwrap(
				api.POST('/api/v1/environments/{environmentId}/disk-health/checks', {
					params: { path: { environmentId: env.id } },
					body: { scope: 'smart' }
				})
			);
			void qc.invalidateQueries({ queryKey: queryKeys.environments.system(env.id) });
			if (res.diskHealth.checking) awaiting = { before };
			else toast.success(checkedToast(res.diskHealth, env.name));
		} catch (e) {
			toast.error(`Couldn’t check the disks on ${env.name}`, {
				body: actionError(e, {
					rate_limited: 'The disks were checked moments ago. Try again in a few seconds.'
				})
			});
		} finally {
			busy = false;
		}
	}

	const columns: Column<DiskDevice>[] = [
		{
			id: 'device',
			header: 'Device',
			cell: deviceCell,
			sortValue: (d) => d.name,
			maxWidth: '340px',
			truncate: true,
			title: (d) => [d.name, d.model].filter(Boolean).join(' · '),
			stack: 'title'
		},
		{ id: 'health', header: 'Health', cell: healthCell, width: '130px', stack: 'status' },
		{
			id: 'temperature',
			header: 'Temperature',
			cell: temperatureCell,
			sortValue: (d) => d.temperatureC,
			numeric: true,
			width: '120px',
			stack: 'hidden'
		},
		{
			id: 'powered',
			header: 'Powered on',
			cell: poweredCell,
			sortValue: (d) => d.powerOnHours,
			width: '120px',
			stack: 'hidden'
		},
		{
			id: 'issues',
			header: 'Issues',
			cell: issuesCell,
			maxWidth: '420px',
			truncate: true,
			// Phones show the issues: what needs doing matters more than the
			// temperature.
			title: (d) => issuesText(d)
		}
	];
	const detailColumns: Column<DiskDevice>[] = [
		{ id: 'name', header: 'Device', mono: true, sortValue: (d) => d.name, stack: 'title' },
		{ id: 'serial', header: 'Serial number', cell: serialCell, mono: true },
		{ id: 'firmware', header: 'Firmware', cell: firmwareCell, mono: true },
		{ id: 'capacity', header: 'Capacity', cell: capacityCell, numeric: true },
		{ id: 'kind', header: 'Kind', cell: kindCell },
		{ id: 'read', header: 'Last read', cell: readCell }
	];
</script>

{#snippet dash()}<span class="muted">—</span>{/snippet}
{#snippet deviceCell(d: DiskDevice)}
	<span class="mono">{d.name}</span>{#if d.model}<span class="muted model">{d.model}</span>{/if}
{/snippet}
{#snippet healthCell(d: DiskDevice)}
	{@const b = diskBadge(d)}
	<StatusBadge status={b.status} label={b.label} title={b.title} />
{/snippet}
{#snippet temperatureCell(d: DiskDevice)}
	{#if d.temperatureC !== undefined}{formatTemperature(
			d.temperatureC
		)}{:else}{@render dash()}{/if}
{/snippet}
{#snippet poweredCell(d: DiskDevice)}
	{poweredOn(d)}
{/snippet}
{#snippet issuesCell(d: DiskDevice)}
	{@const text = issuesText(d)}
	<span class:muted={text === 'None' || text === '—'} class:problem={d.state === 'failing'}
		>{text}</span
	>
{/snippet}
{#snippet serialCell(d: DiskDevice)}
	{#if d.serial}{d.serial}{:else}{@render dash()}{/if}
{/snippet}
{#snippet firmwareCell(d: DiskDevice)}
	{#if d.firmware}{d.firmware}{:else}{@render dash()}{/if}
{/snippet}
{#snippet capacityCell(d: DiskDevice)}
	{#if capacity(d)}{capacity(d)}{:else}{@render dash()}{/if}
{/snippet}
{#snippet kindCell(d: DiskDevice)}
	{#if diskKind(d)}{diskKind(d)}{:else}{@render dash()}{/if}
{/snippet}
{#snippet readCell(d: DiskDevice)}
	{#if d.readAt}<time datetime={d.readAt} title={formatDateTime(d.readAt)}
			>{formatRelative(d.readAt, now)}</time
		>{:else}{@render dash()}{/if}
{/snippet}

<Card title="Disk health" subtitle={diskSummary(health)} padding="none" id="disk-health">
	{#snippet actions()}
		<div class="head-actions">
			{#if health.checkedAt}
				<span class="muted checked" title={formatDateTime(health.checkedAt)}
					>Last checked {formatRelative(health.checkedAt, now)}</span
				>
			{/if}
			{#if showCheck}
				<Button size="sm" icon={RefreshCw} loading={checking} onclick={check}
					>Check disks now</Button
				>
			{/if}
		</div>
	{/snippet}
	{#if notice}
		<div class="notice">
			<Notice tone={notice.tone} title={notice.title} live="none">
				{#if notice.body}
					{#each notice.body as part, i (i)}{#if typeof part === 'string'}{part}{:else}<code
								>{part.code}</code
							>{/if}{/each}
				{/if}
				{#if notice.href}
					<a class="docs" href={notice.href} target="_blank" rel="noopener noreferrer"
						>{notice.linkLabel}<ExternalLink size={12} aria-hidden="true" /></a
					>
				{/if}
			</Notice>
		</div>
	{/if}
	{#if rows.length}
		<Table label="Disks of {env.name}" {rows} {columns} rowKey={(d) => d.name} />
		<div class="advanced">
			<Disclosure summary="Advanced">
				<div class="details">
					<Table
						label="Disk details of {env.name}"
						{rows}
						columns={detailColumns}
						rowKey={(d) => d.name}
					/>
				</div>
			</Disclosure>
		</div>
	{:else if !notice}
		<p class="muted empty">
			{health.checking ? 'Reading the disks…' : 'The agent has not reported its disks yet.'}
		</p>
	{/if}
	{#if noRaid}<p class="muted foot">{noRaid}</p>{/if}
</Card>

<style>
	.head-actions {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-3);
	}

	.checked {
		font-size: var(--text-caption);
	}

	.model {
		margin-left: var(--space-2);
	}

	.problem {
		color: var(--danger);
	}

	.notice {
		padding: var(--space-4);
	}

	.notice + .advanced,
	.notice + .foot {
		padding-top: 0;
	}

	.docs {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		margin-left: var(--space-2);
	}

	.advanced {
		padding: var(--space-3) var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}

	.details {
		margin-top: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		overflow: hidden;
	}

	.empty,
	.foot {
		margin: 0;
		padding: var(--space-3) var(--space-4);
		font-size: var(--text-caption);
	}

	.foot {
		border-top: 1px solid var(--border-subtle);
	}
</style>
