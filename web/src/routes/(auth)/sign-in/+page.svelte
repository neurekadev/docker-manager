<script lang="ts">
	// Sign-in (#16): password, then a second factor when the account has one
	// (TOTP code, passkey, or a recovery code); or a username-less passkey
	// sign-in. Failures never say which part was wrong. The buttons stay
	// enabled (autofill may not report values before the user interacts);
	// the forms check on submit and say what is missing next to the field.
	// "Stay signed in" (when the sign-in policy offers it) applies to the
	// password and the passkey sign-in alike; the browser remembers the last
	// choice (stay.ts). The second step keeps the choice of the first and
	// asks for one factor at a time (verify.ts, #186): a passkey first, its
	// browser prompt opening at once, with a switch to the authenticator
	// code that the browser remembers.
	import { tick, untrack } from 'svelte';
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
	import {
		initialMethod,
		rememberMethod,
		secondFactorMethods,
		type VerifyMethod
	} from '$lib/auth/verify';
	import AuthHeader from '$lib/features/auth/AuthHeader.svelte';
	import { readStaySignedIn, rememberStaySignedIn } from '$lib/features/auth/stay';
	import { isValid, requiredErrors, submitted, untilFilled } from '$lib/features/auth/validate';
	import {
		Button,
		Checkbox,
		Notice,
		PasswordField,
		Skeleton,
		TextField,
		errorView
	} from '$lib/ui';

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
	let method = $state<VerifyMethod | null>(null);
	/** The passkey prompt in progress: aborted when the user moves on. */
	let ceremony: AbortController | null = null;
	const methods = $derived(secondFactorMethods(factors, passkeysSupported()));
	let stay = $state(readStaySignedIn());
	const stayOffered = $derived(!!flow.setup.data?.staySignedInAllowed);
	/** The choice sent with a sign-in: only when the policy offers it. */
	const staySignedIn = $derived(stayOffered && stay);
	type Field = 'username' | 'password' | 'code' | 'recovery';
	let invalid = $state<Partial<Record<Field, string>>>({});
	const MISSING: Record<Field, string> = {
		username: 'Enter your username.',
		password: 'Enter your password.',
		code: 'Enter the code from your authenticator app.',
		recovery: 'Enter one of your recovery codes.'
	};

	/**
	 * Reads the submitted form (autofilled values too), says next to each
	 * empty required field what is missing and moves focus to the first.
	 */
	function check<F extends Field>(e: SubmitEvent, bound: Record<F, string>) {
		const form = e.currentTarget as HTMLFormElement | null;
		const data = form ? new FormData(form) : null;
		const values = {} as Record<F, string>;
		const fields = {} as Record<F, readonly [string, string]>;
		for (const k of Object.keys(bound) as F[]) {
			values[k] = submitted(data, k, bound[k]);
			fields[k] = [values[k], MISSING[k]];
		}
		invalid = requiredErrors(fields);
		const ok = isValid(invalid);
		if (!ok)
			void tick().then(() =>
				form?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus()
			);
		return { ok, values };
	}

	// A pending sign-in (reload during the second step) resumes there.
	$effect(() => {
		const s = flow.session.data;
		if (s?.state === 'second_factor_required' && step === 'password') {
			const pending = s.factors;
			untrack(() => toSecondStep(pending));
		}
	});

	/** Shows the second step with its default (or remembered) factor. */
	function toSecondStep(next: string[]) {
		factors = next;
		step = 'second';
		message = null;
		method = initialMethod(secondFactorMethods(next, passkeysSupported()));
		if (method === 'passkey') void passkey();
	}

	/** Ends a passkey prompt in progress; its late result is ignored. */
	function stopPasskey() {
		ceremony?.abort();
		ceremony = null;
		if (busy === 'passkey') busy = null;
	}

	function switchTo(m: VerifyMethod) {
		stopPasskey();
		method = m;
		message = null;
		invalid = {};
		rememberMethod(m);
		if (m === 'passkey') void passkey();
	}

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
			toSecondStep(s.factors);
			return;
		}
		await flow.proceed(s);
	}

	async function signIn(e: SubmitEvent) {
		e.preventDefault();
		const { ok, values } = check(e, { username, password });
		username = values.username;
		password = values.password;
		if (!ok) return;
		busy = 'password';
		message = null;
		try {
			await after(
				await unwrap(
					api.POST('/api/v1/auth/session', {
						body: { username: username.trim(), password, staySignedIn }
					})
				)
			);
		} catch (err) {
			message = explain(err, 'password');
			if (errorView(err).code === 'no_pending_flow') step = 'password';
		} finally {
			if (busy === 'password') busy = null;
			password = '';
		}
	}

	async function submitCode(e: SubmitEvent) {
		e.preventDefault();
		const { ok, values } = check(e, { code });
		code = values.code;
		if (!ok) return;
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
			if (busy === 'code') busy = null;
			code = '';
		}
	}

	async function submitRecovery(e: SubmitEvent) {
		e.preventDefault();
		const { ok, values } = check(e, { recovery });
		recovery = values.recovery;
		if (!ok) return;
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
			if (busy === 'recovery') busy = null;
		}
	}

	async function passkey() {
		ceremony?.abort();
		const abort = new AbortController();
		ceremony = abort;
		busy = 'passkey';
		message = null;
		try {
			const options = await unwrap(
				api.POST('/api/v1/auth/passkeys/authentication-options', {
					body: { purpose: 'sign_in' }
				})
			);
			const cred = (await navigator.credentials.get({
				publicKey: requestOptions(options),
				signal: abort.signal
			})) as PublicKeyCredential | null;
			if (!cred || abort.signal.aborted) return;
			const s = await unwrap(
				api.POST('/api/v1/auth/passkeys/authentication-verifications', {
					body: { credential: credentialToJSON(cred), staySignedIn }
				})
			);
			await after(s);
		} catch (err) {
			if (!isCancelled(err) && !abort.signal.aborted) message = explain(err, 'passkey');
		} finally {
			if (ceremony === abort) {
				ceremony = null;
				busy = null;
			}
		}
	}
</script>

<svelte:head><title>Sign In · Docker Manager</title></svelte:head>

<div class="stack">
	<AuthHeader
		title="Sign In"
		lead={step === 'second'
			? "Confirm it's you with your second factor."
			: step === 'recovery'
				? 'Each code works once.'
				: undefined}
	/>

	{#if reason === 'expired' && step === 'password'}
		<Notice tone="info" title="Your session ended"
			>Sign in again to continue where you were.</Notice
		>
	{:else if reason === 'signed-out' && step === 'password'}
		<Notice tone="info" title="You signed out" live="status" />
	{:else if reason === 'moved' && step === 'password'}
		<Notice tone="info" title="Docker Manager moved to this server" live="status"
			>Sign in with your usual account.</Notice
		>
	{/if}
	{#if message}<Notice tone="danger" title={message} live="alert" />{/if}

	{#if !flow.ready}
		<Skeleton lines={3} height="36px" />
	{:else if step === 'password'}
		<form onsubmit={signIn} novalidate>
			<TextField
				label="Username"
				name="username"
				bind:value={username}
				error={untilFilled(invalid.username, username)}
				autocomplete="username webauthn"
				autocapitalize="off"
				spellcheck="false"
				required
			/>
			<PasswordField
				label="Password"
				name="password"
				bind:value={password}
				autocomplete="current-password"
				required
				error={untilFilled(invalid.password, password)}
			/>
			{#if stayOffered}
				<Checkbox
					label="Stay Signed In"
					description="Don't use on a shared computer."
					bind:checked={stay}
					onchange={(e) => rememberStaySignedIn(e.currentTarget.checked)}
				/>
			{/if}
			<Button
				type="submit"
				variant="primary"
				block
				loading={busy === 'password'}
				disabled={!!busy}
			>
				Sign In
			</Button>
		</form>
		{#if passkeysSupported()}
			<div class="or" aria-hidden="true"><span>or</span></div>
			<Button
				icon={Fingerprint}
				block
				loading={busy === 'passkey'}
				disabled={!!busy}
				onclick={passkey}>Sign In with a Passkey</Button
			>
		{/if}
		<p class="help">Forgot your password? Ask the owner for a reset link.</p>
	{:else if step === 'second'}
		{#if method === 'passkey'}
			<Button
				icon={Fingerprint}
				block
				variant="primary"
				loading={busy === 'passkey'}
				disabled={!!busy}
				onclick={passkey}
			>
				Use a Passkey
			</Button>
		{:else if method === 'totp'}
			<form onsubmit={submitCode} novalidate>
				<TextField
					label="Authenticator Code"
					name="code"
					bind:value={code}
					error={untilFilled(invalid.code, code)}
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
					disabled={!!busy}
				>
					Verify
				</Button>
			</form>
		{/if}
		<div class="links">
			{#if method === 'passkey' && methods.includes('totp')}
				<button type="button" class="link" onclick={() => switchTo('totp')}
					>Use Authenticator Code Instead</button
				>
			{:else if method === 'totp' && methods.includes('passkey')}
				<button
					type="button"
					class="link"
					disabled={!!busy}
					onclick={() => switchTo('passkey')}>Use a Passkey Instead</button
				>
			{/if}
			{#if factors.includes('recovery_code')}
				<button
					type="button"
					class="link"
					onclick={() => (
						stopPasskey(),
						(step = 'recovery'),
						(message = null),
						(invalid = {})
					)}>Use a Recovery Code</button
				>
			{/if}
			<button
				type="button"
				class="link"
				onclick={() => (
					stopPasskey(),
					(step = 'password'),
					(message = null),
					(invalid = {})
				)}>Start Over</button
			>
		</div>
	{:else}
		<form onsubmit={submitRecovery} novalidate>
			<TextField
				label="Recovery Code"
				name="recovery"
				bind:value={recovery}
				error={untilFilled(invalid.recovery, recovery)}
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
				disabled={!!busy}
			>
				Use Recovery Code
			</Button>
		</form>
		<div class="links">
			<button
				type="button"
				class="link"
				onclick={() => ((step = 'second'), (message = null), (invalid = {}))}>Back</button
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
