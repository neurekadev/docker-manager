<script lang="ts">
	// Revisions (#7, #25 Q1): every definition Docker Manager saw, newest first:
	// what was deployed, what the stack editor or file manager saved, edits
	// made on the host and restores. Runs of revisions with the same files
	// (the same fingerprint) show as one row. While the files on disk differ
	// from the deployed revision, their diff shows straight away with "Deploy
	// These Changes" and "Restore Deployed Revision"; otherwise compare any
	// two (never two with the same files by default) and restore one to
	// disk. A restore never deploys, it offers the deploy afterwards.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import GitCompare from '@lucide/svelte/icons/git-compare';
	import History from '@lucide/svelte/icons/history';
	import Rocket from '@lucide/svelte/icons/rocket';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import { sessionQuery } from '$lib/api/queries';
	import { restoreRevision } from '$lib/features/stacks/actions';
	import { useStackPage } from '$lib/features/stacks/context';
	import { startDeploy } from '$lib/features/stacks/deploy.svelte';
	import {
		compareRevisions,
		comparisonFor,
		defaultComparison,
		groupRevisions,
		revisionSource,
		shortHash,
		stackTitle,
		type RevisionGroup
	} from '$lib/features/stacks/model';
	import {
		stackKeys,
		stackRevisionQuery,
		stackRevisionsQuery,
		type StackRevision
	} from '$lib/features/stacks/queries';
	import { criticalWork } from '$lib/live';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		DiffView,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Notice,
		Select,
		Skeleton,
		Table,
		errorMessage,
		formatDateTime,
		formatRelative,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';

	const ctx = useStackPage();
	const stack = $derived(ctx.stack!);
	const title = $derived(stackTitle(stack));
	const queryClient = useQueryClient();
	const can = (a: string) => stack.actions.includes(a);
	const offline = $derived(stack.readOnly || stack.environmentOnline === false);

	const revisions = createQuery(() => ({
		...stackRevisionsQuery(ctx.id),
		enabled: can('stack.definition.read')
	}));
	const session = createQuery(() => sessionQuery());
	const me = $derived(session.data?.user?.id);
	const list = $derived(revisions.data ?? []);
	const groups = $derived(groupRevisions(list));
	const appliedId = $derived(stack.appliedRevision?.id);
	const diskId = $derived(stack.sourceRevision?.id);
	const failedId = $derived(stack.failedRevision?.id);
	const applied = $derived(list.find((r) => r.id === appliedId));
	const has = (g: RevisionGroup<StackRevision>, id: string | undefined) =>
		!!id && g.members.some((m) => m.id === id);
	/** "Revision 7" or "Revisions 5–7" for a run of equal revisions. */
	const groupLabel = (g: RevisionGroup<StackRevision>) =>
		g.members.length > 1
			? `Revisions ${g.members.at(-1)!.seq}–${g.head.seq}`
			: `Revision ${g.head.seq}`;

	// Compare: deployed → on disk by default (the undeployed changes, shown
	// straight away), else the run before the newest → the newest.
	let from = $state('');
	let to = $state('');
	let showing = $state(false);
	$effect(() => {
		const l = list;
		if (!l.length || untrack(() => from && to)) return;
		const d = defaultComparison(l, appliedId, diskId, !!stack.undeployedChanges);
		if (!d) return;
		from = d.from;
		to = d.to;
		if (stack.undeployedChanges && d.from === appliedId && d.to === diskId) showing = true;
	});
	const pendingDiff = $derived(
		!!stack.undeployedChanges && from === appliedId && to === diskId && !!appliedId
	);
	// One option per run of equal revisions; the chosen IDs map to their run.
	const options = $derived(
		groups.map((g) => ({
			value: g.head.id,
			label: `${groupLabel(g)}${has(g, appliedId) ? ', deployed' : ''}${has(g, diskId) ? ', on disk' : ''}`
		}))
	);
	const headOf = (id: string) => groups.find((g) => has(g, id))?.head.id ?? id;
	const fromRev = createQuery(() => ({
		...stackRevisionQuery(ctx.id, from),
		enabled: showing && !!from
	}));
	const toRev = createQuery(() => ({
		...stackRevisionQuery(ctx.id, to),
		enabled: showing && !!to
	}));
	const changes = $derived(
		fromRev.data && toRev.data ? compareRevisions(fromRev.data, toRev.data) : null
	);
	const changed = $derived(changes?.filter((c) => c.status !== 'same') ?? []);
	const omitted = $derived(
		list.filter((r) => (r.id === from || r.id === to) && r.contentOmitted).length > 0
	);
	const labelOf = (id: string) => {
		const r = list.find((x) => x.id === id);
		if (!r) return 'Revision';
		if (pendingDiff) return id === appliedId ? `Deployed (Revision ${r.seq})` : 'On Disk';
		return `Revision ${r.seq}`;
	};

	function compareWith(r: StackRevision) {
		const c = comparisonFor(list, r, appliedId);
		if (!c) return;
		from = c.from;
		to = c.to;
		showing = true;
		document.getElementById('changes')?.scrollIntoView({ block: 'start' });
	}

	// Restore to disk.
	let restoring = $state<StackRevision | null>(null);
	let restoreOpen = $state(false);
	let offerDeploy = $state<number | null>(null);
	async function restore() {
		const r = restoring;
		if (!r) return;
		const release = criticalWork.register('restore', `${title} revision ${r.seq}`);
		try {
			const res = await restoreRevision(stack.id, r.id);
			queryClient.setQueryData(stackKeys.detail(stack.id), res.stack);
			void queryClient.invalidateQueries({ queryKey: stackKeys.revisions(stack.id) });
			toast.success(`Restored revision ${r.seq} of ${title} to disk`);
			offerDeploy = res.deployOffered ? r.seq : null;
		} finally {
			release();
		}
	}
	let deploying = $state(false);
	async function deployNow() {
		deploying = true;
		try {
			await startDeploy(stack, {}, ctx.tray, queryClient);
			offerDeploy = null;
		} catch (e) {
			toast.error(`${title} was not deployed`, { body: errorMessage(e) });
		} finally {
			deploying = false;
		}
	}
	function restoreDeployed() {
		if (!applied) return;
		restoring = applied;
		restoreOpen = true;
	}

	function authorOf(r: StackRevision): string {
		if (r.authorTokenId) return r.authorUserId === me ? 'You (API Token)' : 'API Token';
		if (r.authorUserId) return r.authorUserId === me ? 'You' : 'Another user';
		return r.source === 'external' ? 'On the host' : 'Docker Manager';
	}

	function rowMenu(g: RevisionGroup<StackRevision>): MenuEntry[] {
		const r = g.head;
		const items: MenuEntry[] = [];
		if (!r.contentOmitted && comparisonFor(list, r, appliedId))
			items.push({ label: 'Compare', icon: GitCompare, onSelect: () => compareWith(r) });
		if (can('stack.definition.write') && !r.contentOmitted && !has(g, diskId))
			items.push({
				label: 'Restore to Disk',
				icon: RotateCcw,
				disabled: offline,
				onSelect: () => {
					restoring = r;
					restoreOpen = true;
				}
			});
		return items;
	}

	const columns: Column<RevisionGroup<StackRevision>>[] = [
		{
			id: 'rev',
			header: 'Revision',
			cell: revCell,
			sortValue: (g) => g.head.seq,
			stack: 'title',
			width: '200px'
		},
		{ id: 'state', header: 'State', cell: stateCell, stack: 'status' },
		{
			id: 'source',
			header: 'Source',
			cell: sourceCell,
			sortValue: (g) => revisionSource(g.head.source)
		},
		{ id: 'author', header: 'Author', cell: authorCell },
		{
			id: 'at',
			header: 'Recorded',
			cell: atCell,
			sortValue: (g) => g.head.createdAt,
			width: '150px'
		},
		{
			id: 'actions',
			header: 'Actions',
			cell: actionsCell,
			hideHeader: true,
			stack: 'head',
			align: 'end',
			width: '64px'
		}
	];
</script>

{#snippet revCell(g: RevisionGroup<StackRevision>)}
	<span class="rev" title="Fingerprint {shortHash(g.head.hash)}">
		<span class="seq">{groupLabel(g)}</span>
		{#if g.members.length > 1}<span class="muted">same files</span>{/if}
	</span>
{/snippet}
{#snippet stateCell(g: RevisionGroup<StackRevision>)}
	{@const r = g.head}
	<span class="marks">
		{#if has(g, appliedId)}<Badge tone="ok" dot>Deployed</Badge>{/if}
		{#if has(g, diskId)}<Badge tone={has(g, appliedId) ? 'neutral' : 'warn'} dot>On Disk</Badge
			>{/if}
		{#if has(g, failedId)}<Badge tone="danger" dot>Deploy Failed</Badge>{/if}
		{#if r.contentOmitted}<Badge
				title="Over 96 KiB: only fingerprints were kept, so it cannot be shown or restored"
				>Too Large to Show</Badge
			>{/if}
		{#if r.restoredFrom}{@const src = list.find((x) => x.id === r.restoredFrom)}<span
				class="muted"
				>Restored from {src ? `revision ${src.seq}` : 'an older revision'}</span
			>{/if}
	</span>
{/snippet}
{#snippet sourceCell(g: RevisionGroup<StackRevision>)}{revisionSource(g.head.source)}{/snippet}
{#snippet authorCell(g: RevisionGroup<StackRevision>)}{authorOf(g.head)}{/snippet}
{#snippet atCell(g: RevisionGroup<StackRevision>)}<time
		datetime={g.head.createdAt}
		title={formatDateTime(g.head.createdAt)}>{formatRelative(g.head.createdAt)}</time
	>{/snippet}
{#snippet actionsCell(g: RevisionGroup<StackRevision>)}
	{@const items = rowMenu(g)}
	{#if items.length}
		<Menu {items} label="Actions for Revision {g.head.seq}" align="end">
			{#snippet trigger(props)}<IconButton
					{...props}
					size="sm"
					variant="secondary"
					label="Actions for Revision {g.head.seq}"
					icon={EllipsisVertical}
				/>{/snippet}
		</Menu>
	{/if}
{/snippet}

{#if !can('stack.definition.read')}
	<Card>
		<p class="muted">
			Revisions hold the Compose and .env files. Ask the owner for access to read the
			definition of {title}.
		</p>
	</Card>
{:else}
	{#if offerDeploy !== null}
		<Notice tone="info" title="Revision {offerDeploy} is on disk now.">
			It differs from the deployed revision; nothing was deployed. Deploy it to apply it.
			{#snippet actions()}
				{#if can('stack.deploy')}<Button size="sm" variant="primary" onclick={deployNow}
						>Deploy Now</Button
					>{/if}
				<Button size="sm" variant="ghost" onclick={() => (offerDeploy = null)}>Later</Button
				>
			{/snippet}
		</Notice>
	{/if}

	<Card title="Changes" id="changes" level={2}>
		{#snippet actions()}
			{#if list.length > 1}
				<div class="pick">
					<Select
						label="From"
						bind:value={() => headOf(from), (v) => (from = v)}
						{options}
					/>
					<Select label="To" bind:value={() => headOf(to), (v) => (to = v)} {options} />
				</div>
			{/if}
		{/snippet}
		{#if revisions.isPending}
			<div aria-busy="true"><Skeleton lines={3} /></div>
		{:else if groups.length < 2 && !stack.undeployedChanges}
			<p class="muted">
				Only one version of the files so far. Changes show up here after the next edit.
			</p>
		{:else if !showing}
			<div class="intro">
				<p>
					Pick two revisions to compare.
					<span class="muted">Opening files is recorded in the audit log.</span>
				</p>
				<Button
					icon={GitCompare}
					onclick={() => (showing = true)}
					disabled={!from || !to || headOf(from) === headOf(to)}>Show Changes</Button
				>
			</div>
		{:else if omitted}
			<p class="muted">
				One of these revisions is too large to show (over 96 KiB), so it cannot be compared.
			</p>
		{:else if fromRev.isError || toRev.isError}
			<ErrorState
				error={fromRev.error ?? toRev.error}
				title="The revisions could not be opened."
				onretry={() => (fromRev.refetch(), toRev.refetch())}
				compact
				bare
			/>
		{:else if !changes}
			<div aria-busy="true"><Skeleton lines={6} /></div>
		{:else if from === to || changed.length === 0}
			<p class="muted">{labelOf(from)} and {labelOf(to)} have the same files.</p>
		{:else}
			{#if pendingDiff}
				<div class="intro pending">
					<p>
						The files on disk differ from what is deployed.
						<span class="muted">Opening files is recorded in the audit log.</span>
					</p>
					<div class="intro-actions">
						{#if can('stack.definition.write') && applied && !applied.contentOmitted}
							<Button icon={RotateCcw} disabled={offline} onclick={restoreDeployed}
								>Restore Deployed Revision</Button
							>
						{/if}
						{#if can('stack.deploy')}
							<Button
								variant="primary"
								icon={Rocket}
								loading={deploying}
								disabled={offline}
								onclick={deployNow}>Deploy These Changes</Button
							>
						{/if}
					</div>
				</div>
			{/if}
			<div class="diffs">
				{#each changed as c (c.path)}
					{#if c.status === 'binary'}
						<p class="muted">
							<span class="mono">{c.path}</span>: binary file changed.
						</p>
					{:else}
						<DiffView
							title={c.status === 'added'
								? `${c.path} (added)`
								: c.status === 'removed'
									? `${c.path} (removed)`
									: c.path}
							before={c.before}
							after={c.after}
							beforeLabel={labelOf(from)}
							afterLabel={labelOf(to)}
						/>
					{/if}
				{/each}
			</div>
		{/if}
	</Card>

	<Card
		title="History"
		padding="none"
		id="history"
		subtitle={list.length
			? `${list.length} ${list.length === 1 ? 'revision' : 'revisions'}`
			: undefined}
	>
		{#if revisions.isPending}
			<div class="loading" aria-busy="true"><Skeleton lines={5} height="20px" /></div>
		{:else if revisions.isError}
			<div class="loading">
				<ErrorState
					error={revisions.error}
					title="The revisions could not be loaded."
					onretry={() => revisions.refetch()}
					compact
					bare
				/>
			</div>
		{:else}
			<Table label="Revisions of {title}" rows={groups} {columns} rowKey={(g) => g.head.id}>
				{#snippet empty()}
					<EmptyState
						icon={History}
						color="violet"
						title="No revisions yet."
						description="Docker Manager records one whenever the files change."
						level={3}
						compact
					/>
				{/snippet}
			</Table>
		{/if}
	</Card>
{/if}

{#if restoring}
	<ConfirmDialog
		bind:open={restoreOpen}
		title="Restore revision {restoring.seq} to disk?"
		consequences={[
			`Writes the files of revision ${restoring.seq} back to the project folder of ${title}.`,
			'The files on disk now are recorded as a revision first, so nothing is lost.',
			'Nothing is deployed: you can deploy the restored files afterwards.'
		]}
		confirmLabel="Restore to Disk"
		onconfirm={restore}
	/>
{/if}

<style>
	.pick {
		display: flex;
		gap: var(--space-2);
		min-width: min(520px, 100%);
	}

	.pick :global(> *) {
		flex: 1;
		min-width: 0;
	}

	.intro {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-3);
		color: var(--text-default);
	}

	.diffs {
		display: grid;
		gap: var(--space-3);
	}

	.loading {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.rev {
		display: inline-flex;
		align-items: baseline;
		gap: var(--space-2);
	}

	.seq {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.pending {
		margin-bottom: var(--space-3);
	}

	.intro-actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.marks {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	@media (max-width: 767px) {
		.pick {
			flex-direction: column;
			min-width: 0;
			width: 100%;
		}
	}
</style>
