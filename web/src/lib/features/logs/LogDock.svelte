<script lang="ts">
	// The logs as a resizable bottom drawer (#22): opened from other tabs
	// (the Files tab), it takes height, never width, so the file list and the
	// editor keep their widths. Drag the handle or use the arrow keys on it.
	import X from '@lucide/svelte/icons/x';
	import { IconButton } from '$lib/ui';
	import LogPanel from './LogPanel.svelte';

	interface Props {
		stackId: string;
		name: string;
		onclose: () => void;
	}

	let { stackId, name, onclose }: Props = $props();
	const MIN = 160;
	const max = () => Math.max(MIN, Math.round(window.innerHeight * 0.7));
	let height = $state(280);

	function onKey(e: KeyboardEvent) {
		if (e.key === 'ArrowUp') height = Math.min(max(), height + 24);
		else if (e.key === 'ArrowDown') height = Math.max(MIN, height - 24);
		else return;
		e.preventDefault();
	}

	function onDown(e: PointerEvent) {
		const start = e.clientY;
		const from = height;
		const el = e.currentTarget as HTMLElement;
		el.setPointerCapture(e.pointerId);
		const move = (ev: PointerEvent) =>
			(height = Math.min(max(), Math.max(MIN, from - (ev.clientY - start))));
		const up = () => {
			el.removeEventListener('pointermove', move);
			el.removeEventListener('pointerup', up);
		};
		el.addEventListener('pointermove', move);
		el.addEventListener('pointerup', up);
	}
</script>

<section class="dock" style="height: {height}px" aria-label="Logs drawer">
	<!-- A focusable separator is a widget (ARIA window splitter): arrows resize it. -->
	<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
	<div
		class="handle"
		role="separator"
		aria-orientation="horizontal"
		aria-label="Resize the logs drawer"
		aria-valuemin={MIN}
		aria-valuenow={height}
		tabindex="0"
		onkeydown={onKey}
		onpointerdown={onDown}
	></div>
	<LogPanel target={{ kind: 'stack', stackId }} {name} dense>
		{#snippet extra()}
			<IconButton icon={X} size="sm" label="Close the logs drawer" onclick={onclose} />
		{/snippet}
	</LogPanel>
</section>

<style>
	.dock {
		position: relative;
		display: flex;
		flex: none;
		flex-direction: column;
		min-height: 160px;
	}

	.handle {
		position: absolute;
		top: -8px;
		left: 0;
		right: 0;
		height: 10px;
		z-index: 1;
		cursor: row-resize;
		background: linear-gradient(var(--border-strong), var(--border-strong)) center / 48px 3px
			no-repeat;
		border-radius: var(--radius-full);
	}

	.handle:hover,
	.handle:focus-visible {
		background-image: linear-gradient(var(--accent-text), var(--accent-text));
		outline: none;
	}
</style>
