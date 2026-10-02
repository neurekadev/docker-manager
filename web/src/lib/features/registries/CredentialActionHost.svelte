<script lang="ts">
	// Rotate, revoke, delete and test a registry connection or a Git
	// credential (#19, #33). Secrets are write-only; rotation takes effect
	// for jobs dispatched afterwards; a revoked credential makes the jobs
	// that need it fail (never an anonymous fallback). Every change needs a
	// step-up and sends If-Match with the loaded revision.
	import { useQueryClient } from '@tanstack/svelte-query';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleX from '@lucide/svelte/icons/circle-x';
	import { api, unwrap, type Schema } from '$lib/api/client';
	import { queryKeys, type GitCredential, type RegistryConnection } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import {
		Button,
		ConfirmDialog,
		DestructiveConfirm,
		Dialog,
		PasswordField,
		TextField,
		errorMessage,
		fieldError,
		formatDateTime,
		toast
	} from '$lib/ui';
	import { registryGuidance, sentence } from '$lib/features/resources/refusals';

	type Target =
		{ kind: 'registry'; item: RegistryConnection } | { kind: 'git'; item: GitCredential };
	type Action = 'rotate' | 'revoke' | 'delete' | 'test';

	const queryClient = useQueryClient();
	let target = $state<Target | null>(null);
	let rotateOpen = $state(false);
	let revokeOpen = $state(false);
	let deleteOpen = $state(false);
	let testOpen = $state(false);

	let secret = $state('');
	let username = $state('');
	let busy = $state(false);
	let failure = $state<unknown>(null);

	let reference = $state('');
	let ref = $state('');
	let testResult = $state<Schema<'RegistryConnectionTest'> | Schema<'GitCredentialTest'> | null>(
		null
	);

	export function request(t: Target, action: Action) {
		target = t;
		failure = null;
		secret = '';
		username = t.item.username ?? '';
		testResult = null;
		if (action === 'rotate') rotateOpen = true;
		if (action === 'revoke') revokeOpen = true;
		if (action === 'delete') deleteOpen = true;
		if (action === 'test') {
			reference = '';
			ref = '';
			testOpen = true;
		}
	}

	const etag = (t: Target) => `"${t.item.revision ?? 0}"`;
	const refresh = () => queryClient.invalidateQueries({ queryKey: queryKeys.registries.all });

	async function rotate() {
		const t = target!;
		busy = true;
		failure = null;
		try {
			await withStepUp(() =>
				t.kind === 'registry'
					? unwrap(
							api.POST('/api/v1/registries/{registryId}/credential-rotations', {
								params: {
									path: { registryId: t.item.id },
									header: { 'If-Match': etag(t) }
								},
								body: {
									secret,
									username:
										username.trim() && username.trim() !== t.item.username
											? username.trim()
											: undefined
								}
							})
						)
					: unwrap(
							api.PATCH('/api/v1/git-credentials/{credentialId}', {
								params: {
									path: { credentialId: t.item.id },
									header: { 'If-Match': etag(t) }
								},
								body: {
									secret,
									username:
										username.trim() && username.trim() !== t.item.username
											? username.trim()
											: undefined
								}
							})
						)
			);
			secret = '';
			toast.success(`Rotated the credential of ${t.item.name}`, {
				body: 'Jobs that start from now on use the new one.'
			});
			void refresh();
			rotateOpen = false;
		} catch (e) {
			failure = e;
		} finally {
			busy = false;
		}
	}

	async function revoke() {
		const t = target!;
		await withStepUp(() =>
			t.kind === 'registry'
				? unwrap(
						api.PATCH('/api/v1/registries/{registryId}', {
							params: {
								path: { registryId: t.item.id },
								header: { 'If-Match': etag(t) }
							},
							body: { status: 'revoked' }
						})
					)
				: unwrap(
						api.PATCH('/api/v1/git-credentials/{credentialId}', {
							params: {
								path: { credentialId: t.item.id },
								header: { 'If-Match': etag(t) }
							},
							body: { status: 'revoked' }
						})
					)
		);
		toast.success(`Revoked ${t.item.name}`);
		void refresh();
	}

	async function remove() {
		const t = target!;
		await withStepUp(() =>
			t.kind === 'registry'
				? unwrap(
						api.DELETE('/api/v1/registries/{registryId}', {
							params: {
								path: { registryId: t.item.id },
								header: { 'If-Match': etag(t) }
							}
						})
					)
				: unwrap(
						api.DELETE('/api/v1/git-credentials/{credentialId}', {
							params: {
								path: { credentialId: t.item.id },
								header: { 'If-Match': etag(t) }
							}
						})
					)
		);
		toast.success(`Deleted ${t.item.name}`);
		void refresh();
	}

	async function test() {
		const t = target!;
		busy = true;
		failure = null;
		testResult = null;
		try {
			testResult =
				t.kind === 'registry'
					? await unwrap(
							api.POST('/api/v1/registries/{registryId}/connection-tests', {
								params: { path: { registryId: t.item.id } },
								body: { imageReference: reference.trim() }
							})
						)
					: await unwrap(
							api.POST('/api/v1/git-credentials/{credentialId}/connection-tests', {
								params: { path: { credentialId: t.item.id } },
								body: {
									repositoryUrl: reference.trim(),
									ref: ref.trim() || undefined
								}
							})
						);
			void refresh();
		} catch (e) {
			failure = e;
		} finally {
			busy = false;
		}
	}

	const isRegistryTest = (r: typeof testResult): r is Schema<'RegistryConnectionTest'> =>
		!!r && 'reference' in r;
	const secretLabel = $derived(
		target?.kind === 'registry' && target.item.credentialType === 'password'
			? 'New Password'
			: 'New Access Token'
	);
	const testPlaceholder = $derived.by(() => {
		if (target?.kind !== 'registry') return '';
		const repo = target.item.repositoryPattern?.replace('*', 'app') || 'org/app';
		return `${target.item.host}/${repo}:latest`;
	});
	const conflict = $derived((failure as { status?: number } | null)?.status === 412);
</script>

{#if target}
	<Dialog
		bind:open={rotateOpen}
		title="Rotate the Credential of {target.item.name}"
		size="sm"
		dismissible={!busy}
	>
		<form
			id="rotate-form"
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				if (secret) void rotate();
			}}
		>
			<p class="text">
				The new credential replaces the stored one{target.item.status === 'revoked'
					? ' and re-activates the connection'
					: ''}. Jobs already running keep what they got; jobs that start afterwards use
				the new one.
			</p>
			<TextField
				label="Username"
				mono
				bind:value={username}
				autocomplete="off"
				description="Change it only if the new credential belongs to another account."
			/>
			<PasswordField
				label={secretLabel}
				autocomplete="new-password"
				required
				bind:value={secret}
				description="Write-only: never shown again."
				error={fieldError(failure, 'body.secret')}
			/>
			{#if failure && !fieldError(failure, 'body.secret')}
				<p class="error" role="alert">
					{conflict
						? 'Someone changed it meanwhile. Close the dialog and try again.'
						: errorMessage(failure)}
				</p>
			{/if}
		</form>
		{#snippet footer()}
			<Button variant="ghost" onclick={() => (rotateOpen = false)} disabled={busy}
				>Cancel</Button
			>
			<Button
				type="submit"
				form="rotate-form"
				variant="primary"
				loading={busy}
				disabled={!secret}>Rotate Credential</Button
			>
		{/snippet}
	</Dialog>

	<ConfirmDialog
		bind:open={revokeOpen}
		title="Revoke {target.item.name}?"
		consequences={[
			'The stored credential is erased; the connection stays so matching jobs fail visibly.',
			target.kind === 'registry'
				? 'Pulls, deploys and update checks of matching images fail until you rotate a new credential in. Docker Manager never falls back to anonymous access.'
				: 'Builds from matching repositories fail until you rotate a new token in.'
		]}
		confirmLabel="Revoke Credential"
		tone="danger"
		onconfirm={revoke}
	/>

	<DestructiveConfirm
		bind:open={deleteOpen}
		title="Delete {target.item.name}?"
		consequences={[
			'The connection and its credential are deleted.',
			'Queued jobs that name it fail when they start (credential unavailable).',
			target.kind === 'registry'
				? 'Matching images pull anonymously afterwards unless another connection matches.'
				: 'Matching repositories are cloned without credentials afterwards unless another credential matches.'
		]}
		confirmText={target.item.name}
		confirmLabel={target.kind === 'registry' ? 'Delete Connection' : 'Delete Credential'}
		onconfirm={remove}
	/>

	<Dialog bind:open={testOpen} title="Test {target.item.name}" size="md" dismissible={!busy}>
		<form
			id="test-form"
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				if (reference.trim()) void test();
			}}
		>
			{#if target.kind === 'registry'}
				<TextField
					label="Image Reference"
					mono
					required
					bind:value={reference}
					placeholder={testPlaceholder}
					description="An image on {target.item
						.host} this connection may read. Docker Manager asks the registry for its digest; nothing is pulled."
					error={fieldError(failure, 'body.imageReference')}
				/>
			{:else}
				<TextField
					label="Repository URL"
					mono
					required
					bind:value={reference}
					placeholder="https://{target.item.host}/{'pathPrefix' in target.item &&
					target.item.pathPrefix
						? target.item.pathPrefix
						: 'org'}/app.git"
					description="Docker Manager lists the repository's refs with the token; nothing is cloned."
					error={fieldError(failure, 'body.repositoryUrl')}
				/>
				<TextField
					label="Ref"
					mono
					bind:value={ref}
					placeholder="main"
					description="Optional. Also resolve this branch or tag."
				/>
			{/if}
			{#if failure && !fieldError(failure, 'body.imageReference') && !fieldError(failure, 'body.repositoryUrl')}
				<p class="error" role="alert">{errorMessage(failure)}</p>
			{/if}
			{#if testResult}
				<div class="result {testResult.ok ? 'ok' : 'bad'}" role="status">
					{#if testResult.ok}
						<CircleCheck size={18} aria-hidden="true" />
						<div>
							<p class="title">The connection works.</p>
							{#if isRegistryTest(testResult)}
								<p class="mono detail">{testResult.digest}</p>
							{:else}
								<p class="detail">
									{testResult.refCount} refs{testResult.commit
										? `; ${testResult.ref ?? 'HEAD'} is ${testResult.commit.slice(0, 12)}`
										: ''}.
								</p>
							{/if}
							<p class="muted detail">
								Checked {formatDateTime(testResult.checkedAt)}
							</p>
						</div>
					{:else}
						<CircleX size={18} aria-hidden="true" />
						<div>
							<p class="title">
								{testResult.message
									? sentence(testResult.message)
									: 'The test failed.'}
							</p>
							{#if registryGuidance(testResult.errorClass)}<p class="detail">
									{registryGuidance(testResult.errorClass)}
								</p>{/if}
							{#if isRegistryTest(testResult) && testResult.retryAfterSeconds}
								<p class="detail">
									The registry asks to wait {testResult.retryAfterSeconds} seconds before
									trying again.
								</p>
							{/if}
							{#if testResult.errorClass}<p class="muted detail">
									{testResult.errorClass}
								</p>{/if}
						</div>
					{/if}
				</div>
			{/if}
		</form>
		{#snippet footer()}
			<Button variant="ghost" onclick={() => (testOpen = false)} disabled={busy}>Close</Button
			>
			<Button
				type="submit"
				form="test-form"
				variant="primary"
				loading={busy}
				disabled={!reference.trim()}>Test Connection</Button
			>
		{/snippet}
	</Dialog>
{/if}

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.text {
		color: var(--text-default);
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}

	.result {
		display: flex;
		gap: var(--space-3);
		padding: var(--space-3);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-md);
	}

	.result.ok {
		border-color: var(--ok-border);
		background: var(--ok-soft);
		color: var(--ok);
	}

	.result.bad {
		border-color: var(--danger-border);
		background: var(--danger-soft);
		color: var(--danger);
	}

	.result .title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.detail {
		margin-top: 2px;
		color: var(--text-default);
		font-size: var(--text-caption);
		overflow-wrap: anywhere;
	}
</style>
