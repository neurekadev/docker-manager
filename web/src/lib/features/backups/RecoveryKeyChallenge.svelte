<script lang="ts">
	// The Recovery Key re-entry challenge (#10): the owner types (or pastes)
	// the key and states it is saved. Nothing is initialized before. The
	// check proves the key was copied correctly, not that it is stored
	// safely, and the copy says so.
	import { api, unwrap, type Schema } from '$lib/api/client';
	import { Button, Checkbox, Notice, TextArea } from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import Fields from '$lib/features/common/Fields.svelte';
	import { looksLikeRecoveryKey, normalizeRecoveryKey, RECOVERY_KEY_WARNING } from './model';

	interface Props {
		repositoryId: string;
		/** Fingerprint of the key to enter (pending or current), to compare. */
		fingerprint?: string;
		onconfirmed: (result: Schema<'RecoveryConfirmation'>) => void;
		submitLabel?: string;
	}

	let {
		repositoryId,
		fingerprint,
		onconfirmed,
		submitLabel = 'Confirm Recovery Key'
	}: Props = $props();

	let key = $state('');
	let backedUp = $state(false);
	let busy = $state(false);
	let error = $state<string | null>(null);
	const wellFormed = $derived(looksLikeRecoveryKey(key));

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/backup-repositories/{repositoryId}/recovery-confirmations', {
					params: { path: { repositoryId } },
					body: { recoveryKey: normalizeRecoveryKey(key), backedUp }
				})
			);
			key = '';
			onconfirmed(out);
		} catch (err) {
			error = actionError(err, {
				recovery_key_mismatch:
					'That is not this Docker Manager’s Recovery Key. Check your saved copy; compare its fingerprint.',
				recovery_key_malformed:
					'The key has a typo: it is DYRK- followed by 13 groups of four characters. Paste it from your saved copy.'
			});
		} finally {
			busy = false;
		}
	}
</script>

<form class="challenge" onsubmit={submit} novalidate>
	<Fields>
		<p>
			Type or paste the Recovery Key from the copy you saved.
			{#if fingerprint}Its fingerprint is <span class="mono">{fingerprint}</span>.{/if}
		</p>
		<TextArea
			label="Recovery Key"
			mono
			rows={2}
			bind:value={key}
			autocomplete="off"
			spellcheck="false"
			placeholder="DYRK-XXXX-XXXX-…"
			description={key && !wellFormed
				? 'Keep going: a key is DYRK- and 13 groups of four characters.'
				: 'Never stored from this field, logged or audited.'}
		/>
		<Checkbox
			bind:checked={backedUp}
			label="I saved the Recovery Key outside Docker Manager"
			description="For example in a password manager and on paper in a safe place."
		/>
		<Notice tone="info" title="What This Check Proves" live="none">
			It proves you copied the key correctly, not that your copy is safe. {RECOVERY_KEY_WARNING}
		</Notice>
		{#if error}<Notice tone="danger" title="The key was not confirmed" live="alert"
				>{error}</Notice
			>{/if}
		<div>
			<Button
				type="submit"
				variant="primary"
				loading={busy}
				disabled={!wellFormed || !backedUp}>{submitLabel}</Button
			>
		</div>
	</Fields>
</form>

<style>
	.challenge {
		max-width: 720px;
	}
</style>
