<script lang="ts">
	// One open file (#15): follows its content query (refetched by live
	// files.changed events, #23) into the EditorSession and shows the
	// editor, a bounded read-only preview (large files), a binary/image
	// preview or the rendered Markdown.
	import { createQuery } from '@tanstack/svelte-query';
	import { onDestroy, untrack } from 'svelte';
	import Download from '@lucide/svelte/icons/download';
	import FileQuestion from '@lucide/svelte/icons/file-question-mark';
	import { ApiRequestError } from '$lib/api/client';
	import type { CodeEditorHandle } from '$lib/lazy';
	import { Button, CodeEditor, EmptyState, ErrorState, Notice, formatBytes } from '$lib/ui';
	import { contentQuery, type FilesApi } from './api';
	import type { EditorSession, EditorTab } from './editor.svelte';
	import { imageType } from './language';
	import MarkdownView from './MarkdownView.svelte';
	import { basename } from './paths';

	interface Props {
		files: FilesApi;
		session: EditorSession;
		tab: EditorTab;
		readOnly: boolean;
		preview: boolean;
		editor?: CodeEditorHandle | null;
		ondownload: (path: string) => void;
		/** The path is a directory (a symlink to one): browse it instead. */
		onisdir: (path: string) => void;
	}

	let {
		files,
		session,
		tab,
		readOnly,
		preview,
		editor = $bindable(),
		ondownload,
		onisdir
	}: Props = $props();

	const path = untrack(() => tab.path);
	const query = createQuery(() => contentQuery(files, path));

	$effect(() => {
		const d = query.data;
		if (d) untrack(() => session.apply(path, d.data, d.etag));
	});
	$effect(() => {
		const e = query.error;
		if (!e) return;
		if (e instanceof ApiRequestError && e.apiError?.code === 'file_type_mismatch') {
			untrack(() => onisdir(path));
			return;
		}
		untrack(() => session.fail(path, e));
	});

	// Replace the editor's text when the buffer changed from outside
	// (reload from disk, a clean buffer refreshed after an external edit).
	let shownRevision = untrack(() => tab.revision);
	$effect(() => {
		const r = tab.revision;
		if (r !== shownRevision && editor) {
			shownRevision = r;
			if (editor.text() !== tab.buffer) editor.setText(tab.buffer);
		}
	});

	// Bounded image preview for binary images (at most 5 MiB).
	const MAX_IMAGE = 5 << 20;
	const mime = $derived(imageType(path));
	let imageUrl = $state<string | null>(null);
	$effect(() => {
		if (tab.status !== 'binary' || !mime || !tab.entry || tab.entry.size > MAX_IMAGE) return;
		const ctrl = new AbortController();
		let url: string | null = null;
		fetch(files.downloadUrl([path]), { credentials: 'same-origin', signal: ctrl.signal })
			.then((r) => (r.ok ? r.blob() : null))
			.then((b) => {
				if (b) imageUrl = url = URL.createObjectURL(new Blob([b], { type: mime }));
			})
			.catch(() => {});
		return () => {
			ctrl.abort();
			if (url) URL.revokeObjectURL(url);
		};
	});
	onDestroy(() => {
		if (imageUrl) URL.revokeObjectURL(imageUrl);
	});

	const denied = $derived(
		tab.error instanceof ApiRequestError &&
			(tab.error.status === 403 || tab.error.apiError?.code === 'forbidden')
	);
</script>

<div class="doc">
	{#if tab.status === 'loading'}
		<div class="center" aria-busy="true">
			<span class="muted">Opening {basename(path)}…</span>
		</div>
	{:else if tab.status === 'error'}
		<div class="center">
			{#if denied}
				<EmptyState
					compact
					icon={FileQuestion}
					title="You can't open {basename(path)}"
					description={tab.error instanceof ApiRequestError
						? tab.error.message
						: 'Ask the owner of this Docker Manager for access to this file.'}
				/>
			{:else}
				<ErrorState
					compact
					error={tab.error}
					title="{basename(path)} could not be opened."
					onretry={() => query.refetch()}
				/>
			{/if}
		</div>
	{:else if tab.status === 'binary'}
		<div class="center binary">
			{#if imageUrl}
				<img src={imageUrl} alt={basename(path)} />
			{/if}
			<p class="muted">
				{basename(path)} is not a text file ({formatBytes(tab.entry?.size ?? 0)}). Download
				it to open it with another program.
			</p>
			<Button icon={Download} onclick={() => ondownload(path)}>Download</Button>
		</div>
	{:else}
		{#if tab.truncated}
			<div class="note">
				<Notice
					tone="info"
					title="Showing the first 512 KiB of {formatBytes(tab.entry?.size ?? 0)}"
				>
					This file is too large to edit here. Download it to see all of it.
					{#snippet actions()}
						<Button size="sm" icon={Download} onclick={() => ondownload(path)}
							>Download</Button
						>
					{/snippet}
				</Notice>
			</div>
		{/if}
		{#if preview}
			<div class="preview">
				<MarkdownView source={tab.buffer} label="Preview of {basename(path)}" />
			</div>
		{/if}
		<div class="code" hidden={preview}>
			<CodeEditor
				value={tab.buffer}
				label="{path} editor"
				language={tab.language}
				readOnly={readOnly || tab.truncated}
				height="100%"
				bind:editor
				onchange={(t) => session.edit(path, t)}
			/>
		</div>
	{/if}
</div>

<style>
	.doc {
		display: flex;
		flex-direction: column;
		flex: 1;
		min-height: 0;
	}

	.code {
		flex: 1;
		min-height: 0;
	}

	.code[hidden] {
		display: none;
	}

	.code :global(.editor) {
		border: 0;
		border-radius: 0;
	}

	.preview {
		flex: 1;
		min-height: 0;
		overflow: auto;
		background: var(--surface-panel);
	}

	.note {
		padding: var(--space-3);
	}

	.center {
		display: flex;
		flex: 1;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-3);
		padding: var(--space-6);
		text-align: center;
	}

	.binary img {
		max-width: min(100%, 480px);
		max-height: 320px;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		object-fit: contain;
	}
</style>
