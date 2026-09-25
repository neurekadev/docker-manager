<script lang="ts">
	// Identity check for sensitive changes (#16 step-up). Opened by
	// withStepUp() when the manager answers step_up_required: the password
	// (plus a TOTP code when the account uses TOTP) or a passkey. Success
	// renews the session; the waiting change is retried by its caller.
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
	import { credentialToJSON, isCancelled, passkeysSupported, requestOptions } from './webauthn';

	let { user, prompt = defaultPrompt }: { user: Account; prompt?: StepUpPrompt } = $props();

	const qc = useQueryClient();
	let password = $state('');
	let totpCode = $state('');
	let busy = $state<'password' | 'passkey' | null>(null);
	let message = $state<string | null>(null);

	const usesPassword = $derived(user.factors.password);
	const usesTotp = $derived(user.factors.totp);
	const usesPasskey = $derived(user.factors.passkeys > 0 && passkeysSupported());

	let open = $state(false);
	$effect(() => {
		open = prompt.open;
		if (prompt.open) {
			password = '';
			totpCode = '';
			message = null;
		}
	});

	function explain(e: unknown): string {
		const v = errorView(e);
		if (v.code === 'invalid_credentials')
			return usesTotp
				? 'The password or the code is not right. Check both and try again.'
				: 'The password is not right. Try again.';
		return v.message;
	}

	async function done() {
		await qc.invalidateQueries({ queryKey: queryKeys.session });
		prompt.settle(true);
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = 'password';
		message = null;
		try {
			await unwrap(
				api.POST('/api/v1/auth/step-ups', {
					body: { password, totpCode: usesTotp ? totpCode.replace(/\s/g, '') : undefined }
				})
			);
			await done();
		} catch (err) {
			message = explain(err);
		} finally {
			busy = null;
		}
	}

	async function withPasskey() {
		busy = 'passkey';
		message = null;
		try {
			const options = await unwrap(
				api.POST('/api/v1/auth/passkeys/authentication-options', {
					body: { purpose: 'step_up' }
				})
			);
			const cred = (await navigator.credentials.get({
				publicKey: requestOptions(options)
			})) as PublicKeyCredential | null;
			if (!cred) return;
			await unwrap(
				api.POST('/api/v1/auth/step-ups', { body: { credential: credentialToJSON(cred) } })
			);
			await done();
		} catch (err) {
			if (!isCancelled(err)) message = explain(err);
		} finally {
			busy = null;
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
	{#if usesPassword}
		<form class="form" onsubmit={submit} novalidate>
			<input type="text" autocomplete="username" value={user.username} hidden readonly />
			<PasswordField
				label="Password"
				autocomplete="current-password"
				bind:value={password}
				required
			/>
			{#if usesTotp}
				<TextField
					label="Authenticator code"
					description="The current 6-digit code from your authenticator app."
					bind:value={totpCode}
					inputmode="numeric"
					autocomplete="one-time-code"
					required
				/>
			{/if}
			<Button
				type="submit"
				variant="primary"
				loading={busy === 'password'}
				disabled={!password || (usesTotp && !totpCode.trim()) || busy !== null}
				block>Confirm with password</Button
			>
		</form>
	{/if}
	{#if usesPasskey}
		<Button
			variant={usesPassword ? 'secondary' : 'primary'}
			icon={Fingerprint}
			loading={busy === 'passkey'}
			disabled={busy !== null}
			onclick={withPasskey}
			block>Confirm with a passkey</Button
		>
	{/if}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
		margin-bottom: var(--space-3);
	}
</style>
