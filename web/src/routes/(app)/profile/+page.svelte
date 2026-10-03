<script lang="ts">
	// Profile (#16): my account, password change, authenticator app (TOTP),
	// passkeys (add, rename, remove) and recovery codes. Changes that weaken
	// sign-in need a recent sign-in (step-up); removing a factor the policy
	// requires is refused with a clear reason. Personal only: instance
	// administration is Settings.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Fingerprint from '@lucide/svelte/icons/fingerprint';
	import KeySquare from '@lucide/svelte/icons/key-square';
	import { api, unwrap, unwrapEmpty } from '$lib/api/client';
	import { queryKeys, sessionQuery } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import {
		credentialToJSON,
		creationOptions,
		isCancelled,
		passkeysSupported
	} from '$lib/auth/webauthn';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		Checkbox,
		ConfirmDialog,
		Dialog,
		Notice,
		PasswordField,
		SecretReveal,
		Table,
		TextField,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import { newIdempotencyKey } from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import Fields from '$lib/features/common/Fields.svelte';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { displayName, factorsText } from '$lib/features/access/model';
	import ProfileHeader from '$lib/features/profile/ProfileHeader.svelte';
	import TotpSetup from '$lib/features/profile/TotpSetup.svelte';
	import {
		passkeysQuery,
		profileKeys,
		recoveryCodesQuery,
		type Passkey
	} from '$lib/features/profile/queries';
	import { FACTOR_POLICY } from '$lib/features/settings/model';

	usePage({ title: 'Profile', crumbs: [{ label: 'Profile' }] });

	const qc = useQueryClient();
	const session = createQuery(() => sessionQuery());
	const me = $derived(session.data?.user);
	const passkeys = createQuery(() => passkeysQuery());
	const codes = createQuery(() => recoveryCodesQuery());

	const FACTOR_REQUIRED = {
		factor_required:
			'The sign-in policy requires this factor and you have no other. Add another first.'
	};

	async function refresh() {
		await qc.invalidateQueries({ queryKey: queryKeys.session });
		await qc.invalidateQueries({ queryKey: profileKeys.passkeys });
		await qc.invalidateQueries({ queryKey: profileKeys.recoveryCodes });
	}

	// Password
	let current = $state('');
	let next = $state('');
	let repeat = $state('');
	let revokeTokens = $state(false);
	let pwBusy = $state(false);
	let pwError = $state<unknown>(null);
	const mismatch = $derived(repeat && next !== repeat ? 'The passwords do not match.' : null);

	async function changePassword(e: SubmitEvent) {
		e.preventDefault();
		if (mismatch) return;
		pwBusy = true;
		pwError = null;
		try {
			await withStepUp(() =>
				unwrapEmpty(
					api.PATCH('/api/v1/me/password', {
						body: {
							currentPassword: current || undefined,
							newPassword: next,
							revokeApiTokens: revokeTokens || undefined
						}
					})
				)
			);
			current = next = repeat = '';
			revokeTokens = false;
			toast.success('Changed your password', { body: 'Your other sessions ended.' });
			await refresh();
		} catch (err) {
			pwError = err;
		} finally {
			pwBusy = false;
		}
	}

	// TOTP
	let totpSetup = $state(false);
	let totpOffOpen = $state(false);
	async function totpOff() {
		try {
			await withStepUp(() => unwrapEmpty(api.DELETE('/api/v1/auth/totp')));
		} catch (e) {
			throw new Error(actionError(e, FACTOR_REQUIRED), { cause: e });
		}
		toast.success('Turned off the authenticator app');
		await refresh();
	}

	// Passkeys
	let pkName = $state('');
	let pkBusy = $state(false);
	let pkError = $state<string | null>(null);
	let removing = $state<Passkey | null>(null);
	let removeOpen = $state(false);
	let renaming = $state<Passkey | null>(null);
	let newName = $state('');
	let renameError = $state<string | null>(null);

	async function addPasskey() {
		pkBusy = true;
		pkError = null;
		try {
			const options = await unwrap(api.POST('/api/v1/auth/passkeys/registration-options'));
			const cred = (await navigator.credentials.create({
				publicKey: creationOptions(options)
			})) as PublicKeyCredential | null;
			if (!cred) return;
			const out = await unwrap(
				api.POST('/api/v1/auth/passkeys/registration-verifications', {
					body: { name: pkName.trim() || undefined, credential: credentialToJSON(cred) }
				})
			);
			qc.setQueryData(queryKeys.session, out.session);
			toast.success(`Added the passkey ${out.passkey.name}`);
			pkName = '';
			await refresh();
		} catch (e) {
			if (!isCancelled(e)) pkError = actionError(e);
		} finally {
			pkBusy = false;
		}
	}

	async function removePasskey(p: Passkey) {
		try {
			await withStepUp(() =>
				unwrapEmpty(
					api.DELETE('/api/v1/me/passkeys/{credentialId}', {
						params: { path: { credentialId: p.id } }
					})
				)
			);
		} catch (e) {
			throw new Error(actionError(e, FACTOR_REQUIRED), { cause: e });
		}
		toast.success(`Removed the passkey ${p.name}`);
		await refresh();
	}

	async function renamePasskey(p: Passkey) {
		renameError = null;
		try {
			await unwrap(
				api.PATCH('/api/v1/me/passkeys/{credentialId}', {
					params: { path: { credentialId: p.id } },
					body: { name: newName.trim() }
				})
			);
			toast.success(`Renamed the passkey to ${newName.trim()}`);
			renaming = null;
			await refresh();
		} catch (e) {
			renameError = actionError(e);
		}
	}

	// Recovery codes
	let newCodes = $state<string[] | null>(null);
	let codesBusy = $state(false);
	let codesOpen = $state(false);
	async function rotateCodes() {
		codesBusy = true;
		const key = newIdempotencyKey();
		try {
			const out = await withStepUp(() =>
				unwrap(
					api.POST('/api/v1/me/recovery-codes', {
						params: { header: { 'Idempotency-Key': key } }
					})
				)
			);
			newCodes = out.codes;
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		} finally {
			codesBusy = false;
		}
	}

	const pkColumns: Column<Passkey>[] = [
		{
			id: 'name',
			header: 'Passkey',
			cell: pkNameCell,
			sortValue: (p) => p.name,
			stack: 'title'
		},
		{
			id: 'used',
			header: 'Last Used',
			cell: pkUsedCell,
			width: '150px',
			sortValue: (p) => p.lastUsedAt ?? ''
		},
		{ id: 'backup', header: 'Synced', cell: pkBackupCell, width: '130px' },
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: pkActions,
			width: '180px',
			pin: 'end',
			stack: 'actions'
		}
	];
</script>

{#snippet pkNameCell(p: Passkey)}<NameCell
		icon="passkey"
		name={p.name}
		sub="Added {formatDateTime(p.createdAt)}"
	/>{/snippet}
{#snippet pkUsedCell(p: Passkey)}
	{#if p.lastUsedAt}<span class="num" title={formatDateTime(p.lastUsedAt)}
			>{formatRelative(p.lastUsedAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet pkBackupCell(p: Passkey)}
	{#if p.backedUp}<Badge tone="ok">Backed Up</Badge>{:else if p.backupEligible}<Badge
			>Can Sync</Badge
		>{:else}<Badge>This Device Only</Badge>{/if}
{/snippet}
{#snippet pkActions(p: Passkey)}
	<span class="acts">
		<Button
			size="sm"
			variant="ghost"
			onclick={() => {
				renaming = p;
				newName = p.name;
				renameError = null;
			}}>Rename</Button
		>
		<Button
			size="sm"
			variant="danger-soft"
			onclick={() => {
				removing = p;
				removeOpen = true;
			}}>Remove</Button
		>
	</span>
{/snippet}

<Page>
	<ProfileHeader title="Profile" />
	{#if me}
		<Card title="Account">
			<Facts
				columns={3}
				items={[
					{ label: 'Name', value: displayName(me) },
					{ label: 'Username', value: me.username, mono: true },
					{ label: 'Email', value: me.email },
					{ label: 'Signs In With', value: factorsText(me.factors) },
					{
						label: 'Sign-In Policy',
						value: session.data
							? FACTOR_POLICY[session.data.requiredFactors]
							: undefined
					},
					{ label: 'Role', value: me.owner ? 'Owner' : 'Member' }
				]}
			/>
		</Card>

		{#if me.factors.password}
			<Card title="Password" subtitle="Changing it signs out your other sessions.">
				<form onsubmit={changePassword} novalidate>
					<!-- Two columns everywhere, so every password field has the same width. -->
					<Fields columns={2}>
						<input
							type="text"
							autocomplete="username"
							value={me.username}
							hidden
							readonly
						/>
						<PasswordField
							label="Current Password"
							autocomplete="current-password"
							bind:value={current}
							required
							error={fieldErrors(pwError)['body.currentPassword']}
						/>
					</Fields>
					<div class="gap"></div>
					<Fields columns={2}>
						<PasswordField
							label="New Password"
							autocomplete="new-password"
							bind:value={next}
							required
							description="Common and breached passwords are refused."
							error={fieldErrors(pwError)['body.newPassword']}
						/>
						<PasswordField
							label="Repeat the New Password"
							autocomplete="new-password"
							bind:value={repeat}
							required
							error={mismatch}
						/>
					</Fields>
					<div class="gap"></div>
					<Fields>
						<Checkbox bind:checked={revokeTokens} label="Also Revoke My API Tokens" />
					</Fields>
					{#if pwError && !Object.keys(fieldErrors(pwError)).length}
						<Notice tone="danger" title="The password was not changed" live="alert">
							{actionError(pwError, {
								invalid_credentials: 'The current password is not right.'
							})}
						</Notice>
					{/if}
					<FormFooter>
						<Button
							type="submit"
							variant="primary"
							loading={pwBusy}
							disabled={!next || !!mismatch || !repeat}>Change Password</Button
						>
					</FormFooter>
				</form>
			</Card>
		{/if}

		<Card title="Authenticator App" subtitle="A 6-digit code asked after your password.">
			{#snippet actions()}
				{#if me.factors.totp}<Badge tone="ok" dot>On</Badge>{:else}<Badge dot>Off</Badge
					>{/if}
			{/snippet}
			{#if me.factors.totp}
				<Button variant="danger-soft" onclick={() => (totpOffOpen = true)}
					>Turn Off Authenticator App</Button
				>
			{:else if totpSetup}
				<TotpSetup ondone={() => (totpSetup = false)} />
			{:else}
				<Button variant="primary" onclick={() => (totpSetup = true)}
					>Set Up Authenticator App</Button
				>
			{/if}
		</Card>

		<Card
			title="Passkeys"
			info="Sign in with your device's fingerprint, face or PIN instead of a password."
			padding="none"
		>
			<QueryView query={passkeys} errorTitle="Your passkeys could not be loaded.">
				{#snippet children(rows)}
					{#if !rows.length}
						<p class="none muted">No passkeys yet.</p>
					{:else}
						<Table
							label="Your Passkeys"
							{rows}
							columns={pkColumns}
							rowKey={(p) => p.id}
							sort={{ column: 'name', direction: 'asc' }}
						/>
					{/if}
				{/snippet}
			</QueryView>
			<div class="add">
				{#if passkeysSupported()}
					<TextField
						label="Name of the New Passkey"
						bind:value={pkName}
						placeholder="Work laptop"
						optional
					/>
					<Button icon={Fingerprint} loading={pkBusy} onclick={addPasskey}
						>Add Passkey</Button
					>
				{:else}
					<p class="muted">
						This browser can't create passkeys here (it needs HTTPS and passkey
						support).
					</p>
				{/if}
				{#if pkError}<Notice tone="danger" title="The passkey was not added" live="alert"
						>{pkError}</Notice
					>{/if}
			</div>
		</Card>

		<Card
			title="Recovery Codes"
			subtitle="One-time codes for when your authenticator app or passkey is lost."
		>
			{#snippet actions()}
				{#if codes.data}<Badge tone={codes.data.remaining > 2 ? 'ok' : 'warn'}
						>{codes.data.remaining} left</Badge
					>{/if}
			{/snippet}
			{#if codes.data?.generatedAt}
				<p class="muted">Generated {formatDateTime(codes.data.generatedAt)}.</p>
			{/if}
			<div class="act">
				<Button icon={KeySquare} onclick={() => (codesOpen = true)}
					>Generate New Codes</Button
				>
			</div>
		</Card>

		<ConfirmDialog
			bind:open={totpOffOpen}
			title="Turn off the authenticator app?"
			consequences={[
				'Password sign-ins no longer ask for a code.',
				'Your other sessions end.'
			]}
			confirmLabel="Turn Off Authenticator App"
			tone="danger"
			onconfirm={totpOff}
		/>
		<ConfirmDialog
			bind:open={removeOpen}
			title="Remove the passkey {removing?.name ?? ''}?"
			consequences={[
				'It can no longer sign you in. Remove it from the device too.',
				'Your other sessions end.'
			]}
			confirmLabel="Remove Passkey"
			tone="danger"
			onconfirm={() => (removing ? removePasskey(removing) : undefined)}
		/>
		<ConfirmDialog
			bind:open={codesOpen}
			title="Generate new recovery codes?"
			consequences={[
				'Your current recovery codes stop working.',
				'The new codes are shown once: store them somewhere safe.'
			]}
			confirmLabel="Generate New Codes"
			onconfirm={rotateCodes}
		/>
		<Dialog
			open={!!newCodes}
			title="Your New Recovery Codes"
			dismissible={false}
			onclose={() => (newCodes = null)}
		>
			{#if newCodes}
				<SecretReveal
					secret={newCodes}
					label="recovery codes"
					filename="docker-manager-recovery-codes.txt"
					description="Each code works once. Store them apart from your password."
					confirmLabel="Done"
					onconfirm={() => {
						newCodes = null;
						toast.success('Generated new recovery codes');
						void refresh();
					}}
				/>
			{/if}
		</Dialog>
		<Dialog
			open={!!renaming}
			title="Rename {renaming?.name ?? 'Passkey'}"
			onclose={() => (renaming = null)}
		>
			<TextField label="Name" bind:value={newName} required />
			{#if renameError}<Notice tone="danger" title="Not Renamed" live="alert"
					>{renameError}</Notice
				>{/if}
			{#snippet footer()}
				<Button variant="ghost" onclick={() => (renaming = null)}>Cancel</Button>
				<Button
					variant="primary"
					disabled={!newName.trim()}
					onclick={() => renaming && renamePasskey(renaming)}>Rename Passkey</Button
				>
			{/snippet}
		</Dialog>
	{/if}
	{#if codesBusy}<span class="sr-only" aria-live="polite">Generating codes…</span>{/if}
</Page>

<style>
	.add {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
		padding: var(--space-4) var(--space-5);
		border-top: 1px solid var(--border-subtle);
	}

	.add > :global(:first-child) {
		flex: 1 1 240px;
	}

	.gap {
		height: var(--space-4);
	}

	.none {
		padding: var(--space-4) var(--space-5) 0;
	}

	.acts {
		display: inline-flex;
		gap: var(--space-2);
	}

	.act {
		margin-top: var(--space-3);
	}
</style>
