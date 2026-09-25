<script lang="ts">
	// Terminal surface (#8, #22): xterm.js loaded lazily with the terminal
	// theme. The exec session wiring (tickets, WebSocket, resize) belongs to
	// the feature view; this component only hosts the terminal.
	import { onDestroy, onMount } from 'svelte';
	import { mountTerminal, type TerminalHandle } from '$lib/lazy';

	interface Props {
		label: string;
		readOnly?: boolean;
		rows?: number;
		/** The handle once loaded (write, onData, focus). */
		terminal?: TerminalHandle | null;
		onready?: (t: TerminalHandle) => void;
	}

	let {
		label,
		readOnly = false,
		rows = 24,
		terminal = $bindable(null),
		onready
	}: Props = $props();
	let el = $state<HTMLElement>();

	onMount(() => {
		if (!el) return;
		void mountTerminal(el, { readOnly, rows }).then((t) => {
			terminal = t;
			onready?.(t);
		});
	});
	onDestroy(() => terminal?.destroy());
</script>

<div class="term" role="region" aria-label={label} bind:this={el}></div>

<style>
	.term {
		padding: var(--space-2);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
		min-height: 120px;
	}
</style>
