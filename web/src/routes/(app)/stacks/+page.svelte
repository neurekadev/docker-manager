<script lang="ts">
	// Stacks (#22, #7): the Compose stacks of the selected environment, or
	// of every visible one with an environment column (hidden while the
	// caller sees one environment). Status is the live Engine state Docker
	// Manager last observed; "Undeployed Changes" and the update dot say
	// what needs attention. Searched by name or description and filtered by
	// status, changes and environment (ListCard, kept per list and browser
	// tab). The whole row opens the stack; its menu deploys, restarts, stops
	// (after a confirmation; a down with stack.down, else a plain stop) or
	// opens the logs, each with its capability.
	// Each row shows the stack tile, or the image of the template the stack
	// was created from. Create and import are shown only with stack.create /
	// stack.import (the server still decides). The Create Stack button's
	// menu creates a stack from a template. A stack's running job (started
	// here, on its page or elsewhere) shows in its Status cell, found in the
	// running list (one match for the whole list), so it stays after a
	// reload (docs/internal/web.md, "Job progress after reload").
	import { goto } from '$app/navigation';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import FolderSearch from '@lucide/svelte/icons/folder-search';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Play from '@lucide/svelte/icons/play';
	import Plus from '@lucide/svelte/icons/plus';
	import Rocket from '@lucide/svelte/icons/rocket';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import ScrollText from '@lucide/svelte/icons/scroll-text';
	import Square from '@lucide/svelte/icons/square';
	import SquareArrowOutUpRight from '@lucide/svelte/icons/square-arrow-out-up-right';
	import type { Job } from '$lib/api/client';
	import { JobWatcher } from '$lib/api/jobs.svelte';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import Page from '$lib/features/common/Page.svelte';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import {
		deployStackWith,
		operateStack,
		type StackOperation
	} from '$lib/features/stacks/actions';
	import {
		canAnywhere,
		canInEnvironment,
		serviceCounts,
		stackStatus,
		stackStopAction,
		stackTitle,
		stopConsequences
	} from '$lib/features/stacks/model';
	import { stackJobGuidance } from '$lib/features/stacks/rename';
	import {
		restoringStacks,
		runningByStack,
		stackListMatch
	} from '$lib/features/stacks/list-jobs';
	import StackJobStatus from '$lib/features/stacks/StackJobStatus.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import StackIcon from '$lib/features/stacks/StackIcon.svelte';
	import CreateFromTemplateDialog from '$lib/features/templates/CreateFromTemplateDialog.svelte';
	import {
		stackKeys,
		stacksQuery,
		updatePoliciesQuery,
		type Stack
	} from '$lib/features/stacks/queries';
	import { stackFilters, stackSearch } from '$lib/features/stacks/filters';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering, listSummary } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import CreateStackDialog from '$lib/features/stacks/CreateStackDialog.svelte';
	import ImportStackDialog from '$lib/features/stacks/ImportStackDialog.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import UpdateStatusBadge from '$lib/features/updates/UpdateStatusBadge.svelte';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		ConfirmDialog,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		PageHeader,
		Skeleton,
		SplitButton,
		StatusBadge,
		Table,
		errorMessage,
		formatDateTime,
		formatRelative,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';

	usePage({ title: 'Stacks', crumbs: [{ label: 'Stacks' }], environmentScoped: true });

	const envId = $derived(environmentSelection.id);
	const stacks = createQuery(() => stacksQuery(envId));
	const envs = createQuery(() => environmentsQuery());
	const single = singleEnvironment();
	const queryClient = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const policies = createQuery(() => updatePoliciesQuery(envId));
	// routes.newStack() opens the create dialog over this list.
	const createDialog = urlDialog('create', ['environment']);
	// routes.importStack() opens the import dialog (discovered projects).
	const importDialog = urlDialog('import', ['environment']);
	// routes.stackFromTemplate() opens the template dialog.
	const templateDialog = urlDialog('fromTemplate', ['template', 'registry', 'environment']);

	// The stacks' running jobs: started here (shown at once) or in the
	// running list.
	const stackJobs = useTrackedJobs(() => stackListMatch(envId));
	const runningOf = $derived(runningByStack(stackJobs.entries));
	const restoring = $derived(restoringStacks(stackJobs.entries));

	const envById = $derived(new Map((envs.data ?? []).map((e) => [e.id, e])));
	const envName = $derived(envId ? (envById.get(envId)?.name ?? 'this environment') : null);
	const updateStates = $derived(
		new Map(
			(policies.data ?? [])
				.filter((p) => p.target.type === 'stack')
				.map((p) => {
					const s = p.summary;
					const status =
						(s?.available ?? 0) > 0
							? 'update_available'
							: (s?.upToDate ?? 0) > 0 &&
								  !(s?.failed || s?.unchecked || s?.quarantined)
								? 'up_to_date'
								: undefined;
					return [p.target.id, status] as const;
				})
		)
	);
	const canCreate = $derived(
		envId
			? canInEnvironment(perms.data, 'stack.create', envId)
			: canAnywhere(perms.data, 'stack.create')
	);
	const canImport = $derived(
		envId
			? canInEnvironment(perms.data, 'stack.import', envId)
			: canAnywhere(perms.data, 'stack.import')
	);

	const filters = new ListFilters('stacks');
	const all = $derived(stacks.data ?? []);
	const defs = $derived(
		stackFilters({
			envs:
				envId || single.current
					? []
					: [...new Set(all.map((s) => s.environmentId))].map((id) => ({
							id,
							name: envById.get(id)?.name ?? id
						})),
			updates: updateStates
		})
	);
	const rows = $derived(applyListFilters(all, defs, filters.state, stackSearch));
	const filtered = $derived(isFiltering(defs, filters.state));

	const columns = $derived.by((): Column<Stack>[] => {
		const cols: Column<Stack>[] = [
			{
				id: 'name',
				header: 'Name',
				cell: nameCell,
				sortValue: (s) => stackTitle(s),
				stack: 'title'
			},
			{
				id: 'status',
				header: 'Status',
				cell: statusCell,
				sortValue: (s) => stackStatus(s),
				stack: 'status',
				width: '160px'
			}
		];
		if (!envId && !single.current)
			cols.push({
				id: 'environment',
				header: 'Environment',
				cell: envCell,
				sortValue: (s) => envById.get(s.environmentId)?.name ?? '',
				width: '160px'
			});
		cols.push(
			{
				id: 'services',
				header: 'Services Running',
				cell: servicesCell,
				sortValue: (s) => serviceCounts(s).servicesRunning,
				numeric: true,
				width: '140px'
			},
			{ id: 'attention', header: 'Changes', cell: attentionCell, width: '220px' },
			{
				id: 'deployed',
				header: 'Last Deploy',
				cell: deployedCell,
				sortValue: (s) => s.appliedRevision?.at ?? '',
				width: '140px'
			},
			{
				id: 'actions',
				header: 'Actions',
				hideHeader: true,
				cell: actionsCell,
				stack: 'head',
				align: 'end',
				width: '56px',
				pin: 'end'
			}
		);
		return cols;
	});

	// Row actions: Deploy, Start and Restart run at once; Stop confirms.
	const VERBS: Record<StackOperation, [string, string]> = {
		start: ['Started', 'started'],
		stop: ['Stopped', 'stopped'],
		restart: ['Restarted', 'restarted'],
		// Stop with stack.down: the user sees a stop.
		down: ['Stopped', 'stopped']
	};

	/**
	 * Shows a job started from the list in its row at once and reports its
	 * end (toast, refresh).
	 */
	function follow(job: Job, done: string, failed: string) {
		stackJobs.add(job);
		const w = new JobWatcher(job.id, {
			onfinish: (j) => {
				w.stop();
				stackJobs.markFinished(j);
				void queryClient.invalidateQueries({ queryKey: stackKeys.all });
				if (j.state === 'succeeded') toast.success(done);
				else if (j.state === 'cancelled') toast.info(`${failed}: the job was cancelled`);
				else
					toast.error(failed, {
						body: stackJobGuidance(j.error),
						action: { label: 'Open Job', onclick: () => void goto(routes.job(j.id)) }
					});
			}
		});
		w.start();
	}

	async function deploy(s: Stack) {
		const t = stackTitle(s);
		try {
			follow(await deployStackWith(s.id, {}), `Deployed ${t}`, `${t} was not deployed`);
		} catch (e) {
			toast.error(`${t} was not deployed`, { body: errorMessage(e) });
		}
	}

	async function operate(s: Stack, action: StackOperation) {
		const t = stackTitle(s);
		const [done, failed] = VERBS[action];
		follow(await operateStack(s.id, action), `${done} ${t}`, `${t} was not ${failed}`);
	}

	async function operateNow(s: Stack, action: 'start' | 'restart') {
		try {
			await operate(s, action);
		} catch (e) {
			toast.error(`${stackTitle(s)} was not ${VERBS[action][1]}`, { body: errorMessage(e) });
		}
	}

	let stopping = $state<Stack | null>(null);
	let stopOpen = $state(false);
	const stopAction = $derived(stopping ? stackStopAction(stopping.actions) : undefined);

	function rowMenu(s: Stack): MenuEntry[] {
		const can = (a: string) => s.actions.includes(a);
		const t = stackTitle(s);
		const offline = !!s.readOnly || s.environmentOnline === false;
		const st = stackStatus(s);
		const stopped = ['stopped', 'down', 'missing', 'undeployed'].includes(st);
		const stopAs = stackStopAction(s.actions);
		// A restore starts what ran before itself: no start, restart or stop
		// meanwhile (a Stop's down would remove those containers).
		const lifecycle = !restoring.has(s.id);
		const items: MenuEntry[] = [
			{ label: `Open ${t}`, icon: SquareArrowOutUpRight, href: routes.stack(s.id) }
		];
		if (can('stack.deploy'))
			items.push({
				label: 'Deploy',
				icon: Rocket,
				disabled: offline,
				onSelect: () => void deploy(s)
			});
		// Start, Restart, Stop: the order of the header's lifecycle menu.
		// Start also starts the rest of a partially running stack.
		if (lifecycle && can('stack.start') && (st === 'stopped' || (!stopped && st !== 'running')))
			items.push({
				label: 'Start',
				icon: Play,
				disabled: offline,
				onSelect: () => void operateNow(s, 'start')
			});
		if (lifecycle && can('stack.restart') && !stopped)
			items.push({
				label: 'Restart',
				icon: RotateCw,
				disabled: offline || !!s.protection,
				onSelect: () => void operateNow(s, 'restart')
			});
		// A stop that takes the stack down also removes a stopped stack's containers.
		if (lifecycle && stopAs && (!stopped || (stopAs === 'down' && st === 'stopped')))
			items.push({
				label: 'Stop…',
				icon: Square,
				tone: 'danger',
				disabled: offline || !!s.protection,
				onSelect: () => {
					stopping = s;
					stopOpen = true;
				}
			});
		if (can('container.logs.read'))
			items.push({ label: 'Logs', icon: ScrollText, href: routes.stack(s.id, 'logs') });
		return items;
	}
</script>

{#snippet nameCell(s: Stack)}
	<a class="name" href={routes.stack(s.id)}>
		<StackIcon stack={s} size="xs" />
		<span class="text">
			<span class="title">{stackTitle(s)}</span>
			{#if s.description}<span class="desc">{s.description}</span
				>{:else if s.displayName}<span class="desc mono">{s.name}</span>{/if}
		</span>
	</a>
{/snippet}
{#snippet statusCell(s: Stack)}
	{@const job = runningOf.get(s.id)}
	{#if job}<StackJobStatus {job} />{:else}<StatusBadge status={stackStatus(s)} />{/if}
{/snippet}
{#snippet envCell(s: Stack)}
	{@const e = envById.get(s.environmentId)}
	<span class="env">
		{e?.name ?? '—'}
		{#if e && !e.online}<Badge tone="offline" dot>Offline</Badge>{/if}
	</span>
{/snippet}
{#snippet servicesCell(s: Stack)}
	{#if s.view === 'full'}
		{@const c = serviceCounts(s)}
		<span class="num">{c.servicesRunning} / {c.services}</span>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet attentionCell(s: Stack)}
	<span class="chips">
		{#if s.undeployedChanges}
			<a class="chip" href={routes.stack(s.id, 'revisions')}
				><Badge tone="warn" dot>Undeployed Changes</Badge></a
			>
		{/if}
		<UpdateStatusBadge status={updateStates.get(s.id)} />
		{#if !s.undeployedChanges && !updateStates.get(s.id)}<span class="muted">—</span>{/if}
	</span>
{/snippet}
{#snippet deployedCell(s: Stack)}
	{#if s.appliedRevision?.at}<time
			datetime={s.appliedRevision.at}
			title={formatDateTime(s.appliedRevision.at)}
			>{formatRelative(s.appliedRevision.at)}</time
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet actionsCell(s: Stack)}
	<span class="row-actions">
		<Menu items={rowMenu(s)} label="Actions for {stackTitle(s)}" align="end">
			{#snippet trigger(props)}<IconButton
					{...props}
					size="sm"
					variant="ghost"
					label="More Actions for {stackTitle(s)}"
					icon={EllipsisVertical}
				/>{/snippet}
		</Menu>
	</span>
{/snippet}

<Page>
	<PageHeader title="Stacks">
		{#snippet actions()}
			{#if canImport}
				<Button icon={FolderSearch} onclick={() => (importDialog.open = true)}
					>Import Project</Button
				>
			{/if}
			{#if canCreate}
				<SplitButton
					label="Create Stack"
					icon={Plus}
					menuLabel="More Ways to Create a Stack"
					onclick={() => (createDialog.open = true)}
					items={[
						{
							label: 'Create Stack From Template',
							icon: LayoutTemplate,
							onSelect: () => (templateDialog.open = true)
						}
					]}
				/>
			{/if}
		{/snippet}
	</PageHeader>

	{#if stacks.isError}
		<ErrorState
			error={stacks.error}
			title="The stacks could not be loaded."
			onretry={() => stacks.refetch()}
		/>
	{:else}
		<ListCard
			title="All Stacks"
			id="stacks"
			summary={stacks.data
				? listSummary(rows.length, all.length, filtered, 'stack', 'stacks')
				: undefined}
			label="Filter Stacks"
			searchLabel="Search Stacks"
			placeholder="Search stacks"
			filters={defs}
			store={filters}
		>
			{#if stacks.isPending}
				<div class="loading" aria-busy="true"><Skeleton lines={5} height="20px" /></div>
			{:else}
				<div class="rows">
					<Table
						label="Stacks"
						{rows}
						{columns}
						rowKey={(s) => s.id}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="stacks"
									icon={resourceIcon('stack').icon}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									{...resourceIcon('stack')}
									title={envName
										? `No stacks on ${envName} yet.`
										: 'No stacks yet.'}
									description={canCreate || canImport
										? 'Create a stack or import an existing Compose project.'
										: 'Stacks you are given access to appear here.'}
									level={3}
									compact
								>
									{#snippet actions()}
										{#if canCreate}<Button
												variant="primary"
												onclick={() => (createDialog.open = true)}
												>Create Stack</Button
											><Button onclick={() => (templateDialog.open = true)}
												>Create From Template</Button
											>{/if}
										{#if canImport}<Button
												onclick={() => (importDialog.open = true)}
												>Import Project</Button
											>{/if}
									{/snippet}
								</EmptyState>
							{/if}
						{/snippet}
					</Table>
				</div>
			{/if}
		</ListCard>
	{/if}

	{#if stopping}
		<ConfirmDialog
			bind:open={stopOpen}
			title="Stop {stackTitle(stopping)}?"
			consequences={stopConsequences(
				`the containers of ${stackTitle(stopping)}`,
				stopAction === 'down'
			)}
			confirmLabel="Stop"
			tone="danger"
			onconfirm={() => operate(stopping!, stopAction ?? 'stop')}
		/>
	{/if}

	<CreateStackDialog
		bind:open={createDialog.open}
		environmentId={createDialog.param('environment') ?? envId}
	/>
	<ImportStackDialog
		bind:open={importDialog.open}
		environmentId={importDialog.param('environment') ?? envId}
	/>
	<CreateFromTemplateDialog
		bind:open={templateDialog.open}
		environmentId={templateDialog.param('environment') ?? envId}
		templateId={templateDialog.param('template')}
		registry={templateDialog.param('registry')}
	/>
</Page>

<style>
	.loading {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.name {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
		color: inherit;
		text-decoration: none;
	}

	.name:hover .title {
		color: var(--accent-text);
	}

	/* The whole row opens the stack: the name link's hit area covers it;
	   links and buttons in other cells stay above it. */
	.rows :global(tbody tr),
	.rows :global(li.card) {
		position: relative;
	}

	.name::after {
		position: absolute;
		inset: 0;
		content: '';
	}

	.chips a,
	.chips :global(button),
	.row-actions {
		position: relative;
		z-index: 2;
	}

	.row-actions {
		display: inline-flex;
	}

	.text {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.desc {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.env,
	.chips {
		display: inline-flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.chip {
		display: inline-flex;
		text-decoration: none;
	}
</style>
