<script lang="ts">
	// Backup repository detail (#10): one status line (health, size, last
	// backup, last verification), the Recovery Key state (confirm, rotate),
	// health, the verification schedule in one sentence (Edit schedule), the
	// last connection test and what a recovery from here needs. Locations,
	// key generations, fingerprints and restic's raw snapshots wait under
	// Advanced. Running verifications of its locations (scheduled, by hand
	// or moving them to a rotated key) show their progress, also after a
	// reload (docs/internal/web.md, "Job progress after reload").
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Camera from '@lucide/svelte/icons/camera';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Pencil from '@lucide/svelte/icons/pencil';
	import PlugZap from '@lucide/svelte/icons/plug-zap';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { api, unwrap, unwrapEmpty, type Job } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		CronField,
		DestructiveConfirm,
		Dialog,
		IconButton,
		Menu,
		Notice,
		PageHeader,
		PasswordField,
		RadioGroup,
		Switch,
		Table,
		TextField,
		formatBytes,
		formatDateTime,
		formatGoDuration,
		formatRelative,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { environmentName, ifMatch } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Columns from '$lib/features/common/Columns.svelte';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import Facts from '$lib/features/common/Facts.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import CompressionField from '$lib/features/backups/CompressionField.svelte';
	import ConnectionTestResult from '$lib/features/backups/ConnectionTestResult.svelte';
	import { verifyMatch, verifyTitle } from '$lib/features/backups/jobs';
	import KeyRotationDialog from '$lib/features/backups/KeyRotationDialog.svelte';
	import RecoveryKeyChallenge from '$lib/features/backups/RecoveryKeyChallenge.svelte';
	import {
		RECOVERY_KEY_SCOPE,
		compressionText,
		ratioText,
		repositoryLocation,
		repositoryStatusLine,
		scopeName,
		sentenceCase,
		verificationText,
		verifyReadOptions,
		type BackupRepository,
		type ConnectionTest
	} from '$lib/features/backups/model';
	import {
		backupKeys,
		repositoryHealthQuery,
		repositoryQuery
	} from '$lib/features/backups/queries';

	type Location = import('$lib/api/client').Schema<'BackupLocationHealth'>;

	const id = $derived(page.params.repositoryId ?? '');
	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const repo = createQuery(() => repositoryQuery(id));
	const health = createQuery(() => ({
		...repositoryHealthQuery(id),
		enabled: repo.data?.state === 'ready'
	}));
	const envs = createQuery(() => environmentsQuery());
	const envName = (e: string) => environmentName(envs.data, e);
	// Verifications of the repository's locations.
	const verifications = useTrackedJobs(() => (id ? verifyMatch(id) : null));

	function verified(j: Job) {
		void qc.invalidateQueries({ queryKey: backupKeys.repository(id) });
		void qc.invalidateQueries({ queryKey: backupKeys.health(id) });
		const name = repo.data?.name ?? 'the repository';
		const where =
			j.kind === 'manager.verify' || !j.environmentId
				? 'the manager state'
				: envName(j.environmentId);
		if (j.state === 'succeeded')
			toast.success(`Verified ${name} for ${where}: no damage found`);
		else if (j.state !== 'cancelled')
			toast.error(`The verification of ${name} for ${where} found a problem`, {
				body: j.error?.recovery ?? j.error?.message
			});
	}

	usePage(() => ({
		title: repo.data?.name ?? 'Backup Repository',
		crumbs: [
			{ label: 'Backups', href: routes.backups() },
			{ label: 'Repositories', href: routes.backupRepositories() },
			{ label: repo.data?.name ?? 'Repository' }
		]
	}));

	let test = $state<ConnectionTest | null>(null);
	let testing = $state(false);
	let rotateOpen = $state(false);
	let deleteOpen = $state(false);
	let editOpen = $state(false);
	let verifyOpen = $state(false);

	// Edit and verification form state (filled when a dialog opens).
	let editName = $state('');
	let editRegion = $state('');
	let editPathStyle = $state(true);
	let editCompression = $state<string>('auto');
	let editAccessKey = $state('');
	let editSecret = $state('');
	let vEnabled = $state(false);
	let vCron = $state('0 5 * * 0');
	let vZone = $state('UTC');
	let vSubset = $state('');
	let saving = $state(false);
	let saveError = $state<string | null>(null);

	async function runTest(r: BackupRepository) {
		testing = true;
		try {
			test = await unwrap(
				api.POST('/api/v1/backup-repositories/{repositoryId}/connection-tests', {
					params: { path: { repositoryId: r.id } }
				})
			);
			if (test.ok) toast.success(`Tested ${r.name}: the connection works`);
			else toast.error(`Tested ${r.name}: the connection failed`, { body: test.message });
			void qc.invalidateQueries({ queryKey: backupKeys.repository(r.id) });
		} catch (e) {
			toast.error('The connection test did not run', { body: actionError(e) });
		} finally {
			testing = false;
		}
	}

	function openEdit(r: BackupRepository) {
		editName = r.name;
		editRegion = r.region ?? '';
		editPathStyle = r.pathStyle ?? true;
		editCompression = r.compression ?? 'auto';
		editAccessKey = '';
		editSecret = '';
		saveError = null;
		editOpen = true;
	}

	function openVerify(r: BackupRepository) {
		vEnabled = r.verification?.enabled ?? false;
		vCron = r.verification?.cron ?? '0 5 * * 0';
		vZone = r.verification?.timeZone ?? 'UTC';
		vSubset = r.verification?.readDataSubset ?? '';
		saveError = null;
		verifyOpen = true;
	}

	async function patch(r: BackupRepository, body: Record<string, unknown>, message: string) {
		saving = true;
		saveError = null;
		try {
			const saved = await unwrap(
				api.PATCH('/api/v1/backup-repositories/{repositoryId}', {
					params: {
						path: { repositoryId: r.id },
						header: { 'If-Match': ifMatch(r.revision) }
					},
					body
				})
			);
			qc.setQueryData(backupKeys.repository(r.id), saved);
			void qc.invalidateQueries({ queryKey: ['backups', 'list'] });
			toast.success(message);
			editOpen = false;
			verifyOpen = false;
		} catch (e) {
			saveError = actionError(e, {
				recovery_key_not_confirmed: 'Confirm the Recovery Key first; verification needs it.'
			});
		} finally {
			saving = false;
		}
	}

	async function remove(r: BackupRepository) {
		try {
			await unwrapEmpty(
				api.DELETE('/api/v1/backup-repositories/{repositoryId}', {
					params: {
						path: { repositoryId: r.id },
						header: { 'If-Match': ifMatch(r.revision) }
					}
				})
			);
		} catch (e) {
			throw new Error(
				actionError(e, {
					backup_repository_in_use:
						'A backup policy uses this repository. Change or delete that policy first.'
				}),
				{ cause: e }
			);
		}
		toast.success(`Removed backup repository ${r.name}`);
		await qc.invalidateQueries({ queryKey: ['backups'] });
		await goto(routes.backupRepositories());
	}

	function menuFor(r: BackupRepository): MenuEntry[] {
		const items: MenuEntry[] = [];
		if (has(r, 'backup_repository.manage'))
			items.push({ label: 'Edit Repository', icon: Pencil, onSelect: () => openEdit(r) });
		if (owner && r.state === 'ready')
			items.push({
				label: 'Rotate Recovery Key',
				icon: KeyRound,
				onSelect: () => (rotateOpen = true)
			});
		if (r.state === 'ready' && r.view === 'full')
			items.push({
				label: 'Raw Snapshots',
				icon: Camera,
				href: routes.backupSnapshots(r.id)
			});
		if (has(r, 'backup_repository.manage')) {
			items.push({ separator: true });
			items.push({
				label: 'Remove Repository',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleteOpen = true)
			});
		}
		return items;
	}

	const locationColumns: Column<Location>[] = [
		{
			id: 'scope',
			header: 'Holds',
			cell: scopeCell,
			sortValue: (l) => l.scope,
			maxWidth: '420px',
			title: (l) => l.repository,
			stack: 'title'
		},
		{ id: 'backup', header: 'Last Backup', cell: lastBackupCell, width: '150px' },
		{ id: 'verified', header: 'Last Verified', cell: verifiedCell, width: '190px' },
		{ id: 'size', header: 'Size', cell: sizeCell, numeric: true, width: '100px' },
		{ id: 'key', header: 'Key', cell: keyCell, width: '120px', stack: 'hidden' }
	];
</script>

{#snippet scopeCell(l: Location)}
	<NameCell name={scopeName(l.scope, envName)} sub={l.repository} subMono />
{/snippet}
{#snippet lastBackupCell(l: Location)}
	{#if l.lastBackupAt}<span class="num" title={formatDateTime(l.lastBackupAt)}
			>{formatRelative(l.lastBackupAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet verifiedCell(l: Location)}
	{#if l.lastVerifiedAt}
		<span class="num" title={formatDateTime(l.lastVerifiedAt)}
			>{formatRelative(l.lastVerifiedAt)}</span
		>
		{#if l.lastVerifyResult && l.lastVerifyResult !== 'ok'}<Badge tone="danger"
				>{sentenceCase(l.lastVerifyResult.replaceAll('_', ' '))}</Badge
			>{/if}
	{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet sizeCell(l: Location)}<span class="num">{formatBytes(l.sizeBytes)}</span>{/snippet}
{#snippet keyCell(l: Location)}<span class="num">Generation {l.keyGeneration}</span>{/snippet}

<Page>
	<QueryView
		query={repo}
		errorTitle="The backup repository could not be loaded."
		notFoundTitle="This repository does not exist."
	>
		{#snippet children(r: BackupRepository)}
			{@const menu = menuFor(r)}
			{@const manage = has(r, 'backup_repository.manage')}
			<PageHeader
				title={r.name}
				{...resourceIcon('backupRepository')}
				description={r.state === 'ready' && health.data
					? repositoryStatusLine(health.data, r.storage?.sizeBytes)
					: undefined}
				meta={[
					{ label: r.kind === 's3' ? 'S3 Storage' : 'Local Directory' },
					{
						label: repositoryLocation(r, envName),
						mono: true,
						title: repositoryLocation(r, envName)
					}
				]}
			>
				{#snippet status()}
					{#if r.state === 'ready'}<Badge tone="ok" dot>Ready</Badge>{:else}<Badge
							tone="warn"
							dot>Awaiting Key Confirmation</Badge
						>{/if}
				{/snippet}
				{#snippet actions()}
					{#if manage}
						<Button icon={PlugZap} loading={testing} onclick={() => runTest(r)}
							>Test Connection</Button
						>
					{/if}
					{#if menu.length}
						<Menu items={menu} label="More Actions for {r.name}">
							{#snippet trigger(props)}
								<IconButton
									{...props}
									label="More Actions"
									icon={Ellipsis}
									variant="secondary"
								/>
							{/snippet}
						</Menu>
					{/if}
				{/snippet}
			</PageHeader>

			{#if r.state === 'awaiting_confirmation'}
				<Card
					title="Confirm the Recovery Key"
					subtitle="Nothing is stored here until the owner re-enters the key."
				>
					{#if owner}
						<RecoveryKeyChallenge
							repositoryId={r.id}
							onconfirmed={(out) => {
								qc.setQueryData(backupKeys.repository(r.id), out.repository);
								void qc.invalidateQueries({ queryKey: ['backups'] });
								toast.success(`Confirmed the Recovery Key for ${r.name}`, {
									body: out.activated
										? 'The new key is now current.'
										: 'The repository is ready.'
								});
							}}
						/>
					{:else}
						<p class="muted">
							Ask the owner of this Docker Manager to confirm the Recovery Key.
						</p>
					{/if}
				</Card>
			{/if}

			<ActiveJobs
				jobs={verifications}
				titleOf={(j) => verifyTitle(j, r.name, envName)}
				onfinish={verified}
				label="Verifications of {r.name}"
			/>

			{#if test}
				<Card title="Connection Test"><ConnectionTestResult {test} /></Card>
			{/if}

			{#if r.state === 'ready'}
				<QueryView query={health} errorTitle="The repository health could not be loaded.">
					{#snippet children(h)}
						{#if h.problems.length}
							<Notice
								tone={h.healthy ? 'warn' : 'danger'}
								title="Needs Attention"
								live="none"
							>
								<ul class="plain" role="list">
									{#each h.problems as pr (pr)}<li>{pr}</li>{/each}
								</ul>
							</Notice>
						{/if}
						{#if h.keyState.rotationInProgress}
							<Notice
								tone="warn"
								icon={KeyRound}
								title="Recovery Key Rotation in Progress"
								live="none"
							>
								{(h.keyState.pendingLocations ?? []).length === 1
									? 'One location still uses'
									: `${(h.keyState.pendingLocations ?? []).length} locations still use`}
								the previous Recovery Key: keep your copy of it until the rotation finishes.
							</Notice>
						{/if}
						<Columns ratio="equal">
							<Card title="Health">
								<Facts
									items={[
										{
											label: 'State',
											value: h.healthy ? 'Healthy' : 'Needs Attention'
										},
										{
											label: 'Stored',
											value: r.storage
												? `${formatBytes(r.storage.sizeBytes)} (${ratioText(r.storage.compressionRatio)} compression)`
												: 'Measured after the next backup'
										},
										{
											label: 'Compression',
											value: compressionText(r.compression)
										},
										{ label: 'Backups Kept', value: h.snapshots },
										{
											label: 'Newest Backup',
											value: h.lastBackupAt
												? formatDateTime(h.lastBackupAt)
												: 'None yet'
										},
										{
											label: 'Age of the Newest Backup',
											value: h.backupAge ? formatGoDuration(h.backupAge) : '—'
										},
										{
											label: 'Last Verified',
											value: h.lastVerifiedAt
												? formatDateTime(h.lastVerifiedAt)
												: 'Never'
										}
									]}
								/>
							</Card>
							<Card title="Verification">
								{#snippet actions()}
									{#if manage}
										<Button
											size="sm"
											icon={RotateCw}
											onclick={() => openVerify(r)}>Edit Schedule</Button
										>
									{/if}
								{/snippet}
								<p>{verificationText(r.verification)}</p>
								<p class="muted small note">
									{r.lastTest
										? `Connection ${r.lastTest.ok ? 'works' : 'failed'}, last checked ${formatRelative(r.lastTest.at)}.`
										: 'The connection has not been tested yet.'}
								</p>
							</Card>
						</Columns>
					{/snippet}
				</QueryView>
			{/if}

			<Columns ratio="equal">
				<Card title="Recovery Key">
					<p>{RECOVERY_KEY_SCOPE}</p>
					{#if health.data?.keyState.confirmedAt}
						<p class="muted small note">
							Confirmed {formatDateTime(health.data.keyState.confirmedAt)}.
						</p>
					{/if}
				</Card>
				<Card title="What a Recovery Needs">
					{#if r.recoveryRequirements?.length}
						<ul class="plain" role="list">
							{#each r.recoveryRequirements as q (q)}<li>{sentenceCase(q)}</li>{/each}
						</ul>
					{:else}
						<p class="muted">
							The Recovery Key and access to {repositoryLocation(r, envName)}.
						</p>
					{/if}
				</Card>
			</Columns>

			{#if r.state === 'ready'}
				<Disclosure summary="Advanced: Locations, Key Details and Raw Snapshots">
					<div class="advanced">
						{#if health.data}
							<Card
								title="Locations"
								info="One restic repository per scope: the manager state and each environment."
								padding="none"
							>
								{#snippet actions()}
									{#if r.view === 'full'}
										<Button
											size="sm"
											variant="ghost"
											icon={Camera}
											href={routes.backupSnapshots(r.id)}
											>Raw Snapshots</Button
										>
									{/if}
								{/snippet}
								<Table
									label="Locations of {r.name}"
									rows={health.data.locations}
									columns={locationColumns}
									rowKey={(l) => l.scope}
								/>
							</Card>
							<Card title="Key Details">
								<Facts
									columns={1}
									items={[
										{
											label: 'Recovery Key Fingerprint',
											value: health.data.keyState.fingerprint,
											mono: true
										},
										{
											label: 'Key Generation',
											value: health.data.keyState.generation
										},
										...(health.data.keyState.rotationInProgress
											? [
													{
														label: 'Previous Key Fingerprint',
														value: health.data.keyState
															.previousFingerprint,
														mono: true
													},
													{
														label: 'Still on the Previous Key',
														value: (
															health.data.keyState.pendingLocations ??
															[]
														)
															.map((s) => scopeName(s, envName))
															.join(', ')
													}
												]
											: []),
										...(r.kind === 's3' && r.credential
											? [
													{
														label: 'Stored S3 Key Pair Fingerprint',
														value: r.credential.fingerprint,
														mono: true
													}
												]
											: [])
									]}
								/>
							</Card>
						{/if}
					</div>
				</Disclosure>
			{/if}

			<KeyRotationDialog bind:open={rotateOpen} repositoryId={r.id} />
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Remove Backup Repository {r.name}"
				consequences={[
					'Docker Manager stops using this destination and forgets its settings and S3 credentials.',
					'Its backups leave the Backups lists: without the repository they can no longer be browsed or restored here.',
					'The backups stored at the destination are left untouched.',
					'Policies must not use it: change them first.'
				]}
				confirmText={r.name}
				confirmLabel="Remove Repository"
				onconfirm={() => remove(r)}
			/>
			<Dialog
				bind:open={editOpen}
				title="Edit {r.name}"
				description="The destination itself can't move."
			>
				<Fields>
					<TextField label="Name" bind:value={editName} required />
					{#if r.kind === 's3'}
						<TextField label="Region" optional bind:value={editRegion} />
						<Switch label="Path-Style Addressing" bind:checked={editPathStyle} />
						<TextField
							label="New Access Key ID"
							mono
							optional
							description="Replaces the stored key pair."
							bind:value={editAccessKey}
							autocomplete="off"
						/>
						<PasswordField
							label="New Secret Access Key"
							bind:value={editSecret}
							autocomplete="off"
						/>
					{/if}
					<CompressionField bind:value={editCompression} />
					{#if saveError}<Notice tone="danger" title="Not Saved" live="alert"
							>{saveError}</Notice
						>{/if}
				</Fields>
				{#snippet footer()}
					<Button variant="ghost" onclick={() => (editOpen = false)}>Cancel</Button>
					<Button
						variant="primary"
						loading={saving}
						disabled={!editName.trim() || !!editAccessKey.trim() !== !!editSecret}
						onclick={() =>
							patch(
								r,
								{
									name: editName.trim(),
									compression: editCompression,
									...(r.kind === 's3'
										? {
												region: editRegion.trim(),
												pathStyle: editPathStyle,
												...(editAccessKey.trim()
													? {
															accessKeyId: editAccessKey.trim(),
															secretAccessKey: editSecret
														}
													: {})
											}
										: {})
								},
								`Saved backup repository ${editName.trim()}`
							)}>Save Changes</Button
					>
				{/snippet}
			</Dialog>
			<Dialog
				bind:open={verifyOpen}
				title="Verification Schedule"
				description="Checks every location of {r.name} for damage."
			>
				<Fields>
					<Switch label="Verify Automatically" bind:checked={vEnabled} />
					{#if vEnabled}
						<CronField
							label="Schedule"
							kind="backup_verification"
							bind:cron={vCron}
							bind:timeZone={vZone}
						/>
					{/if}
					<RadioGroup
						label="How Much to Check"
						options={verifyReadOptions(r.verification?.readDataSubset)}
						bind:value={vSubset}
					/>
					{#if saveError}<Notice tone="danger" title="Not Saved" live="alert"
							>{saveError}</Notice
						>{/if}
				</Fields>
				{#snippet footer()}
					<Button variant="ghost" onclick={() => (verifyOpen = false)}>Cancel</Button>
					<Button
						variant="primary"
						loading={saving}
						onclick={() =>
							patch(
								r,
								{
									verifyEnabled: vEnabled,
									verifyCron: vCron,
									verifyTimeZone: vZone,
									verifyReadData: vSubset
								},
								'Saved the verification schedule'
							)}>Save Schedule</Button
					>
				{/snippet}
			</Dialog>
		{/snippet}
	</QueryView>
</Page>

<style>
	.small {
		font-size: var(--text-caption);
	}

	.note {
		margin-top: var(--space-3);
	}

	.plain {
		display: grid;
		gap: var(--space-1);
	}

	.advanced {
		display: grid;
		gap: var(--space-4);
		margin-top: var(--space-3);
	}
</style>
