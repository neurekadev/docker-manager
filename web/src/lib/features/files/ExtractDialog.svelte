<script lang="ts">
	// Extract an archive (#15): into a new folder named after it (default)
	// or into the current folder. Existing names are previewed and asked
	// about before anything is written.
	import { Button, Dialog, RadioGroup, TextField, errorMessage } from '$lib/ui';
	import { nameProblem } from './paths';

	interface Props {
		open?: boolean;
		archive: string;
		/** The folder the archive is in (display). */
		here: string;
		stem: string;
		/** name: a new folder's name, or null for the current folder. */
		onsubmit: (folder: string | null) => Promise<unknown>;
	}

	let { open = $bindable(false), archive, here, stem, onsubmit }: Props = $props();
	let where = $state('new');
	let name = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);

	$effect(() => {
		if (!open) return;
		where = 'new';
		name = stem;
		error = null;
	});

	async function submit(e: Event) {
		e.preventDefault();
		if (where === 'new') {
			const p = nameProblem(name.trim());
			if (p) return void (error = p);
		}
		busy = true;
		error = null;
		try {
			await onsubmit(where === 'new' ? name.trim() : null);
			open = false;
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<Dialog bind:open title="Extract {archive}" size="sm" dismissible={!busy}>
	<form id="extract-form" class="form" onsubmit={submit}>
		<RadioGroup
			label="Extract Into"
			bind:value={where}
			options={[
				{ value: 'new', label: 'A New Folder' },
				{ value: 'here', label: `This Folder (${here})` }
			]}
		/>
		{#if where === 'new'}
			<TextField label="Folder Name" mono bind:value={name} {error} autocomplete="off" />
		{:else if error}
			<p class="error" role="alert">{error}</p>
		{/if}
		<p class="note">
			Files that already exist are listed before anything is written. Links that leave this
			root and oversized archives are refused.
		</p>
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button variant="primary" type="submit" form="extract-form" loading={busy}>Extract</Button>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}

	.note {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.error {
		color: var(--danger);
	}
</style>
