<script lang="ts">
	// Terminal surface (#8, #22): xterm.js loaded lazily with the terminal
	// theme. The exec session wiring (tickets, WebSocket, resize) belongs to
	// the feature view; this component only hosts the terminal. With `fit`
	// the grid follows the element's size (ResizeObserver) and fills it.
	import { onDestroy, onMount, untrack } from 'svelte';
	import { mountTerminal, type TerminalHandle } from '$lib/lazy';

	interface Props {
		label: string;
		readOnly?: boolean;
		rows?: number;
		/** Size the grid to the element (full-height terminals). */
		fit?: boolean;
		/** TTY output keeps its own carriage returns. */
		rawNewlines?: boolean;
		/** The handle once loaded (write, onData, focus). */
		terminal?: TerminalHandle | null;
		onready?: (t: TerminalHandle) => void;
	}

	let {
		label,
		readOnly = false,
		rows = 24,
		fit = false,
		rawNewlines = false,
		terminal = $bindable(null),
		onready
	}: Props = $props();
	let el = $state<HTMLElement>();
	let observer: ResizeObserver | null = null;
	let destroyed = false;

	onMount(() => {
		if (!el) return;
		const o = untrack(() => ({ readOnly, rows, fit, rawNewlines }));
		void mountTerminal(el, o).then((t) => {
			if (destroyed) {
				t.destroy();
				return;
			}
			terminal = t;
			if (o.fit && el && typeof ResizeObserver !== 'undefined') {
				t.fit();
				observer = new ResizeObserver(() => t.fit());
				observer.observe(el);
			}
			onready?.(t);
		});
	});
	onDestroy(() => {
		destroyed = true;
		observer?.disconnect();
		terminal?.destroy();
	});
</script>

<div class="term" class:fill={fit} role="region" aria-label={label} bind:this={el}></div>

<style>
	.term {
		padding: var(--space-2);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
		min-height: 120px;
	}

	.term.fill {
		height: 100%;
		min-height: 0;
		overflow: hidden;
	}

	/* xterm takes keystrokes through a hidden textarea that inherits the
	   page's 13 px, and iOS zooms into it on focus. It is invisible, so only
	   its size changes: 16 px on touch screens. */
	.term :global(.xterm-helper-textarea) {
		font-size: var(--text-input-mono);
	}
</style>
