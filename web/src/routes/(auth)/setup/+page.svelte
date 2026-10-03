<script lang="ts">
	// First-run setup (#16): creates the instance owner, once. Setup needs
	// the HTTPS public origin (or the explicit http://localhost development
	// mode); when this request does not qualify the manager says why and the
	// form stays disabled until the proxy or URL is fixed.
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import { tick } from 'svelte';
	import { api, unwrap } from '$lib/api/client';
	import { usePublicPage } from '$lib/auth/flow.svelte';
	import AuthHeader from '$lib/features/auth/AuthHeader.svelte';
	import { isValid, requiredErrors, submitted, untilFilled } from '$lib/features/auth/validate';
	import { routes } from '$lib/routes';
	import { Button, Notice, PasswordField, Skeleton, TextField, errorView, toast } from '$lib/ui';

	const flow = usePublicPage('setup');
	const status = $derived(flow.setup.data);
	const insecure = $derived(status ? !status.secureOrigin : false);

	let username = $state('');
	let displayName = $state('');
	let email = $state('');
	let password = $state('');
	let confirm = $state('');
	let busy = $state(false);
	let error = $state<ReturnType<typeof errorView> | null>(null);
	let attempted = $state(false);
	let invalid = $state<Partial<Record<'username' | 'password', string>>>({});

	const mismatch = $derived(
		attempted && confirm !== password ? 'The passwords do not match.' : null
	);
	const field = (name: string) => error?.fields.find((f) => f.field === `body.${name}`)?.message;
	const fieldOrMissing = (name: 'username' | 'password') =>
		untilFilled(invalid[name], name === 'username' ? username : password) ?? field(name);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		// Autofilled values count even before the browser reported them.
		const form = e.currentTarget as HTMLFormElement | null;
		const data = form ? new FormData(form) : null;
		username = submitted(data, 'username', username);
		password = submitted(data, 'password', password);
		confirm = submitted(data, 'confirm', confirm);
		attempted = true;
		invalid = requiredErrors({
			username: [username, 'Choose a username.'],
			password: [password, 'Choose a password.']
		});
		if (!isValid(invalid) || confirm !== password) {
			await tick();
			form?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
			return;
		}
		busy = true;
		error = null;
		try {
			const s = await unwrap(
				api.POST('/api/v1/setup/owner', {
					body: {
						username: username.trim(),
						displayName: displayName.trim() || undefined,
						email: email.trim() || undefined,
						password
					}
				})
			);
			toast.success('Created the owner account', {
				body: `Signed in as ${username.trim()}.`
			});
			await flow.proceed(s);
		} catch (err) {
			error = errorView(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Set Up Docker Manager</title></svelte:head>

<div class="stack">
	<AuthHeader title="Set Up Docker Manager" lead="Create the owner account." />

	{#if !flow.ready}
		<Skeleton lines={5} height="36px" />
	{:else}
		{#if insecure}
			<Notice
				tone="danger"
				icon={ShieldAlert}
				title="Finish Setup on Docker Manager's Public URL"
				live="alert"
			>
				<p>
					{status?.explanation ??
						"This page was not opened over HTTPS on Docker Manager's public address."}
				</p>
				<p class="hint">Fix the address or the proxy, then reload this page.</p>
			</Notice>
		{/if}
		{#if error && error.code === 'setup_complete'}
			<Notice tone="info" title="Setup is already complete" live="alert">
				The owner account exists. <a href={routes.signIn()}>Sign in</a> instead.
			</Notice>
		{:else if error && error.code === 'insecure_origin'}
			<Notice
				tone="danger"
				icon={ShieldAlert}
				title="Finish Setup on Docker Manager's Public URL"
				live="alert"
			>
				{error.message}
			</Notice>
		{:else if error && !error.fields.length}
			<Notice tone="danger" title="The owner account was not created" live="alert"
				>{error.message}</Notice
			>
		{/if}

		<form onsubmit={submit} novalidate>
			<fieldset disabled={insecure || busy}>
				<TextField
					label="Username"
					name="username"
					bind:value={username}
					autocomplete="username"
					autocapitalize="off"
					spellcheck="false"
					required
					error={fieldOrMissing('username')}
				/>
				<TextField
					label="Display Name"
					optional
					bind:value={displayName}
					autocomplete="name"
					error={field('displayName')}
				/>
				<TextField
					label="Email"
					type="email"
					optional
					bind:value={email}
					autocomplete="email"
					error={field('email')}
				/>
				<PasswordField
					label="Password"
					autocomplete="new-password"
					description="At least 15 characters. Common and breached passwords are refused."
					name="password"
					bind:value={password}
					required
					error={fieldOrMissing('password')}
				/>
				<PasswordField
					label="Repeat the Password"
					autocomplete="new-password"
					name="confirm"
					bind:value={confirm}
					required
					error={mismatch}
				/>
				<Button type="submit" variant="primary" block loading={busy} disabled={insecure}>
					Create Owner Account
				</Button>
			</fieldset>
		</form>
		<div class="import">
			<p class="hint">Recovering an existing Docker Manager from its backups?</p>
			<Button href={routes.setupImport()} icon={DatabaseBackup} block disabled={insecure}
				>Import From Backup</Button
			>
		</div>
	{/if}
</div>

<style>
	.stack {
		display: flex;
		flex-direction: column;
		gap: var(--space-5);
	}

	fieldset {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		margin: 0;
		padding: 0;
		border: 0;
		min-width: 0;
	}

	.hint {
		margin-top: var(--space-1);
		color: var(--text-muted);
	}

	.import {
		display: grid;
		gap: var(--space-2);
		padding-top: var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}
</style>
