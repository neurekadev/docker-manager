<script lang="ts">
	// Identity check for sensitive changes (#16 step-up). Opened by
	// withStepUp() when the manager answers step_up_required. It asks for
	// exactly one factor (verify.ts, #186): a passkey when the account has
	// one (the browser's prompt opens at once), else the authenticator code,
	// else the password. The user can switch to any other factor the
	// account has (the password is always a fallback); a switch between
	// passkey and code is remembered. Success renews the session; the
	// waiting change is retried by its caller.
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import Fingerprint from '@lucide/svelte/icons/fingerprint';
	import { api, unwrap, type Account } from '$lib/api/client';
	import { queryKeys } from '$lib/api/queries';
	import Button from '$lib/ui/Button.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';
	import Notice from '$lib/ui/Notice.svelte';
	import PasswordField from '$lib/ui/PasswordField.svelte';
	import TextField from '$lib/ui/TextField.svelte';
	import { errorView } from '$lib/ui/errors';
	import { stepUp as defaultPrompt, type StepUpPrompt } from './stepup.svelte';
	import { initialMethod, rememberMethod, stepUpMethods, type VerifyMethod } from './verify';
	import { credentialToJSON, isCancelled, passkeysSupported, requestOptions } from './webauthn';

	let { user, prompt = defaultPrompt }: { user: Account; prompt?: StepUpPrompt } = $props();

	const qc = useQueryClient();
	let password = $state('');
	let totpCode = $state('');
	let busy = $state(false);
	let message = $state<string | null>(null);
	let method = $state<VerifyMethod | null>(null);
	let ceremony: AbortController | null = null;

	const methods = $derived(stepUpMethods(user.factors, passkeysSupported()));
	const SWITCH: Record<VerifyMethod, string> = {
		passkey: 'Use a passkey instead',
		totp: 'Use authenticator code instead',
		password: 'Use your password instead'
	};

	let open = $state(false);
	$effect(() => {
		open = prompt.open;
		if (prompt.open) {
			untrack(() => {
				password = '';
				totpCode = '';
				message = null;
				method = initialMethod(methods);
				if (method === 'passkey') void withPasskey();
			});
		} else {
			ceremony?.abort();
		}
	});

	function explain(e: unknown, used: VerifyMethod): string {
		const v = errorView(e);
		if (v.code === 'invalid_credentials')
			return used === 'passkey'
				? 'That passkey did not work for this Docker Manager.'
				: used === 'totp'
					? 'That code did not work. Codes change every 30 seconds; enter the current one.'
					: 'The password is not right. Try again.';
		if (v.code === 'rate_limited') return 'Too many attempts. Wait a minute, then try again.';
		return v.message;
	}

	function switchTo(m: VerifyMethod) {
		ceremony?.abort();
		ceremony = null;
		busy = false;
		method = m;
		message = null;
		rememberMethod(m);
		if (m === 'passkey') void withPasskey();
	}

	async function done() {
		await qc.invalidateQueries({ queryKey: queryKeys.session });
		prompt.settle(true);
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		const used = method;
		if (used !== 'password' && used !== 'totp') return;
		busy = true;
		message = null;
		try {
			await unwrap(
				api.POST('/api/v1/auth/step-ups', {
					body: used === 'totp' ? { totpCode: totpCode.replace(/\s/g, '') } : { password }
				})
			);
			await done();
		} catch (err) {
			message = explain(err, used);
		} finally {
			busy = false;
		}
	}

	async function withPasskey() {
		ceremony?.abort();
		const abort = new AbortController();
		ceremony = abort;
		busy = true;
		message = null;
		try {
			const options = await unwrap(
				api.POST('/api/v1/auth/passkeys/authentication-options', {
					body: { purpose: 'step_up' }
				})
			);
			const cred = (await navigator.credentials.get({
				publicKey: requestOptions(options),
				signal: abort.signal
			})) as PublicKeyCredential | null;
			if (!cred || abort.signal.aborted) return;
			await unwrap(
				api.POST('/api/v1/auth/step-ups', { body: { credential: credentialToJSON(cred) } })
			);
			await done();
		} catch (err) {
			if (!isCancelled(err) && !abort.signal.aborted) message = explain(err, 'passkey');
		} finally {
			if (ceremony === abort) {
				ceremony = null;
				busy = false;
			}
		}
	}
</script>

<Dialog
	bind:open
	title="Confirm it's you"
	description="This change needs a recent sign-in. Confirm your identity to continue; it stays confirmed for 10 minutes."
	size="sm"
	onclose={() => {
		if (prompt.open) prompt.settle(false);
	}}
>
	{#if message}
		<Notice tone="danger" title="Not confirmed" live="alert">{message}</Notice>
	{/if}
	{#if method === 'passkey'}
		<div class="form">
			<p class="lead">Use your passkey to continue.</p>
			<Button
				variant="primary"
				icon={Fingerprint}
				loading={busy}
				disabled={busy}
				onclick={withPasskey}
				block>Confirm with a passkey</Button
			>
		</div>
	{:else if method === 'totp'}
		<form class="form" onsubmit={submit} novalidate>
			<TextField
				label="Authenticator code"
				description="The current 6-digit code from your authenticator app."
				bind:value={totpCode}
				inputmode="numeric"
				autocomplete="one-time-code"
				required
			/>
			<Button
				type="submit"
				variant="primary"
				loading={busy}
				disabled={!totpCode.trim() || busy}
				block>Confirm</Button
			>
		</form>
	{:else if method === 'password'}
		<form class="form" onsubmit={submit} novalidate>
			<input type="text" autocomplete="username" value={user.username} hidden readonly />
			<PasswordField
				label="Password"
				autocomplete="current-password"
				bind:value={password}
				required
			/>
			<Button
				type="submit"
				variant="primary"
				loading={busy}
				disabled={!password || busy}
				block>Confirm</Button
			>
		</form>
	{:else}
		<Notice tone="warn" title="Passkeys don't work in this browser"
			>Your account confirms changes with a passkey. Open Docker Manager in a browser that
			supports passkeys to continue.</Notice
		>
	{/if}
	{#each methods.filter((m) => m !== method) as other (other)}
		<Button variant="ghost" onclick={() => switchTo(other)} block>{SWITCH[other]}</Button>
	{/each}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
		margin-bottom: var(--space-3);
	}
	.lead {
		margin: 0;
		color: var(--text-muted);
	}
</style>
