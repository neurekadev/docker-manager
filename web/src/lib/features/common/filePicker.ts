// The shared file picker (FilePicker.svelte): browse places one folder at
// a time and choose one file or several files and folders. Paths are
// absolute and slash-separated ("/stacks/web/compose.yaml"); a source
// with relative paths maps them. In several-items mode the selection
// holds no path inside another: ticking a folder selects it whole,
// unticking something inside a ticked folder splits the folder into its
// other entries. Folders are listed lazily, so splitting uses the
// listings already loaded.
import type { IconComponent } from '$lib/design/icons';

export interface PickerEntry {
	name: string;
	path: string;
	type: 'dir' | 'file' | 'symlink' | 'other';
	size?: number;
	mtime?: string;
}

export interface PickerListing {
	/** The folder's entries, without the folder itself. */
	entries: PickerEntry[];
	/** Only the first entries are listed. */
	truncated: boolean;
}

/** One folder's listing as a query (the picker caches and reads it back). */
export interface PickerSource {
	query: (dir: string) => {
		queryKey: readonly unknown[];
		queryFn: (ctx: { signal: AbortSignal }) => Promise<PickerListing>;
	};
}

/** A place to browse (a stack's project files, a volume); never left upwards. */
export interface PickerPlace {
	path: string;
	label: string;
	icon: IconComponent;
}

export type TickState = 'checked' | 'mixed' | 'unchecked';

/** Whether `path` equals `dir` or lies below it. */
export function within(path: string, dir: string): boolean {
	return path === dir || path.startsWith(dir === '/' ? '/' : `${dir}/`);
}

/** The parent folder of a path ("/" for top-level entries). */
export function parentOf(path: string): string {
	const i = path.lastIndexOf('/');
	return i <= 0 ? '/' : path.slice(0, i);
}

/** Sorted, without duplicates and without paths inside a selected one. */
export function normalize(paths: readonly string[]): string[] {
	const sorted = [...new Set(paths)].sort();
	const out: string[] = [];
	for (const p of sorted) if (!out.some((d) => within(p, d))) out.push(p);
	return out;
}

/** checked: selected itself or through a folder; mixed: something inside is. */
export function tickState(selection: readonly string[], path: string): TickState {
	if (selection.some((s) => within(path, s))) return 'checked';
	if (selection.some((s) => s !== path && within(s, path))) return 'mixed';
	return 'unchecked';
}

/**
 * Toggles `path`. `children` returns the loaded entries of a folder
 * (undefined when not loaded: a ticked folder whose listing is unknown
 * cannot be split, and the selection stays as it is).
 */
export function toggle(
	selection: readonly string[],
	path: string,
	children: (dir: string) => readonly string[] | undefined
): string[] {
	const state = tickState(selection, path);
	if (state !== 'checked') {
		return normalize([...selection.filter((s) => !within(s, path)), path]);
	}
	if (selection.includes(path)) return selection.filter((s) => s !== path);
	// Ticked through an ancestor: replace the ancestor by its other entries,
	// level by level down to path.
	const ancestor = selection.find((s) => within(path, s))!;
	const added: string[] = [];
	let dir = ancestor;
	while (dir !== path) {
		const entries = children(dir);
		if (!entries) return [...selection];
		const next = entries.find((e) => e !== dir && within(path, e));
		if (!next) return [...selection];
		added.push(...entries.filter((e) => e !== next && e !== dir));
		dir = next;
	}
	return normalize([...selection.filter((s) => s !== ancestor), ...added]);
}

/** The folders from `ancestor` down to the parent of `path`: what splitting `ancestor` needs listed. */
export function foldersBetween(ancestor: string, path: string): string[] {
	const out: string[] = [];
	if (path === ancestor) return out;
	for (let d = parentOf(path); within(d, ancestor); d = parentOf(d)) {
		out.unshift(d);
		if (d === ancestor || d === '/') break;
	}
	return out;
}

/** The place holding `path` (the deepest one), if any. */
export function placeOf<P extends { path: string }>(
	places: readonly P[],
	path: string
): P | undefined {
	let best: P | undefined;
	for (const p of places)
		if (within(path, p.path) && (!best || p.path.length > best.path.length)) best = p;
	return best;
}

/** Where the picker opens: the folder of the first chosen path below a place, else the first place. */
export function pickerStart(
	places: readonly { path: string }[],
	chosen: readonly string[]
): string {
	const p = chosen
		.map((c) => c.trim())
		.find((c) => {
			const place = placeOf(places, c);
			return place && c !== place.path;
		});
	return p ? parentOf(p) : (places[0]?.path ?? '/');
}

/** Crumbs from the place (named by its label) down to `dir`. */
export function pickerCrumbs(
	place: { path: string; label: string },
	dir: string
): { name: string; path: string }[] {
	const out = [{ name: place.label, path: place.path }];
	if (!within(dir, place.path) || dir === place.path) return out;
	let acc = place.path === '/' ? '' : place.path;
	for (const part of dir.slice(acc.length).split('/').filter(Boolean)) {
		acc += `/${part}`;
		out.push({ name: part, path: acc });
	}
	return out;
}

/** Folders first, then by name; `filter` keeps names containing it (any case). */
export function pickerEntries(entries: readonly PickerEntry[], filter = ''): PickerEntry[] {
	const f = filter.trim().toLowerCase();
	return entries
		.filter((e) => !f || e.name.toLowerCase().includes(f))
		.sort(
			(a, b) =>
				Number(b.type === 'dir') - Number(a.type === 'dir') || a.name.localeCompare(b.name)
		);
}

/** How many selected paths lie in `place`. */
export function selectedIn(selection: readonly string[], place: string): number {
	return selection.filter((s) => within(s, place)).length;
}

/** "Nothing selected", "1 item selected", "3 items selected". */
export function selectionText(n: number): string {
	return n === 0 ? 'Nothing selected' : `${n} ${n === 1 ? 'item' : 'items'} selected`;
}
