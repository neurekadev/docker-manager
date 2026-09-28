<script lang="ts">
	// Invitation redemption (#16): the owner's one-time invite link carries
	// the code in the URL fragment, so the page is a plain registration form;
	// without it (link opened without its fragment) the link can be pasted.
	// The new account joins the default group (initially Restricted: no
	// access until the owner grants some) and is signed in, or sent to factor
	// enrollment when the policy requires factors.
	import { onMount, tick } from 'svelte';
	import { api, unwrap } from '$lib/api/client';
	import { codeFromPasted, takeCodeFromFragment } from '$lib/auth/code';
	import { usePublicPage } from '$lib/auth/flow.svelte';
	import AuthHeader from '$lib/features/auth/AuthHeader.svelte';
	import { isValid, requiredErrors, submitted, untilFilled } from '$lib/features/auth/validate';
	import { routes } from '$lib/routes';
	import { Button, Notice, PasswordField, TextField, errorView, toast } from '$lib/ui';

	const flow = usePublicPage('invitation');
	let linkCode = $state('');
	let pasted = $state('');
	const code = $derived(linkCode || codeFromPasted(pasted));
	let username = $state('');
	let displayName = $state('');
	let email = $state('');
	let password = $state('');
	let busy = $state(false);
	let error = $state<ReturnType<typeof errorView> | null>(null);
	let invalid = $state<Partial<Record<'code' | 'username', string>>>({});

	onMount(() => {
		linkCode = takeCodeFromFragment();
	});

	const field = (name: string) => error?.fields.find((f) => f.field === `body.${name}`)?.message;

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		// Autofilled values count even before the browser reported them.
		const form = e.currentTarget as HTMLFormElement | null;
		const data = form ? new FormData(form) : null;
		if (!linkCode) pasted = submitted(data, 'invite', pasted);
		username = submitted(data, 'username', username);
		password = submitted(data, 'password', password);
		invalid = requiredErrors({
			code: [code, 'Paste the invite link the owner sent you.'],
			username: [username, 'Choose a username.']
		});
		if (!isValid(invalid)) {
			await tick();
			form?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
			return;
		}
		busy = true;
		error = null;
		try {
			const s = await unwrap(
				api.POST('/api/v1/invitations/redemptions', {
					body: {
						code: code.trim(),
						username: username.trim(),
						displayName: displayName.trim() || undefined,
						email: email.trim() || undefined,
						password: password || undefined
					}
				})
			);
			toast.success('Created your account', { body: `Signed in as ${username.trim()}.` });
			await flow.proceed(s);
		} catch (err) {
			error = errorView(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Accept invitation · Docker Manager</title></svelte:head>

<div class="stack">
	<AuthHeader
		title="Join Docker Manager"
		lead="Create your account. The owner decides what you can see and do."
	/>

	{#if error?.code === 'invalid_code'}
		<Notice tone="danger" title="This invite link does not work" live="alert">
			It may have expired, been used or been revoked, or it was issued for another email
			address. Ask the owner for a new invite link.
		</Notice>
	{:else if error && !error.fields.length}
		<Notice tone="danger" title="The account was not created" live="alert"
			>{error.message}</Notice
		>
	{/if}

	<form onsubmit={submit} novalidate>
		{#if !linkCode}
			<TextField
				label="Invite link"
				name="invite"
				bind:value={pasted}
				mono
				autocomplete="off"
				spellcheck="false"
				description="Paste the invite link the owner sent you."
				required
				error={untilFilled(invalid.code, pasted) ?? field('code')}
			/>
		{/if}
		<TextField
			label="Username"
			name="username"
			bind:value={username}
			autocomplete="username"
			autocapitalize="off"
			spellcheck="false"
			required
			error={untilFilled(invalid.username, username) ?? field('username')}
		/>
		<TextField
			label="Display name"
			description="Optional."
			bind:value={displayName}
			autocomplete="name"
			error={field('displayName')}
		/>
		<TextField
			label="Email"
			type="email"
			description="Optional, unless the invite link was issued for your email address."
			bind:value={email}
			autocomplete="email"
			error={field('email')}
		/>
		<PasswordField
			label="Password"
			autocomplete="new-password"
			description="Use a long passphrase; common and breached passwords are refused."
			name="password"
			bind:value={password}
			error={field('password')}
		/>
		<Button type="submit" variant="primary" block loading={busy}>Create account</Button>
	</form>
	<p class="help">Already have an account? <a href={routes.signIn()}>Sign in</a></p>
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

	.help {
		color: var(--text-muted);
		text-align: center;
	}
</style>
