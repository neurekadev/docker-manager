<script lang="ts">
	// Type-to-confirm field (#22): shows the exact text to type as a
	// single-line code block with a copy button (long names scroll instead
	// of wrapping), then the input. The input's accessible name is
	// "Type <text> to confirm". Used by DestructiveConfirm and every dialog
	// that asks for a typed name.
	import CopyButton from './CopyButton.svelte';

	interface Props {
		/** The exact text to type (usually the resource name). */
		text: string;
		value?: string;
	}

	let { text, value = $bindable('') }: Props = $props();
	const uid = $props.id();
</script>

<div class="ttc">
	<p class="prompt" id="ttc-{uid}-prompt">To confirm, type the name below.</p>
	<div class="name-row">
		<code class="name" title={text}>{text}</code>
		<CopyButton value={text} what="name" />
	</div>
	<input
		class="dy-input mono"
		bind:value
		aria-label="Type {text} to confirm"
		aria-describedby="ttc-{uid}-prompt"
		autocomplete="off"
		spellcheck="false"
		autocapitalize="off"
	/>
</div>

<style>
	.ttc {
		display: grid;
		gap: var(--space-2);
	}

	.prompt {
		color: var(--text-default);
		font-size: var(--text-control);
	}

	.name-row {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
	}

	.name {
		flex: 0 1 auto;
		min-width: 0;
		max-width: 100%;
		overflow-x: auto;
		padding: 2px var(--space-2);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-canvas);
		color: var(--text-strong);
		font-family: var(--font-mono);
		font-size: 12.5px;
		line-height: 22px;
		white-space: nowrap;
		user-select: all;
	}
</style>
