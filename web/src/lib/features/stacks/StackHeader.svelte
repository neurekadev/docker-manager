<script lang="ts">
	// Stack header (#22 mockup): icon tile, name, status, description, meta
	// row (services, containers, created, logical location with the host
	// path on hover and copy) and the actions: Deploy split button, Restart,
	// Stop (Start when stopped), Update with its "update available" dot, and
	// overflow (Down, Migrate, Edit details, Delete). Each action is shown
	// only with its capability (the server still decides) and confirms with
	// its exact consequences before it starts a job.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import CircleArrowDown from '@lucide/svelte/icons/circle-arrow-down';
	import Clock from '@lucide/svelte/icons/clock';
	import Download from '@lucide/svelte/icons/download';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import Folder from '@lucide/svelte/icons/folder';
	import Hammer from '@lucide/svelte/icons/hammer';
	import Package from '@lucide/svelte/icons/package';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Square from '@lucide/svelte/icons/square';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Workflow from '@lucide/svelte/icons/workflow';
	import type { Environment } from '$lib/api/client';
	import { JobWatcher } from '$lib/api/jobs.svelte';
	import { serviceIcon } from '$lib/design/icons';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		ConfirmDialog,
		DestructiveConfirm,
		IconButton,
		Menu,
		PageHeader,
		SplitButton,
		StatusBadge,
		errorMessage,
		formatDateTime,
		formatRelative,
		toast,
		type MenuEntry,
		type MetaItem
	} from '$lib/ui';
	import {
		deleteStack,
		deployStack,
		operateStack,
		type DeployMode,
		type StackOperation
	} from './actions';
	import EditDetailsDialog from './EditDetailsDialog.svelte';
	import { serviceCounts, stackIcon, stackStatus, stackTitle, updateAvailable } from './model';
	import { stackImageStatusQuery, stackKeys, type Stack } from './queries';
	import type { JobTray } from './tray.svelte';
	import UpdateDrawer from './UpdateDrawer.svelte';

	interface Props {
		stack: Stack;
		environment?: Environment;
		tray: JobTray;
		now?: Date;
	}

	let { stack, environment, tray, now = new Date() }: Props = $props();

	const queryClient = useQueryClient();
	const title = $derived(stackTitle(stack));
	const can = (a: string) => stack.actions.includes(a);
	const full = $derived(stack.view === 'full');
	const offline = $derived(stack.readOnly || stack.environmentOnline === false);
	const counts = $derived(serviceCounts(stack));
	const icon = $derived(stackIcon(stack));
	const envName = $derived(environment?.name ?? 'Unknown environment');
	const current = $derived(stackStatus(stack));
	const stoppedLike = $derived(['stopped', 'down', 'missing', 'undeployed'].includes(current));
	const hasBuild = $derived((stack.services ?? []).some((s) => s.build));

	const images = createQuery(() => ({
		...stackImageStatusQuery(stack.id),
		enabled: full
	}));
	const updateDot = $derived(updateAvailable(images.data));

	const meta = $derived.by((): MetaItem[] => {
		if (!full) return [{ icon: Folder, label: `${envName} · ${stack.name}` }];
		const out: MetaItem[] = [
			{
				icon: Workflow,
				label: `${counts.services} ${counts.services === 1 ? 'service' : 'services'}`
			},
			{
				icon: Package,
				label: `${counts.containers} ${counts.containers === 1 ? 'container' : 'containers'}`
			}
		];
		if (stack.createdAt)
			out.push({
				icon: Clock,
				label: `Created ${formatRelative(stack.createdAt, now)}`,
				title: formatDateTime(stack.createdAt)
			});
		const host = stack.location?.hostPath;
		out.push({
			icon: Folder,
			label: `${envName} · ${stack.location?.dir ?? stack.name}`,
			title: host,
			copy: host ? { value: host, what: 'host path' } : undefined
		});
		return out;
	});

	// Dialog state.
	let pending = $state<StackOperation>('restart');
	let confirming = $state(false);
	function ask(action: StackOperation) {
		pending = action;
		confirming = true;
	}
	let deleting = $state(false);
	let editing = $state(false);
	let updating = $state(false);
	let starting = $state<string | null>(null);

	const OPS: Record<
		StackOperation,
		{ verb: string; done: string; failure: string; title: string; label: string }
	> = {
		start: {
			verb: 'Start',
			done: 'Started',
			failure: 'started',
			title: 'Start',
			label: 'Start'
		},
		stop: { verb: 'Stop', done: 'Stopped', failure: 'stopped', title: 'Stop', label: 'Stop' },
		restart: {
			verb: 'Restart',
			done: 'Restarted',
			failure: 'restarted',
			title: 'Restart',
			label: 'Restart'
		},
		down: {
			verb: 'Take down',
			done: 'Took down',
			failure: 'taken down',
			title: 'Take down',
			label: 'Take down'
		}
	};

	const containerWord = (n: number) => `${n} ${n === 1 ? 'container' : 'containers'}`;
	const consequences = $derived.by((): Record<StackOperation, string[]> => ({
		start: [
			`Starts the services of ${title} with their dependencies first, waiting for each depends_on condition.`
		],
		stop: [
			`Stops ${containerWord(counts.containersRunning)} in reverse dependency order.`,
			'Containers, volumes and files are kept; Start brings them back.'
		],
		restart: [
			`Restarts ${containerWord(counts.containersRunning)} in dependency order; services restart one after another.`
		],
		down: [
			`Removes ${containerWord(counts.containers)} and the stack's networks.`,
			'Volumes and the project directory are kept; Deploy creates the containers again.'
		]
	}));

	async function deploy(mode: DeployMode) {
		if (starting) return;
		starting = mode;
		const what =
			mode === 'build' ? 'Build and deploy' : mode === 'pull' ? 'Deploy with pull' : 'Deploy';
		try {
			const job = await deployStack(stack.id, mode);
			tray.add(job, {
				title: `${what} ${title}`,
				success: mode === 'build' ? `Built and deployed ${title}` : `Deployed ${title}`,
				failure: `${title} was not deployed`
			});
		} catch (e) {
			toast.error(`${title} was not deployed`, { body: errorMessage(e) });
		} finally {
			starting = null;
		}
	}

	async function operate(action: StackOperation) {
		const op = OPS[action];
		const job = await operateStack(stack.id, action);
		tray.add(job, {
			title: `${op.title} ${title}`,
			success: `${op.done} ${title}`,
			failure: `${title} was not ${op.failure}`
		});
	}

	async function remove() {
		const job = await deleteStack(stack.id);
		const name = title;
		// The stack page goes away when the stack does (a live event can
		// arrive before the job's end), so a watcher of its own reports the
		// end and leaves the page; the tray only shows the progress.
		const watcher = new JobWatcher(job.id, {
			onfinish: (j) => {
				watcher.stop();
				void queryClient.invalidateQueries({ queryKey: stackKeys.all });
				if (j.state === 'succeeded') {
					toast.success(`Deleted ${name}`);
					void goto(routes.stacks());
				} else {
					toast.error(`${name} was not deleted`, {
						body: j.error?.recovery ?? j.error?.message
					});
				}
			}
		});
		watcher.start();
		tray.add(job, {
			title: `Delete ${title}`,
			success: `Deleted ${title}`,
			failure: `${title} was not deleted`,
			silent: true
		});
	}

	const deployItems = $derived.by((): MenuEntry[] => {
		const items: MenuEntry[] = [
			{ label: 'Deploy', icon: Download, onSelect: () => deploy('deploy') },
			{ label: 'Deploy with pull', icon: CircleArrowDown, onSelect: () => deploy('pull') }
		];
		if (hasBuild)
			items.push({
				label: 'Build and deploy',
				icon: Hammer,
				onSelect: () => deploy('build')
			});
		return items;
	});

	const overflow = $derived.by((): MenuEntry[] => {
		const items: MenuEntry[] = [];
		if (can('stack.start') && !stoppedLike && current !== 'running')
			items.push({
				label: 'Start',
				icon: Play,
				onSelect: () => ask('start'),
				disabled: offline
			});
		if (can('stack.down'))
			items.push({
				label: 'Take down',
				icon: CircleArrowDown,
				onSelect: () => ask('down'),
				disabled: offline
			});
		if (can('stack.migrate'))
			items.push({
				label: 'Migrate',
				icon: ArrowRightLeft,
				href: routes.stack(stack.id, 'migrate')
			});
		if (can('stack.manage') && stack.revision !== undefined)
			items.push({ label: 'Edit details', icon: Pencil, onSelect: () => (editing = true) });
		if (can('stack.remove')) {
			if (items.length) items.push({ separator: true });
			items.push({
				label: 'Delete',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleting = true),
				disabled: offline
			});
		}
		return items;
	});

	const canUpdate = $derived(full && can('update.check'));
</script>

<PageHeader
	{title}
	description={stack.description || undefined}
	icon={serviceIcon(icon.icon)}
	color={icon.color}
	{meta}
>
	{#snippet status()}
		<StatusBadge status={stackStatus(stack)} />
		{#if offline}<Badge tone="offline" dot>Read-only while {envName} is offline</Badge>{/if}
	{/snippet}
	{#snippet actions()}
		{#if can('stack.deploy')}
			<SplitButton
				label="Deploy"
				icon={Download}
				menuLabel="More deploy options"
				loading={starting !== null}
				disabled={offline}
				onclick={() => deploy('deploy')}
				items={deployItems}
			/>
		{/if}
		{#if can('stack.restart') && !stoppedLike}
			<Button icon={RotateCw} disabled={offline} onclick={() => ask('restart')}
				>Restart</Button
			>
		{/if}
		{#if stoppedLike && can('stack.start') && current !== 'undeployed' && current !== 'down'}
			<Button icon={Play} disabled={offline} onclick={() => ask('start')}>Start</Button>
		{:else if can('stack.stop') && !stoppedLike}
			<Button
				variant="danger-soft"
				icon={Square}
				disabled={offline}
				onclick={() => ask('stop')}>Stop</Button
			>
		{/if}
		{#if canUpdate}
			<Button icon={RefreshCw} disabled={offline} onclick={() => (updating = true)}>
				Update
				{#if updateDot}<span class="update-dot" aria-hidden="true"></span><span
						class="sr-only">(update available)</span
					>{/if}
			</Button>
		{/if}
		{#if overflow.length}
			<Menu label="More stack actions" items={overflow} align="end">
				{#snippet trigger(props)}<IconButton
						{...props}
						variant="secondary"
						label="More stack actions"
						icon={EllipsisVertical}
					/>{/snippet}
			</Menu>
		{/if}
	{/snippet}
</PageHeader>

<ConfirmDialog
	bind:open={confirming}
	title="{OPS[pending].verb} {title}?"
	consequences={consequences[pending]}
	confirmLabel={OPS[pending].label}
	tone={pending === 'stop' || pending === 'down' ? 'danger' : 'default'}
	onconfirm={() => operate(pending)}
/>

<DestructiveConfirm
	bind:open={deleting}
	title="Delete {title}?"
	consequences={[
		`Takes ${title} down: removes ${containerWord(counts.containers)} and its networks.`,
		'Keeps its volumes and the project directory on the host.',
		'Removes the stack from DockYard with its revision history and the permission rules naming it.'
	]}
	affected={(stack.engine?.services ?? []).map((s) => ({
		label: s.service,
		detail: `${containerWord(s.containers)}`
	}))}
	confirmText={stack.name}
	confirmLabel="Delete stack"
	onconfirm={remove}
/>

{#if editing}
	<EditDetailsDialog {stack} onclose={() => (editing = false)} />
{/if}

{#if canUpdate}
	<UpdateDrawer bind:open={updating} {stack} {tray} />
{/if}

<style>
	.update-dot {
		display: inline-block;
		width: 7px;
		height: 7px;
		margin-left: 2px;
		border-radius: var(--radius-full);
		background: var(--warn);
	}
</style>
