<script lang="ts">
	// Recovery Key rotation (#10): explain what happens, generate the new key
	// (step-up), show it once, then the re-entry challenge makes it current.
	// Each repository location moves to it the next time it is used; until
	// then the previous key is still needed, and the dialog says so.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap, type Schema } from '$lib/api/client';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import { useCriticalWork } from '$lib/features/common/unsaved.svelte';
	import { Button, Dialog, Notice, SecretReveal, toast } from '$lib/ui';
	import { actionError } from '$lib/features/common/errors';
	import RecoveryKeyChallenge from './RecoveryKeyChallenge.svelte';
	import { RECOVERY_KEY_WARNING } from './model';

	let { open = $bindable(false), repositoryId }: { open?: boolean; repositoryId: string } =
		$props();

	const qc = useQueryClient();
	let started = $state<Schema<'KeyRotationStarted'> | null>(null);
	let stored = $state(false);
	let busy = $state(false);
	let error = $state<string | null>(null);

	useCriticalWork(
		'other',
		() => 'New Recovery Key not confirmed yet',
		() => !!started && open
	);

	async function generate() {
		busy = true;
		error = null;
		try {
			started = await withStepUp(() =>
				unwrap(
					api.POST('/api/v1/backup-repositories/{repositoryId}/key-rotations', {
						params: { path: { repositoryId } }
					})
				)
			);
		} catch (e) {
			error = actionError(e, {
				key_rotation_in_progress:
					'A previous rotation is still moving repositories to the newer key. Wait until no location is pending.'
			});
		} finally {
			busy = false;
		}
	}

	function done() {
		toast.success('Rotated the Recovery Key', {
			body: 'Keep the previous key until every location shows the new one.'
		});
		void qc.invalidateQueries({ queryKey: ['backups'] });
		started = null;
		stored = false;
		open = false;
	}
</script>

<Dialog
	bind:open
	title="Rotate the Recovery Key"
	description="A new key replaces the current one for every repository of this Docker Manager."
	size="lg"
	dismissible={!started}
>
	{#if !started}
		<ul class="points" role="list">
			<li>Docker Manager generates a new Recovery Key and shows it once.</li>
			<li>
				It becomes current when you re-enter it; each repository location then moves to it.
			</li>
			<li>
				Until every location moved, restoring older data may need the previous key too. Keep
				both keys until this repository shows no pending location.
			</li>
		</ul>
		{#if error}<Notice tone="danger" title="The rotation did not start" live="alert"
				>{error}</Notice
			>{/if}
	{:else if !stored}
		<SecretReveal
			secret={started.recoveryKey.key}
			label="new Recovery Key"
			filename="docker-manager-recovery-key-new.txt"
			fingerprint={started.recoveryKey.fingerprint}
			description={RECOVERY_KEY_WARNING}
			confirmLabel="I Saved It, Continue"
			onconfirm={() => (stored = true)}
		/>
	{:else}
		<RecoveryKeyChallenge
			{repositoryId}
			fingerprint={started.keyState.pendingFingerprint}
			submitLabel="Make the New Key Current"
			onconfirmed={done}
		/>
	{/if}

	{#snippet footer()}
		{#if !started}
			<Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
			<Button variant="primary" loading={busy} onclick={generate}>Generate New Key</Button>
		{/if}
	{/snippet}
</Dialog>

<style>
	.points {
		display: grid;
		gap: var(--space-2);
		padding-left: var(--space-5);
		list-style: disc;
	}
</style>
