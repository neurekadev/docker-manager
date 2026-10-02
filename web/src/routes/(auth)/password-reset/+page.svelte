<script lang="ts">
	// Password reset redemption (#16): an owner-issued reset code, or an
	// owner-recovery code from `docker-manager owner-recovery`, in the URL
	// fragment. Every session of the account ends; the user signs in after.
	import { onMount, tick } from 'svelte';
	import { goto } from '$app/navigation';
	import { api, unwrap } from '$lib/api/client';
	import { takeCodeFromFragment } from '$lib/auth/code';
	import { usePublicPage } from '$lib/auth/flow.svelte';
	import AuthHeader from '$lib/features/auth/AuthHeader.svelte';
	import { isValid, requiredErrors, submitted, untilFilled } from '$lib/features/auth/validate';
	import { routes } from '$lib/routes';
	import { Button, Checkbox, Notice, PasswordField, TextField, errorView, toast } from '$lib/ui';

	usePublicPage('password-reset');
	let code = $state('');
	let password = $state('');
	let confirm = $state('');
	let revokeTokens = $state(false);
	let busy = $state(false);
	let attempted = $state(false);
	let error = $state<ReturnType<typeof errorView> | null>(null);
	let invalid = $state<Partial<Record<'code' | 'password', string>>>({});

	onMount(() => {
		code = takeCodeFromFragment();
	});

	const mismatch = $derived(
		attempted && confirm !== password ? 'The passwords do not match.' : null
	);
	const field = (name: string) => error?.fields.find((f) => f.field === `body.${name}`)?.message;

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		// Autofilled values count even before the browser reported them.
		const form = e.currentTarget as HTMLFormElement | null;
		const data = form ? new FormData(form) : null;
		code = submitted(data, 'code', code);
		password = submitted(data, 'password', password);
		confirm = submitted(data, 'confirm', confirm);
		attempted = true;
		invalid = requiredErrors({
			code: [code, 'Paste the reset code from your reset link.'],
			password: [password, 'Choose a new password.']
		});
		if (!isValid(invalid) || confirm !== password) {
			await tick();
			form?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
			return;
		}
		busy = true;
		error = null;
		try {
			await unwrap(
				api.POST('/api/v1/auth/password-resets/redemptions', {
					body: {
						code: code.trim(),
						newPassword: password,
						revokeApiTokens: revokeTokens
					}
				})
			);
			toast.success('Set your new password', { body: 'Sign in with it now.' });
			await goto(routes.signIn(), { replaceState: true });
		} catch (err) {
			error = errorView(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Set a New Password · Docker Manager</title></svelte:head>

<div class="stack">
	<AuthHeader
		title="Set a New Password"
		lead="This signs you out everywhere. Sign in with the new password afterwards."
	/>

	{#if error?.code === 'invalid_code'}
		<Notice tone="danger" title="This reset link does not work" live="alert">
			It may have expired or been used already. Ask the owner of this Docker Manager for a new
			one.
		</Notice>
	{:else if error && !error.fields.length}
		<Notice tone="danger" title="The password was not changed" live="alert"
			>{error.message}</Notice
		>
	{/if}

	<form onsubmit={submit} novalidate>
		<TextField
			label="Reset Code"
			name="code"
			bind:value={code}
			mono
			autocomplete="off"
			spellcheck="false"
			required
			error={untilFilled(invalid.code, code) ?? field('code')}
		/>
		<PasswordField
			label="New Password"
			autocomplete="new-password"
			description="Use a long passphrase; common and breached passwords are refused."
			name="password"
			bind:value={password}
			required
			error={untilFilled(invalid.password, password) ?? field('newPassword')}
		/>
		<PasswordField
			label="Repeat the New Password"
			autocomplete="new-password"
			name="confirm"
			bind:value={confirm}
			required
			error={mismatch}
		/>
		<Checkbox
			bind:checked={revokeTokens}
			label="Also Revoke My API Tokens"
			description="Choose this if someone else may have used your account."
		/>
		<Button type="submit" variant="primary" block loading={busy}>Set New Password</Button>
	</form>
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
</style>
