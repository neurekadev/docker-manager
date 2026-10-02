<script lang="ts">
	// Add or edit a Git credential (#33): an HTTPS username and access token
	// for private build contexts, handled like registry connections
	// (write-only token, sealed, sent only to the build that needs it).
	// Editing shows the stored token's fingerprint (not in the list).
	import { untrack } from 'svelte';
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap } from '$lib/api/client';
	import { queryKeys, type GitCredential } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import {
		Button,
		Checkbox,
		Dialog,
		PasswordField,
		Notice,
		TextField,
		errorMessage,
		fieldError,
		toast
	} from '$lib/ui';
	import { maskFingerprint } from './model';

	interface Props {
		open?: boolean;
		credential?: GitCredential | null;
	}

	let { open = $bindable(false), credential = null }: Props = $props();
	const queryClient = useQueryClient();

	let name = $state('');
	let host = $state('');
	let pathPrefix = $state('');
	let username = $state('');
	let secret = $state('');
	let plainHttp = $state(false);
	let busy = $state(false);
	let failure = $state<unknown>(null);

	$effect(() => {
		if (!open) return;
		untrack(() => {
			name = credential?.name ?? '';
			host = credential?.host ?? '';
			pathPrefix = credential?.pathPrefix ?? '';
			username = credential?.username ?? '';
			secret = '';
			plainHttp = !!credential?.plainHttp;
			failure = null;
		});
	});

	const valid = $derived(
		!!name.trim() && !!username.trim() && (!!credential || (!!host.trim() && !!secret))
	);

	async function save() {
		busy = true;
		failure = null;
		try {
			if (credential) {
				await withStepUp(() =>
					unwrap(
						api.PATCH('/api/v1/git-credentials/{credentialId}', {
							params: {
								path: { credentialId: credential!.id },
								header: { 'If-Match': `"${credential!.revision ?? 0}"` }
							},
							body: {
								name: name.trim(),
								pathPrefix: pathPrefix.trim(),
								username: username.trim(),
								plainHttp
							}
						})
					)
				);
				toast.success(`Saved ${name.trim()}`);
			} else {
				await withStepUp(() =>
					unwrap(
						api.POST('/api/v1/git-credentials', {
							body: {
								name: name.trim(),
								host: host.trim(),
								pathPrefix: pathPrefix.trim() || undefined,
								username: username.trim(),
								secret,
								plainHttp: plainHttp || undefined
							}
						})
					)
				);
				toast.success(`Added ${name.trim()}`);
			}
			secret = '';
			void queryClient.invalidateQueries({ queryKey: queryKeys.registries.all });
			open = false;
		} catch (e) {
			failure = e;
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title={credential ? `Edit ${credential.name}` : 'Add a Git Credential'}
	description="For builds from private repositories over HTTPS. Shared by the whole instance; builds get the token only while they run."
	size="lg"
	dismissible={!busy}
>
	<form
		id="git-form"
		class="form"
		onsubmit={(e) => {
			e.preventDefault();
			if (valid) void save();
		}}
	>
		<TextField
			label="Name"
			required
			bind:value={name}
			placeholder="GitHub (acme builds)"
			error={fieldError(failure, 'body.name')}
		/>
		{#if !credential}
			<TextField
				label="Host"
				mono
				required
				bind:value={host}
				placeholder="github.com"
				error={fieldError(failure, 'body.host')}
			/>
		{/if}
		<div class="full">
			<TextField
				label="Repositories Below"
				mono
				bind:value={pathPrefix}
				placeholder="acme"
				description="Optional. Only repositories under this path get the token; empty: every repository on the host."
			/>
		</div>
		<TextField
			label="Username"
			mono
			required
			bind:value={username}
			autocomplete="off"
			description="For GitHub and GitLab tokens any name works, e.g. x-access-token or oauth2."
		/>
		{#if credential}
			<div class="full">
				<Notice tone="info" title="{credential.host}, Stored Token" live="none">
					{#if credential.secret?.set}Fingerprint <span
							class="mono"
							title="Version {credential.secret.version}"
							>{maskFingerprint(credential.secret.fingerprint)}</span
						>. The token is write-only: use Rotate Token to replace it.{:else}No token
						is stored. Use Rotate Token to add one.{/if}
				</Notice>
			</div>
		{/if}
		{#if !credential}
			<PasswordField
				label="Access Token"
				autocomplete="new-password"
				required
				bind:value={secret}
				description="A read-only repository token is enough. Write-only: never shown again."
				error={fieldError(failure, 'body.secret')}
			/>
		{/if}
		<div class="full">
			<Checkbox
				label="Allow Plain HTTP"
				description="Send the token to http:// repositories (trusted networks only)."
				bind:checked={plainHttp}
			/>
		</div>
		{#if failure && !fieldError(failure, 'body.name')}
			<p class="error" role="alert">
				{(failure as { status?: number }).status === 412
					? 'Someone changed this credential meanwhile. Close the dialog and open it again.'
					: errorMessage(failure)}
			</p>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button type="submit" form="git-form" variant="primary" loading={busy} disabled={!valid}
			>{credential ? 'Save Changes' : 'Add Credential'}</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-4) var(--space-5);
		align-items: start;
	}

	.full,
	.error {
		grid-column: 1 / -1;
	}

	.error {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}

	@media (max-width: 767px) {
		.form {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
