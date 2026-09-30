<script lang="ts">
	// Invite a user (#16): one click creates a single-use invite link, shown
	// once (Docker Manager keeps a verifier, not the code). The person opens it and
	// registers their own account, which joins the default group. Options:
	// bind the link to an email address, change its expiry. No email is
	// sent: hand the link over yourself.
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
	import Disclosure from '$lib/features/common/Disclosure.svelte';
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
	const optionError = $derived(!!(fields['body.email'] || fields['body.expiresInHours']));
</script>

<Dialog
	bind:open
	title="Invite a user"
	description="Creates a link that registers one account. The account joins {defaultGroupName ??
		'the default group'}."
	dismissible={!issued}
>
	{#if issued}
		<SecretReveal
			secret={issued.url}
			label="invite link"
			filename="docker-manager-invite-link.txt"
			description="Send this link to the person you invite: it opens a form to create their account. It works once and expires {formatDateTime(
				issued.expiresAt
			)}{issued.invitation.email
				? `, only for ${issued.invitation.email}`
				: ''}. Docker Manager doesn't email the link and cannot show it again."
			acknowledgeLabel="I copied or sent the invite link"
			confirmLabel="Done"
			onconfirm={() => {
				toast.success('Created the invite link');
				open = false;
			}}
		/>
	{:else}
		<form id="invite-form" onsubmit={create} novalidate>
			<Fields>
				<Disclosure summary="Options" open={optionError}>
					<TextField
						label="Email"
						type="email"
						bind:value={email}
						description="Optional. Only this address can use the link; nothing is sent to it."
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
				</Disclosure>
				{#if error && !Object.keys(fields).length}
					<Notice tone="danger" title="The invite link was not created" live="alert"
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
				>Create invite link</Button
			>
		{/if}
	{/snippet}
</Dialog>
