<script lang="ts">
	// Add or edit a registry connection (#19). The secret is write-only: it
	// is entered once, sealed on the manager and never shown again (only
	// its fingerprint). Editing changes the matching and binding; a new
	// secret goes through "Rotate credential". Changes need a step-up.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { api, unwrap } from '$lib/api/client';
	import { environmentsQuery, queryKeys, type RegistryConnection } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import { stackNamesQuery } from './model';
	import {
		Button,
		Checkbox,
		Dialog,
		Notice,
		PasswordField,
		RadioGroup,
		Select,
		TextField,
		errorMessage,
		fieldError,
		toast
	} from '$lib/ui';

	interface Props {
		open?: boolean;
		connection?: RegistryConnection | null;
	}

	let { open = $bindable(false), connection = null }: Props = $props();
	const queryClient = useQueryClient();
	const envs = createQuery(() => ({ ...environmentsQuery(), enabled: open }));
	const stacks = createQuery(() => ({ ...stackNamesQuery(), enabled: open }));

	let name = $state('');
	let host = $state('');
	let credentialType = $state('token');
	let username = $state('');
	let secret = $state('');
	let pattern = $state('');
	let binding = $state('none');
	let environmentId = $state('');
	let stackId = $state('');
	let priority = $state('0');
	let plainHttp = $state(false);
	let busy = $state(false);
	let failure = $state<unknown>(null);

	$effect(() => {
		if (!open) return;
		untrack(() => {
			const c = connection;
			name = c?.name ?? '';
			host = c?.host ?? '';
			credentialType = c?.credentialType ?? 'token';
			username = c?.username ?? '';
			secret = '';
			pattern = c?.repositoryPattern ?? '';
			binding = c?.stackId ? 'stack' : c?.environmentId ? 'environment' : 'none';
			environmentId = c?.environmentId ?? '';
			stackId = c?.stackId ?? '';
			priority = String(c?.priority ?? 0);
			plainHttp = !!c?.plainHttp;
			failure = null;
		});
	});

	const prio = $derived(Number(priority));
	const valid = $derived(
		!!name.trim() &&
			(!!connection || (!!host.trim() && !!username.trim() && !!secret)) &&
			Number.isInteger(prio) &&
			prio >= -1000 &&
			prio <= 1000 &&
			(binding !== 'environment' || !!environmentId) &&
			(binding !== 'stack' || !!stackId)
	);
	const selfHosted = $derived(
		/[:]\d+$/.test(host.trim()) || host.includes('.lan') || host.includes('localhost')
	);

	async function save() {
		busy = true;
		failure = null;
		const env = binding === 'environment' ? environmentId : '';
		const stack = binding === 'stack' ? stackId : '';
		try {
			if (connection) {
				await withStepUp(() =>
					unwrap(
						api.PATCH('/api/v1/registries/{registryId}', {
							params: {
								path: { registryId: connection!.id },
								header: { 'If-Match': `"${connection!.revision ?? 0}"` }
							},
							body: {
								name: name.trim(),
								repositoryPattern: pattern.trim(),
								environmentId: env,
								stackId: stack,
								priority: prio,
								plainHttp
							}
						})
					)
				);
				toast.success(`Saved ${name.trim()}`);
			} else {
				await withStepUp(() =>
					unwrap(
						api.POST('/api/v1/registries', {
							body: {
								name: name.trim(),
								host: host.trim(),
								credentialType: credentialType as 'token' | 'password',
								username: username.trim(),
								secret,
								repositoryPattern: pattern.trim() || undefined,
								environmentId: env || undefined,
								stackId: stack || undefined,
								priority: prio || undefined,
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
	title={connection ? `Edit ${connection.name}` : 'Add a registry connection'}
	description="Docker Manager uses it for pulls, deploys and update checks of matching images. It is shared by the whole instance, not a personal login."
	size="lg"
	dismissible={!busy}
>
	<form
		id="registry-form"
		class="form"
		onsubmit={(e) => {
			e.preventDefault();
			if (valid) void save();
		}}
	>
		<section class="col" aria-labelledby="registry-login">
			<h3 id="registry-login" class="section">Registry and credential</h3>
			<TextField
				label="Name"
				required
				bind:value={name}
				placeholder="GHCR (acme pull token)"
				error={fieldError(failure, 'body.name')}
			/>
			{#if !connection}
				<TextField
					label="Registry host"
					mono
					required
					bind:value={host}
					placeholder="ghcr.io"
					description="docker.io for Docker Hub (its aliases are the same registry); host:port for self-hosted registries."
					error={fieldError(failure, 'body.host')}
				/>
				<RadioGroup
					label="Credential"
					bind:value={credentialType}
					options={[
						{
							value: 'token',
							label: 'Access token',
							description: 'Recommended: a read-only (pull) token.'
						},
						{
							value: 'password',
							label: 'Password',
							description: 'Only where the registry has no tokens.'
						}
					]}
				/>
				<TextField
					label="Username"
					mono
					required
					bind:value={username}
					autocomplete="off"
					error={fieldError(failure, 'body.username')}
				/>
				<PasswordField
					label={credentialType === 'token' ? 'Access token' : 'Password'}
					autocomplete="new-password"
					required
					bind:value={secret}
					description="Shown only while you type it. Docker Manager stores it sealed and never displays it again; you'll see its fingerprint."
					error={fieldError(failure, 'body.secret')}
				/>
			{:else}
				<Notice
					tone="info"
					icon={KeyRound}
					title="{connection.host}, {connection.username ?? 'no username'}"
					live="none"
				>
					The credential {connection.secret?.fingerprint
						? `(${connection.secret.fingerprint})`
						: ''} is write-only. Use Rotate credential to replace it.
				</Notice>
			{/if}
		</section>
		<section class="col" aria-labelledby="registry-match">
			<h3 id="registry-match" class="section">Which images use it</h3>
			<TextField
				label="Repositories"
				mono
				bind:value={pattern}
				placeholder="acme/*"
				description="Optional. An exact repository or namespace/*; empty matches every repository on the host."
				error={fieldError(failure, 'body.repositoryPattern')}
			/>
			<Select
				label="Use it for"
				bind:value={binding}
				options={[
					{ value: 'none', label: 'Every environment and stack' },
					{ value: 'environment', label: 'One environment' },
					{ value: 'stack', label: 'One stack' }
				]}
				description="A bound connection wins over a general one for the same image."
			/>
			{#if binding === 'environment'}
				<Select
					label="Environment"
					bind:value={environmentId}
					options={[
						{ value: '', label: 'Choose an environment' },
						...(envs.data ?? []).map((e) => ({ value: e.id, label: e.name }))
					]}
				/>
			{:else if binding === 'stack'}
				<Select
					label="Stack"
					bind:value={stackId}
					options={[
						{ value: '', label: 'Choose a stack' },
						...(stacks.data ?? []).map((s) => ({ value: s.id, label: s.name }))
					]}
				/>
			{/if}
			<TextField
				label="Priority"
				inputmode="numeric"
				bind:value={priority}
				description="Breaks ties between equally specific connections: higher wins. From -1000 to 1000."
				error={Number.isInteger(prio)
					? fieldError(failure, 'body.priority')
					: 'Enter a whole number.'}
			/>
			{#if selfHosted || plainHttp}
				<Checkbox
					label="Plain HTTP"
					description="Only for self-hosted registries without TLS on a trusted network."
					bind:checked={plainHttp}
				/>
			{/if}
		</section>
		{#if failure && !fieldError(failure, 'body.name') && !fieldError(failure, 'body.host')}
			<p class="error" role="alert">
				{(failure as { status?: number }).status === 412
					? 'Someone changed this connection meanwhile. Close the dialog and open it again.'
					: errorMessage(failure)}
			</p>
		{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button
			type="submit"
			form="registry-form"
			variant="primary"
			loading={busy}
			disabled={!valid}>{connection ? 'Save changes' : 'Add connection'}</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--space-4) var(--space-6);
		align-items: start;
	}

	.col {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}

	.section {
		color: var(--text-strong);
		font-size: var(--text-control);
		font-weight: var(--weight-semibold);
	}

	.error {
		grid-column: 1 / -1;
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
