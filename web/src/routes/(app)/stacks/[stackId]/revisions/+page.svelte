<script lang="ts">
	// Revisions (#7, #25 Q1): every definition DockYard saw, newest first:
	// what was deployed, what the stack editor or file manager saved, edits
	// made on the host and restores. Compare any two (the files on disk
	// against the deployed revision by default) and restore one to disk;
	// a restore never deploys, it offers the deploy afterwards.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { untrack } from 'svelte';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import GitCompare from '@lucide/svelte/icons/git-compare';
	import History from '@lucide/svelte/icons/history';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import { sessionQuery } from '$lib/api/queries';
	import { deployStack, restoreRevision } from '$lib/features/stacks/actions';
	import { useStackPage } from '$lib/features/stacks/context';
	import {
		compareRevisions,
		revisionLabel,
		revisionSource,
		shortHash,
		stackTitle
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
	const appliedId = $derived(stack.appliedRevision?.id);
	const diskId = $derived(stack.sourceRevision?.id);
	const failedId = $derived(stack.failedRevision?.id);

	// Compare: deployed → on disk by default (the undeployed changes),
	// else the one before the newest → the newest.
	let from = $state('');
	let to = $state('');
	let showing = $state(false);
	$effect(() => {
		const l = list;
		if (!l.length || untrack(() => from && to)) return;
		if (stack.undeployedChanges && appliedId && diskId) {
			from = appliedId;
			to = diskId;
		} else {
			to = l[0].id;
			from = (l[1] ?? l[0]).id;
		}
	});
	const options = $derived(
		list.map((r) => ({
			value: r.id,
			label: `${revisionLabel(r)}${r.id === appliedId ? ', deployed' : ''}${r.id === diskId ? ', on disk' : ''}`
		}))
	);
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
		return r ? `Revision ${r.seq}` : 'Revision';
	};

	function compareWith(r: StackRevision) {
		from =
			appliedId && appliedId !== r.id ? appliedId : (list[list.indexOf(r) + 1]?.id ?? r.id);
		to = r.id;
		showing = true;
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
	async function deployNow() {
		try {
			const job = await deployStack(stack.id, 'deploy');
			ctx.tray.add(job, {
				title: `Deploy ${title}`,
				success: `Deployed ${title}`,
				failure: `${title} was not deployed`
			});
			offerDeploy = null;
		} catch (e) {
			toast.error(`${title} was not deployed`, { body: errorMessage(e) });
		}
	}

	function authorOf(r: StackRevision): string {
		if (r.authorTokenId) return r.authorUserId === me ? 'You (API token)' : 'API token';
		if (r.authorUserId) return r.authorUserId === me ? 'You' : 'Another user';
		return r.source === 'external' ? 'On the host' : 'DockYard';
	}

	function rowMenu(r: StackRevision): MenuEntry[] {
		const items: MenuEntry[] = [];
		if (!r.contentOmitted)
			items.push({ label: 'Compare', icon: GitCompare, onSelect: () => compareWith(r) });
		if (can('stack.definition.write') && !r.contentOmitted && r.id !== diskId)
			items.push({
				label: 'Restore to disk',
				icon: RotateCcw,
				disabled: offline,
				onSelect: () => {
					restoring = r;
					restoreOpen = true;
				}
			});
		return items;
	}

	const columns: Column<StackRevision>[] = [
		{
			id: 'rev',
			header: 'Revision',
			cell: revCell,
			sortValue: (r) => r.seq,
			stack: 'title',
			width: '180px'
		},
		{ id: 'state', header: 'State', cell: stateCell, stack: 'status' },
		{
			id: 'source',
			header: 'Source',
			cell: sourceCell,
			sortValue: (r) => revisionSource(r.source)
		},
		{ id: 'author', header: 'Author', cell: authorCell },
		{
			id: 'at',
			header: 'Recorded',
			cell: atCell,
			sortValue: (r) => r.createdAt,
			width: '150px'
		},
		{
			id: 'actions',
			header: 'Actions',
			cell: actionsCell,
			hideHeader: true,
			stack: 'actions',
			align: 'end',
			width: '64px'
		}
	];
</script>

{#snippet revCell(r: StackRevision)}
	<span class="rev">
		<span class="seq">Revision {r.seq}</span>
		<span class="hash mono" title={r.hash}>{shortHash(r.hash)}</span>
	</span>
{/snippet}
{#snippet stateCell(r: StackRevision)}
	<span class="marks">
		{#if r.id === appliedId}<Badge tone="ok" dot>Deployed</Badge>{/if}
		{#if r.id === diskId}<Badge tone={r.id === appliedId ? 'neutral' : 'warn'} dot
				>On disk</Badge
			>{/if}
		{#if r.id === failedId}<Badge tone="danger" dot>Deploy failed</Badge>{/if}
		{#if r.contentOmitted}<Badge
				title="Over 96 KiB: only hashes were kept, so it cannot be shown or restored"
				>Hashes only</Badge
			>{/if}
		{#if r.restoredFrom}{@const src = list.find((x) => x.id === r.restoredFrom)}<span
				class="muted"
				>Restored from {src ? `revision ${src.seq}` : 'an older revision'}</span
			>{/if}
	</span>
{/snippet}
{#snippet sourceCell(r: StackRevision)}{revisionSource(r.source)}{/snippet}
{#snippet authorCell(r: StackRevision)}<span title={r.authorUserId ?? r.authorTokenId}
		>{authorOf(r)}</span
	>{/snippet}
{#snippet atCell(r: StackRevision)}<span title={formatDateTime(r.createdAt)}
		>{formatRelative(r.createdAt)}</span
	>{/snippet}
{#snippet actionsCell(r: StackRevision)}
	{@const items = rowMenu(r)}
	{#if items.length}
		<Menu {items} label="Actions for revision {r.seq}" align="end">
			{#snippet trigger(props)}<IconButton
					{...props}
					size="sm"
					variant="secondary"
					label="Actions for revision {r.seq}"
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
						>Deploy now</Button
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
					<Select label="From" bind:value={from} {options} />
					<Select label="To" bind:value={to} {options} />
				</div>
			{/if}
		{/snippet}
		{#if revisions.isPending}
			<div aria-busy="true"><Skeleton lines={3} /></div>
		{:else if list.length < 2 && !stack.undeployedChanges}
			<p class="muted">
				Only one revision so far. Changes show up here after the next edit or deploy.
			</p>
		{:else if !showing}
			<div class="intro">
				<p>
					{#if stack.undeployedChanges && from === appliedId && to === diskId}
						The files on disk differ from the deployed revision.
					{:else}
						Pick two revisions to compare.
					{/if}
					Opening the files is recorded in the audit log, because they can hold secrets.
				</p>
				<Button
					icon={GitCompare}
					onclick={() => (showing = true)}
					disabled={!from || !to || from === to}>Show changes</Button
				>
			</div>
		{:else if omitted}
			<p class="muted">
				One of these revisions kept only hashes (over 96 KiB), so it cannot be compared.
			</p>
		{:else if fromRev.isError || toRev.isError}
			<ErrorState
				error={fromRev.error ?? toRev.error}
				title="The revisions could not be opened."
				onretry={() => (fromRev.refetch(), toRev.refetch())}
				compact
			/>
		{:else if !changes}
			<div aria-busy="true"><Skeleton lines={6} /></div>
		{:else if from === to || changed.length === 0}
			<p class="muted">{labelOf(from)} and {labelOf(to)} have the same files.</p>
		{:else}
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
				/>
			</div>
		{:else}
			<Table label="Revisions of {title}" rows={list} {columns} rowKey={(r) => r.id}>
				{#snippet empty()}
					<EmptyState
						icon={History}
						color="violet"
						title="No revisions yet."
						description="DockYard records one at every deploy and whenever the files change."
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
			`Writes the files of revision ${restoring.seq} (${shortHash(restoring.hash)}) back to the project directory of ${title}.`,
			'The files on disk now are recorded as a revision first, so nothing is lost.',
			'Nothing is deployed: you can deploy the restored files afterwards.'
		]}
		confirmLabel="Restore to disk"
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

	.hash {
		color: var(--accent-text);
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
