<script lang="ts">
	// One backup (#10): what it holds and how consistent it is, the run it
	// belongs to (every member's own time, each linked to its backup), its
	// contents with single-file download, verification of the repository
	// location that holds it, and the restore wizard. Internal paths, the
	// location and the snapshot ID wait under Advanced. A running
	// verification of that location shows its progress, also after a
	// reload (docs/internal/web.md, "Job progress after reload").
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import History from '@lucide/svelte/icons/history';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		Dialog,
		Notice,
		PageHeader,
		RadioGroup,
		formatBytes,
		formatDateTime,
		shortId,
		toast
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { environmentName, newIdempotencyKey } from '$lib/features/common/data';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { actionError } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import ContentsBrowser from '$lib/features/backups/ContentsBrowser.svelte';
	import SetMembers from '$lib/features/backups/SetMembers.svelte';
	import {
		CONSISTENCY_LABEL,
		KIND_LABEL,
		VERIFY_READ_OPTIONS,
		itemName,
		scopeName,
		setState,
		type BackupDetail
	} from '$lib/features/backups/model';
	import { verifyMatch, verifyTitle } from '$lib/features/backups/jobs';
	import { backupKeys, backupQuery, repositoriesQuery } from '$lib/features/backups/queries';

	const id = $derived(page.params.backupId ?? '');
	const qc = useQueryClient();
	const backup = createQuery(() => backupQuery(id));
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const envName = (e: string) => environmentName(envs.data, e);

	usePage(() => ({
		title: backup.data ? itemName(backup.data) : 'Backup',
		crumbs: [
			{ label: 'Backups', href: routes.backups() },
			{ label: 'All Backups', href: routes.backupList() },
			{ label: backup.data ? itemName(backup.data) : 'Backup' }
		]
	}));

	let verifyOpen = $state(false);
	let subset = $state('');
	let verifying = $state(false);
	let verifyError = $state<string | null>(null);

	// Verifications of the location that holds this backup.
	const verifications = useTrackedJobs(() =>
		backup.data ? verifyMatch(backup.data.repositoryId, backup.data.scope) : null
	);
	const repoName = $derived(
		repos.data?.find((r) => r.id === backup.data?.repositoryId)?.name ?? 'the repository'
	);

	async function verify(b: BackupDetail) {
		verifying = true;
		verifyError = null;
		try {
			const job = await unwrap(
				api.POST('/api/v1/backups/{backupId}/verifications', {
					params: {
						path: { backupId: b.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { readDataSubset: subset.trim() || undefined }
				})
			);
			verifications.add(job, verifyTitle(job, repoName, envName));
			verifyOpen = false;
		} catch (e) {
			verifyError = actionError(e);
		} finally {
			verifying = false;
		}
	}

	function verified(j: Job) {
		void qc.invalidateQueries({ queryKey: backupKeys.backup(id) });
		if (j.state === 'succeeded') toast.success('Verified the repository: no damage found');
		else
			toast.error('The verification found a problem', {
				body: j.error?.recovery ?? j.error?.message
			});
	}
</script>

<Page>
	<QueryView
		query={backup}
		errorTitle="The backup could not be loaded."
		notFoundTitle="This backup does not exist."
	>
		{#snippet children(b: BackupDetail)}
			{@const repo = repos.data?.find((r) => r.id === b.repositoryId)}
			{@const st = b.set ? setState(b.set.state) : null}
			{@const where =
				b.kind === 'manager_state' || !b.environmentId
					? 'Manager'
					: envName(b.environmentId)}
			<PageHeader
				title={itemName(b)}
				{...resourceIcon('backup')}
				description="{b.kind ? KIND_LABEL[b.kind] : 'Backup'} from {formatDateTime(
					b.snapshotTime
				)}"
				meta={[
					{ label: where },
					{ label: repo?.name ?? 'Repository' },
					...(b.snapshotId
						? [
								{
									label: shortId(b.snapshotId, 8),
									mono: true,
									title: 'Snapshot ID',
									copy: { value: b.snapshotId, what: 'snapshot ID' }
								}
							]
						: [])
				]}
			>
				{#snippet status()}
					{#if b.forgottenAt}<Badge tone="neutral" dot>Forgotten by Retention</Badge>
					{:else if b.state === 'complete'}<Badge tone="ok" dot>Complete</Badge>
					{:else}<Badge tone="warn" dot>Partial</Badge>{/if}
				{/snippet}
				{#snippet actions()}
					{#if has(b, 'backup.restore') && !b.forgottenAt}
						<Button variant="primary" icon={History} href={routes.backupRestore(b.id)}
							>Restore</Button
						>
					{/if}
					{#if has(b, 'backup.verify')}
						<Button icon={ShieldCheck} onclick={() => (verifyOpen = true)}
							>Verify</Button
						>
					{/if}
				{/snippet}
			</PageHeader>

			{#if b.state === 'partial'}
				<Notice tone="warn" title="Some Files Could Not Be Read" live="none">
					This backup is usable but incomplete; its job lists the files that were skipped.
					{#if b.jobId}<a href={routes.job(b.jobId)}>Open Job</a>{/if}
				</Notice>
			{/if}
			{#if b.kind === 'manager_state'}
				<Notice tone="info" title="Restore on a New Docker Manager" live="none">
					Set up a fresh Docker Manager and choose Import From Backup, with this
					repository and your Recovery Key. A running manager is never overwritten.
				</Notice>
			{/if}
			<ActiveJobs
				jobs={verifications}
				titleOf={(j) => verifyTitle(j, repoName, envName)}
				onfinish={verified}
				label="Verification of {repoName}"
			/>

			<Card title="Details">
				<Facts
					columns={3}
					items={[
						{ label: 'Taken', value: formatDateTime(b.snapshotTime) },
						{
							label: 'Consistency',
							value: b.consistency ? CONSISTENCY_LABEL[b.consistency] : '—'
						},
						{ label: 'Size', value: formatBytes(b.bytes) },
						{
							label: b.kind === 'stack' ? 'Volumes' : 'Volume',
							value: b.volumes?.join(', ') || b.volume || '—'
						},
						{ label: 'Environment', value: where },
						{
							label: 'Last Verified',
							value: b.verifiedAt ? formatDateTime(b.verifiedAt) : 'Not yet'
						}
					]}
				/>
				<div class="advanced">
					<Disclosure summary="Advanced">
						<Facts
							columns={1}
							items={[
								{
									label: 'Repository Location',
									value: b.scope ? scopeName(b.scope, envName) : '—'
								},
								{
									label: 'Paths in the Backup',
									value: b.paths?.join(', '),
									mono: true
								},
								{ label: 'Snapshot ID', value: b.snapshotId, mono: true }
							]}
						/>
					</Disclosure>
				</div>
			</Card>

			{#if b.set && st}
				<Card
					title="Backup Run"
					info="Hosts are backed up one after another, so each backup has its own time."
				>
					{#snippet actions()}<Badge tone={st.tone} dot>{st.label}</Badge>{/snippet}
					<SetMembers
						members={b.set.members}
						environmentName={envName}
						repositoryName={(id) =>
							repos.data?.find((x) => x.id === id)?.name ?? 'a removed repository'}
						currentId={b.id}
					/>
				</Card>
			{/if}

			{#if has(b, 'backup.contents.read') && !b.forgottenAt}
				<Card title="Contents" padding="none">
					<ContentsBrowser
						backupId={b.id}
						canDownload={has(b, 'backup.contents.download')}
					/>
				</Card>
			{/if}

			<Dialog
				bind:open={verifyOpen}
				title="Verify {repo?.name ?? 'the repository'}"
				description="Checks every backup in the repository location that holds this one, not only this backup."
			>
				<RadioGroup
					label="How Much to Check"
					options={VERIFY_READ_OPTIONS}
					bind:value={subset}
				/>
				{#if verifyError}<Notice
						tone="danger"
						title="The verification did not start"
						live="alert">{verifyError}</Notice
					>{/if}
				{#snippet footer()}
					<Button variant="ghost" onclick={() => (verifyOpen = false)}>Cancel</Button>
					<Button variant="primary" loading={verifying} onclick={() => verify(b)}
						>Verify Repository</Button
					>
				{/snippet}
			</Dialog>
		{/snippet}
	</QueryView>
</Page>

<style>
	.advanced {
		margin-top: var(--space-4);
	}
</style>
