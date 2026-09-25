<script lang="ts">
	// Password reset redemption (#16): an owner-issued reset code, or an
	// owner-recovery code from `dockyard-manager owner-recovery`, in the URL
	// fragment. Every session of the account ends; the user signs in after.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api, unwrap } from '$lib/api/client';
	import { takeCodeFromFragment } from '$lib/auth/code';
	import { usePublicPage } from '$lib/auth/flow.svelte';
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

	onMount(() => {
		code = takeCodeFromFragment();
	});

	const mismatch = $derived(
		attempted && confirm !== password ? 'The passwords do not match.' : null
	);
	const field = (name: string) => error?.fields.find((f) => f.field === `body.${name}`)?.message;

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		attempted = true;
		if (confirm !== password) return;
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

<svelte:head><title>Set a new password · DockYard</title></svelte:head>

<div class="stack">
	<header>
		<h1>Set a new password</h1>
		<p class="lead">This signs you out everywhere. Sign in with the new password afterwards.</p>
	</header>

	{#if error?.code === 'invalid_code'}
		<Notice tone="danger" title="This reset link does not work" live="alert">
			It may have expired or been used already. Ask the owner of this DockYard for a new one.
		</Notice>
	{:else if error && !error.fields.length}
		<Notice tone="danger" title="The password was not changed" live="alert"
			>{error.message}</Notice
		>
	{/if}

	<form onsubmit={submit} novalidate>
		<TextField
			label="Reset code"
			bind:value={code}
			mono
			autocomplete="off"
			spellcheck="false"
			required
			error={field('code')}
		/>
		<PasswordField
			label="New password"
			autocomplete="new-password"
			description="Use a long passphrase; common and breached passwords are refused."
			bind:value={password}
			required
			error={field('newPassword')}
		/>
		<PasswordField
			label="Repeat the new password"
			autocomplete="new-password"
			bind:value={confirm}
			required
			error={mismatch}
		/>
		<Checkbox
			bind:checked={revokeTokens}
			label="Also revoke my API tokens"
			description="Choose this if someone else may have used your account."
		/>
		<Button
			type="submit"
			variant="primary"
			block
			loading={busy}
			disabled={!code.trim() || !password}>Set new password</Button
		>
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

	h1 {
		font-size: 22px;
		line-height: 28px;
	}

	.lead {
		margin-top: var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-control);
	}
</style>
