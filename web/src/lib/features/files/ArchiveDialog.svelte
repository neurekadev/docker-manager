<script lang="ts">
	// Create an archive (#15): name and format (zip or tar.gz) of a new
	// archive in the current folder. Conflicts are asked afterwards.
	import { Button, Dialog, RadioGroup, TextField, errorMessage } from '$lib/ui';
	import { nameProblem } from './paths';

	interface Props {
		open?: boolean;
		/** What goes in, e.g. "config" or "3 items". */
		what: string;
		base: string;
		onsubmit: (name: string, format: 'zip' | 'tar.gz') => Promise<unknown>;
	}

	let { open = $bindable(false), what, base, onsubmit }: Props = $props();
	let format = $state<'zip' | 'tar.gz'>('zip');
	let name = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);

	const ext = (f: string) => (f === 'zip' ? '.zip' : '.tar.gz');
	$effect(() => {
		if (!open) return;
		format = 'zip';
		name = base + '.zip';
		error = null;
	});

	function setFormat(f: string) {
		const next = f as 'zip' | 'tar.gz';
		if (name.endsWith(ext(format))) name = name.slice(0, -ext(format).length) + ext(next);
		format = next;
	}

	async function submit(e: Event) {
		e.preventDefault();
		const n = name.trim();
		const p = nameProblem(n);
		if (p) return void (error = p);
		busy = true;
		error = null;
		try {
			await onsubmit(n, format);
			open = false;
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Create Archive"
	description="Packs {what} into one file in this folder."
	size="sm"
	dismissible={!busy}
>
	<form id="archive-form" class="form" onsubmit={submit}>
		<RadioGroup
			label="Format"
			value={format}
			onchange={setFormat}
			options={[
				{ value: 'zip', label: 'ZIP', description: 'Opens anywhere.' },
				{ value: 'tar.gz', label: 'tar.gz', description: 'Keeps Unix permissions.' }
			]}
		/>
		<TextField label="Archive Name" mono bind:value={name} {error} autocomplete="off" />
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button variant="primary" type="submit" form="archive-form" loading={busy}
			>Create Archive</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}
</style>
