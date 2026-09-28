<script lang="ts">
	// Raw snapshots (#10): what restic itself holds, read live from every
	// location of one repository (?repository=, the repository page's "Raw
	// snapshots") or of every repository the caller may read: the backups,
	// the set and host manifests, and snapshots Docker Manager did not
	// write. Linked rows open the backup; a location that cannot be read is
	// named with the reason. Not a tab of Backups: the Backups tab lists
	// the same backups by run.
	import { createQueries, createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		Notice,
		PageHeader,
		Skeleton,
		Table,
		TextField,
		formatBytes,
		formatDateTime,
		type Column
	} from '$lib/ui';
	import { environmentName } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import Page from '$lib/features/common/Page.svelte';
	import {
		SNAPSHOT_CLASS,
		sentenceCase,
		snapshotName,
		snapshotRows,
		type SnapshotRow
	} from '$lib/features/backups/model';
	import { repositoriesQuery, resticSnapshotsQuery } from '$lib/features/backups/queries';

	const qc = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const envName = (id: string) => environmentName(envs.data, id);
	const repos = createQuery(() => repositoriesQuery());
	const only = $derived(page.url.searchParams.get('repository'));
	const onlyRepo = $derived(only ? repos.data?.find((r) => r.id === only) : undefined);
	const readable = $derived(
		(repos.data ?? []).filter(
			(r) =>
				(!only || r.id === only) &&
				r.state === 'ready' &&
				r.view === 'full' &&
				r.actions.includes('backup_repository.read')
		)
	);

	usePage(() => ({
		title: 'Raw snapshots',
		crumbs: onlyRepo
			? [
					{ label: 'Backups', href: routes.backups() },
					{ label: 'Repositories', href: routes.backupRepositories() },
					{ label: onlyRepo.name, href: routes.backupRepository(onlyRepo.id) },
					{ label: 'Raw snapshots' }
				]
			: [{ label: 'Backups', href: routes.backups() }, { label: 'Raw snapshots' }],
		environmentScoped: true
	}));
	const listings = createQueries(() => ({
		queries: readable.map((r) => resticSnapshotsQuery(r.id))
	}));
	const loading = $derived(repos.isPending || listings.some((q) => q.isPending));
	const failed = $derived(
		readable
			.map((r, i) => ({ r, q: listings[i] }))
			.filter(({ q }) => q?.isError)
			.map(({ r, q }) => ({ name: r.name, error: q.error }))
	);
	const data = $derived(
		snapshotRows(
			readable
				.map((r, i) => ({ repository: r, locations: listings[i]?.data ?? [] }))
				.filter((l) => l.locations.length),
			environmentSelection.id
		)
	);

	let filter = $state('');
	const rows = $derived.by(() => {
		const q = filter.trim().toLowerCase();
		if (!q) return data.rows;
		return data.rows.filter((r) =>
			[
				snapshotName(r.snapshot),
				SNAPSHOT_CLASS[r.snapshot.class].label,
				r.snapshot.shortId,
				r.repositoryName,
				r.environmentId ? envName(r.environmentId) : 'manager',
				...r.snapshot.tags
			]
				.join(' ')
				.toLowerCase()
				.includes(q)
		);
	});

	let refreshing = $state(false);
	async function refresh() {
		refreshing = true;
		await Promise.all(
			readable.map((r) =>
				qc.refetchQueries({ queryKey: resticSnapshotsQuery(r.id).queryKey })
			)
		);
		refreshing = false;
	}

	const where = (r: SnapshotRow | { environmentId?: string }) =>
		r.environmentId ? envName(r.environmentId) : 'Manager';

	const columns: Column<SnapshotRow>[] = [
		{
			id: 'time',
			header: 'Taken',
			cell: timeCell,
			sortValue: (r) => r.snapshot.time,
			width: '190px',
			stack: 'meta'
		},
		{
			id: 'what',
			header: 'Snapshot',
			cell: whatCell,
			sortValue: (r) => snapshotName(r.snapshot),
			maxWidth: '320px',
			stack: 'title'
		},
		{
			id: 'class',
			header: 'Kind',
			cell: classCell,
			sortValue: (r) => SNAPSHOT_CLASS[r.snapshot.class].label,
			width: '190px',
			stack: 'status'
		},
		{
			id: 'where',
			header: 'Repository',
			cell: whereCell,
			sortValue: (r) => `${r.repositoryName} ${where(r)}`,
			width: '220px',
			stack: 'meta'
		},
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			sortValue: (r) => r.snapshot.bytesProcessed ?? -1,
			numeric: true,
			width: '100px',
			stack: 'hidden'
		},
		{
			id: 'added',
			header: 'Added',
			cell: addedCell,
			sortValue: (r) => r.snapshot.dataAdded ?? -1,
			numeric: true,
			width: '100px',
			stack: 'hidden'
		}
	];
</script>

{#snippet timeCell(r: SnapshotRow)}<span class="num">{formatDateTime(r.snapshot.time)}</span
	>{/snippet}
{#snippet whatCell(r: SnapshotRow)}
	<NameCell
		icon="snapshot"
		name={snapshotName(r.snapshot)}
		href={r.snapshot.backupId ? routes.backup(r.snapshot.backupId) : undefined}
		sub={r.snapshot.shortId}
		subMono
	/>
{/snippet}
{#snippet classCell(r: SnapshotRow)}
	{@const c = SNAPSHOT_CLASS[r.snapshot.class]}
	<span class="cell">
		<Badge tone={c.tone} dot>{c.label}</Badge>
		{#if r.snapshot.forgotten}<span class="muted">Forgotten</span>{/if}
	</span>
{/snippet}
{#snippet whereCell(r: SnapshotRow)}
	<NameCell
		name={r.repositoryName}
		href={routes.backupRepository(r.repositoryId)}
		sub={where(r)}
	/>
{/snippet}
{#snippet sizeCell(r: SnapshotRow)}<span class="num">{formatBytes(r.snapshot.bytesProcessed)}</span
	>{/snippet}
{#snippet addedCell(r: SnapshotRow)}<span class="num">{formatBytes(r.snapshot.dataAdded)}</span
	>{/snippet}

<Page>
	<PageHeader
		title="Raw snapshots"
		description={onlyRepo
			? `What restic itself holds in ${onlyRepo.name}, read live. Backups lists the same backups by run.`
			: 'What restic itself holds in your repositories, read live. Backups lists the same backups by run.'}
	/>

	{#each failed as f (f.name)}
		<Notice tone="warn" title="The snapshots of {f.name} could not be listed.">
			{actionError(f.error)}
		</Notice>
	{/each}
	{#each data.problems as p (`${p.repositoryName}/${p.scope}`)}
		<Notice
			tone={p.errorClass ? 'warn' : 'info'}
			title={p.errorClass
				? `${p.repositoryName}, ${where(p)}: not listed (${sentenceCase(p.errorClass.replaceAll('_', ' '))}).`
				: `${p.repositoryName}, ${where(p)}: only the newest snapshots are listed.`}
			live="none"
		>
			{p.errorClass
				? 'The other locations are listed. Check that the agent is connected and the repository is reachable, then refresh.'
				: 'restic holds older snapshots at this location too.'}
		</Notice>
	{/each}

	<Card
		title="Snapshots"
		subtitle={loading
			? 'Reading the repositories…'
			: `${rows.length} restic ${rows.length === 1 ? 'snapshot' : 'snapshots'}, read live from the repositories`}
		padding="none"
	>
		{#snippet actions()}
			<div class="tools">
				<div class="filter">
					<TextField
						label="Search snapshots"
						hideLabel
						placeholder="Search snapshots"
						bind:value={filter}
					/>
				</div>
				<Button
					size="sm"
					variant="ghost"
					icon={RefreshCw}
					loading={refreshing}
					disabled={!readable.length}
					onclick={refresh}>Refresh</Button
				>
			</div>
		{/snippet}
		{#if loading && !data.rows.length}
			<div class="pad"><Skeleton lines={5} height="20px" /></div>
		{:else}
			<Table
				label="Restic snapshots"
				{rows}
				{columns}
				rowKey={(r) => r.key}
				sort={{ column: 'time', direction: 'desc' }}
			>
				{#snippet empty()}
					<EmptyState
						{...resourceIcon('snapshot')}
						title={filter.trim()
							? 'No snapshot matches the filter.'
							: readable.length
								? 'No snapshots here.'
								: 'No repository to read.'}
						description={filter.trim()
							? 'Clear the filter to see every snapshot.'
							: readable.length
								? 'Snapshots appear after a policy runs.'
								: 'Add a backup repository and confirm its Recovery Key first.'}
						level={3}
						compact
					/>
				{/snippet}
			</Table>
		{/if}
	</Card>
</Page>

<style>
	.tools {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}

	.filter {
		width: min(320px, 50vw);
	}

	.cell {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.pad {
		padding: var(--space-4);
	}
</style>
