<script lang="ts">
	// Asks for a name (new file, new folder, rename, save as, archive name):
	// validated like the API (one path component, docs/api/files.md), taken
	// names refused before the request, server errors shown inline.
	import type { Snippet } from 'svelte';
	import { Button, Dialog, TextField, errorMessage } from '$lib/ui';
	import { nameProblem } from './paths';

	interface Props {
		open?: boolean;
		title: string;
		label: string;
		description?: string;
		initial?: string;
		confirmLabel: string;
		/** Names that already exist where the entry goes. */
		taken?: readonly string[];
		onsubmit: (name: string) => unknown | Promise<unknown>;
		children?: Snippet;
	}

	let {
		open = $bindable(false),
		title,
		label,
		description,
		initial = '',
		confirmLabel,
		taken = [],
		onsubmit,
		children
	}: Props = $props();

	let name = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);
	let input = $state<HTMLInputElement | null>(null);

	$effect(() => {
		if (!open) return;
		name = initial;
		error = null;
		// Select the stem (before the extension), like desktop renames.
		queueMicrotask(() => {
			if (!input) return;
			input.focus();
			const dot = initial.lastIndexOf('.');
			input.setSelectionRange(0, dot > 0 ? dot : initial.length);
		});
	});

	async function submit(e?: Event) {
		e?.preventDefault();
		const n = name.trim();
		const problem = nameProblem(n);
		if (problem) return void (error = problem);
		if (n !== initial && taken.includes(n))
			return void (error = `${n} already exists here. Choose another name.`);
		busy = true;
		error = null;
		try {
			await onsubmit(n);
			open = false;
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<Dialog bind:open {title} {description} size="sm" dismissible={!busy}>
	<form id="name-dialog-form" onsubmit={submit}>
		<TextField
			{label}
			bind:value={name}
			bind:ref={input}
			mono
			required
			autocomplete="off"
			spellcheck="false"
			{error}
		/>
		{#if children}{@render children()}{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button variant="primary" type="submit" form="name-dialog-form" loading={busy}
			>{confirmLabel}</Button
		>
	{/snippet}
</Dialog>
