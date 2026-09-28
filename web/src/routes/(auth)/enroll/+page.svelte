<script lang="ts">
	// Required-factor enrollment (#16): a limited session may only add the
	// sign-in factors the instance policy requires. TOTP: the otpauth URI is
	// rendered as a QR code in the browser (never sent anywhere else) with
	// the secret for manual entry, confirmed by a code. Passkeys: registered
	// with navigator.credentials.create(). Afterwards the user may save
	// recovery codes (shown once).
	import { useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { routes } from '$lib/routes';
	import Fingerprint from '@lucide/svelte/icons/fingerprint';
	import Smartphone from '@lucide/svelte/icons/smartphone';
	import { api, unwrap, type Session } from '$lib/api/client';
	import { queryKeys } from '$lib/api/queries';
	import { usePublicPage } from '$lib/auth/flow.svelte';
	import AuthHeader from '$lib/features/auth/AuthHeader.svelte';
	import { qrPath } from '$lib/auth/qr';
	import {
		creationOptions,
		credentialToJSON,
		isCancelled,
		passkeysSupported
	} from '$lib/auth/webauthn';
	import {
		Button,
		CopyButton,
		Notice,
		SecretReveal,
		Skeleton,
		TextField,
		errorView,
		formatDateTime
	} from '$lib/ui';

	const flow = usePublicPage('enroll');
	const qc = useQueryClient();
	const session = $derived(flow.session.data);
	const missing = $derived(session?.missingFactors ?? []);
	const policy = $derived(session?.requiredFactors ?? 'none');

	let totp = $state<{ secret: string; uri: string; expiresAt: string } | null>(null);
	let code = $state('');
	let passkeyName = $state('');
	let busy = $state<string | null>(null);
	let message = $state<string | null>(null);
	let done = $state<Session | null>(null);
	let codes = $state<string[] | null>(null);

	const policyText = $derived(
		{
			totp: 'an authenticator app (TOTP)',
			passkey: 'a passkey',
			either: 'an authenticator app or a passkey',
			both: 'an authenticator app and a passkey',
			none: 'a second sign-in factor'
		}[policy]
	);
	const qr = $derived(totp ? qrPath(totp.uri) : null);

	async function record(s: Session) {
		// Fully enrolled: offer recovery codes first; flow.proceed(done) then
		// records the session and leaves (setting it now would redirect).
		if (s.state === 'authenticated') done = s;
		else qc.setQueryData(queryKeys.session, s);
	}

	async function switchUser() {
		try {
			await api.DELETE('/api/v1/auth/session');
		} finally {
			qc.setQueryData(queryKeys.session, null);
			await goto(routes.signIn(), { replaceState: true });
		}
	}

	async function startTotp() {
		busy = 'totp';
		message = null;
		try {
			totp = await unwrap(api.POST('/api/v1/auth/totp/enrollments'));
		} catch (e) {
			message = errorView(e).message;
		} finally {
			busy = null;
		}
	}

	async function verifyTotp(e: SubmitEvent) {
		e.preventDefault();
		busy = 'verify';
		message = null;
		try {
			const s = await unwrap(
				api.POST('/api/v1/auth/totp/enrollments/verifications', {
					body: { code: code.replace(/\s/g, '') }
				})
			);
			totp = null;
			await record(s);
		} catch (err) {
			const v = errorView(err);
			message =
				v.code === 'invalid_credentials'
					? 'That code did not match. Check the time on your phone and enter the current code.'
					: v.code === 'no_pending_flow'
						? 'The setup timed out. Start again.'
						: v.message;
			if (v.code === 'no_pending_flow') totp = null;
		} finally {
			busy = null;
			code = '';
		}
	}

	async function addPasskey() {
		busy = 'passkey';
		message = null;
		try {
			const options = await unwrap(api.POST('/api/v1/auth/passkeys/registration-options'));
			const cred = (await navigator.credentials.create({
				publicKey: creationOptions(options)
			})) as PublicKeyCredential | null;
			if (!cred) return;
			const out = await unwrap(
				api.POST('/api/v1/auth/passkeys/registration-verifications', {
					body: {
						name: passkeyName.trim() || undefined,
						credential: credentialToJSON(cred)
					}
				})
			);
			await record(out.session);
		} catch (e) {
			if (!isCancelled(e)) message = errorView(e).message;
		} finally {
			busy = null;
		}
	}

	async function createCodes() {
		busy = 'codes';
		message = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/me/recovery-codes', {
					params: { header: { 'Idempotency-Key': crypto.randomUUID() } }
				})
			);
			codes = out.codes;
		} catch (e) {
			const v = errorView(e);
			message =
				v.code === 'step_up_required'
					? 'Create recovery codes later under Profile, in the account menu.'
					: v.message;
		} finally {
			busy = null;
		}
	}

	async function finish() {
		await flow.proceed(done!);
	}
</script>

<svelte:head><title>Add a sign-in factor · Docker Manager</title></svelte:head>

<div class="stack">
	<AuthHeader
		title={done ? 'You are all set' : 'Add a sign-in factor'}
		lead={done
			? undefined
			: `This Docker Manager requires ${policyText} before you can continue.`}
	/>

	{#if message}<Notice tone="danger" title={message} live="alert" />{/if}

	{#if !flow.ready || !session}
		<Skeleton lines={3} height="36px" />
	{:else if done}
		{#if codes}
			<SecretReveal
				secret={codes}
				label="recovery codes"
				filename="docker-manager-recovery-codes.txt"
				description="Each code signs you in once when your authenticator or passkey is not at hand."
				confirmLabel="Continue to Docker Manager"
				onconfirm={finish}
			/>
		{:else}
			<p class="lead">
				Recovery codes let you sign in when your phone or passkey is not at hand. Docker
				Manager shows them only once.
			</p>
			<div class="row">
				<Button variant="primary" loading={busy === 'codes'} onclick={createCodes}
					>Create recovery codes</Button
				>
				<Button variant="ghost" onclick={finish}>Skip for now</Button>
			</div>
		{/if}
	{:else}
		{#if session.enrollmentDeadline}
			<p class="deadline">
				Add it before {formatDateTime(session.enrollmentDeadline)}; after that the owner
				must reset your account.
			</p>
		{/if}

		{#if missing.includes('totp')}
			<section class="factor" aria-labelledby="totp-title">
				<h2 id="totp-title">
					<Smartphone size={18} strokeWidth={1.75} aria-hidden="true" /> Authenticator app
				</h2>
				{#if !totp}
					<p class="muted">
						Use an app such as your password manager or an authenticator app to create
						sign-in codes.
					</p>
					<Button variant="primary" loading={busy === 'totp'} onclick={startTotp}
						>Set up an authenticator app</Button
					>
				{:else}
					<p class="muted">Scan the code with your app, then enter the code it shows.</p>
					{#if qr}
						<svg
							class="qr"
							viewBox="0 0 {qr.size} {qr.size}"
							role="img"
							aria-label="QR code for your authenticator app"
							shape-rendering="crispEdges"
						>
							<rect class="qr-light" width={qr.size} height={qr.size} />
							<path class="qr-dark" d={qr.path} />
						</svg>
					{/if}
					<div class="secret">
						<span class="muted">Or enter this key by hand</span>
						<span class="key mono">{totp.secret}</span>
						<CopyButton value={totp.secret} what="setup key" />
					</div>
					<form onsubmit={verifyTotp}>
						<TextField
							label="Code from the app"
							bind:value={code}
							inputmode="numeric"
							autocomplete="one-time-code"
							maxlength={8}
							mono
							required
						/>
						<Button
							type="submit"
							variant="primary"
							loading={busy === 'verify'}
							disabled={code.replace(/\s/g, '').length < 6}>Confirm code</Button
						>
					</form>
				{/if}
			</section>
		{/if}

		{#if missing.includes('passkey')}
			<section class="factor" aria-labelledby="passkey-title">
				<h2 id="passkey-title">
					<Fingerprint size={18} strokeWidth={1.75} aria-hidden="true" /> Passkey
				</h2>
				{#if passkeysSupported()}
					<p class="muted">
						Use your device's screen lock, a security key or your password manager.
					</p>
					<TextField
						label="Passkey name"
						description="Optional, e.g. “Work laptop”."
						bind:value={passkeyName}
					/>
					<Button
						variant={missing.includes('totp') ? 'secondary' : 'primary'}
						icon={Fingerprint}
						loading={busy === 'passkey'}
						onclick={addPasskey}
					>
						Add a passkey
					</Button>
				{:else}
					<Notice tone="warn" title="Passkeys need a secure connection">
						Open Docker Manager over HTTPS on its public address to add a passkey.
					</Notice>
				{/if}
			</section>
		{/if}

		<button type="button" class="link" onclick={switchUser}>Sign in as someone else</button>
	{/if}
</div>

<style>
	.stack,
	form,
	.factor {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.stack {
		gap: var(--space-5);
	}

	h2 {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-section);
		line-height: var(--leading-section);
	}

	.lead {
		margin-top: var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-control);
		line-height: 22px;
	}

	.deadline {
		color: var(--warn);
	}

	.factor + .factor {
		padding-top: var(--space-5);
		border-top: 1px solid var(--border-subtle);
	}

	.qr {
		width: 184px;
		height: 184px;
		align-self: center;
		border-radius: var(--radius-md);
	}

	/* Dark modules on a light field: scanners read the usual contrast. */
	.qr-light {
		fill: var(--text-strong);
	}

	.qr-dark {
		fill: var(--surface-canvas);
	}

	.secret {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.key {
		color: var(--text-strong);
		word-break: break-all;
	}

	.row {
		display: flex;
		gap: var(--space-2);
	}

	.link {
		align-self: center;
		padding: 0;
		border: 0;
		background: none;
		color: var(--accent-text);
	}
</style>
