<script lang="ts">
	// Code editor (#15, #22): CodeMirror loaded lazily with Docker Manager's editor
	// theme. `value` is the initial text; changes are reported through
	// onchange (the parent owns dirty state, ETags and conflicts, #15).
	// `language`, `readOnly` and `wrap` may change while mounted; the handle
	// (bind:editor) offers setText, openSearch, focus and text.
	import { onDestroy, onMount, untrack } from 'svelte';
	import { mountCodeEditor, type CodeEditorHandle, type EditorLanguage } from '$lib/lazy';
	import Skeleton from './Skeleton.svelte';

	interface Props {
		value: string;
		/** Accessible name, e.g. the file path. */
		label: string;
		readOnly?: boolean;
		/** Syntax highlighting (default YAML). */
		language?: EditorLanguage;
		onchange?: (text: string) => void;
		/** CSS height; "100%" fills a sized parent. */
		height?: string;
		/** The editor handle once loaded (setText, focus, text, openSearch). */
		editor?: CodeEditorHandle | null;
		/** Wrap long lines instead of scrolling sideways. */
		wrap?: boolean;
	}

	let {
		value,
		label,
		readOnly = false,
		language = 'yaml',
		onchange,
		height = '420px',
		wrap = false,
		// No fallback: a parent may bind an entry that is still undefined.
		editor = $bindable()
	}: Props = $props();
	let el = $state<HTMLElement>();
	let failed = $state(false);
	let destroyed = false;

	onMount(() => {
		if (!el) return;
		const initial = untrack(() => ({ value, readOnly, language, label, wrap }));
		mountCodeEditor(el, initial.value, {
			readOnly: initial.readOnly,
			label: initial.label,
			language: initial.language,
			wrap: initial.wrap,
			onChange: (t) => onchange?.(t)
		})
			.then((e) => {
				if (destroyed) e.destroy();
				else editor = e;
			})
			.catch(() => (failed = true));
	});
	onDestroy(() => {
		destroyed = true;
		editor?.destroy();
	});

	$effect(() => {
		const l = language;
		void untrack(() => editor)?.setLanguage(l);
	});
	$effect(() => {
		const ro = readOnly;
		untrack(() => editor)?.setReadOnly(ro);
	});
	$effect(() => {
		const w = wrap;
		untrack(() => editor)?.setWrap?.(w);
	});
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

	.editor:focus-within {
		border-color: var(--border-strong);
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
