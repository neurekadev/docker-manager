<script lang="ts">
	// Sign-in (#16): password, then a second factor when the account has one
	// (TOTP code, passkey, or a recovery code); or a username-less passkey
	// sign-in. Failures never say which part was wrong.
	import { page } from '$app/state';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Fingerprint from '@lucide/svelte/icons/fingerprint';
	import { api, unwrap, type Session } from '$lib/api/client';
	import { usePublicPage } from '$lib/auth/flow.svelte';
	import {
		credentialToJSON,
		isCancelled,
		passkeysSupported,
		requestOptions
	} from '$lib/auth/webauthn';
	import { Button, Notice, PasswordField, Skeleton, TextField, errorView } from '$lib/ui';

	const flow = usePublicPage('sign-in');
	const reason = $derived(page.url.searchParams.get('reason'));

	type Step = 'password' | 'second' | 'recovery';
	let step = $state<Step>('password');
	let username = $state('');
	let password = $state('');
	let code = $state('');
	let recovery = $state('');
	let busy = $state<string | null>(null);
	let message = $state<string | null>(null);
	let factors = $state<string[]>([]);

	// A pending sign-in (reload during the second step) resumes there.
	$effect(() => {
		const s = flow.session.data;
		if (s?.state === 'second_factor_required' && step === 'password') {
			factors = s.factors;
			step = 'second';
		}
	});

	function explain(e: unknown, what: 'password' | 'code' | 'passkey' | 'recovery'): string {
		const v = errorView(e);
		switch (v.code) {
			case 'invalid_credentials':
				return what === 'password'
					? 'The username or password is not right.'
					: what === 'code'
						? 'That code did not work. Codes change every 30 seconds; enter the current one.'
						: what === 'recovery'
							? 'That recovery code did not work. Each code works once.'
							: 'That passkey did not work for this Docker Manager.';
			case 'rate_limited':
				return 'Too many attempts. Wait a minute, then try again.';
			case 'no_pending_flow':
				return 'The sign-in timed out. Enter your password again.';
			default:
				return v.message;
		}
	}

	async function after(s: Session) {
		if (s.state === 'second_factor_required') {
			factors = s.factors;
			step = 'second';
			message = null;
			return;
		}
		await flow.proceed(s);
	}

	async function signIn(e: SubmitEvent) {
		e.preventDefault();
		busy = 'password';
		message = null;
		try {
			await after(
				await unwrap(
					api.POST('/api/v1/auth/session', {
						body: { username: username.trim(), password }
					})
				)
			);
		} catch (err) {
			message = explain(err, 'password');
			if (errorView(err).code === 'no_pending_flow') step = 'password';
		} finally {
			busy = null;
			password = '';
		}
	}

	async function submitCode(e: SubmitEvent) {
		e.preventDefault();
		busy = 'code';
		message = null;
		try {
			await after(
				await unwrap(
					api.POST('/api/v1/auth/session', {
						body: { totpCode: code.replace(/\s/g, '') }
					})
				)
			);
		} catch (err) {
			message = explain(err, 'code');
			if (errorView(err).code === 'no_pending_flow') step = 'password';
		} finally {
			busy = null;
			code = '';
		}
	}

	async function submitRecovery(e: SubmitEvent) {
		e.preventDefault();
		busy = 'recovery';
		message = null;
		try {
			await after(
				await unwrap(
					api.POST('/api/v1/auth/recovery-codes/redemptions', {
						body: { code: recovery.trim() }
					})
				)
			);
		} catch (err) {
			message = explain(err, 'recovery');
			if (errorView(err).code === 'no_pending_flow') step = 'password';
		} finally {
			busy = null;
		}
	}

	async function passkey() {
		busy = 'passkey';
		message = null;
		try {
			const options = await unwrap(
				api.POST('/api/v1/auth/passkeys/authentication-options', {
					body: { purpose: 'sign_in' }
				})
			);
			const cred = (await navigator.credentials.get({
				publicKey: requestOptions(options)
			})) as PublicKeyCredential | null;
			if (!cred) return;
			const s = await unwrap(
				api.POST('/api/v1/auth/passkeys/authentication-verifications', {
					body: { credential: credentialToJSON(cred) }
				})
			);
			await after(s);
		} catch (err) {
			if (!isCancelled(err)) message = explain(err, 'passkey');
		} finally {
			busy = null;
		}
	}
</script>

<svelte:head><title>Sign in · Docker Manager</title></svelte:head>

<div class="stack">
	<header>
		<h1>Sign in to Docker Manager</h1>
		{#if step === 'second'}
			<p class="lead">Confirm it's you with your second factor.</p>
		{:else if step === 'recovery'}
			<p class="lead">Enter one of your recovery codes. Each code works once.</p>
		{/if}
	</header>

	{#if reason === 'expired' && step === 'password'}
		<Notice tone="info" title="Your session ended"
			>Sign in again to continue where you were.</Notice
		>
	{:else if reason === 'signed-out' && step === 'password'}
		<Notice tone="info" title="You signed out" live="status" />
	{/if}
	{#if message}<Notice tone="danger" title={message} live="alert" />{/if}

	{#if !flow.ready}
		<Skeleton lines={3} height="36px" />
	{:else if step === 'password'}
		<form onsubmit={signIn}>
			<TextField
				label="Username"
				bind:value={username}
				autocomplete="username webauthn"
				autocapitalize="off"
				spellcheck="false"
				required
			/>
			<PasswordField
				label="Password"
				bind:value={password}
				autocomplete="current-password"
				required
			/>
			<Button
				type="submit"
				variant="primary"
				block
				loading={busy === 'password'}
				disabled={!!busy || !username.trim() || !password}
			>
				Sign in
			</Button>
		</form>
		{#if passkeysSupported()}
			<div class="or" aria-hidden="true"><span>or</span></div>
			<Button
				icon={Fingerprint}
				block
				loading={busy === 'passkey'}
				disabled={!!busy}
				onclick={passkey}>Sign in with a passkey</Button
			>
		{/if}
		<p class="help">
			Forgot your password? Ask the owner of this Docker Manager for a reset link.
		</p>
	{:else if step === 'second'}
		{#if factors.includes('totp')}
			<form onsubmit={submitCode}>
				<TextField
					label="Authenticator code"
					bind:value={code}
					inputmode="numeric"
					autocomplete="one-time-code"
					pattern="[0-9 ]*"
					maxlength={8}
					mono
					required
				/>
				<Button
					type="submit"
					variant="primary"
					block
					loading={busy === 'code'}
					disabled={!!busy || code.replace(/\s/g, '').length < 6}
				>
					Verify
				</Button>
			</form>
		{/if}
		{#if factors.includes('passkey') && passkeysSupported()}
			<Button
				icon={Fingerprint}
				block
				variant={factors.includes('totp') ? 'secondary' : 'primary'}
				loading={busy === 'passkey'}
				disabled={!!busy}
				onclick={passkey}
			>
				Use a passkey
			</Button>
		{/if}
		<div class="links">
			{#if factors.includes('recovery_code')}
				<button
					type="button"
					class="link"
					onclick={() => ((step = 'recovery'), (message = null))}
					>Use a recovery code</button
				>
			{/if}
			<button
				type="button"
				class="link"
				onclick={() => ((step = 'password'), (message = null))}>Start over</button
			>
		</div>
	{:else}
		<form onsubmit={submitRecovery}>
			<TextField
				label="Recovery code"
				bind:value={recovery}
				autocomplete="off"
				autocapitalize="off"
				spellcheck="false"
				mono
				required
			/>
			<Button
				type="submit"
				variant="primary"
				block
				icon={KeyRound}
				loading={busy === 'recovery'}
				disabled={!!busy || !recovery.trim()}
			>
				Use recovery code
			</Button>
		</form>
		<div class="links">
			<button type="button" class="link" onclick={() => ((step = 'second'), (message = null))}
				>Back</button
			>
		</div>
	{/if}
</div>

<style>
	.stack,
	form {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.stack {
		gap: var(--space-5);
	}

	h1 {
		font-size: 22px;
		line-height: 28px;
	}

	.lead {
		margin-top: var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-control);
	}

	.or {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.or::before,
	.or::after {
		content: '';
		flex: 1;
		height: 1px;
		background: var(--border-subtle);
	}

	.help {
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-align: center;
	}

	.links {
		display: flex;
		justify-content: center;
		gap: var(--space-4);
	}

	.link {
		padding: 0;
		border: 0;
		background: none;
		color: var(--accent-text);
		font-size: var(--text-body);
	}
</style>
