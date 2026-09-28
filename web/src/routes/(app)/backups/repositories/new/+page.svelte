<script lang="ts">
	// Add a backup repository and set up the Recovery Key (#10): destination
	// (Save and continue saves it at once) → the Recovery Key shown once
	// (copy, download, fingerprint, "I stored it") → the re-entry challenge,
	// which initializes the repository → connection test ("Finish anyway"
	// after a failed test). Cancel leaves before anything is saved; once
	// saved it becomes "Finish later" (the repository page finishes setup).
	// The first repository of an instance generates the key; later ones
	// reuse it and only ask for the challenge. Errors show in the wizard.
	// Confirming a rotated key starts verifications that move the existing
	// repositories to it: they show here and, found again in the running
	// list, on each repository's page (docs/internal/web.md, "Job progress
	// after reload").
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { api, unwrap, type Job, type Schema } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { criticalWork } from '$lib/live';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		DeniedState,
		Notice,
		PageHeader,
		SecretReveal,
		Skeleton,
		StepWizard,
		TextField,
		toast
	} from '$lib/ui';
	import { environmentName } from '$lib/features/common/data';
	import { fieldErrors, actionError } from '$lib/features/common/errors';
	import Fields from '$lib/features/common/Fields.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import CompressionField from '$lib/features/backups/CompressionField.svelte';
	import ConnectionTestResult from '$lib/features/backups/ConnectionTestResult.svelte';
	import DestinationFields from '$lib/features/backups/DestinationFields.svelte';
	import { destinationReady, emptyDestination } from '$lib/features/backups/destination';
	import { VERIFY_KINDS, verifyTitle } from '$lib/features/backups/jobs';
	import RecoveryKeyChallenge from '$lib/features/backups/RecoveryKeyChallenge.svelte';
	import {
		COMPRESSION_NEW_NOTE,
		RECOVERY_KEY_SCOPE,
		RECOVERY_KEY_WARNING,
		type BackupRepository,
		type CompressionMode,
		type ConnectionTest
	} from '$lib/features/backups/model';
	import { repositoriesQuery } from '$lib/features/backups/queries';

	usePage({
		title: 'Add backup repository',
		crumbs: [
			{ label: 'Backups', href: routes.backups() },
			{ label: 'Repositories', href: routes.backupRepositories() },
			{ label: 'Add repository' }
		]
	});

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const envs = createQuery(() => environmentsQuery());

	let current = $state(0);
	let name = $state('');
	let dest = $state(emptyDestination());
	let compression = $state<string>('auto');
	let error = $state<unknown>(null);
	let created = $state<Schema<'CreatedBackupRepository'> | null>(null);
	let keyStored = $state(false);
	let confirmed = $state<Schema<'RecoveryConfirmation'> | null>(null);
	let test = $state<ConnectionTest | null>(null);
	let testing = $state(false);

	const repo = $derived<BackupRepository | undefined>(
		confirmed?.repository ?? created?.repository
	);

	// The verifications moving the repositories to the confirmed key.
	const repos = createQuery(() => ({
		...repositoriesQuery(),
		enabled: !!confirmed?.jobs?.length
	}));
	const migrated = $derived([
		...new Set(
			(confirmed?.jobs ?? []).flatMap((j) =>
				(j.targets ?? []).filter((t) => t.type === 'repository').map((t) => t.id)
			)
		)
	]);
	const migration = useTrackedJobs(() =>
		migrated.length
			? {
					kinds: [...VERIFY_KINDS],
					targets: migrated.map((id) => ({ type: 'repository', id }))
				}
			: null
	);
	const migrationTitle = (j: Job) =>
		verifyTitle(
			j,
			repos.data?.find((r) =>
				j.targets?.some((t) => t.type === 'repository' && t.id === r.id)
			)?.name ?? 'a repository',
			(id) => environmentName(envs.data, id)
		);
	const fields = $derived(fieldErrors(error));
	const executors = $derived([
		{ value: 'manager', label: 'The manager' },
		...(envs.data ?? [])
			.filter((e) => e.status !== 'archived')
			.map((e) => ({ value: e.id, label: `Environment ${e.name}` }))
	]);

	// A generated key that is not confirmed yet must not be reloaded away.
	$effect(() => {
		if (!created?.recoveryKey || confirmed) return;
		const release = untrack(() =>
			criticalWork.register('other', 'Recovery Key not confirmed yet')
		);
		const warn = (e: BeforeUnloadEvent) => e.preventDefault();
		window.addEventListener('beforeunload', warn);
		return () => {
			release();
			window.removeEventListener('beforeunload', warn);
		};
	});

	const steps = [
		{
			id: 'destination',
			label: 'Destination',
			description:
				'Where the encrypted backups are stored. Save and continue saves the repository; you can finish setup later from its page.'
		},
		{
			id: 'key',
			label: 'Recovery Key',
			description: 'The key that decrypts every backup of this Docker Manager.'
		},
		{
			id: 'confirm',
			label: 'Confirm the key',
			description: 'Prove you saved it. Nothing is written before.'
		},
		{
			id: 'test',
			label: 'Test',
			description: 'Check that Docker Manager can read and write there.'
		}
	];

	const canAdvance = $derived(
		current === 0
			? !!name.trim() && destinationReady(dest)
			: current === 1
				? !created?.recoveryKey || keyStored
				: current === 2
					? !!confirmed
					: true
	);

	async function onnext(step: { id: string }) {
		if (step.id === 'destination' && !created) {
			error = null;
			try {
				created = await unwrap(
					api.POST('/api/v1/backup-repositories', {
						body:
							dest.kind === 'local'
								? {
										name: name.trim(),
										kind: 'local',
										executor: dest.executor,
										path: dest.path.trim(),
										compression: compression as CompressionMode
									}
								: {
										name: name.trim(),
										kind: 's3',
										endpoint: dest.endpoint.trim(),
										bucket: dest.bucket.trim(),
										prefix: dest.prefix.trim() || undefined,
										region: dest.region.trim() || undefined,
										pathStyle: dest.pathStyle,
										accessKeyId: dest.accessKeyId.trim(),
										secretAccessKey: dest.secretAccessKey,
										compression: compression as CompressionMode
									}
					})
				);
				dest.secretAccessKey = '';
				await qc.invalidateQueries({ queryKey: ['backups'] });
			} catch (e) {
				error = e;
				throw new Error(
					Object.keys(fieldErrors(e)).length
						? 'Check the highlighted fields.'
						: actionError(e, {
								backup_repository_name_taken:
									'Another repository has this name. Choose a different name.'
							}),
					{ cause: e }
				);
			}
		}
		if (step.id === 'confirm' && confirmed) await runTest();
	}

	async function runTest() {
		if (!repo) return;
		testing = true;
		try {
			test = await unwrap(
				api.POST('/api/v1/backup-repositories/{repositoryId}/connection-tests', {
					params: { path: { repositoryId: repo.id } }
				})
			);
		} catch (e) {
			toast.error('The connection test did not run', { body: actionError(e) });
		} finally {
			testing = false;
		}
	}

	async function finish() {
		if (!repo) return;
		toast.success(`Added backup repository ${repo.name}`);
		await goto(routes.backupRepository(repo.id));
	}

	// Before saving: back to the list. Saved: the repository page, where
	// the Recovery Key can still be confirmed.
	async function cancel() {
		await goto(repo ? routes.backupRepository(repo.id) : routes.backupRepositories());
	}
</script>

<Page narrow>
	<PageHeader
		title="Add backup repository"
		description="A local directory or an S3 bucket where Docker Manager stores encrypted backups of the manager and each environment."
	/>
	{#if perms.isPending}
		<Skeleton lines={6} height="36px" />
	{:else if !perms.data?.owner}
		<DeniedState
			title="Only the owner adds backup repositories."
			description="Repositories and the Recovery Key are administered by the owner of this Docker Manager."
			level={2}
		/>
	{:else}
		<Card>
			<StepWizard
				label="Add backup repository"
				{steps}
				bind:current
				{onnext}
				onfinish={finish}
				{canAdvance}
				nextLabel={current === 0 && !created ? 'Save and continue' : 'Next'}
				finishLabel={test && !test.ok ? 'Finish anyway' : 'Done'}
				oncancel={cancel}
				cancelLabel={created ? 'Finish later' : 'Cancel'}
			>
				{#snippet step(s)}
					{#if s.id === 'destination'}
						{#if created}
							<Notice
								tone="info"
								title="The repository {created.repository.name} exists"
								live="none"
							>
								Its destination can't change any more. Continue with the Recovery
								Key.
							</Notice>
						{:else}
							<Fields>
								<TextField
									label="Name"
									bind:value={name}
									required
									placeholder="NAS backups"
									error={fields['body.name']}
								/>
								<DestinationFields bind:value={dest} {executors} errors={fields} />
								<CompressionField
									bind:value={compression}
									description={COMPRESSION_NEW_NOTE}
									error={fields['body.compression']}
								/>
							</Fields>
						{/if}
					{:else if s.id === 'key'}
						{#if created?.recoveryKey}
							<Fields>
								<p>{RECOVERY_KEY_SCOPE}</p>
								<SecretReveal
									secret={created.recoveryKey.key}
									label="Recovery Key"
									filename="docker-manager-recovery-key.txt"
									fingerprint={created.recoveryKey.fingerprint}
									description={RECOVERY_KEY_WARNING}
									confirmLabel="I saved it, continue"
									onconfirm={() => {
										keyStored = true;
										current = 2;
									}}
								/>
								{#each created.recoveryKey.notice as n (n)}<p class="muted">
										{n}
									</p>{/each}
							</Fields>
						{:else}
							<Notice
								tone="info"
								icon={KeyRound}
								title="This repository uses your existing Recovery Key"
								live="none"
							>
								{RECOVERY_KEY_SCOPE} Its fingerprint is
								<span class="mono"
									>{created?.keyState.fingerprint ??
										created?.keyState.pendingFingerprint}</span
								>. Keep your saved copy at hand for the next step.
							</Notice>
						{/if}
					{:else if s.id === 'confirm'}
						{#if confirmed}
							<Notice tone="info" title="Recovery Key confirmed" live="status">
								{confirmed.repository.name} is ready. Docker Manager is preparing it for
								backups now.
							</Notice>
							<ActiveJobs
								jobs={migration}
								titleOf={migrationTitle}
								variant="inline"
								label="Moving the repositories to the confirmed key"
							/>
						{:else if created}
							<RecoveryKeyChallenge
								repositoryId={created.repository.id}
								fingerprint={created.keyState.pendingFingerprint ??
									created.keyState.fingerprint}
								onconfirmed={(r) => {
									confirmed = r;
									void qc.invalidateQueries({ queryKey: ['backups'] });
									for (const j of r.jobs ?? []) migration.add(j);
								}}
							/>
						{/if}
					{:else if s.id === 'test'}
						{#if testing}
							<Skeleton lines={3} height="20px" />
						{:else if test}
							<ConnectionTestResult {test} />
						{/if}
						<div class="retest">
							<Button onclick={runTest} loading={testing}>Test again</Button>
						</div>
					{/if}
				{/snippet}
			</StepWizard>
		</Card>
	{/if}
</Page>

<style>
	.retest {
		margin-top: var(--space-3);
	}
</style>
