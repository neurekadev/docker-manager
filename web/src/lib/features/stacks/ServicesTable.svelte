<script lang="ts">
	// Services of a stack (#22 mockup table): hue tile + name + description,
	// status, then the live figures (uptime ticking every second, CPU and
	// memory from the newest 10 s samples), running/desired containers,
	// addresses, published ports (links only with the environment's
	// service address), image, image update, restart policy, and the row
	// actions: open (only with a web port and an address), terminal (track
	// B3's route with the service preselected) and a menu with the
	// service's own start/stop/restart and logs. Restart and stop of Docker
	// Manager's own project (#32) are shown disabled, not hidden.
	import Box from '@lucide/svelte/icons/box';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import FileText from '@lucide/svelte/icons/file-text';
	import Play from '@lucide/svelte/icons/play';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Square from '@lucide/svelte/icons/square';
	import SquareTerminal from '@lucide/svelte/icons/square-terminal';
	import { MediaQuery } from 'svelte/reactivity';
	import { serviceIdentity } from '$lib/design/hue';
	import { serviceIcon } from '$lib/design/icons';
	import { routes } from '$lib/routes';
	import {
		IconButton,
		IconTile,
		Menu,
		StatusBadge,
		Table,
		Uptime,
		formatBytes,
		formatPercent,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import type { StackOperation } from './actions';
	import AddressList from '$lib/features/resources/AddressList.svelte';
	import { uptimeSortValue } from '$lib/features/resources/model';
	import {
		openTarget,
		runningOf,
		serviceAddresses,
		servicePorts,
		serviceUsage,
		upSince,
		type StackUsage
	} from './model';
	import type { Stack, StackServiceStatus } from './queries';
	import type { StackImageStatus } from './queries';
	import UpdateStatusBadge from '$lib/features/updates/UpdateStatusBadge.svelte';

	interface Props {
		stack: Stack;
		services: StackServiceStatus[];
		imageStatuses?: StackImageStatus[];
		usage: StackUsage | null;
		serviceAddress?: string;
		/** Starts start/stop/restart of one service (after confirming). */
		onoperate?: (service: string, action: StackOperation) => void;
		readOnly?: boolean;
	}

	let {
		stack,
		services,
		imageStatuses = [],
		usage,
		serviceAddress,
		onoperate,
		readOnly = false
	}: Props = $props();
	const can = (a: string) => stack.actions.includes(a);

	function statusOf(s: StackServiceStatus): string {
		if (s.containers.some((c) => c.health === 'unhealthy')) return 'unhealthy';
		if (s.containers.some((c) => c.state === 'restarting')) return 'restarting';
		return s.status;
	}

	function identity(s: StackServiceStatus) {
		return serviceIdentity({ stackId: stack.id, name: s.name, image: s.image, icon: s.icon });
	}

	function rowMenu(s: StackServiceStatus): MenuEntry[] {
		const items: MenuEntry[] = [];
		const running = runningOf(s).running > 0;
		if (!readOnly && onoperate) {
			if (running && can('stack.restart'))
				items.push({
					label: `Restart ${s.name}`,
					icon: RotateCw,
					disabled: !!stack.protection,
					onSelect: () => onoperate(s.name, 'restart')
				});
			if (!running && can('stack.start'))
				items.push({
					label: `Start ${s.name}`,
					icon: Play,
					onSelect: () => onoperate(s.name, 'start')
				});
			if (running && can('stack.stop'))
				items.push({
					label: `Stop ${s.name}`,
					icon: Square,
					tone: 'danger',
					disabled: !!stack.protection,
					onSelect: () => onoperate(s.name, 'stop')
				});
		}
		const named = s.containers.filter((c) => c.name);
		if (can('container.logs.read') || (can('container.details.read') && named.length)) {
			if (items.length) items.push({ separator: true });
		}
		if (can('container.logs.read'))
			items.push({
				label: 'View logs',
				icon: FileText,
				href: routes.stack(stack.id, 'logs')
			});
		if (can('container.details.read'))
			for (const c of named)
				items.push({
					label: named.length === 1 ? 'Container details' : `Container ${c.name}`,
					icon: Box,
					href: routes.container(stack.environmentId, c.name!)
				});
		return items;
	}

	const since = (s: StackServiceStatus) => upSince([s]) ?? null;
	const usageOf = (s: StackServiceStatus) =>
		usage ? serviceUsage(s, usage) : { cpu: null, memory: null };

	// Below the full desktop layout the restart policy column gives way
	// (it stays in the stacked cards and in the container details). The
	// live figures come right after the status.
	const wide = new MediaQuery('min-width: 1280px');
	const allColumns: Column<StackServiceStatus>[] = [
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (s) => s.name,
			stack: 'title',
			width: '200px'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (s) => statusOf(s),
			stack: 'status',
			width: '120px'
		},
		{
			id: 'uptime',
			header: 'Uptime',
			cell: uptimeCell,
			sortValue: (s) => uptimeSortValue(since(s)),
			numeric: true,
			width: '112px'
		},
		{
			id: 'cpu',
			header: 'CPU',
			cell: cpuCell,
			sortValue: (s) => usageOf(s).cpu,
			numeric: true,
			width: '72px'
		},
		{
			id: 'memory',
			header: 'Memory',
			cell: memCell,
			sortValue: (s) => usageOf(s).memory,
			numeric: true,
			width: '88px'
		},
		{
			id: 'containers',
			header: 'Containers',
			cell: containersCell,
			sortValue: (s) => runningOf(s).running,
			numeric: true,
			width: '104px'
		},
		{
			id: 'addresses',
			header: 'IP addresses',
			cell: addressesCell,
			sortValue: (s) => serviceAddresses(s)[0]?.address,
			width: '140px'
		},
		{
			id: 'ports',
			header: 'Ports',
			cell: portsCell,
			sortValue: (s) => servicePorts(s.containers)[0]?.label
		},
		{
			id: 'image',
			header: 'Image',
			cell: imageCell,
			sortValue: (s) => s.image ?? '',
			mono: true
		},
		{
			id: 'update',
			header: 'Image update',
			cell: updateCell,
			sortValue: (s) => imageStatuses.find((i) => i.service === s.name)?.update ?? '',
			width: '150px'
		},
		{
			id: 'restart',
			header: 'Restart policy',
			cell: restartCell,
			sortValue: (s) => s.containers.find((c) => c.restartPolicy)?.restartPolicy,
			width: '130px'
		},
		{
			id: 'actions',
			header: 'Actions',
			cell: actionsCell,
			stack: 'actions',
			align: 'end',
			width: '116px'
		}
	];
	const columns = $derived(allColumns.filter((c) => wide.current || c.id !== 'restart'));
</script>

{#snippet nameCell(s: StackServiceStatus)}
	{@const id = identity(s)}
	<span class="svc">
		<IconTile icon={serviceIcon(id.icon)} color={id.color} size="sm" />
		<span class="svc-text">
			<span class="svc-name">{s.name}</span>
			{#if s.description}<span class="svc-desc">{s.description}</span>{/if}
		</span>
	</span>
{/snippet}
{#snippet statusCell(s: StackServiceStatus)}<StatusBadge status={statusOf(s)} />{/snippet}
{#snippet updateCell(s: StackServiceStatus)}
	<UpdateStatusBadge status={imageStatuses.find((i) => i.service === s.name)?.update} />
{/snippet}
{#snippet uptimeCell(s: StackServiceStatus)}<Uptime since={since(s)} />{/snippet}
{#snippet addressesCell(s: StackServiceStatus)}<AddressList
		addresses={serviceAddresses(s)}
	/>{/snippet}
{#snippet containersCell(s: StackServiceStatus)}
	{@const r = runningOf(s)}
	<span class="num" class:ok={r.total > 0 && r.running === r.total}>{r.running} / {r.total}</span>
{/snippet}
{#snippet imageCell(s: StackServiceStatus)}
	<span class="image" title={s.image}
		>{s.image || '—'}{#if s.build}<span class="muted"> (built)</span>{/if}</span
	>
{/snippet}
{#snippet portsCell(s: StackServiceStatus)}
	{@const ports = servicePorts(s.containers, serviceAddress)}
	{#if ports.length}
		<span class="ports">
			{#each ports as p (p.label)}
				{#if p.href}
					<a class="port mono" href={p.href} target="_blank" rel="noopener noreferrer"
						>{p.label}</a
					>
				{:else}
					<span class="mono">{p.label}</span>
				{/if}
			{/each}
		</span>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet restartCell(s: StackServiceStatus)}
	<span class="nowrap">{s.containers.find((c) => c.restartPolicy)?.restartPolicy ?? '—'}</span>
{/snippet}
{#snippet cpuCell(s: StackServiceStatus)}
	{formatPercent(usageOf(s).cpu)}
{/snippet}
{#snippet memCell(s: StackServiceStatus)}
	{formatBytes(usageOf(s).memory)}
{/snippet}
{#snippet actionsCell(s: StackServiceStatus)}
	{@const target = openTarget(s.containers, serviceAddress)}
	{@const menu = rowMenu(s)}
	<span class="row-actions">
		{#if target}
			<IconButton
				size="sm"
				variant="secondary"
				label="Open {s.name}"
				icon={ExternalLink}
				href={target}
				external
			/>
		{/if}
		{#if can('container.exec') && !readOnly && runningOf(s).running > 0}
			<IconButton
				size="sm"
				variant="secondary"
				label="Open a terminal in {s.name}"
				icon={SquareTerminal}
				href={routes.stackTerminal(
					stack.id,
					s.containers.find((c) => c.state === 'running')?.name
				)}
			/>
		{/if}
		{#if menu.length}
			<Menu items={menu} label="Actions for {s.name}" align="end">
				{#snippet trigger(props)}<IconButton
						{...props}
						size="sm"
						variant="secondary"
						label="More actions for {s.name}"
						icon={EllipsisVertical}
					/>{/snippet}
			</Menu>
		{/if}
	</span>
{/snippet}

<Table
	label="Services of {stack.displayName || stack.name}"
	rows={services}
	{columns}
	rowKey={(s) => s.name}
>
	{#snippet empty()}
		<p class="empty">No services yet. Deploy the stack to create its containers.</p>
	{/snippet}
</Table>

<style>
	.svc {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
	}

	.svc-text {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.svc-name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.svc-desc {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		white-space: nowrap;
	}

	.ok {
		color: var(--ok);
	}

	.nowrap {
		white-space: nowrap;
	}

	.image {
		display: block;
		max-width: clamp(180px, 22vw, 440px);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.ports {
		display: inline-flex;
		flex-wrap: wrap;
		gap: 2px var(--space-2);
	}

	.port {
		color: var(--accent-text);
	}

	.row-actions {
		display: inline-flex;
		gap: var(--space-1);
	}

	.empty {
		padding: var(--space-6) var(--space-5);
		color: var(--text-muted);
	}
</style>
