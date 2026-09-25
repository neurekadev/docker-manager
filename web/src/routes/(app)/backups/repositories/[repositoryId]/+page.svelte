<script lang="ts">
	// Backup repository detail (#10): Recovery Key state (confirm, rotate),
	// health per location (last backup and verification, size, problems),
	// the connection test (Object Lock warning), the verification schedule
	// and what a recovery from here needs.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Pencil from '@lucide/svelte/icons/pencil';
	import PlugZap from '@lucide/svelte/icons/plug-zap';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
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
		Switch,
		Table,
		TextField,
		formatBytes,
		formatDateTime,
		formatRelative,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { environmentName, ifMatch } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Columns from '$lib/features/common/Columns.svelte';
	import Facts from '$lib/features/common/Facts.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import ConnectionTestResult from '$lib/features/backups/ConnectionTestResult.svelte';
	import KeyRotationDialog from '$lib/features/backups/KeyRotationDialog.svelte';
	import RecoveryKeyChallenge from '$lib/features/backups/RecoveryKeyChallenge.svelte';
	import {
		RECOVERY_KEY_SCOPE,
		sentenceCase,
		repositoryLocation,
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

	usePage(() => ({
		title: repo.data?.name ?? 'Backup repository',
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
			await unwrap(
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
		if (has(r, 'backup_repository.manage')) {
			items.push({ label: 'Edit repository', icon: Pencil, onSelect: () => openEdit(r) });
			items.push({
				label: 'Verification schedule',
				icon: RotateCw,
				onSelect: () => openVerify(r)
			});
		}
		if (owner && r.state === 'ready')
			items.push({
				label: 'Rotate Recovery Key',
				icon: KeyRound,
				onSelect: () => (rotateOpen = true)
			});
		if (has(r, 'backup_repository.manage')) {
			items.push({ separator: true });
			items.push({
				label: 'Remove repository',
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
			header: 'Scope',
			cell: scopeCell,
			sortValue: (l) => l.scope,
			stack: 'title'
		},
		{ id: 'backup', header: 'Last backup', cell: lastBackupCell, width: '150px' },
		{ id: 'verified', header: 'Last verified', cell: verifiedCell, width: '190px' },
		{ id: 'size', header: 'Size', cell: sizeCell, numeric: true, width: '100px' },
		{ id: 'key', header: 'Key', cell: keyCell, width: '120px' }
	];

	function scopeName(scope: string): string {
		if (scope === 'manager' || scope === 'dockyard-manager') return 'Manager state';
		const m = scope.match(/^(?:env:|dockyard-env-)(.+)$/);
		return m ? `Environment ${envName(m[1])}` : scope;
	}
</script>

{#snippet scopeCell(l: Location)}
	<NameCell name={scopeName(l.scope)} sub={l.repository} subMono />
{/snippet}
{#snippet lastBackupCell(l: Location)}
	{#if l.lastBackupAt}<span class="num" title={l.lastBackupAt}
			>{formatRelative(l.lastBackupAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet verifiedCell(l: Location)}
	{#if l.lastVerifiedAt}
		<span class="num">{formatRelative(l.lastVerifiedAt)}</span>
		{#if l.lastVerifyResult && l.lastVerifyResult !== 'ok'}<Badge tone="danger"
				>{l.lastVerifyResult.replaceAll('_', ' ')}</Badge
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
			<PageHeader
				title={r.name}
				icon={HardDrive}
				color="teal"
				description={r.kind === 's3' ? 'S3-compatible storage' : 'Local directory'}
				meta={[{ label: repositoryLocation(r, envName), mono: true }]}
			>
				{#snippet status()}
					{#if r.state === 'ready'}<Badge tone="ok" dot>Ready</Badge>{:else}<Badge
							tone="warn"
							dot>Awaiting key confirmation</Badge
						>{/if}
				{/snippet}
				{#snippet actions()}
					{#if has(r, 'backup_repository.manage')}
						<Button icon={PlugZap} loading={testing} onclick={() => runTest(r)}
							>Test connection</Button
						>
					{/if}
					{#if menu.length}
						<Menu items={menu} label="More actions for {r.name}">
							{#snippet trigger(props)}
								<IconButton
									{...props}
									label="More actions"
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
					subtitle="Nothing is written to this repository and no policy can use it until the owner re-enters the key."
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
							Ask the owner of this DockYard to confirm the Recovery Key.
						</p>
					{/if}
				</Card>
			{/if}

			{#if test}
				<Card title="Connection test"><ConnectionTestResult {test} /></Card>
			{/if}

			{#if r.state === 'ready'}
				<QueryView query={health} errorTitle="The repository health could not be loaded.">
					{#snippet children(h)}
						{#if h.problems.length}
							<Notice
								tone={h.healthy ? 'warn' : 'danger'}
								title="Needs attention"
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
								title="Recovery Key rotation in progress"
								live="none"
							>
								These locations still use the previous key ({h.keyState
									.previousFingerprint}); keep it until the list is empty:
								<span class="mono"
									>{(h.keyState.pendingLocations ?? []).join(', ')}</span
								>
							</Notice>
						{/if}
						<Card
							title="Locations"
							subtitle="One restic repository per scope below this destination."
							padding="none"
						>
							<Table
								label="Locations of {r.name}"
								rows={h.locations}
								columns={locationColumns}
								rowKey={(l) => l.scope}
							/>
						</Card>
						<Columns ratio="equal">
							<Card title="Health">
								<Facts
									items={[
										{
											label: 'State',
											value: h.healthy ? 'Healthy' : 'Needs attention'
										},
										{ label: 'Snapshots', value: h.snapshots },
										{ label: 'Stored', value: formatBytes(h.sizeBytes) },
										{
											label: 'Newest backup',
											value: h.lastBackupAt
												? formatDateTime(h.lastBackupAt)
												: 'None yet'
										},
										{ label: 'Backup age', value: h.backupAge },
										{
											label: 'Last verified',
											value: h.lastVerifiedAt
												? formatDateTime(h.lastVerifiedAt)
												: 'Never'
										}
									]}
								/>
							</Card>
							<Card title="Recovery Key">
								<Facts
									columns={1}
									items={[
										{
											label: 'Fingerprint',
											value: h.keyState.fingerprint,
											mono: true
										},
										{ label: 'Generation', value: h.keyState.generation },
										{
											label: 'Confirmed',
											value: h.keyState.confirmedAt
												? formatDateTime(h.keyState.confirmedAt)
												: '—'
										}
									]}
								/>
								<p class="muted small note">{RECOVERY_KEY_SCOPE}</p>
							</Card>
						</Columns>
					{/snippet}
				</QueryView>
			{/if}

			<Columns ratio="equal">
				<Card title="Verification">
					{#if r.verification}
						<ScheduleSummary
							cron={r.verification.cron}
							timeZone={r.verification.timeZone}
							enabled={r.verification.enabled}
						/>
						<p class="muted small note">
							{r.verification.readDataSubset
								? `Also reads ${r.verification.readDataSubset} of the stored data.`
								: 'Checks the repository structure; add a data subset to also read stored data.'}
						</p>
					{:else}<p class="muted">No verification schedule.</p>{/if}
				</Card>
				<Card title="What a recovery needs">
					{#if r.recoveryRequirements?.length}
						<ul class="plain" role="list">
							{#each r.recoveryRequirements as q (q)}<li>{sentenceCase(q)}</li>{/each}
						</ul>
					{:else}
						<p class="muted">
							The Recovery Key and access to {repositoryLocation(r, envName)}.
						</p>
					{/if}
					{#if r.kind === 's3' && r.credential}
						<p class="muted small note">
							Stored key pair fingerprint <span class="mono"
								>{r.credential.fingerprint ?? '—'}</span
							>
						</p>
					{/if}
				</Card>
			</Columns>

			<KeyRotationDialog bind:open={rotateOpen} repositoryId={r.id} />
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Remove backup repository {r.name}"
				consequences={[
					'DockYard stops using this destination and forgets its settings and S3 credentials.',
					'The restic repositories and every backup at the destination are left untouched.',
					'Policies must not use it: change them first.'
				]}
				confirmText={r.name}
				confirmLabel="Remove repository"
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
						<TextField label="Region" description="Optional." bind:value={editRegion} />
						<Switch label="Path-style addressing" bind:checked={editPathStyle} />
						<TextField
							label="New access key ID"
							mono
							description="Optional. Replaces the stored key pair (both fields)."
							bind:value={editAccessKey}
							autocomplete="off"
						/>
						<PasswordField
							label="New secret access key"
							bind:value={editSecret}
							autocomplete="off"
						/>
					{/if}
					{#if saveError}<Notice tone="danger" title="Not saved" live="alert"
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
							)}>Save changes</Button
					>
				{/snippet}
			</Dialog>
			<Dialog
				bind:open={verifyOpen}
				title="Verification schedule"
				description="Checks every location of {r.name} for damage (restic check)."
			>
				<Fields>
					<Switch
						label="Verify automatically"
						description="Off: verify from a backup when you want."
						bind:checked={vEnabled}
					/>
					<CronField
						label="Schedule"
						kind="backup_verification"
						bind:cron={vCron}
						bind:timeZone={vZone}
					/>
					<TextField
						label="Also read stored data"
						description="Optional. A share such as 5% or 1/10 of the pack data; empty checks the structure only."
						bind:value={vSubset}
						mono
					/>
					{#if saveError}<Notice tone="danger" title="Not saved" live="alert"
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
							)}>Save schedule</Button
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
</style>
