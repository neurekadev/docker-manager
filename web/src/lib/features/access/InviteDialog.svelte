<script lang="ts">
	// Invite a user (#16): a single-use invitation link, shown once (DockYard
	// keeps a verifier, not the code), optionally bound to an email address,
	// expiring after the chosen hours. No email is sent: hand it over
	// yourself. The new account joins the default group.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, type Schema } from '$lib/api/client';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import {
		Button,
		Dialog,
		Notice,
		SecretReveal,
		TextField,
		formatDateTime,
		toast
	} from '$lib/ui';
	import { newIdempotencyKey } from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import Fields from '$lib/features/common/Fields.svelte';
	import { accessKeys } from './queries';

	let {
		open = $bindable(false),
		defaultGroupName
	}: { open?: boolean; defaultGroupName?: string } = $props();

	const qc = useQueryClient();
	let email = $state('');
	let hours = $state('');
	let busy = $state(false);
	let error = $state<unknown>(null);
	let issued = $state<Schema<'CreateInvitationOutputBody'> | null>(null);

	$effect(() => {
		if (!open) {
			issued = null;
			email = '';
			hours = '';
			error = null;
		}
	});

	async function create(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		const key = newIdempotencyKey();
		try {
			issued = await withStepUp(() =>
				unwrap(
					api.POST('/api/v1/invitations', {
						params: { header: { 'Idempotency-Key': key } },
						body: {
							email: email.trim() || undefined,
							expiresInHours: hours.trim() ? Number(hours) : undefined
						}
					})
				)
			);
			void qc.invalidateQueries({ queryKey: accessKeys.invitations() });
		} catch (err) {
			error = err;
		} finally {
			busy = false;
		}
	}

	const fields = $derived(fieldErrors(error));
</script>

<Dialog
	bind:open
	title="Invite a user"
	description="The link works once. The new account joins {defaultGroupName ??
		'the default group'}."
	dismissible={!issued}
>
	{#if issued}
		<SecretReveal
			secret={issued.url}
			label="invitation link"
			filename="dockyard-invitation.txt"
			description="Send this link to the person you invite. It expires {formatDateTime(
				issued.expiresAt
			)}{issued.invitation.email
				? ` and only works for ${issued.invitation.email}`
				: ''}. DockYard sends no email and cannot show it again."
			confirmLabel="Done"
			onconfirm={() => {
				toast.success('Created the invitation');
				open = false;
			}}
		/>
	{:else}
		<form id="invite-form" onsubmit={create} novalidate>
			<Fields>
				<TextField
					label="Email"
					type="email"
					bind:value={email}
					description="Optional. Only this address can redeem the invitation; nothing is sent to it."
					error={fields['body.email']}
				/>
				<TextField
					label="Expires after (hours)"
					type="number"
					min="1"
					bind:value={hours}
					description="Optional. Empty: the default of the sign-in policy."
					error={fields['body.expiresInHours']}
				/>
				{#if error && !Object.keys(fields).length}
					<Notice tone="danger" title="The invitation was not created" live="alert"
						>{actionError(error)}</Notice
					>
				{/if}
			</Fields>
		</form>
	{/if}
	{#snippet footer()}
		{#if !issued}
			<Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
			<Button variant="primary" type="submit" form="invite-form" loading={busy}
				>Create invitation</Button
			>
		{/if}
	{/snippet}
</Dialog>
