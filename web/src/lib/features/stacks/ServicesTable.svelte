<script lang="ts">
	// Services of a stack (#22 mockup table), in the containers list's
	// order: hue tile + name + description (linked to its container when the
	// service has exactly one), status, running/desired containers, the live
	// figures (CPU and memory from the newest samples, uptime ticking every
	// second), the image with its update state as an icon that checks the
	// stack's images again (#20), the networks (linked, with the addresses
	// on them; one line, the rest in a tooltip), published ports (links only
	// with the environment's service address; one line) and the row actions,
	// pinned to the right edge while the table scrolls sideways: open (only
	// with a web port and an address), terminal (track B3's route with the
	// service preselected) and a menu with the service's own
	// start/stop/restart, its logs (the Logs tab filtered to the service)
	// and its containers. Restart and stop of Docker Manager's own project
	// (#32) are shown disabled, not hidden. Names, images and networks are
	// capped so every row keeps one height.
	import Box from '@lucide/svelte/icons/box';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import Layers from '@lucide/svelte/icons/layers';
	import Play from '@lucide/svelte/icons/play';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Rocket from '@lucide/svelte/icons/rocket';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import Square from '@lucide/svelte/icons/square';
	import SquareTerminal from '@lucide/svelte/icons/square-terminal';
	import { serviceIdentity } from '$lib/design/hue';
	import { serviceIcon } from '$lib/design/icons';
	import { routes } from '$lib/routes';
	import {
		Button,
		EmptyState,
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
	import NetworkList from '$lib/features/resources/NetworkList.svelte';
	import { uptimeSortValue } from '$lib/features/resources/model';
	import {
		openTarget,
		runningOf,
		serviceNetworks,
		servicePorts,
		serviceUsage,
		upSince,
		type StackUsage
	} from './model';
	import type { Stack, StackServiceStatus } from './queries';
	import type { StackImageStatus } from './queries';
	import ImageUpdateBadge from '$lib/features/updates/ImageUpdateBadge.svelte';

	interface Props {
		stack: Stack;
		services: StackServiceStatus[];
		imageStatuses?: StackImageStatus[];
		usage: StackUsage | null;
		serviceAddress?: string;
		/** Starts start/stop/restart of one service (stop confirms first). */
		onoperate?: (service: string, action: StackOperation) => void;
		/** Deploys the stack (the empty state's action). */
		ondeploy?: () => void;
		deploying?: boolean;
		readOnly?: boolean;
	}

	let {
		stack,
		services,
		imageStatuses = [],
		usage,
		serviceAddress,
		onoperate,
		ondeploy,
		deploying = false,
		readOnly = false
	}: Props = $props();
	const can = (a: string) => stack.actions.includes(a);

	/** Ports shown in the cell; the rest are in its tooltip. */
	const PORTS_SHOWN = 2;

	function statusOf(s: StackServiceStatus): string {
		if (s.containers.some((c) => c.health === 'unhealthy')) return 'unhealthy';
		if (s.containers.some((c) => c.state === 'restarting')) return 'restarting';
		return s.status;
	}

	function identity(s: StackServiceStatus) {
		return serviceIdentity({ stackId: stack.id, name: s.name, image: s.image, icon: s.icon });
	}

	/** The service's container page when it has exactly one container. */
	function containerHref(s: StackServiceStatus): string | undefined {
		const named = s.containers.filter((c) => c.name);
		if (named.length !== 1 || s.containers.length !== 1 || !can('container.details.read'))
			return undefined;
		return routes.container(stack.environmentId, named[0].name!);
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
		const more = can('container.logs.read') || (can('container.details.read') && named.length);
		if (more && items.length) items.push({ separator: true });
		if (can('container.logs.read'))
			items.push({
				label: `Logs of ${s.name}`,
				icon: ScrollText,
				href: routes.stackLogs(stack.id, s.name)
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
	const imageOf = (s: StackServiceStatus) => imageStatuses.find((i) => i.service === s.name);
	const usageOf = (s: StackServiceStatus) =>
		usage ? serviceUsage(s, usage) : { cpu: null, memory: null };

	const columns: Column<StackServiceStatus>[] = [
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (s) => s.name,
			stack: 'title',
			maxWidth: '220px',
			title: (s) => (s.description ? `${s.name}: ${s.description}` : s.name)
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
			id: 'containers',
			header: 'Containers',
			cell: containersCell,
			sortValue: (s) => runningOf(s).running,
			numeric: true,
			width: '96px'
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
			id: 'uptime',
			header: 'Uptime',
			cell: uptimeCell,
			sortValue: (s) => uptimeSortValue(since(s)),
			numeric: true,
			width: '104px'
		},
		{
			id: 'image',
			header: 'Image',
			cell: imageCell,
			sortValue: (s) => s.image ?? '',
			maxWidth: '220px',
			title: (s) => s.image || undefined
		},
		{
			id: 'networks',
			header: 'Networks',
			cell: networksCell,
			sortValue: (s) => serviceNetworks(s)[0]?.name,
			maxWidth: '200px'
		},
		{
			id: 'ports',
			header: 'Ports',
			cell: portsCell,
			sortValue: (s) => servicePorts(s.containers)[0]?.label,
			maxWidth: '160px'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			stack: 'actions',
			align: 'end',
			width: '112px',
			pin: 'end'
		}
	];
</script>

{#snippet nameCell(s: StackServiceStatus)}
	{@const id = identity(s)}
	{@const href = containerHref(s)}
	<span class="svc">
		<IconTile icon={serviceIcon(id.icon)} color={id.color} size="sm" />
		<span class="svc-text">
			{#if href}
				<a class="svc-name link" {href}>{s.name}</a>
			{:else}
				<span class="svc-name">{s.name}</span>
			{/if}
			{#if s.description}<span class="svc-desc">{s.description}</span>{/if}
		</span>
	</span>
{/snippet}
{#snippet statusCell(s: StackServiceStatus)}<StatusBadge status={statusOf(s)} />{/snippet}
{#snippet uptimeCell(s: StackServiceStatus)}<Uptime since={since(s)} />{/snippet}
{#snippet networksCell(s: StackServiceStatus)}
	<NetworkList environmentId={stack.environmentId} networks={serviceNetworks(s)} max={1} />
{/snippet}
{#snippet containersCell(s: StackServiceStatus)}
	{@const r = runningOf(s)}
	<span class="num" class:ok={r.total > 0 && r.running === r.total}>{r.running} / {r.total}</span>
{/snippet}
{#snippet imageCell(s: StackServiceStatus)}
	{@const st = imageOf(s)}
	<span class="image-cell">
		<span class="image mono"
			>{s.image || '—'}{#if s.build}<span class="muted"> (built)</span>{/if}</span
		>
		<ImageUpdateBadge
			status={st?.update}
			image={s.image}
			policyId={st?.policyId}
			canCheck={can('update.check') && !readOnly}
		/>
	</span>
{/snippet}
{#snippet portsCell(s: StackServiceStatus)}
	{@const ports = servicePorts(s.containers, serviceAddress)}
	{@const rest = ports.slice(PORTS_SHOWN)}
	{#if ports.length}
		<span class="ports">
			{#each ports.slice(0, PORTS_SHOWN) as p (p.label)}
				{#if p.href}
					<a class="port mono" href={p.href} target="_blank" rel="noopener noreferrer"
						>{p.label}</a
					>
				{:else}
					<span class="mono">{p.label}</span>
				{/if}
			{/each}
			{#if rest.length}
				<span class="muted" title={rest.map((p) => p.label).join('\n')}
					>+{rest.length}<span class="sr-only"
						>: {rest.map((p) => p.label).join(', ')}</span
					></span
				>
			{/if}
		</span>
	{:else}<span class="muted">—</span>{/if}
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
		<EmptyState
			icon={Layers}
			color="blue"
			title="No services running yet"
			description="Deploy the stack to create its containers."
			level={3}
			compact
		>
			{#snippet actions()}
				{#if ondeploy && can('stack.deploy') && !readOnly}
					<Button variant="primary" icon={Rocket} loading={deploying} onclick={ondeploy}
						>Deploy</Button
					>
				{/if}
			{/snippet}
		</EmptyState>
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

	.svc-name,
	.svc-desc {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.svc-name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.link {
		text-decoration: none;
	}

	.link:hover {
		color: var(--accent-text);
		text-decoration: underline;
	}

	.svc-desc {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.ok {
		color: var(--ok);
	}

	.image-cell {
		display: flex;
		align-items: center;
		gap: var(--space-1);
		min-width: 0;
	}

	.image {
		min-width: 0;
		overflow: hidden;
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.ports {
		display: inline-flex;
		gap: var(--space-2);
		white-space: nowrap;
	}

	.port {
		color: var(--accent-text);
	}

	.row-actions {
		display: inline-flex;
		gap: var(--space-1);
	}
</style>
