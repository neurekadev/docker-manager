<script lang="ts">
	// The scoped file manager (#15, #22 "Files: full width"): one component
	// for a stack's project directory and a volume. Breadcrumbs and path,
	// the directory list (FileList) with sorting, filter, hidden files, the
	// ".." row, selection, keyboard, context menu with the same actions in
	// the toolbar and each row's menu (touch), drag and drop, uploads, jobs
	// with progress, and the editor beside the list (Files and Editor tabs
	// below 1024 px, never both squeezed). Permissions and owners are a
	// details view (off by default). The card takes the height of its
	// content and grows to the page's height while the editor is open. The
	// server authorizes every call; the UI hides what the caller's
	// capabilities do not allow.
	import { createInfiniteQuery, keepPreviousData, useQueryClient } from '@tanstack/svelte-query';
	import { onDestroy, onMount, untrack, type Snippet } from 'svelte';
	import { MediaQuery } from 'svelte/reactivity';
	import { beforeNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ClipboardPaste from '@lucide/svelte/icons/clipboard-paste';
	import Copy from '@lucide/svelte/icons/copy';
	import Download from '@lucide/svelte/icons/download';
	import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
	import Eye from '@lucide/svelte/icons/eye';
	import EyeOff from '@lucide/svelte/icons/eye-off';
	import FileArchive from '@lucide/svelte/icons/file-archive';
	import FilePlus from '@lucide/svelte/icons/file-plus';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import FolderPlus from '@lucide/svelte/icons/folder-plus';
	import FolderUp from '@lucide/svelte/icons/folder-up';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import ListTree from '@lucide/svelte/icons/list-tree';
	import LockKeyhole from '@lucide/svelte/icons/lock-keyhole';
	import PackageOpen from '@lucide/svelte/icons/package-open';
	import Pencil from '@lucide/svelte/icons/pencil';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Scissors from '@lucide/svelte/icons/scissors';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Upload from '@lucide/svelte/icons/upload';
	import X from '@lucide/svelte/icons/x';
	import { ApiRequestError, type Job } from '$lib/api/client';
	import { liveClient } from '$lib/live';
	import { liveKeys } from '$lib/live/keys';
	import {
		Button,
		ConfirmDialog,
		ContextMenu,
		DestructiveConfirm,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		OfflineEnvironment,
		Skeleton,
		Tabs,
		TextField,
		errorMessage,
		formatBytes,
		toast,
		type MenuEntry
	} from '$lib/ui';
	import {
		FilesApi,
		filesCapability,
		listingQuery,
		liveScopeOf,
		scopeKey,
		type FileEntry,
		type FilePreview,
		type FileScope,
		type FileVerb,
		type ListFilters
	} from './api';
	import ArchiveDialog from './ArchiveDialog.svelte';
	import { fileClipboard } from './clipboard.svelte';
	import ConflictDialog from './ConflictDialog.svelte';
	import {
		groupRequests,
		NO_DECISIONS,
		type ConflictChoice,
		type ConflictItem,
		type ConflictPolicy
	} from './conflicts';
	import { isDefinitionFile, type StackFiles } from './definition';
	import { directoriesOf, filesFromDrop, filesFromInput, type PickedFile } from './dropped';
	import { EditorSession } from './editor.svelte';
	import EditorPane from './EditorPane.svelte';
	import ExtractDialog from './ExtractDialog.svelte';
	import FileList from './FileList.svelte';
	import type { FileCommand } from './keyboard';
	import { archiveFormat } from './language';
	import NameDialog from './NameDialog.svelte';
	import OperationsPanel, { type FileOperation } from './OperationsPanel.svelte';
	import { basename, crumbs, isRoot, join, normalize, parent } from './paths';
	import PermissionsDialog, { type PermissionChange } from './PermissionsDialog.svelte';
	import * as sel from './selection';
	import { UploadQueue } from './uploads.svelte';

	interface Props {
		scope: FileScope;
		/** Name of the root in the breadcrumbs ("silo", "pgdata"). */
		rootLabel: string;
		/** Granted capabilities of the root (the DTO's `actions`). */
		capabilities: readonly string[];
		/** Stack scope: Compose sources, the revisions page, validation and deploy. */
		stack?: StackFiles | null;
		environmentOnline?: boolean;
		environmentName?: string;
		/** Accessible name of the region, e.g. "Files of Silo". */
		label: string;
		/** Heading of the panel (the mockup's "Stack Files"). */
		title?: string;
		/** Extra controls at the end of the header (e.g. the logs drawer toggle). */
		actions?: Snippet;
	}

	let {
		scope,
		rootLabel,
		capabilities,
		stack = null,
		environmentOnline = true,
		environmentName = 'The environment',
		label,
		title = 'Files',
		actions
	}: Props = $props();

	const files = untrack(() => new FilesApi(scope));
	const key = untrack(() => scopeKey(scope));
	const qc = useQueryClient();
	const caps = $derived(new Set(capabilities));
	const can = (verb: FileVerb) => caps.has(filesCapability(scope, verb));
	const writable = $derived(environmentOnline);
	const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);
	const mod = mac ? '⌘' : 'Ctrl+';

	// Where we are: ?path= keeps deep links and the back button working.
	const dir = $derived(normalize(page.url.searchParams.get('path') ?? '.'));
	let selection = $state<sel.Selection>(sel.EMPTY);
	let listEl = $state<HTMLElement | null>(null);

	// Going up puts the cursor on the folder we came from (desktop habit).
	let cursorAfterNavigate: string | null = null;

	function navigate(to: string) {
		const u = new URL(page.url);
		const d = normalize(to);
		if (d === '.') u.searchParams.delete('path');
		else u.searchParams.set('path', d);
		cursorAfterNavigate = d !== dir && dir.startsWith(d === '.' ? '' : d + '/') ? dir : null;
		selection = sel.EMPTY;
		void goto(u, { keepFocus: true, noScroll: true });
	}

	// Listing, filters ---------------------------------------------------------
	let filters = $state<ListFilters>({ sort: 'type', q: '', hidden: false });
	let filterText = $state('');
	/** The details view: Permissions and Owner columns. */
	let details = $state(false);
	$effect(() => {
		const q = filterText.trim();
		const t = setTimeout(() => (filters = { ...untrack(() => filters), q }), 200);
		return () => clearTimeout(t);
	});

	// The previous folder stays on screen while the next one loads, so the
	// list keeps the keyboard focus across folder changes.
	const listing = createInfiniteQuery(() => ({
		...listingQuery(files, dir, filters),
		enabled: can('read'),
		placeholderData: keepPreviousData
	}));
	const rows = $derived<FileEntry[]>(listing.data?.pages.flatMap((p) => p.items) ?? []);
	const total = $derived(listing.data?.pages[0]?.total ?? 0);
	const truncated = $derived(listing.data?.pages[0]?.truncated ?? false);
	/** The root's file manager limits (the manager's configuration). */
	const limits = $derived(listing.data?.pages[0]?.limits);
	const keys = $derived(rows.map((r) => r.path));
	const targets = $derived(sel.targets(selection, keys));
	const targetEntries = $derived(rows.filter((r) => targets.includes(r.path)));

	// Drop selected paths that disappeared (deleted here or elsewhere); after
	// going up, the keyboard cursor rests on the folder we came from (not
	// selected: actions apply to explicit selections only).
	$effect(() => {
		const k = keys;
		if (listing.isPlaceholderData) return;
		const back = cursorAfterNavigate;
		if (back && k.includes(back)) {
			cursorAfterNavigate = null;
			selection = { selected: [], anchor: back, cursor: back };
			return;
		}
		selection = sel.retain(
			untrack(() => selection),
			k
		);
	});

	function refresh() {
		void qc.invalidateQueries({ queryKey: liveKeys.files(liveScopeOf(scope)) });
	}

	// Live updates: declare this open file view (#23) so changes made by
	// other sessions, containers or host tools refresh it.
	onMount(() => {
		const c = liveClient();
		switch (scope.kind) {
			case 'stack':
				c?.setScopes({ stackIds: [scope.stackId] });
				break;
			case 'volume':
				c?.setScopes({ volumes: [`${scope.environmentId}/${scope.volume}`] });
				break;
			case 'template':
				c?.setScopes({ templateIds: [scope.templateId] });
				break;
		}
	});
	onDestroy(() => liveClient()?.setScopes({}));

	// Editor ---------------------------------------------------------------------
	const session = new EditorSession(files);
	const wide = new MediaQuery('min-width: 1024px');
	let pane = $state<'files' | 'editor'>('files');
	const showEditor = $derived(session.tabs.length > 0);
	/** Below 1024 px the list and the editor are tabs, never side by side. */
	const tabbed = $derived(showEditor && !wide.current);
	const paneTabs = $derived([
		{ id: 'files', label: 'Files' },
		{ id: 'editor', label: 'Editor', count: session.tabs.length }
	]);

	function openFile(entry: FileEntry) {
		if (!can('read')) return;
		session.open(entry.path);
		pane = 'editor';
	}

	function onopen(entry: FileEntry) {
		if (entry.type === 'dir') navigate(entry.path);
		else if (entry.type === 'symlink' && entry.linkStatus !== 'inside') {
			toast.info(`${entry.name} points outside this root`, {
				body: `Docker Manager doesn't follow links that leave ${rootLabel}.`
			});
		} else if (entry.type === 'other') {
			toast.info(`${entry.name} can't be opened`, {
				body: 'Devices, sockets and pipes have no content to edit.'
			});
		} else openFile(entry);
	}

	function onisdir(path: string) {
		session.close(path);
		navigate(path);
	}

	// Unsaved edits: ask before leaving the page (and let the browser ask
	// before closing the tab).
	let leaveTo = $state<URL | null>(null);
	let leaveOpen = $state(false);
	let leaving = false;
	beforeNavigate((nav) => {
		if (leaving || session.dirtyCount === 0) return;
		if (nav.to?.url.pathname === page.url.pathname) return; // folder changes keep the editor
		if (nav.type === 'leave') {
			nav.cancel();
			return;
		}
		nav.cancel();
		leaveTo = nav.to?.url ?? null;
		leaveOpen = true;
	});
	function onBeforeUnload(e: BeforeUnloadEvent) {
		if (session.dirtyCount > 0) e.preventDefault();
	}
	onDestroy(() => session.closeAll());

	// Split pane width (resizable, keyboard too).
	let listWidth = $state(380);
	function onSplitKey(e: KeyboardEvent) {
		if (e.key === 'ArrowLeft') listWidth = Math.max(280, listWidth - 24);
		else if (e.key === 'ArrowRight') listWidth = Math.min(900, listWidth + 24);
		else return;
		e.preventDefault();
	}
	function onSplitDown(e: PointerEvent) {
		const start = e.clientX;
		const from = listWidth;
		const el = e.currentTarget as HTMLElement;
		el.setPointerCapture(e.pointerId);
		const move = (ev: PointerEvent) =>
			(listWidth = Math.min(900, Math.max(280, from + ev.clientX - start)));
		const up = () => {
			el.removeEventListener('pointermove', move);
			el.removeEventListener('pointerup', up);
		};
		el.addEventListener('pointermove', move);
		el.addEventListener('pointerup', up);
	}

	// Jobs -----------------------------------------------------------------------
	let operations = $state<FileOperation[]>([]);
	let nextOp = 1;
	function track(job: Job, title: string, done: string) {
		operations = [...operations, { id: nextOp++, jobId: job.id, title, done, finished: false }];
	}
	function onJobFinish(op: FileOperation, job: Job) {
		operations = operations.map((o) =>
			o.id === op.id ? { ...o, finished: true, state: job.state } : o
		);
		refresh();
		if (job.state === 'succeeded') {
			toast.success(op.done);
			setTimeout(() => (operations = operations.filter((o) => o.id !== op.id)), 4000);
		} else if (job.state === 'partial') {
			toast.warn(`${op.title}: partly done`, {
				body: 'Some items failed. The list under the files says which and why.'
			});
		} else if (job.state === 'cancelled') {
			toast.info(`${op.title}: cancelled`);
		} else {
			toast.error(`${op.title} failed`, {
				body:
					job.error?.recovery || job.error?.message || 'See the details under the files.'
			});
		}
	}

	// Uploads ----------------------------------------------------------------------
	const uploads = new UploadQueue({
		url: (d, n, c) => files.uploadUrl(d, n, c),
		maxBytes: () => limits?.uploadMaxBytes,
		ondrained: (items) => {
			refresh();
			const done = items.filter((i) => i.state === 'done').length;
			const failed = items.filter((i) => i.state === 'failed').length;
			if (failed)
				toast.error(`${failed} of ${items.length} uploads failed`, {
					body: 'The list under the files says why.'
				});
			else if (done) toast.success(`Uploaded ${done} ${done === 1 ? 'file' : 'files'}`);
		}
	});
	onDestroy(() => uploads.cancelAll());

	let fileInput = $state<HTMLInputElement | null>(null);
	let folderInput = $state<HTMLInputElement | null>(null);
	let uploadTarget = '.';

	function pickFiles(folder: boolean, target = dir) {
		uploadTarget = target;
		(folder ? folderInput : fileInput)?.click();
	}

	function onPicked(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const list = input.files;
		if (list?.length) void upload(filesFromInput(list), uploadTarget);
		input.value = '';
	}

	async function upload(picked: PickedFile[], target: string) {
		if (!picked.length || !can('write') || !writable) return;
		try {
			const dirs = directoriesOf(picked);
			// Folders first; one that already existed may hold conflicting names.
			const existing: string[] = [''];
			for (const d of dirs) {
				try {
					await files.createEntry(join(target, d), 'dir');
				} catch (e) {
					if (e instanceof ApiRequestError && e.apiError?.code === 'file_exists')
						existing.push(d);
					else throw e;
				}
			}
			const byDir: Record<string, PickedFile[]> = {};
			for (const p of picked) (byDir[p.relDir] ??= []).push(p);
			const conflicts: ConflictItem[] = [];
			for (const [rel, list] of Object.entries(byDir)) {
				if (!existing.includes(rel)) continue; // a folder we just created is empty
				const dest = rel ? join(target, rel) : target;
				const p = await files.preview({
					operation: 'upload',
					destination: dest,
					names: list.map((f) => f.file.name)
				});
				for (const c of p.conflicts)
					conflicts.push({
						...c,
						source: rel ? `${rel}/${basename(c.destination)}` : basename(c.destination)
					});
			}
			let decisions: ReadonlyMap<string, ConflictChoice> = NO_DECISIONS;
			if (conflicts.length) {
				const d = await askConflicts(
					`Upload into ${target === '.' ? rootLabel : target}`,
					conflicts
				);
				if (!d) return;
				decisions = d;
			}
			uploads.enqueue(
				picked.map((p) => {
					const localKey = p.relDir ? `${p.relDir}/${p.file.name}` : p.file.name;
					const d = decisions.get(localKey);
					return {
						file: p.file,
						dir: p.relDir ? join(target, p.relDir) : target,
						name: p.file.name,
						conflict: d === 'overwrite' || d === 'keep_both' ? d : undefined,
						skip: d === 'skip'
					};
				})
			);
		} catch (e) {
			toast.error('The upload could not start', { body: errorMessage(e) });
		}
	}

	async function ondropfiles(dt: DataTransfer, target: string) {
		const picked = await filesFromDrop(dt);
		await upload(picked, target);
	}

	// Conflicts ------------------------------------------------------------------
	let conflict = $state<{
		title: string;
		items: ConflictItem[];
		single: boolean;
		allowSkip: boolean;
		truncated: boolean;
		resolve: (d: Map<string, ConflictChoice> | null) => void;
	} | null>(null);
	let conflictOpen = $state(false);

	function askConflicts(
		title: string,
		items: ConflictItem[],
		o: { single?: boolean; allowSkip?: boolean; truncated?: boolean } = {}
	): Promise<Map<string, ConflictChoice> | null> {
		return new Promise((resolve) => {
			conflict = {
				title,
				items,
				single: !!o.single,
				allowSkip: o.allowSkip ?? true,
				truncated: !!o.truncated,
				resolve
			};
			conflictOpen = true;
		});
	}

	// Copy, cut, paste, move ---------------------------------------------------------
	function describe(paths: string[]): string {
		return paths.length === 1 ? basename(paths[0]) : `${paths.length} items`;
	}
	function where(d: string): string {
		return isRoot(d) ? rootLabel : basename(d);
	}

	function copySelection(mode: 'copy' | 'cut') {
		if (!targets.length) return;
		if (mode === 'cut' && !can('move')) return;
		if (mode === 'copy' && !can('copy')) return;
		fileClipboard.set(mode, key, targets);
		toast.info(`${mode === 'cut' ? 'Cut' : 'Copied'} ${describe(targets)}`, {
			body: `Open a folder and paste (${mod}V) to ${mode === 'cut' ? 'move' : 'copy'} ${targets.length === 1 ? 'it' : 'them'} there.`
		});
	}

	async function transfer(op: 'copy' | 'move', paths: string[], destination: string) {
		if (!writable) return;
		try {
			const preview = await files.preview({ operation: op, paths, destination });
			let decisions: ReadonlyMap<string, ConflictChoice> = NO_DECISIONS;
			if (preview.conflicts.length) {
				const d = await askConflicts(
					`${op === 'move' ? 'Move' : 'Copy'} ${describe(paths)} to ${where(destination)}`,
					preview.conflicts.map((c) => ({
						...c,
						self: op === 'copy' && c.source === c.destination
					})),
					{ truncated: preview.conflictsTruncated }
				);
				if (!d) return;
				decisions = d;
			}
			const { groups, skipped } = groupRequests(paths, preview.conflicts, decisions);
			for (const g of groups) {
				const job =
					op === 'move'
						? await files.move(g.paths, destination, g.conflict as ConflictPolicy)
						: await files.copy(g.paths, destination, g.conflict as ConflictPolicy);
				const verb = op === 'move' ? ['Move', 'Moved'] : ['Copy', 'Copied'];
				track(
					job,
					`${verb[0]} ${describe(g.paths)} to ${where(destination)}`,
					`${verb[1]} ${describe(g.paths)} to ${where(destination)}`
				);
			}
			if (skipped.length && !groups.length) toast.info(`Skipped ${describe(skipped)}`);
			return true;
		} catch (e) {
			toast.error(`${op === 'move' ? 'Moving' : 'Copying'} ${describe(paths)} failed`, {
				body: errorMessage(e)
			});
		}
	}

	async function paste(target = dir) {
		const c = fileClipboard.content;
		if (!c || c.scope !== key) return;
		const ok = await transfer(c.mode === 'cut' ? 'move' : 'copy', c.paths, target);
		if (ok && c.mode === 'cut') fileClipboard.clear();
	}

	// Create, rename --------------------------------------------------------------
	let nameDialog = $state<{
		title: string;
		label: string;
		initial: string;
		confirm: string;
		submit: (name: string) => Promise<unknown>;
	} | null>(null);
	let nameOpen = $state(false);
	const takenHere = $derived(rows.map((r) => r.name));

	function create(type: 'file' | 'dir') {
		nameDialog = {
			title: type === 'file' ? 'New file' : 'New folder',
			label: type === 'file' ? 'File name' : 'Folder name',
			initial: '',
			confirm: type === 'file' ? 'Create file' : 'Create folder',
			submit: async (name) => {
				const entry = await files.createEntry(
					join(dir, name),
					type,
					type === 'file' ? '' : undefined
				);
				toast.success(`Created ${name}`);
				refresh();
				if (type === 'file') openFile(entry);
			}
		};
		nameOpen = true;
	}

	function rename(entry: FileEntry) {
		if (!can('move')) return;
		nameDialog = {
			title: `Rename ${entry.name}`,
			label: 'New name',
			initial: entry.name,
			confirm: 'Rename',
			submit: async (name) => {
				if (name === entry.name) return;
				const job = await files.move([entry.path], parent(entry.path), 'fail', name);
				track(job, `Rename ${entry.name} to ${name}`, `Renamed ${entry.name} to ${name}`);
			}
		};
		nameOpen = true;
	}

	// Delete ---------------------------------------------------------------------
	let deleting = $state<{ paths: string[]; impact: FilePreview['impact'] } | null>(null);
	let deleteOpen = $state(false);
	let deleteBigOpen = $state(false);

	async function requestDelete(paths: string[]) {
		if (!paths.length || !can('delete') || !writable) return;
		try {
			const p = await files.preview({ operation: 'delete', paths });
			deleting = { paths, impact: p.impact };
			if (p.impact.entries > 1) deleteBigOpen = true;
			else deleteOpen = true;
		} catch (e) {
			toast.error(`${describe(paths)} can't be deleted`, { body: errorMessage(e) });
		}
	}

	const deleteConsequences = $derived.by(() => {
		if (!deleting) return [];
		const i = deleting.impact;
		const parts: string[] = [];
		if (i.dirs) parts.push(`${i.dirs} ${i.dirs === 1 ? 'folder' : 'folders'}`);
		if (i.files) parts.push(`${i.files} ${i.files === 1 ? 'file' : 'files'}`);
		if (i.symlinks) parts.push(`${i.symlinks} ${i.symlinks === 1 ? 'link' : 'links'}`);
		if (i.other) parts.push(`${i.other} special ${i.other === 1 ? 'file' : 'files'}`);
		const out = [
			`Deletes ${parts.join(', ') || 'nothing'}${i.truncated ? ' or more' : ''} (${formatBytes(i.bytes)}) from ${where(dir)}. Links are removed, never followed.`,
			'This cannot be undone: Docker Manager keeps no copy.'
		];
		if (stack && deleting.paths.some((p) => isDefinitionFile(p, stack.configFiles)))
			out.push(
				`Removes a Compose source of ${stack.name}: a new revision is recorded; nothing is deployed.`
			);
		const open = deleting.paths.filter((p) => session.get(p));
		if (open.length) out.push(`Closes ${describe(open)} in the editor.`);
		return out;
	});

	async function confirmDelete() {
		if (!deleting) return;
		const paths = deleting.paths;
		const job = await files.remove(paths);
		for (const p of paths) if (session.get(p)) session.close(p);
		selection = sel.clear(selection);
		track(job, `Delete ${describe(paths)}`, `Deleted ${describe(paths)}`);
	}

	// Download, archive, extract, permissions ------------------------------------
	function download(entries: FileEntry[]) {
		if (!entries.length || !can('download')) return;
		const single = entries.length === 1 && entries[0].type === 'file';
		const url = single
			? files.downloadUrl([entries[0].path])
			: files.downloadUrl(
					entries.map((e) => e.path),
					'zip'
				);
		const a = document.createElement('a');
		a.href = url;
		a.download = '';
		document.body.appendChild(a);
		a.click();
		a.remove();
		toast.info(
			single
				? `Downloading ${entries[0].name}`
				: `Downloading ${describe(entries.map((e) => e.path))} as a ZIP archive`
		);
	}

	function downloadPath(path: string) {
		const e = rows.find((r) => r.path === path);
		if (e) download([e]);
		else {
			const a = document.createElement('a');
			a.href = files.downloadUrl([path]);
			a.download = '';
			document.body.appendChild(a);
			a.click();
			a.remove();
		}
	}

	let archiving = $state<string[] | null>(null);
	let archiveOpen = $state(false);
	function requestArchive(paths: string[]) {
		if (!paths.length || !can('archive')) return;
		archiving = paths;
		archiveOpen = true;
	}
	async function createArchive(name: string, format: 'zip' | 'tar.gz') {
		if (!archiving) return;
		const paths = archiving;
		const destination = join(dir, name);
		const p = await files.preview({ operation: 'archive', paths, destination });
		let policy: 'fail' | 'overwrite' | 'keep_both' = 'fail';
		if (p.conflicts.length) {
			const d = await askConflicts(`Create ${name}`, p.conflicts, {
				single: true,
				allowSkip: false
			});
			if (!d) return;
			policy = d.values().next().value === 'overwrite' ? 'overwrite' : 'keep_both';
		}
		const job = await files.archive(paths, destination, format, policy);
		track(job, `Create ${name}`, `Created archive ${name}`);
	}

	let extracting = $state<FileEntry | null>(null);
	let extractOpen = $state(false);
	function requestExtract(entry: FileEntry) {
		if (!can('extract')) return;
		extracting = entry;
		extractOpen = true;
	}
	async function extract(folder: string | null) {
		if (!extracting) return;
		const archive = extracting.path;
		const destination = folder ? join(parent(archive), folder) : parent(archive);
		const p = await files.preview({ operation: 'extract', paths: [archive], destination });
		let policy: 'fail' | 'overwrite' | 'skip' | 'keep_both' = 'fail';
		if (p.conflicts.length) {
			const d = await askConflicts(`Extract ${basename(archive)}`, p.conflicts, {
				single: true,
				truncated: p.conflictsTruncated
			});
			if (!d) return;
			policy = d.values().next().value ?? 'fail';
		}
		const job = await files.extract(archive, destination, policy);
		track(
			job,
			`Extract ${basename(archive)}`,
			`Extracted ${basename(archive)} to ${where(destination)}`
		);
	}

	let permissions = $state<FileEntry[] | null>(null);
	let permissionsOpen = $state(false);
	function requestPermissions(entries: FileEntry[]) {
		if (!entries.length || !(can('chmod') || can('chown'))) return;
		permissions = entries;
		permissionsOpen = true;
	}
	async function applyPermissions(c: PermissionChange) {
		if (!permissions) return;
		const paths = permissions.map((e) => e.path);
		const job = await files.metadata({ paths, ...c });
		track(
			job,
			`Change permissions of ${describe(paths)}`,
			`Changed permissions of ${describe(paths)}`
		);
	}

	// Commands from the keyboard, toolbar and menus -------------------------------
	function oncommand(c: FileCommand) {
		switch (c.kind) {
			case 'copy':
				return copySelection('copy');
			case 'cut':
				return copySelection('cut');
			case 'paste':
				return void paste();
			case 'rename':
				if (targetEntries.length === 1) rename(targetEntries[0]);
				return;
			case 'delete':
				return void requestDelete(targets);
			case 'escape':
				if (selection.selected.length) selection = sel.clear(selection);
				else fileClipboard.clear();
				return;
		}
	}

	const canPaste = $derived(fileClipboard.canPaste(key) && writable);

	function menuFor(entries: FileEntry[]): MenuEntry[] {
		const out: MenuEntry[] = [];
		const w = writable;
		if (entries.length === 0) {
			if (can('write') && w) {
				out.push({ label: 'New file', icon: FilePlus, onSelect: () => create('file') });
				out.push({ label: 'New folder', icon: FolderPlus, onSelect: () => create('dir') });
				out.push({ label: 'Upload files', icon: Upload, onSelect: () => pickFiles(false) });
				out.push({
					label: 'Upload folder',
					icon: FolderUp,
					onSelect: () => pickFiles(true)
				});
			}
			if (canPaste)
				out.push({
					label: 'Paste',
					icon: ClipboardPaste,
					shortcut: `${mod}V`,
					onSelect: () => void paste()
				});
			if (out.length) out.push({ separator: true });
			out.push({ label: 'Refresh', icon: RefreshCw, onSelect: refresh });
			return out;
		}
		const one = entries.length === 1 ? entries[0] : null;
		const paths = entries.map((e) => e.path);
		if (one) {
			out.push({
				label: one.type === 'dir' ? 'Open folder' : 'Open',
				icon: FolderOpen,
				shortcut: 'Enter',
				onSelect: () => onopen(one)
			});
		}
		if (can('download'))
			out.push({
				label: one && one.type === 'file' ? 'Download' : 'Download as ZIP',
				icon: Download,
				onSelect: () => download(entries)
			});
		if (one && one.type === 'file' && archiveFormat(one.name) && can('extract') && w)
			out.push({ label: 'Extract…', icon: PackageOpen, onSelect: () => requestExtract(one) });
		out.push({ separator: true });
		if (can('copy') && w)
			out.push({
				label: 'Copy',
				icon: Copy,
				shortcut: `${mod}C`,
				onSelect: () => {
					fileClipboard.set('copy', key, paths);
					toast.info(`Copied ${describe(paths)}`);
				}
			});
		if (can('move') && w)
			out.push({
				label: 'Cut',
				icon: Scissors,
				shortcut: `${mod}X`,
				onSelect: () => {
					fileClipboard.set('cut', key, paths);
					toast.info(`Cut ${describe(paths)}`);
				}
			});
		if (one && one.type === 'dir' && canPaste)
			out.push({
				label: 'Paste into folder',
				icon: ClipboardPaste,
				onSelect: () => void paste(one.path)
			});
		if (one && can('move') && w)
			out.push({
				label: 'Rename…',
				icon: Pencil,
				shortcut: 'F2',
				onSelect: () => rename(one)
			});
		if ((can('archive') || can('chmod') || can('chown')) && w) out.push({ separator: true });
		if (can('archive') && w)
			out.push({
				label: 'Create archive…',
				icon: FileArchive,
				onSelect: () => requestArchive(paths)
			});
		if ((can('chmod') || can('chown')) && w)
			out.push({
				label: 'Permissions…',
				icon: KeyRound,
				onSelect: () => requestPermissions(entries)
			});
		if (can('delete') && w) {
			out.push({ separator: true });
			out.push({
				label: 'Delete…',
				icon: Trash2,
				tone: 'danger',
				shortcut: mac ? '⌘⌫' : 'Del',
				onSelect: () => void requestDelete(paths)
			});
		}
		// No leading/trailing/double separators.
		return out.filter(
			(e, i, a) =>
				!('separator' in e) || (i > 0 && i < a.length - 1 && !('separator' in a[i - 1]))
		);
	}

	let contextEntries = $state<FileEntry[]>([]);
	const contextItems = $derived(menuFor(contextEntries));
	function oncontext(entry: FileEntry | null) {
		contextEntries = entry
			? rows.filter(
					(r) => sel.targets(selection, keys).includes(r.path) || r.path === entry.path
				)
			: [];
	}

	function rowMenu(entry: FileEntry) {
		return menuFor(
			selection.selected.includes(entry.path) && targetEntries.length > 1
				? targetEntries
				: [entry]
		);
	}

	const clip = $derived(
		fileClipboard.content && fileClipboard.content.scope === key ? fileClipboard.content : null
	);
	const cutSet = $derived(fileClipboard.cutPaths(key));
	const pathCrumbs = $derived(crumbs(dir, rootLabel));
	const denied = $derived(!can('read'));
	const listError = $derived(listing.error);
</script>

<svelte:window onbeforeunload={onBeforeUnload} />

<input bind:this={fileInput} type="file" multiple hidden onchange={onPicked} />
<input bind:this={folderInput} type="file" webkitdirectory multiple hidden onchange={onPicked} />

<section class="fm" class:full={showEditor} aria-label={label}>
	{#if !environmentOnline}
		<div class="offline"><OfflineEnvironment name={environmentName} /></div>
	{/if}
	{#if denied}
		<div class="state">
			<EmptyState
				icon={LockKeyhole}
				title="You can't browse these files"
				description="Ask the owner of this Docker Manager for the “Browse and view files” permission on {rootLabel}."
			/>
		</div>
	{:else}
		<header class="head">
			<h2 class="title">{title}</h2>
			<nav class="crumbs" aria-label="Folder path">
				<ol role="list">
					{#each pathCrumbs as c, i (c.path)}
						<li>
							{#if i < pathCrumbs.length - 1}
								<a
									href={c.path === '.'
										? '?'
										: `?path=${encodeURIComponent(c.path)}`}
									onclick={(e) => {
										e.preventDefault();
										navigate(c.path);
									}}
									ondragover={(e) => e.preventDefault()}
									ondrop={(e) => {
										const raw = e.dataTransfer?.getData(
											'application/x-docker-manager-files'
										);
										if (!raw) return;
										e.preventDefault();
										try {
											const d = JSON.parse(raw);
											if (d.scope === key && can('move'))
												void transfer('move', d.paths, c.path);
										} catch {
											// not ours
										}
									}}
									class:root={i === 0}>{c.label}</a
								>
								<span class="sep" aria-hidden="true">/</span>
							{:else}
								<span class="here" class:root={i === 0} aria-current="page"
									>{c.label}</span
								>
							{/if}
						</li>
					{/each}
				</ol>
			</nav>
			{#if actions}<div class="head-actions">{@render actions()}</div>{/if}
		</header>

		{#snippet browserPane()}
			<div class="browser">
				<div
					class="toolbar"
					class:selecting={targets.length > 0}
					role="toolbar"
					aria-label="File actions"
				>
					{#if targets.length}
						<!-- The selection's actions take the place of Upload/New (same
						     row height, so the list never jumps under the pointer). -->
						<div class="selgroup" role="group" aria-label="Selection">
							<IconButton
								icon={X}
								size="sm"
								label="Clear selection"
								onclick={() => (selection = sel.clear(selection))}
							/>
							<span class="count num" aria-live="polite"
								>{targets.length} selected</span
							>
							{#if can('download')}
								<IconButton
									icon={Download}
									size="sm"
									label="Download selection"
									onclick={() => download(targetEntries)}
								/>
							{/if}
							{#if can('copy') && writable}
								<IconButton
									icon={Copy}
									size="sm"
									label="Copy selection"
									onclick={() => copySelection('copy')}
								/>
							{/if}
							{#if can('move') && writable}
								<IconButton
									icon={Scissors}
									size="sm"
									label="Cut selection"
									onclick={() => copySelection('cut')}
								/>
							{/if}
							{#if can('delete') && writable}
								<IconButton
									icon={Trash2}
									size="sm"
									variant="danger-soft"
									label="Delete selection"
									onclick={() => requestDelete(targets)}
								/>
							{/if}
							<Menu
								label="More actions for the selection"
								items={menuFor(targetEntries)}
							>
								{#snippet trigger(props)}
									<IconButton
										{...props}
										icon={EllipsisVertical}
										size="sm"
										label="More actions"
									/>
								{/snippet}
							</Menu>
						</div>
					{:else if can('write') && writable}
						<Menu
							label="Upload"
							align="start"
							items={[
								{
									label: 'Upload files',
									icon: Upload,
									onSelect: () => pickFiles(false)
								},
								{
									label: 'Upload folder',
									icon: FolderUp,
									onSelect: () => pickFiles(true)
								}
							]}
						>
							{#snippet trigger(props)}
								<Button
									{...props}
									variant="secondary"
									icon={Upload}
									iconEnd={ChevronDown}
									aria-label="Upload"
									><span class="btn-label">Upload</span></Button
								>
							{/snippet}
						</Menu>
						<Button
							variant="secondary"
							icon={FilePlus}
							aria-label="New file"
							onclick={() => create('file')}
							><span class="btn-label">New file</span></Button
						>
						<Button
							variant="secondary"
							icon={FolderPlus}
							aria-label="New folder"
							onclick={() => create('dir')}
							><span class="btn-label">New folder</span></Button
						>
					{/if}
					{#if clip && canPaste}
						<Button variant="secondary" icon={ClipboardPaste} onclick={() => paste()}
							>Paste {clip.paths.length}
							{clip.paths.length === 1 ? 'item' : 'items'}</Button
						>
					{/if}
					<div class="filter" role="search" aria-label="Filter by name">
						<TextField
							label="Filter by name"
							hideLabel
							type="search"
							placeholder="Filter by name"
							bind:value={filterText}
						/>
					</div>
					<IconButton
						icon={filters.hidden ? Eye : EyeOff}
						label={filters.hidden ? 'Hide hidden files' : 'Show hidden files'}
						pressed={filters.hidden}
						onclick={() => (filters = { ...filters, hidden: !filters.hidden })}
					/>
					<span class="details-toggle">
						<IconButton
							icon={ListTree}
							label="Show permissions and owners"
							pressed={details}
							onclick={() => (details = !details)}
						/>
					</span>
					<IconButton icon={RefreshCw} label="Refresh" onclick={refresh} />
				</div>

				<div class="list">
					{#if listing.isPending}
						<div class="state" aria-busy="true"><Skeleton lines={8} /></div>
					{:else if listError}
						<div class="state">
							{#if listError instanceof ApiRequestError && listError.status === 404 && !isRoot(dir)}
								<EmptyState
									compact
									title="{basename(dir)} doesn't exist anymore"
									description="It was moved or deleted. Go back to {rootLabel}."
								>
									{#snippet actions()}
										<Button onclick={() => navigate('.')}
											>Open {rootLabel}</Button
										>
									{/snippet}
								</EmptyState>
							{:else}
								<ErrorState
									compact
									bare
									error={listError}
									title="The files of {where(dir)} could not be listed."
									onretry={() => listing.refetch()}
								/>
							{/if}
						</div>
					{:else}
						<ContextMenu items={contextItems} label="File actions">
							{#snippet children(props)}
								<FileList
									bind:ref={listEl}
									bind:selection
									triggerProps={props}
									{details}
									active={showEditor ? session.active : null}
									{rows}
									{dir}
									showParent={!isRoot(dir)}
									cut={cutSet}
									sort={filters.sort}
									onsort={(s) => (filters = { ...filters, sort: s })}
									label="Files in {where(dir)}"
									hasMore={listing.hasNextPage}
									onloadmore={() =>
										!listing.isFetchingNextPage && listing.fetchNextPage()}
									onopen={(e) => onopen(e)}
									onparent={() => navigate(parent(dir))}
									{oncommand}
									dragScope={key}
									canMove={can('move') && writable}
									canCopy={can('copy') && writable}
									ondropentries={(paths, target, copy) =>
										void transfer(copy ? 'copy' : 'move', paths, target)}
									canUpload={can('write') && writable}
									{ondropfiles}
									{oncontext}
								>
									{#snippet rowActions(entry)}
										<Menu
											label="Actions for {entry.name}"
											items={rowMenu(entry)}
										>
											{#snippet trigger(props)}
												<IconButton
													{...props}
													icon={EllipsisVertical}
													size="sm"
													label="Actions for {entry.name}"
													tabindex={-1}
													tooltip={false}
												/>
											{/snippet}
										</Menu>
									{/snippet}
									{#snippet empty()}
										{#if filters.q}
											<EmptyState
												compact
												title="No names contain “{filters.q}”"
												description="Clear the filter to see every entry of {where(
													dir
												)}."
											>
												{#snippet actions()}
													<Button
														size="sm"
														onclick={() => (filterText = '')}
														>Clear filter</Button
													>
												{/snippet}
											</EmptyState>
										{:else}
											<EmptyState
												compact
												title="{where(dir)} is empty"
												description={can('write') && writable
													? 'Create a file or folder, or drop files here to upload them.'
													: 'Nothing is stored here yet.'}
											>
												{#snippet actions()}
													{#if can('write') && writable}
														<Button
															size="sm"
															icon={FilePlus}
															onclick={() => create('file')}
															>New file</Button
														>
														<Button
															size="sm"
															icon={Upload}
															onclick={() => pickFiles(false)}
															>Upload files</Button
														>
													{/if}
												{/snippet}
											</EmptyState>
										{/if}
									{/snippet}
								</FileList>
							{/snippet}
						</ContextMenu>
					{/if}
				</div>

				<footer class="status num">
					{#if listing.data}
						<span>
							{rows.length < total ? `${rows.length} of ${total}` : total}
							{total === 1 ? 'entry' : 'entries'}{filters.q
								? ` matching “${filters.q}”`
								: ''}
						</span>
						{#if listing.isFetchingNextPage}<span>Loading more…</span>{/if}
						{#if listing.isPlaceholderData}<span role="status"
								>Opening {where(dir)}…</span
							>{/if}
						{#if truncated}
							<span class="warn"
								>This folder holds over 100 000 entries: only the first are listed.</span
							>
						{/if}
					{/if}
					{#if clip}
						<span class="clip" role="status">
							{clip.mode === 'cut' ? 'Cut' : 'Copied'}
							{describe(clip.paths)}: paste in a folder to {clip.mode === 'cut'
								? 'move'
								: 'copy'}
							{clip.paths.length === 1 ? 'it' : 'them'}.
							<IconButton
								icon={X}
								size="sm"
								label="Clear the clipboard"
								onclick={() => fileClipboard.clear()}
							/>
						</span>
					{/if}
				</footer>
				<OperationsPanel
					{uploads}
					{operations}
					onfinish={onJobFinish}
					ondismiss={(op) => (operations = operations.filter((o) => o.id !== op.id))}
				/>
			</div>
		{/snippet}

		{#snippet editorPane()}
			<div class="editor">
				<EditorPane
					{files}
					{session}
					canWrite={can('write') && writable}
					{stack}
					ondownload={downloadPath}
					{onisdir}
					takenNames={(d) => (d === dir ? takenHere : [])}
					editLimit={limits?.editMaxBytes}
				/>
			</div>
		{/snippet}

		<div
			class="split"
			class:editing={showEditor}
			class:tabbed
			style="--list-width: {listWidth}px"
		>
			{#if tabbed}
				<Tabs
					items={paneTabs}
					bind:value={() => pane, (v) => (pane = v === 'editor' ? 'editor' : 'files')}
					label="Files and editor"
				>
					{#snippet panel(id)}
						{#if id === 'editor'}{@render editorPane()}{:else}{@render browserPane()}{/if}
					{/snippet}
				</Tabs>
			{:else}
				{@render browserPane()}
				{#if showEditor}
					<!-- A focusable separator is a widget (ARIA window splitter): arrows resize it. -->
					<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
					<div
						class="splitter"
						role="separator"
						aria-orientation="vertical"
						aria-label="Resize the file list"
						aria-valuemin={280}
						aria-valuemax={900}
						aria-valuenow={listWidth}
						tabindex="0"
						onkeydown={onSplitKey}
						onpointerdown={onSplitDown}
					></div>
					{@render editorPane()}
				{/if}
			{/if}
		</div>
	{/if}
</section>

{#if nameDialog}
	<NameDialog
		bind:open={nameOpen}
		title={nameDialog.title}
		label={nameDialog.label}
		initial={nameDialog.initial}
		confirmLabel={nameDialog.confirm}
		taken={takenHere}
		onsubmit={nameDialog.submit}
	/>
{/if}

{#if conflict}
	<ConflictDialog
		bind:open={conflictOpen}
		title={conflict.title}
		items={conflict.items}
		single={conflict.single}
		allowSkip={conflict.allowSkip}
		truncated={conflict.truncated}
		{rootLabel}
		onresolve={(d) => conflict?.resolve(d)}
	/>
{/if}

{#if deleting}
	<ConfirmDialog
		bind:open={deleteOpen}
		title="Delete {describe(deleting.paths)}?"
		consequences={deleteConsequences}
		confirmLabel="Delete"
		tone="danger"
		onconfirm={confirmDelete}
	/>
	<DestructiveConfirm
		bind:open={deleteBigOpen}
		title="Delete {describe(deleting.paths)}?"
		consequences={deleteConsequences}
		affected={deleting.paths.slice(0, 20).map((p) => ({ label: p }))}
		confirmText={deleting.paths.length === 1 ? basename(deleting.paths[0]) : 'delete'}
		confirmLabel="Delete"
		onconfirm={confirmDelete}
	/>
{/if}

{#if archiving}
	<ArchiveDialog
		bind:open={archiveOpen}
		what={describe(archiving)}
		base={archiving.length === 1 ? basename(archiving[0]) : where(dir)}
		onsubmit={createArchive}
	/>
{/if}

{#if extracting}
	<ExtractDialog
		bind:open={extractOpen}
		archive={extracting.name}
		here={where(parent(extracting.path))}
		stem={extracting.name.replace(/(\.tar\.gz|\.tgz|\.zip)$/i, '')}
		onsubmit={extract}
	/>
{/if}

{#if permissions}
	<PermissionsDialog
		bind:open={permissionsOpen}
		entries={permissions}
		canChmod={can('chmod')}
		canChown={can('chown')}
		preview={(recursive) =>
			files.preview({
				operation: 'metadata',
				paths: permissions!.map((e) => e.path),
				recursive
			})}
		onsubmit={applyPermissions}
	/>
{/if}

<ConfirmDialog
	bind:open={leaveOpen}
	title="Leave with unsaved changes?"
	message="{session.dirtyCount} open {session.dirtyCount === 1
		? 'file has'
		: 'files have'} unsaved edits. Leaving discards them; the files on disk stay as they are."
	confirmLabel="Discard edits and leave"
	tone="danger"
	onconfirm={() => {
		leaving = true;
		session.closeAll();
		if (leaveTo) void goto(leaveTo);
	}}
/>

<style>
	/* As tall as its content (a short folder is a short card), at most the
	   page's height; while the editor is open it takes the whole height. */
	.fm {
		display: flex;
		flex: 0 1 auto;
		flex-direction: column;
		min-height: 0;
		max-height: 100%;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		overflow: hidden;
	}

	.fm.full {
		flex: 1 1 auto;
		height: 100%;
	}

	.offline {
		padding: var(--space-3) var(--space-3) 0;
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-3);
		min-height: 52px;
		padding: var(--space-2) var(--space-4);
		border-bottom: 1px solid var(--border-subtle);
	}

	.title {
		color: var(--text-strong);
		font-size: var(--text-section);
		line-height: var(--leading-section);
		font-weight: var(--weight-semibold);
	}

	.crumbs {
		min-width: 0;
		margin-right: auto;
	}

	/* The path pill beside the title (the mockup's "/opt/stacks/silo"). */
	.crumbs ol {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0;
		margin: 0;
		padding: 1px 4px;
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
		font-family: var(--font-mono);
		font-size: 12.5px;
	}

	.head-actions {
		display: flex;
		align-items: center;
		gap: var(--space-1);
	}

	.crumbs li {
		display: flex;
		align-items: center;
		gap: 2px;
	}

	.crumbs a,
	.crumbs .here {
		padding: 3px 6px;
		border-radius: var(--radius-sm);
		color: var(--text-muted);
		text-decoration: none;
	}

	.crumbs a:hover {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.crumbs .here {
		color: var(--text-strong);
	}

	.sep {
		color: var(--text-faint);
	}

	.split {
		display: flex;
		flex: 1 1 auto;
		min-height: 0;
	}

	/* Files and Editor tabs below 1024 px: the active pane fills the card. */
	.split.tabbed :global(.dy-tabs) {
		display: flex;
		flex: 1;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
	}

	.split.tabbed :global(.dy-tab-list) {
		flex: none;
		padding: 0 var(--space-3);
	}

	.split.tabbed :global(.dy-tab-panel[data-state='active']) {
		display: flex;
		flex: 1;
		min-width: 0;
		min-height: 0;
		padding-top: 0;
	}

	.browser {
		display: flex;
		flex-direction: column;
		flex: 1 1 auto;
		min-width: 0;
		min-height: 0;
		container-type: inline-size;
	}

	/* The details view needs the room of a wide list. */
	@container (max-width: 656px) {
		.toolbar .details-toggle {
			display: none;
		}
	}

	/* A narrow list beside the editor: icon-only Upload, New file and New
	   folder (their names stay their accessible names). */
	@container (max-width: 520px) {
		.btn-label {
			display: none;
		}
	}

	@container (max-width: 460px) {
		.browser .filter {
			min-width: 64px;
		}

		/* The selection's actions need the room: the filter waits. */
		.browser .toolbar.selecting .filter {
			display: none;
		}
	}

	.split.editing:not(.tabbed) .browser {
		flex: 0 0 var(--list-width);
		min-width: 280px;
	}

	.splitter {
		flex: none;
		width: 6px;
		margin: 0 -3px;
		z-index: 1;
		cursor: col-resize;
		background: linear-gradient(var(--border-subtle), var(--border-subtle)) center / 1px 100%
			no-repeat;
	}

	.splitter:hover,
	.splitter:focus-visible {
		background-size: 2px 100%;
		background-image: linear-gradient(var(--accent-text), var(--accent-text));
		outline: none;
	}

	.editor {
		display: flex;
		flex: 1;
		min-width: 0;
		min-height: 0;
	}

	.toolbar {
		display: flex;
		flex: none;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1);
		padding: var(--space-2) var(--space-3);
	}

	.filter {
		flex: 1;
		min-width: 120px;
		max-width: 280px;
		margin-left: auto;
	}

	.details-toggle {
		display: contents;
	}

	.selgroup {
		display: flex;
		align-items: center;
		gap: 2px;
		height: var(--control-height);
		padding: 0 var(--space-1);
		border-radius: var(--radius-sm);
		background: var(--surface-selected);
		color: var(--text-strong);
	}

	.selgroup .count {
		margin: 0 var(--space-2) 0 2px;
		font-weight: var(--weight-medium);
		white-space: nowrap;
	}

	.status .clip {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		margin-left: auto;
		color: var(--text-default);
	}

	.list {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		min-height: 0;
		padding: 0 var(--space-2);
	}

	.state {
		padding: var(--space-5) var(--space-4);
	}

	.status {
		display: flex;
		flex: none;
		flex-wrap: wrap;
		gap: var(--space-3);
		min-height: 30px;
		padding: var(--space-1) var(--space-4);
		border-top: 1px solid var(--border-subtle);
		color: var(--text-muted);
		font-size: var(--text-caption);
		align-items: center;
	}

	.status .warn {
		color: var(--warn);
	}
</style>
