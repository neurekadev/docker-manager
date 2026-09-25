<script lang="ts">
	// The file editor (#15, the mockup's editor card): tabs of open files,
	// language select, Format (YAML/JSON), Search, a Markdown preview toggle
	// and Save. Saving a Compose source of a stack records a new revision and
	// deploys nothing (#7, #25 Q1). An external change keeps the unsaved
	// buffer and shows the conflict banner exactly per the brief:
	// "<file> changed on disk. Your edits are kept." with Compare, Reload from
	// disk, Save as… and Overwrite (confirmed).
	import { useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import Eye from '@lucide/svelte/icons/eye';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Save from '@lucide/svelte/icons/save';
	import Search from '@lucide/svelte/icons/search';
	import X from '@lucide/svelte/icons/x';
	import { EDITOR_LANGUAGES, formatDocument, type CodeEditorHandle } from '$lib/lazy';
	import { liveKeys } from '$lib/live/keys';
	import {
		Button,
		ConfirmDialog,
		IconButton,
		Notice,
		Select,
		errorMessage,
		formatDateTime,
		toast
	} from '$lib/ui';
	import { liveScopeOf, type FilesApi } from './api';
	import CompareDialog from './CompareDialog.svelte';
	import { isDefinitionFile } from './definition';
	import { isDirty, SaveBlockedError, type EditorSession, type EditorTab } from './editor.svelte';
	import EditorDocument from './EditorDocument.svelte';
	import { formattable, LANGUAGE_LABELS } from './language';
	import NameDialog from './NameDialog.svelte';
	import { basename, join, parent } from './paths';

	interface Props {
		files: FilesApi;
		session: EditorSession;
		canWrite: boolean;
		/** Stack scope: Compose sources and where their revisions are. */
		stack?: { name: string; configFiles: string[]; revisionsHref?: string } | null;
		ondownload: (path: string) => void;
		onisdir: (path: string) => void;
		/** Names in a directory (Save as… refuses taken names early). */
		takenNames?: (dir: string) => string[];
	}

	let {
		files,
		session,
		canWrite,
		stack = null,
		ondownload,
		onisdir,
		takenNames
	}: Props = $props();
	const qc = useQueryClient();

	let editors = $state<Record<string, CodeEditorHandle | null>>({});
	let previews = $state<Record<string, boolean>>({});
	const tab = $derived(session.current);
	const handle = $derived(tab ? (editors[tab.path] ?? null) : null);
	const isMarkdown = $derived(tab?.language === 'markdown');
	const definition = $derived(
		!!tab &&
			!!stack &&
			files.scope.kind === 'stack' &&
			isDefinitionFile(tab.path, stack.configFiles)
	);
	const readOnly = $derived(!canWrite);

	const languageOptions = EDITOR_LANGUAGES.map((l) => ({ value: l, label: LANGUAGE_LABELS[l] }));

	function refetchContent(path: string) {
		void qc.invalidateQueries({
			queryKey: liveKeys.files(liveScopeOf(files.scope), 'content', path)
		});
	}

	function savedToast(t: EditorTab) {
		const name = basename(t.path);
		if (definition && stack) {
			toast.success(`Saved ${name}`, {
				body: `Recorded a new revision of ${stack.name}. It is not deployed: deploy ${stack.name} to apply it.`,
				action: stack.revisionsHref
					? { label: 'Open revisions', onclick: () => void goto(stack.revisionsHref!) }
					: undefined
			});
		} else toast.success(`Saved ${name}`);
	}

	async function save() {
		const t = tab;
		if (!t || !canWrite || t.saving || t.status !== 'ready' || t.truncated) return;
		if (t.conflict) {
			toast.warn(`${basename(t.path)} changed on disk`, {
				body: 'Your edits are kept. Choose Compare, Reload from disk, Save as… or Overwrite first.'
			});
			return;
		}
		if (!isDirty(t)) return;
		try {
			await session.save(t.path);
			savedToast(t);
		} catch (e) {
			if (e instanceof SaveBlockedError) {
				refetchContent(t.path);
				toast.warn(`${basename(t.path)} was not saved`, {
					body: 'It changed on disk since you opened it. Your edits are kept; choose how to resolve it.'
				});
			} else toast.error(`${basename(t.path)} was not saved`, { body: errorMessage(e) });
		}
	}

	async function format() {
		const t = tab;
		if (!t || !handle || !formattable(t.language)) return;
		try {
			const out = await formatDocument(handle.text(), t.language);
			if (out !== handle.text()) handle.setText(out);
		} catch (e) {
			toast.error(`${basename(t.path)} could not be formatted`, {
				body: `${e instanceof Error ? e.message : String(e)} Fix it and format again.`
			});
		}
	}

	// Closing a tab with unsaved edits asks first.
	let closing = $state<EditorTab | null>(null);
	let closeOpen = $state(false);
	function requestClose(t: EditorTab) {
		if (isDirty(t) || (t.conflict && t.buffer !== t.base)) {
			closing = t;
			closeOpen = true;
		} else session.close(t.path);
	}

	let overwriteOpen = $state(false);
	let compareOpen = $state(false);
	let reloadOpen = $state(false);
	let saveAsOpen = $state(false);

	async function overwrite() {
		if (!tab) return;
		const t = tab;
		try {
			await session.overwrite(t.path);
			savedToast(t);
		} catch (e) {
			if (e instanceof SaveBlockedError) refetchContent(t.path);
			throw e;
		}
	}

	async function saveAs(name: string) {
		if (!tab) return;
		const t = tab;
		const target = join(parent(t.path), name);
		await session.saveAs(t.path, target);
		toast.success(`Saved ${name}`, {
			body: `Your edits of ${basename(t.path)} are in ${name}.`
		});
	}

	function onKeydown(e: KeyboardEvent) {
		if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 's') {
			e.preventDefault();
			void save();
		}
	}

	function selectTab(e: KeyboardEvent, i: number) {
		const n = session.tabs.length;
		let j = -1;
		if (e.key === 'ArrowRight') j = (i + 1) % n;
		else if (e.key === 'ArrowLeft') j = (i - 1 + n) % n;
		else if (e.key === 'Home') j = 0;
		else if (e.key === 'End') j = n - 1;
		else if (e.key === 'Delete') {
			e.preventDefault();
			requestClose(session.tabs[i]);
			return;
		}
		if (j < 0) return;
		e.preventDefault();
		session.active = session.tabs[j].path;
		(e.currentTarget as HTMLElement).parentElement?.parentElement
			?.querySelectorAll<HTMLElement>('[role="tab"]')
			[j]?.focus();
	}
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section class="pane" aria-label="Editor" onkeydown={onKeydown}>
	<div class="bar">
		<div class="tabs" role="tablist" aria-label="Open files">
			{#each session.tabs as t, i (t.path)}
				{@const dirty = isDirty(t)}
				<div class="tab" class:active={t.path === session.active}>
					<button
						type="button"
						role="tab"
						id="tab-{i}"
						aria-selected={t.path === session.active}
						aria-controls="editor-panel"
						tabindex={t.path === session.active ? 0 : -1}
						title={t.path}
						onclick={() => (session.active = t.path)}
						onkeydown={(e) => selectTab(e, i)}
					>
						{basename(t.path)}
						{#if dirty}<span class="dot" aria-label="unsaved changes"></span>{/if}
						{#if t.conflict}<span class="conflict-dot" aria-label="changed on disk"
							></span>{/if}
					</button>
					<IconButton
						icon={X}
						size="sm"
						label="Close {basename(t.path)}"
						tooltip={false}
						tabindex={-1}
						onclick={() => requestClose(t)}
					/>
				</div>
			{/each}
		</div>
		{#if tab && tab.status === 'ready'}
			<div class="tools">
				<div class="lang">
					<Select
						label="Language"
						hideLabel
						options={languageOptions}
						value={tab.language}
						onchange={(e) =>
							session.setLanguage(
								tab.path,
								(e.currentTarget as HTMLSelectElement)
									.value as EditorTab['language']
							)}
					/>
				</div>
				{#if isMarkdown}
					<Button
						size="sm"
						variant="secondary"
						icon={previews[tab.path] ? Pencil : Eye}
						aria-pressed={!!previews[tab.path]}
						onclick={() =>
							(previews = { ...previews, [tab.path]: !previews[tab.path] })}
						>{previews[tab.path] ? 'Edit' : 'Preview'}</Button
					>
				{/if}
				{#if formattable(tab.language) && canWrite && !tab.truncated}
					<Button size="sm" variant="secondary" onclick={format} disabled={!handle}
						>Format</Button
					>
				{/if}
				<IconButton
					icon={Search}
					variant="secondary"
					size="sm"
					label="Search and replace"
					disabled={!handle || !!previews[tab.path]}
					onclick={() => handle?.openSearch()}
				/>
				{#if canWrite && !tab.truncated}
					<Button
						size="sm"
						variant="primary"
						icon={Save}
						loading={tab.saving}
						disabled={!isDirty(tab) || !!tab.conflict}
						onclick={save}>Save</Button
					>
				{/if}
			</div>
		{/if}
	</div>

	{#if tab?.conflict}
		<Notice
			tone="warn"
			live="alert"
			bar
			title="{tab.path} changed on disk. Your edits are kept."
		>
			{#if tab.conflict.diskModifiedAt}Changed {formatDateTime(tab.conflict.diskModifiedAt)}.
			{/if}
			{#if tab.conflict.diskText === null}Reading the new version…{:else}Saving is paused
				until you choose what to keep.{/if}
			{#snippet actions()}
				<Button
					size="sm"
					variant="ghost"
					disabled={tab.conflict?.diskText == null}
					onclick={() => (compareOpen = true)}>Compare</Button
				>
				<Button
					size="sm"
					variant="ghost"
					disabled={tab.conflict?.diskText == null}
					onclick={() => (reloadOpen = true)}>Reload from disk</Button
				>
				{#if canWrite}
					<Button size="sm" variant="ghost" onclick={() => (saveAsOpen = true)}
						>Save as…</Button
					>
					<Button
						size="sm"
						variant="danger-soft"
						disabled={!tab.conflict?.diskEtag}
						onclick={() => (overwriteOpen = true)}>Overwrite</Button
					>
				{/if}
			{/snippet}
		</Notice>
	{/if}

	<div
		class="docs"
		id="editor-panel"
		role="tabpanel"
		aria-labelledby="tab-{session.tabs.findIndex((t) => t.path === session.active)}"
	>
		{#each session.tabs as t (t.path)}
			<div class="doc" hidden={t.path !== session.active}>
				<EditorDocument
					{files}
					{session}
					tab={t}
					{readOnly}
					preview={!!previews[t.path]}
					bind:editor={editors[t.path]}
					{ondownload}
					{onisdir}
				/>
			</div>
		{/each}
	</div>

	{#if tab}
		<footer class="status">
			<span class="mono path" title={tab.path}>{tab.path}</span>
			{#if tab.saving}
				<span>Saving…</span>
			{:else if isDirty(tab)}
				<span class="unsaved">Unsaved changes</span>
			{:else if tab.status === 'ready' && !readOnly}
				<span>Saved</span>
			{/if}
			{#if readOnly && tab.status === 'ready'}<span>Read only</span>{/if}
			{#if definition && stack}
				<span class="hint"
					>Compose source: saving records a new revision of {stack.name}; it is not
					deployed.</span
				>
			{/if}
		</footer>
	{/if}
</section>

<ConfirmDialog
	bind:open={closeOpen}
	title="Close {closing ? basename(closing.path) : ''} without saving?"
	message="Your unsaved edits are lost. The file on disk stays as it is."
	confirmLabel="Discard edits and close"
	tone="danger"
	onconfirm={() => closing && session.close(closing.path)}
/>

<ConfirmDialog
	bind:open={overwriteOpen}
	title="Overwrite {tab ? basename(tab.path) : ''} on disk?"
	message="Replaces the version on disk with your edits. The other change is lost."
	consequences={definition && stack
		? [`Records a new revision of ${stack.name}. Nothing is deployed.`]
		: []}
	confirmLabel="Overwrite"
	tone="danger"
	onconfirm={overwrite}
/>

<ConfirmDialog
	bind:open={reloadOpen}
	title="Reload {tab ? basename(tab.path) : ''} from disk?"
	message="Your unsaved edits are discarded and the editor shows the version on disk."
	confirmLabel="Discard edits and reload"
	tone="danger"
	onconfirm={() => tab && session.reloadFromDisk(tab.path)}
/>

{#if tab?.conflict && tab.conflict.diskText !== null}
	<CompareDialog
		bind:open={compareOpen}
		name={basename(tab.path)}
		disk={tab.conflict.diskText}
		buffer={tab.buffer}
	/>
{/if}

{#if tab}
	<NameDialog
		bind:open={saveAsOpen}
		title="Save {basename(tab.path)} as"
		label="New file name"
		initial={basename(tab.path).replace(/(\.[^.]+)?$/, (m) => ` (edited)${m}`)}
		confirmLabel="Save as"
		taken={takenNames?.(parent(tab.path)) ?? []}
		onsubmit={saveAs}
	/>
{/if}

<style>
	.pane {
		display: flex;
		flex: 1;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
		height: 100%;
		background: var(--surface-panel);
	}

	.bar {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 48px;
		padding: var(--space-2) var(--space-3) 0;
		border-bottom: 1px solid var(--border-subtle);
	}

	.tabs {
		display: flex;
		flex: 1;
		align-self: stretch;
		align-items: flex-end;
		gap: 2px;
		min-width: 0;
		overflow-x: auto;
		scrollbar-width: none;
	}

	.tab {
		display: flex;
		align-items: center;
		flex: none;
		max-width: 220px;
		padding-right: 2px;
		border-radius: var(--radius-md) var(--radius-md) 0 0;
		color: var(--text-muted);
		box-shadow: inset 0 -2px 0 transparent;
	}

	.tab:hover {
		background: var(--surface-hover);
		color: var(--text-default);
	}

	.tab.active {
		background: var(--surface-selected);
		color: var(--text-strong);
		box-shadow: inset 0 -2px 0 var(--accent);
	}

	.tab button[role='tab'] {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		height: 36px;
		padding: 0 var(--space-2) 0 var(--space-3);
		border: 0;
		background: none;
		color: inherit;
		font-size: var(--text-body);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.dot,
	.conflict-dot {
		flex: none;
		width: 7px;
		height: 7px;
		border-radius: var(--radius-full);
		background: var(--text-default);
	}

	.conflict-dot {
		background: var(--warn);
	}

	.tools {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		padding-bottom: var(--space-2);
	}

	.lang {
		width: 150px;
	}

	.lang :global(.dy-input) {
		height: var(--control-height-sm);
	}

	.docs {
		position: relative;
		display: flex;
		flex: 1;
		min-height: 0;
	}

	.doc {
		display: flex;
		flex: 1;
		min-width: 0;
		min-height: 0;
	}

	.doc[hidden] {
		display: none;
	}

	.status {
		display: flex;
		align-items: center;
		gap: var(--space-4);
		min-height: 30px;
		padding: 0 var(--space-3);
		overflow: hidden;
		border-top: 1px solid var(--border-subtle);
		color: var(--text-muted);
		font-size: var(--text-caption);
		white-space: nowrap;
	}

	.path {
		overflow: hidden;
		text-overflow: ellipsis;
		font-size: 12px;
	}

	.unsaved {
		color: var(--text-default);
	}

	.hint {
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--warn);
	}

	@media (max-width: 767px) {
		.bar {
			flex-wrap: wrap;
			padding-bottom: var(--space-2);
		}

		.tabs {
			flex-basis: 100%;
		}

		.tools {
			flex-wrap: wrap;
			padding-bottom: 0;
		}

		.lang {
			width: 124px;
		}

		.hint {
			display: none;
		}
	}
</style>
