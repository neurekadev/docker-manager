<script lang="ts">
	// Stack header (#22 mockup): icon tile, name, status, description, meta
	// row (services, containers, created, template, logical location: the
	// host path is its tooltip and copy button; each item stays on one line),
	// the stack's links below it (full view, when it has any), the rename
	// pencil right of the name (the name turns into a field in place:
	// RenameStackInline, which renames at once) and the actions: the Deploy
	// split button (the one primary: Deploy, Build & Deploy for stacks that
	// build an image, Pull & Deploy — which says when newer images are
	// available — and Cleanup Orphans & Deploy), the lifecycle split button
	// (LifecycleButton: Stop while anything runs, Start when stopped; its
	// menu has Start, Restart and Stop) and overflow (Migrate with more than
	// one environment, Edit details, Save as template, Delete). Each action
	// is shown only with its capability (the server still decides). Start
	// and Restart run at once; Stop, Delete and Cleanup Orphans & Deploy
	// confirm with their exact consequences first. Docker Manager's own stack
	// (#32) deploys; Restart, Stop, Migrate, Rename and Delete stay visible
	// but disabled, with the reason. While a rename of the stack runs (the
	// tray's stack.rename job, or one in the stack's jobs after a reload)
	// every action is off, with the reason.
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left';
	import Clock from '@lucide/svelte/icons/clock';
	import Package from '@lucide/svelte/icons/package';
	import Workflow from '@lucide/svelte/icons/workflow';
	import Download from '@lucide/svelte/icons/download';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import Folder from '@lucide/svelte/icons/folder';
	import Hammer from '@lucide/svelte/icons/hammer';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Rocket from '@lucide/svelte/icons/rocket';
	import Eraser from '@lucide/svelte/icons/eraser';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import type { Environment } from '$lib/api/client';
	import { JobWatcher } from '$lib/api/jobs.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import LifecycleButton from '$lib/features/common/LifecycleButton.svelte';
	import type { LifecycleActions } from '$lib/features/common/lifecycle';
	import { routes } from '$lib/routes';
	import {
		Badge,
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
	import { RemoveOrphansRequest, startDeploy } from './deploy.svelte';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import RemoveOrphansDialog from './RemoveOrphansDialog.svelte';
	import EditDetailsDialog from './EditDetailsDialog.svelte';
	import RenameStackInline from './RenameStackInline.svelte';
	import {
		deployFailure,
		serviceCounts,
		stackStatus,
		stackTitle,
		updateAvailable,
		type DeployChoice
	} from './model';
	import { stackImageStatusQuery, stackJobsQuery, stackKeys, type Stack } from './queries';
	import { activeRename } from './rename';
	import { activeRestore } from '$lib/features/backups/restore';
	import type { JobTray } from './tray.svelte';
	import StackIcon from './StackIcon.svelte';
	import SaveAsTemplateDialog from '$lib/features/templates/SaveAsTemplateDialog.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import LinkList from '$lib/features/common/LinkList.svelte';

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
		'Docker Manager cannot stop, restart, migrate, rename or delete its own stack. Deploy works.';
	// A restore of the stack's data starts what was running itself; the
	// server refuses starts meanwhile (restore_in_progress), so hide them.
	const jobs = createQuery(() => stackJobsQuery(stack.id));
	const restoring = $derived(!!activeRestore(jobs.data));
	// A rename of the stack runs (started here, or found in its jobs after a
	// reload): every action is off until it ends.
	const renaming = $derived(!!activeRename(jobs.data) || tray.running('stack.rename'));
	const renamingReason = $derived(`Renaming ${title}…`);
	const counts = $derived(serviceCounts(stack));
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

	// Only Stop confirms (it ends what runs); Start and Restart run at once,
	// like a container's.
	let confirming = $state(false);
	let operating = $state<'start' | 'restart' | null>(null);
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
	// The name is being edited in place (RenameStackInline).
	let editingName = $state(false);
	let savingTemplate = $state(false);
	let starting = $state<'deploy' | 'build' | 'pull' | null>(null);

	const OPS: Record<
		Exclude<StackOperation, 'down'>,
		{ done: string; failure: string; title: string }
	> = {
		start: { done: 'Started', failure: 'started', title: 'Start' },
		stop: { done: 'Stopped', failure: 'stopped', title: 'Stop' },
		restart: { done: 'Restarted', failure: 'restarted', title: 'Restart' }
	};

	const containerWord = (n: number) => `${n} ${n === 1 ? 'container' : 'containers'}`;
	const stopConsequences = $derived([
		`Stops ${containerWord(counts.containersRunning)}, the services that need others first.`,
		'Containers, volumes and files are kept; Start brings them back.'
	]);

	// Start, Restart and Stop as one split button: Stop while anything runs
	// (a partially running stack too: Start in its menu starts the rest),
	// Start when stopped. A restore hides Start and Restart.
	const lifecycle = $derived.by((): LifecycleActions => {
		const out: LifecycleActions = {};
		const locked = protectedStack ? selfReason : undefined;
		if (can('stack.start') && !restoring)
			out.start = { run: () => void runNow('start'), disabled: current === 'running' };
		if (can('stack.restart') && !restoring)
			out.restart = {
				run: () => void runNow('restart'),
				disabled: current === 'stopped' || protectedStack,
				reason: locked
			};
		if (can('stack.stop'))
			out.stop = {
				run: () => (confirming = true),
				disabled: current === 'stopped' || protectedStack,
				reason: locked
			};
		return out;
	});

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

	async function operate(action: Exclude<StackOperation, 'down'>) {
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
				label: 'Build & Deploy',
				icon: Hammer,
				onSelect: () => deploy({ build: true })
			});
		// Pulls every image first, then deploys (what the former Update did).
		items.push({
			label: 'Pull & Deploy',
			icon: Download,
			description: updateDot ? 'Newer images are available' : undefined,
			onSelect: () => deploy({ pull: true })
		});
		items.push({
			label: 'Cleanup Orphans & Deploy',
			icon: Eraser,
			onSelect: () => removeOrphans.request()
		});
		return items;
	});

	const overflow = $derived.by((): MenuEntry[] => {
		const items: MenuEntry[] = [];
		if (can('stack.migrate') && !single.current)
			items.push({
				label: 'Migrate',
				icon: ArrowRightLeft,
				href: protectedStack || renaming ? undefined : routes.stack(stack.id, 'migrate'),
				disabled: protectedStack || renaming
			});
		if (can('stack.manage') && stack.revision !== undefined)
			items.push({
				label: 'Edit details',
				icon: Pencil,
				onSelect: () => (editing = true),
				disabled: renaming
			});
		if (can('stack.files.download') && can('stack.definition.read'))
			items.push({
				label: 'Save as template',
				icon: LayoutTemplate,
				onSelect: () => (savingTemplate = true),
				disabled: offline || renaming
			});
		if (can('stack.remove')) {
			if (items.length) items.push({ separator: true });
			items.push({
				label: 'Delete',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleting = true),
				disabled: offline || protectedStack || renaming
			});
		}
		// While a rename runs every entry is off; the reason heads the menu.
		if (renaming && items.length) items.unshift({ heading: renamingReason });
		return items;
	});

	// The pencil right of the name: a rename needs the stack's revision
	// (If-Match); it is off while offline, for Docker Manager's own stack
	// and while a rename runs, with the reason as its tooltip.
	const canRenameStack = $derived(can('stack.rename') && stack.revision !== undefined);
	const renameOff = $derived.by((): string | undefined => {
		if (protectedStack) return selfReason;
		if (offline) return `Read-only while ${envName} is offline`;
		if (renaming) return renamingReason;
		return undefined;
	});
	$effect(() => {
		if (renameOff || !showActions) editingName = false;
	});
</script>

{#snippet links()}<LinkList links={stack.links} label="Links of {title}" />{/snippet}

{#snippet renamePencil()}
	<IconButton
		size="sm"
		icon={Pencil}
		label="Rename {title}"
		tooltip={!renameOff}
		title={renameOff}
		disabled={!!renameOff}
		onclick={() => (editingName = true)}
	/>
{/snippet}

{#snippet nameEditor()}
	<RenameStackInline {stack} {tray} onclose={() => (editingName = false)} />
{/snippet}

<PageHeader
	{title}
	description={stack.description || undefined}
	{...resourceIcon('stack')}
	{meta}
	below={full && stack.links?.length ? links : undefined}
	titleAction={canRenameStack && showActions ? renamePencil : undefined}
	titleEditor={editingName ? nameEditor : undefined}
>
	{#snippet media()}<StackIcon {stack} size="lg" />{/snippet}
	{#snippet status()}
		<StatusBadge status={stackStatus(stack)} />
		{#if offline}<Badge tone="offline" dot>Read-only while {envName} is offline</Badge>{/if}
		{#if stack.protection}<ProtectionBadge protection={stack.protection} />{/if}
		{#if restoring}<Badge tone="warn" dot>Restoring from a backup</Badge>{/if}
		{#if renaming}<Badge tone="warn" dot>{renamingReason}</Badge>{/if}
	{/snippet}
	{#snippet actions()}
		{#if showActions}
			{#if can('stack.deploy') && !restoring}
				<SplitButton
					label="Deploy"
					icon={Rocket}
					menuLabel={updateDot
						? 'More deploy options (newer images are available)'
						: 'More deploy options'}
					loading={starting !== null}
					disabled={offline || renaming}
					title={renaming ? renamingReason : undefined}
					onclick={() => deploy({})}
					items={deployItems}
				/>
			{/if}
			<!-- A stopped stack starts; a down, missing or undeployed one deploys. -->
			{#if !stoppedLike || current === 'stopped'}
				<LifecycleButton
					running={current !== 'stopped'}
					actions={lifecycle}
					busy={operating}
					disabled={offline || renaming}
					reason={renaming ? renamingReason : undefined}
				/>
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
	title="Stop {title}?"
	consequences={stopConsequences}
	confirmLabel="Stop"
	tone="danger"
	onconfirm={() => operate('stop')}
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

{#if can('stack.files.download') && can('stack.definition.read')}
	<SaveAsTemplateDialog bind:open={savingTemplate} {stack} />
{/if}
