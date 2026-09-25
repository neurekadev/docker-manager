<script lang="ts">
	// First-run setup (#16): creates the instance owner, once. Setup needs
	// the HTTPS public origin (or the explicit http://localhost development
	// mode); when this request does not qualify the manager says why and the
	// form stays disabled until the proxy or URL is fixed.
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import ShieldAlert from '@lucide/svelte/icons/shield-alert';
	import { api, unwrap } from '$lib/api/client';
	import { usePublicPage } from '$lib/auth/flow.svelte';
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

<svelte:head><title>Set up DockYard</title></svelte:head>

<div class="stack">
	<header>
		<h1>Set up DockYard</h1>
		<p class="lead">
			Create the owner account. The owner can do everything and invites everyone else.
		</p>
	</header>

	{#if !flow.ready}
		<Skeleton lines={5} height="36px" />
	{:else}
		{#if insecure}
			<Notice
				tone="danger"
				icon={ShieldAlert}
				title="Finish setup on DockYard's public URL"
				live="alert"
			>
				<p>
					{status?.explanation ??
						'This request did not reach DockYard over HTTPS on DOCKYARD_PUBLIC_URL.'}
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
				title="Finish setup on DockYard's public URL"
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
					bind:value={username}
					autocomplete="username"
					autocapitalize="off"
					spellcheck="false"
					required
					error={field('username')}
				/>
				<TextField
					label="Display name"
					description="Optional. Shown in the UI and the audit log."
					bind:value={displayName}
					autocomplete="name"
					error={field('displayName')}
				/>
				<TextField
					label="Email"
					type="email"
					description="Optional. DockYard sends no email."
					bind:value={email}
					autocomplete="email"
					error={field('email')}
				/>
				<PasswordField
					label="Password"
					autocomplete="new-password"
					description="At least 15 characters. Long passphrases are welcome; common and breached passwords are refused."
					bind:value={password}
					required
					error={field('password')}
				/>
				<PasswordField
					label="Repeat the password"
					autocomplete="new-password"
					bind:value={confirm}
					required
					error={mismatch}
				/>
				<Button
					type="submit"
					variant="primary"
					block
					loading={busy}
					disabled={insecure || !username.trim() || !password}
				>
					Create owner account
				</Button>
			</fieldset>
		</form>
		<div class="import">
			<p class="hint">Recovering an existing DockYard from its backups?</p>
			<Button href={routes.setupImport()} icon={DatabaseBackup} block disabled={insecure}
				>Import from backup</Button
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

	h1 {
		font-size: 22px;
		line-height: 28px;
	}

	.lead {
		margin-top: var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-control);
		line-height: 22px;
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
