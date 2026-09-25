<script lang="ts">
	// Code editor (#15, #22): CodeMirror loaded lazily with DockYard's editor
	// theme. `value` is the initial text; changes are reported through
	// onchange (the parent owns dirty state, ETags and conflicts, #15).
	import { onDestroy, onMount } from 'svelte';
	import { mountYamlEditor, type YamlEditor } from '$lib/lazy';
	import Skeleton from './Skeleton.svelte';

	interface Props {
		value: string;
		/** Accessible name, e.g. the file path. */
		label: string;
		readOnly?: boolean;
		onchange?: (text: string) => void;
		height?: string;
		/** The editor handle once loaded (setText, focus, text). */
		editor?: YamlEditor | null;
	}

	let {
		value,
		label,
		readOnly = false,
		onchange,
		height = '420px',
		editor = $bindable(null)
	}: Props = $props();
	let el = $state<HTMLElement>();
	let failed = $state(false);

	onMount(() => {
		if (!el) return;
		mountYamlEditor(el, value, { readOnly, label, onChange: onchange })
			.then((e) => (editor = e))
			.catch(() => (failed = true));
	});
	onDestroy(() => editor?.destroy());
</script>

<div class="editor" style="height: {height}" aria-busy={!editor}>
	{#if !editor && !failed}
		<div class="loading"><Skeleton lines={6} /></div>
	{/if}
	{#if failed}
		<p class="failed">The editor could not be loaded. Reload the page to try again.</p>
	{/if}
	<div class="mount" bind:this={el}></div>
</div>

<style>
	.editor {
		position: relative;
		overflow: hidden;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
	}

	.mount,
	.mount :global(.cm-editor) {
		height: 100%;
	}

	.loading {
		position: absolute;
		inset: var(--space-4);
	}

	.failed {
		padding: var(--space-4);
		color: var(--danger);
	}
</style>
