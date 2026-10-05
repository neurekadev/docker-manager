<script lang="ts">
	// Services of a stack (#22 mockup table), in the containers list's
	// order: service tile (the same for every service) + name + description (linked to its container when the
	// service has exactly one), status, running/desired containers, the live
	// figures (CPU and memory from the newest samples, uptime ticking every
	// second), the image (linked to its page when its ID is known) with its
	// update state as an icon that checks the stack's images again (#20),
	// the volumes (linked; anonymous ones marked with their mount path; at
	// most two lines, the rest in a tooltip), the networks (linked, with
	// the addresses on them; one line, the rest in a tooltip), published
	// ports (links only with the environment's service address; one line)
	// and the row actions, pinned to the right edge while the table scrolls
	// sideways: open (only with a web port and an address), terminal (track
	// B3's route with the service preselected) and a menu with the service's
	// own start, restart and stop, its force recreate (a deploy of the
	// service that replaces its containers), its logs (the Logs tab filtered
	// to the service) and its containers. Restart and stop of Docker Manager's own project
	// (#32) are shown disabled, not hidden. Names, images, volumes and
	// networks are capped so every row keeps one height.
	import Box from '@lucide/svelte/icons/box';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import Layers from '@lucide/svelte/icons/layers';
	import Play from '@lucide/svelte/icons/play';
	import RefreshCcw from '@lucide/svelte/icons/refresh-ccw';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Rocket from '@lucide/svelte/icons/rocket';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import Square from '@lucide/svelte/icons/square';
	import SquareTerminal from '@lucide/svelte/icons/square-terminal';
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
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import NetworkList from '$lib/features/resources/NetworkList.svelte';
	import { uptimeSortValue } from '$lib/features/resources/model';
	import {
		openTarget,
		runningOf,
		serviceImageId,
		serviceNetworks,
		servicePorts,
		serviceUsage,
		serviceVolumes,
		upSince,
		volumeText,
		type StackUsage,
		type VolumeEntry
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
		/** Force recreates one service (confirms first). */
		onrecreate?: (service: string) => void;
		/** Deploys the stack (the empty state's action). */
		ondeploy?: () => void;
		deploying?: boolean;
		readOnly?: boolean;
		/** A restore of the stack runs: no Start, Restart, Stop or Force Recreate. */
		restoring?: boolean;
	}

	let {
		stack,
		services,
		imageStatuses = [],
		usage,
		serviceAddress,
		onoperate,
		onrecreate,
		ondeploy,
		deploying = false,
		readOnly = false,
		restoring = false
	}: Props = $props();
	const can = (a: string) => stack.actions.includes(a);
	// What the caller may do with one service: Start, Stop and Restart may
	// be granted on the service alone (#280); its actions also hold the
	// stack's and its containers' capabilities.
	const canOn = (s: StackServiceStatus, a: string) => (s.actions ?? stack.actions).includes(a);

	/** Every service has the same tile: services have no icon of their own. */
	const SERVICE_TILE = resourceIcon('service');
	/** Ports shown in the cell; the rest are in its tooltip. */
	const PORTS_SHOWN = 2;
	/** Lines of the volumes cell; past them, "+N more" takes the last line. */
	const VOLUME_LINES = 2;

	/** The volumes shown in the cell and the rest (in the "+N more" tooltip). */
	function volumeLines(vols: VolumeEntry[]): { shown: VolumeEntry[]; rest: VolumeEntry[] } {
		const n = vols.length <= VOLUME_LINES ? vols.length : VOLUME_LINES - 1;
		return { shown: vols.slice(0, n), rest: vols.slice(n) };
	}

	function statusOf(s: StackServiceStatus): string {
		if (s.containers.some((c) => c.health === 'unhealthy')) return 'unhealthy';
		if (s.containers.some((c) => c.state === 'restarting')) return 'restarting';
		return s.status;
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
		// Start, Restart, Stop: the order of the header's lifecycle menu.
		// A restore of the stack starts what ran before itself (#282).
		if (!readOnly && !restoring && onoperate) {
			if (!running && canOn(s, 'stack.start'))
				items.push({
					label: `Start ${s.name}`,
					icon: Play,
					onSelect: () => onoperate(s.name, 'start')
				});
			if (running && canOn(s, 'stack.restart'))
				items.push({
					label: `Restart ${s.name}`,
					icon: RotateCw,
					disabled: !!stack.protection,
					onSelect: () => onoperate(s.name, 'restart')
				});
			if (running && canOn(s, 'stack.stop'))
				items.push({
					label: `Stop ${s.name}…`,
					icon: Square,
					tone: 'danger',
					disabled: !!stack.protection,
					onSelect: () => onoperate(s.name, 'stop')
				});
		}
		// A deploy of the service: Docker Manager's own stack may (#32).
		if (!readOnly && !restoring && onrecreate && can('stack.deploy'))
			items.push({
				label: `Force Recreate ${s.name}…`,
				icon: RefreshCcw,
				onSelect: () => onrecreate(s.name)
			});
		const named = s.containers.filter((c) => c.name);
		const more =
			canOn(s, 'container.logs.read') || (canOn(s, 'container.details.read') && named.length);
		if (more && items.length) items.push({ separator: true });
		if (canOn(s, 'container.logs.read'))
			items.push({
				label: `Logs of ${s.name}`,
				icon: ScrollText,
				href: routes.stackLogs(stack.id, s.name)
			});
		if (canOn(s, 'container.details.read'))
			for (const c of named)
				items.push({
					label: named.length === 1 ? 'Container Details' : `Container ${c.name}`,
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
			id: 'volumes',
			header: 'Volumes',
			cell: volumesCell,
			sortValue: (s) => serviceVolumes(s)[0]?.name,
			maxWidth: '200px'
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
			stack: 'head',
			align: 'end',
			width: '112px',
			pin: 'end'
		}
	];
</script>

{#snippet nameCell(s: StackServiceStatus)}
	{@const href = containerHref(s)}
	<span class="svc">
		<IconTile {...SERVICE_TILE} size="sm" />
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
	{@const id = s.image ? serviceImageId(s) : undefined}
	<span class="image-cell">
		{#if id}
			<a class="image mono link" href={routes.image(stack.environmentId, id)}
				>{s.image}{#if s.build}<span class="muted"> (built)</span>{/if}</a
			>
		{:else}
			<span class="image mono"
				>{s.image || '—'}{#if s.build}<span class="muted"> (built)</span>{/if}</span
			>
		{/if}
		<ImageUpdateBadge
			status={st?.update}
			image={s.image}
			policyId={st?.policyId}
			canCheck={can('update.check') && !readOnly}
		/>
	</span>
{/snippet}
{#snippet volumesCell(s: StackServiceStatus)}
	{@const vols = serviceVolumes(s)}
	{@const lines = volumeLines(vols)}
	{#if vols.length}
		<span class="volumes">
			{#each lines.shown as v (v.name)}
				<span class="vol" title={volumeText(v)}>
					{#if v.anonymous}
						<a
							class="vol-name link"
							href={routes.volume(stack.environmentId, v.name)}
							aria-label="Anonymous Volume {v.name}">Anonymous</a
						>
						{#if v.destinations.length}<span class="vol-path mono"
								>{v.destinations[0]}</span
							>{/if}
					{:else}
						<a class="vol-name link" href={routes.volume(stack.environmentId, v.name)}
							>{v.name}</a
						>
					{/if}
				</span>
			{/each}
			{#if lines.rest.length}
				<span class="muted" title={lines.rest.map(volumeText).join('\n')}
					>+{lines.rest.length} more<span class="sr-only"
						>: {lines.rest.map(volumeText).join('; ')}</span
					></span
				>
			{/if}
		</span>
	{:else}<span class="muted">—</span>{/if}
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
				label="Open a Terminal in {s.name}"
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
						label="More Actions for {s.name}"
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
			title="No Services Running Yet"
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

	/* The phone's row card wraps the reference instead of cutting it. */
	@media (max-width: 767px) {
		.image {
			white-space: normal;
			overflow-wrap: anywhere;
		}
	}

	.image.link {
		color: var(--text-default);
	}

	.image.link:hover {
		color: var(--accent-text);
	}

	/* position: relative keeps the hidden .sr-only texts inside the cell. */
	.volumes {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.vol {
		display: flex;
		align-items: baseline;
		gap: var(--space-2);
		min-width: 0;
		white-space: nowrap;
	}

	.vol-name {
		overflow: hidden;
		color: var(--text-default);
		text-overflow: ellipsis;
	}

	.vol-path {
		overflow: hidden;
		color: var(--text-muted);
		text-overflow: ellipsis;
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
