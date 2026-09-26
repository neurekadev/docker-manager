<script lang="ts">
	// One backup (#10): what it holds and how consistent it is, the set it
	// belongs to (every member's own snapshot time), its contents with
	// single-file download, verification and the restore wizard.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Archive from '@lucide/svelte/icons/archive';
	import History from '@lucide/svelte/icons/history';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		Dialog,
		JobProgress,
		Notice,
		PageHeader,
		TextField,
		formatBytes,
		formatDateTime,
		toast
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { environmentName, newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ContentsBrowser from '$lib/features/backups/ContentsBrowser.svelte';
	import SetMembers from '$lib/features/backups/SetMembers.svelte';
	import {
		CONSISTENCY_LABEL,
		KIND_LABEL,
		itemName,
		setState,
		type BackupDetail
	} from '$lib/features/backups/model';
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
			{ label: backup.data ? itemName(backup.data) : 'Backup' }
		]
	}));

	let verifyOpen = $state(false);
	let subset = $state('');
	let verifying = $state(false);
	let verifyJob = $state<Job | null>(null);
	let verifyError = $state<string | null>(null);

	async function verify(b: BackupDetail) {
		verifying = true;
		verifyError = null;
		try {
			verifyJob = await unwrap(
				api.POST('/api/v1/backups/{backupId}/verifications', {
					params: {
						path: { backupId: b.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { readDataSubset: subset.trim() || undefined }
				})
			);
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
			<PageHeader
				title={itemName(b)}
				icon={Archive}
				color="teal"
				description="{b.kind ? KIND_LABEL[b.kind] : 'Backup'} from {formatDateTime(
					b.snapshotTime
				)}"
				meta={[
					{
						label:
							b.kind === 'manager_state' || !b.environmentId
								? 'Manager'
								: envName(b.environmentId)
					},
					{ label: repo?.name ?? 'Repository' },
					...(b.snapshotId
						? [
								{
									label: b.snapshotId.slice(0, 12),
									mono: true,
									title: `restic snapshot ${b.snapshotId}`
								}
							]
						: [])
				]}
			>
				{#snippet status()}
					{#if b.forgottenAt}<Badge tone="neutral" dot>Forgotten by retention</Badge>
					{:else if b.state === 'complete'}<Badge tone="ok" dot>Complete</Badge>
					{:else}<Badge tone="warn" dot>Partial</Badge>{/if}
				{/snippet}
				{#snippet actions()}
					{#if has(b, 'backup.verify')}
						<Button icon={ShieldCheck} onclick={() => (verifyOpen = true)}
							>Verify</Button
						>
					{/if}
					{#if has(b, 'backup.restore') && !b.forgottenAt}
						<Button variant="primary" icon={History} href={routes.backupRestore(b.id)}
							>Restore</Button
						>
					{/if}
				{/snippet}
			</PageHeader>

			{#if b.state === 'partial'}
				<Notice tone="warn" title="Some files could not be read" live="none">
					This backup is usable but incomplete; its job lists the files that were skipped.
					{#if b.jobId}<a href={routes.job(b.jobId)}>Open the job</a>{/if}
				</Notice>
			{/if}
			{#if b.kind === 'manager_state'}
				<Notice
					tone="info"
					title="Manager state restores happen on a new Docker Manager"
					live="none"
				>
					To recover the manager, set up a fresh Docker Manager and choose Import from
					backup during setup, with this repository and your Recovery Key. A running
					manager is never overwritten.
				</Notice>
			{/if}
			{#if verifyJob}
				<JobProgress
					jobId={verifyJob.id}
					title="Verify {repo?.name ?? 'the repository'}"
					onfinish={verified}
				/>
			{/if}

			<Card title="Details">
				<Facts
					columns={3}
					items={[
						{ label: 'Snapshot time', value: formatDateTime(b.snapshotTime) },
						{
							label: 'Consistency',
							value: b.consistency ? CONSISTENCY_LABEL[b.consistency] : '—'
						},
						{ label: 'Size', value: formatBytes(b.bytes) },
						{ label: 'Repository location', value: b.scope ?? '—', mono: true },
						{
							label: 'Last verified',
							value: b.verifiedAt ? formatDateTime(b.verifiedAt) : 'Not yet'
						},
						{ label: 'Volumes', value: b.volumes?.join(', ') || b.volume || '—' },
						{ label: 'Paths', value: b.paths?.join(', ') || '—', mono: true }
					]}
				/>
			</Card>

			{#if b.set && st}
				<Card
					title="Backup set"
					subtitle="Everything backed up by the same run. Hosts are backed up one after another, so each member has its own time."
				>
					{#snippet actions()}<Badge tone={st.tone} dot>{st.label}</Badge>{/snippet}
					<SetMembers members={b.set.members} environmentName={envName} />
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
				description="Runs restic check on the repository location that holds this backup. Damage fails the job."
			>
				<TextField
					label="Also read stored data"
					description="Optional. A share such as 5% or 1/10; reading data takes longer and finds damaged packs."
					bind:value={subset}
					mono
				/>
				{#if verifyError}<Notice
						tone="danger"
						title="The verification did not start"
						live="alert">{verifyError}</Notice
					>{/if}
				{#snippet footer()}
					<Button variant="ghost" onclick={() => (verifyOpen = false)}>Cancel</Button>
					<Button variant="primary" loading={verifying} onclick={() => verify(b)}
						>Verify</Button
					>
				{/snippet}
			</Dialog>
		{/snippet}
	</QueryView>
</Page>
