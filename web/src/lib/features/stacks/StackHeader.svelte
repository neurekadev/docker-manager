<script lang="ts">
	// Stack header (#22 mockup): icon tile, name, status, description, meta
	// row (services, containers, created, template, logical location: the
	// host path is its tooltip and copy button; each item stays on one line)
	// and the actions: the Deploy split button (the one primary:
	// Deploy, Build and deploy, Pull images only, Deploy and remove orphaned
	// containers), Restart, Stop (Start when stopped), Update with its
	// "update available" dot (a confirmation naming the newer images, then
	// one deploy that pulls them), and overflow (Take down, Migrate with more
	// than one environment, Rename, Edit details, Save as template, Delete).
	// Each action is shown only with its capability (the server still
	// decides). Start and Restart run at once; Stop, Take down and Delete
	// confirm with their exact consequences first. Docker Manager's own
	// stack (#32) deploys and updates; Restart, Stop, Take down, Migrate,
	// Rename and Delete stay visible but disabled, with the reason.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import CircleArrowUp from '@lucide/svelte/icons/circle-arrow-up';
	import Clock from '@lucide/svelte/icons/clock';
	import Package from '@lucide/svelte/icons/package';
	import Workflow from '@lucide/svelte/icons/workflow';
	import Download from '@lucide/svelte/icons/download';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import Folder from '@lucide/svelte/icons/folder';
	import Hammer from '@lucide/svelte/icons/hammer';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import PowerOff from '@lucide/svelte/icons/power-off';
	import Rocket from '@lucide/svelte/icons/rocket';
	import Eraser from '@lucide/svelte/icons/eraser';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Square from '@lucide/svelte/icons/square';
	import TextCursorInput from '@lucide/svelte/icons/text-cursor-input';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import type { Environment } from '$lib/api/client';
	import { JobWatcher } from '$lib/api/jobs.svelte';
	import { serviceIcon } from '$lib/design/icons';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Checkbox,
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
	import { deleteStack, operateStack, volumeResults, type StackOperation } from './actions';
	import { RemoveOrphansRequest, startDeploy, startPull } from './deploy.svelte';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import RemoveOrphansDialog from './RemoveOrphansDialog.svelte';
	import EditDetailsDialog from './EditDetailsDialog.svelte';
	import RenameStackDialog from './RenameStackDialog.svelte';
	import {
		deployFailure,
		serviceCounts,
		stackIcon,
		stackStatus,
		stackTitle,
		updateAvailable,
		type DeployChoice
	} from './model';
	import { stackImageStatusQuery, stackJobsQuery, stackKeys, type Stack } from './queries';
	import { activeRestore } from '$lib/features/backups/restore';
	import type { JobTray } from './tray.svelte';
	import UpdateDialog from './UpdateDialog.svelte';
	import StackIcon from './StackIcon.svelte';
	import SaveAsTemplateDialog from '$lib/features/templates/SaveAsTemplateDialog.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';

	interface Props {
		stack: Stack;
		environment?: Environment;
		tray: JobTray;
		/** The "deploy and remove orphans" confirmation (shared with the drift notice). */
		removeOrphans?: RemoveOrphansRequest;
		/** Show the actions (hidden while the migration wizard is open). */
		showActions?: boolean;
		now?: Date;
	}

	let {
		stack,
		environment,
		tray,
		removeOrphans = new RemoveOrphansRequest(),
		showActions = true,
		now = new Date()
	}: Props = $props();

	const queryClient = useQueryClient();
	const title = $derived(stackTitle(stack));
	const can = (a: string) => stack.actions.includes(a);
	const full = $derived(stack.view === 'full');
	const offline = $derived(stack.readOnly || stack.environmentOnline === false);
	// Docker Manager's own stack: the server refuses what would stop or
	// delete Docker Manager.
	const protectedStack = $derived(!!stack.protection);
	const selfReason =
		'Docker Manager cannot stop, take down, migrate, rename or delete its own stack. Deploy and Update work.';
	// A restore of the stack's data starts what was running itself; the
	// server refuses starts meanwhile (restore_in_progress), so hide them.
	const jobs = createQuery(() => stackJobsQuery(stack.id));
	const restoring = $derived(!!activeRestore(jobs.data));
	const counts = $derived(serviceCounts(stack));
	const icon = $derived(stackIcon(stack));
	const envName = $derived(environment?.name ?? 'Unknown environment');
	// With one environment there is nowhere to migrate to and no need to name it.
	const single = singleEnvironment();
	const current = $derived(stackStatus(stack));
	const stoppedLike = $derived(['stopped', 'down', 'missing', 'undeployed'].includes(current));
	const hasBuild = $derived((stack.services ?? []).some((s) => s.build));

	const images = createQuery(() => ({
		...stackImageStatusQuery(stack.id),
		enabled: full
	}));
	const updateDot = $derived(updateAvailable(images.data));

	const place = (dir: string) => (single.current ? dir : `${envName} · ${dir}`);
	const meta = $derived.by((): MetaItem[] => {
		if (!full) return [{ icon: Folder, label: place(stack.name) }];
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
		if (stack.template)
			out.push({
				icon: LayoutTemplate,
				label: `From template ${stack.template.name} ${stack.template.versionLabel}`
			});
		const host = stack.location?.hostPath;
		out.push({
			icon: Folder,
			label: place(stack.location?.dir ?? stack.name),
			title: host,
			copy: host ? { value: host, what: 'host path' } : undefined
		});
		return out;
	});

	// Dialog state: only Stop and Take down confirm (they end what runs);
	// Start and Restart run at once, like a container's.
	let pending = $state<'stop' | 'down'>('stop');
	let confirming = $state(false);
	function ask(action: 'stop' | 'down') {
		pending = action;
		confirming = true;
	}
	let operating = $state<StackOperation | null>(null);
	async function runNow(action: 'start' | 'restart') {
		if (operating) return;
		operating = action;
		try {
			await operate(action);
		} catch (e) {
			toast.error(`${title} was not ${OPS[action].failure}`, { body: errorMessage(e) });
		} finally {
			operating = null;
		}
	}
	let deleting = $state(false);
	// Unchecked every time the dialog opens: volumes are kept by default.
	let removeVolumes = $state(false);
	$effect(() => {
		if (!deleting) removeVolumes = false;
	});
	let editing = $state(false);
	let renaming = $state(false);
	let savingTemplate = $state(false);
	let updating = $state(false);
	let starting = $state<'deploy' | 'build' | 'pull' | null>(null);

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
	const consequences = $derived.by((): Record<'stop' | 'down', string[]> => ({
		stop: [
			`Stops ${containerWord(counts.containersRunning)}, the services that need others first.`,
			'Containers, volumes and files are kept; Start brings them back.'
		],
		down: [
			`Removes ${containerWord(counts.containers)} and the stack's networks.`,
			'Volumes and the project directory are kept; Deploy creates the containers again.'
		]
	}));

	async function deploy(choice: DeployChoice) {
		if (starting) return;
		starting = choice.build ? 'build' : 'deploy';
		try {
			await startDeploy(stack, choice, tray, queryClient);
		} catch (e) {
			toast.error(deployFailure(title, choice), { body: errorMessage(e) });
		} finally {
			starting = null;
		}
	}

	// Pull downloads the images only; nothing is recreated until a deploy.
	async function pull() {
		if (starting) return;
		starting = 'pull';
		try {
			await startPull(
				stack,
				tray,
				queryClient,
				can('stack.deploy') ? () => void deploy({}) : undefined
			);
		} catch (e) {
			toast.error(`The images of ${title} were not pulled`, { body: errorMessage(e) });
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
		const withVolumes = removeVolumes;
		const job = await deleteStack(stack.id, { removeVolumes: withVolumes });
		const name = title;
		// The stack page goes away when the stack does (a live event can
		// arrive before the job's end), so a watcher of its own reports the
		// end and leaves the page; the tray only shows the progress.
		const watcher = new JobWatcher(job.id, {
			onfinish: (j) => {
				watcher.stop();
				void queryClient.invalidateQueries({ queryKey: stackKeys.all });
				if (j.state === 'succeeded') {
					const v = volumeResults(j.items);
					toast.success(
						withVolumes ? `Deleted ${name} and its volumes` : `Deleted ${name}`,
						{
							body: withVolumes
								? `${v.removed} ${v.removed === 1 ? 'volume' : 'volumes'} removed` +
									(v.kept
										? `; ${v.kept} kept because another container uses them or they are not the stack's own. The job lists why.`
										: '.')
								: undefined
						}
					);
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
		const items: MenuEntry[] = [{ label: 'Deploy', icon: Rocket, onSelect: () => deploy({}) }];
		if (hasBuild)
			items.push({
				label: 'Build and deploy',
				icon: Hammer,
				onSelect: () => deploy({ build: true })
			});
		// Pull downloads the images only: nothing is recreated until a deploy.
		if (can('stack.update'))
			items.push({ label: 'Pull images only', icon: Download, onSelect: () => void pull() });
		items.push({
			label: 'Deploy and remove orphaned containers…',
			icon: Eraser,
			onSelect: () => removeOrphans.request()
		});
		return items;
	});

	const overflow = $derived.by((): MenuEntry[] => {
		const items: MenuEntry[] = [];
		if (can('stack.start') && !stoppedLike && current !== 'running' && !restoring)
			items.push({
				label: 'Start',
				icon: Play,
				onSelect: () => void runNow('start'),
				disabled: offline
			});
		if (can('stack.down'))
			items.push({
				label: 'Take down',
				icon: PowerOff,
				onSelect: () => ask('down'),
				disabled: offline || protectedStack
			});
		if (can('stack.migrate') && !single.current)
			items.push({
				label: 'Migrate',
				icon: ArrowRightLeft,
				href: protectedStack ? undefined : routes.stack(stack.id, 'migrate'),
				disabled: protectedStack
			});
		if (can('stack.rename') && stack.revision !== undefined)
			items.push({
				label: 'Rename',
				icon: TextCursorInput,
				onSelect: () => (renaming = true),
				disabled: offline || protectedStack
			});
		if (can('stack.manage') && stack.revision !== undefined)
			items.push({ label: 'Edit details', icon: Pencil, onSelect: () => (editing = true) });
		if (can('stack.files.download') && can('stack.definition.read'))
			items.push({
				label: 'Save as template',
				icon: LayoutTemplate,
				onSelect: () => (savingTemplate = true),
				disabled: offline
			});
		if (can('stack.remove')) {
			if (items.length) items.push({ separator: true });
			items.push({
				label: 'Delete',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleting = true),
				disabled: offline || protectedStack
			});
		}
		return items;
	});

	// Update is a deploy that pulls every image first.
	const canUpdate = $derived(full && can('stack.deploy') && !restoring);
</script>

<PageHeader
	{title}
	description={stack.description || undefined}
	icon={serviceIcon(icon.icon)}
	color={icon.color}
	{meta}
>
	{#snippet media()}<StackIcon {stack} size="lg" />{/snippet}
	{#snippet status()}
		<StatusBadge status={stackStatus(stack)} />
		{#if offline}<Badge tone="offline" dot>Read-only while {envName} is offline</Badge>{/if}
		{#if stack.protection}<ProtectionBadge protection={stack.protection} />{/if}
		{#if restoring}<Badge tone="warn" dot>Restoring from a backup</Badge>{/if}
	{/snippet}
	{#snippet actions()}
		{#if showActions}
			{#if can('stack.deploy') && !restoring}
				<SplitButton
					label="Deploy"
					icon={Rocket}
					menuLabel="More deploy options"
					loading={starting !== null}
					disabled={offline}
					onclick={() => deploy({})}
					items={deployItems}
				/>
			{/if}
			{#if can('stack.restart') && !stoppedLike && !restoring}
				<Button
					icon={RotateCw}
					loading={operating === 'restart'}
					disabled={offline || protectedStack || operating !== null}
					title={protectedStack ? selfReason : undefined}
					onclick={() => runNow('restart')}>Restart</Button
				>
			{/if}
			{#if current === 'stopped' && can('stack.start') && !restoring}
				<Button
					icon={Play}
					loading={operating === 'start'}
					disabled={offline || operating !== null}
					onclick={() => runNow('start')}>Start</Button
				>
			{:else if can('stack.stop') && !stoppedLike}
				<Button
					icon={Square}
					disabled={offline || protectedStack}
					title={protectedStack ? selfReason : undefined}
					onclick={() => ask('stop')}>Stop</Button
				>
			{/if}
			{#if canUpdate}
				<Button
					icon={CircleArrowUp}
					disabled={offline || starting !== null}
					title={updateDot ? 'Newer images are available' : undefined}
					onclick={() => (updating = true)}
				>
					Update
					{#if updateDot}<span class="update-dot" aria-hidden="true"></span><span
							class="sr-only">(update available)</span
						>{/if}
				</Button>
			{/if}
			{#if protectedStack}<span class="sr-only">{selfReason}</span>{/if}
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
		{/if}
	{/snippet}
</PageHeader>

<ConfirmDialog
	bind:open={confirming}
	title="{OPS[pending].verb} {title}?"
	consequences={consequences[pending]}
	confirmLabel={OPS[pending].label}
	tone="danger"
	onconfirm={() => operate(pending)}
/>

<DestructiveConfirm
	bind:open={deleting}
	title="Delete {title}?"
	consequences={[
		`Takes ${title} down: removes ${containerWord(counts.containers)} and its networks.`,
		removeVolumes
			? 'Removes the volumes the stack owns and all data in them. Keeps external volumes, other stacks’ volumes and volumes other containers use, and the project directory.'
			: 'Keeps its volumes and the project directory on the host.',
		'Removes the stack from Docker Manager with its revision history and the permission rules naming it.'
	]}
	affected={(stack.engine?.services ?? []).map((s) => ({
		label: s.service,
		detail: `${containerWord(s.containers)}`
	}))}
	confirmText={stack.name}
	confirmLabel={removeVolumes ? 'Delete stack and volumes' : 'Delete stack'}
	onconfirm={remove}
>
	{#snippet extra()}
		<Checkbox
			bind:checked={removeVolumes}
			label="Also remove the stack’s volumes"
			description="Only the volumes this stack created: the named volumes its Compose file declares (not external) and the anonymous volumes of its containers. Their data is deleted."
		/>
	{/snippet}
</DestructiveConfirm>

{#if can('stack.deploy')}
	<RemoveOrphansDialog request={removeOrphans} {stack} {tray} />
{/if}

{#if editing}
	<EditDetailsDialog {stack} onclose={() => (editing = false)} />
{/if}

{#if can('stack.rename')}
	<RenameStackDialog bind:open={renaming} {stack} {tray} />
{/if}

{#if can('stack.files.download') && can('stack.definition.read')}
	<SaveAsTemplateDialog bind:open={savingTemplate} {stack} />
{/if}

{#if canUpdate}
	<UpdateDialog bind:open={updating} {stack} images={images.data} {tray} />
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
