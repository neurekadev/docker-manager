<script lang="ts">
	// Confirmation dialog (#22). The message states exactly what will happen
	// ("Restarts 5 containers of Silo."); the confirm button names the action
	// ("Restart"), never "OK". onconfirm may be async: the button shows
	// progress, and a failure is shown inline (the dialog stays open).
	import type { Snippet } from 'svelte';
	import Button from './Button.svelte';
	import Dialog from './Dialog.svelte';
	import { errorMessage } from './errors';

	interface Props {
		open?: boolean;
		title: string;
		message?: string;
		/** Plain-language consequences, one per line item. */
		consequences?: string[];
		confirmLabel: string;
		cancelLabel?: string;
		tone?: 'default' | 'danger';
		onconfirm: () => unknown | Promise<unknown>;
		children?: Snippet;
		/** Extra condition (e.g. typed confirmation) before confirming. */
		canConfirm?: boolean;
		/** md or lg when the dialog shows a preview (children) beside the message. */
		size?: 'sm' | 'md' | 'lg';
	}

	let {
		open = $bindable(false),
		title,
		message,
		consequences = [],
		confirmLabel,
		cancelLabel = 'Cancel',
		tone = 'default',
		onconfirm,
		children,
		canConfirm = true,
		size = 'sm'
	}: Props = $props();

	let busy = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		if (!open) error = null;
	});

	async function confirm() {
		busy = true;
		error = null;
		try {
			await onconfirm();
			open = false;
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = false;
		}
	}
</script>

<Dialog bind:open {title} {size} alert dismissible={!busy}>
	{#if message}<p class="message">{message}</p>{/if}
	{#if consequences.length}
		<ul class="consequences">
			{#each consequences as c (c)}<li>{c}</li>{/each}
		</ul>
	{/if}
	{#if children}{@render children()}{/if}
	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>{cancelLabel}</Button
		>
		<Button
			variant={tone === 'danger' ? 'danger' : 'primary'}
			loading={busy}
			disabled={!canConfirm}
			onclick={confirm}
		>
			{confirmLabel}
		</Button>
	{/snippet}
</Dialog>

<style>
	.message {
		color: var(--text-default);
		font-size: var(--text-control);
		line-height: 22px;
	}

	.consequences {
		display: grid;
		gap: 4px;
		margin: var(--space-3) 0 0;
		padding-left: 18px;
		color: var(--text-default);
	}

	.error {
		margin-top: var(--space-3);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--danger-border);
		border-radius: var(--radius-sm);
		background: var(--danger-soft);
		color: var(--danger);
	}
</style>
