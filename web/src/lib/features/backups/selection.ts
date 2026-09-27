// Selection of a backup's files and directories for a partial restore
// (#10). The selection holds absolute paths inside the backup, never one
// inside another: ticking a directory selects it whole (the restore makes
// it identical to the backup), unticking something inside a ticked
// directory splits the directory into its other entries. Directories are
// listed lazily, so splitting uses the listings already loaded.

export type TickState = 'checked' | 'mixed' | 'unchecked';

/** Whether `path` equals `dir` or lies below it. */
export function within(path: string, dir: string): boolean {
	return path === dir || path.startsWith(dir === '/' ? '/' : `${dir}/`);
}

/** The parent directory of a backup path ("/" for top-level entries). */
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

/** checked: selected itself or through a directory; mixed: something inside is. */
export function tickState(selection: readonly string[], path: string): TickState {
	if (selection.some((s) => within(path, s))) return 'checked';
	if (selection.some((s) => s !== path && within(s, path))) return 'mixed';
	return 'unchecked';
}

/**
 * Toggles `path`. `children` returns the loaded entries of a directory
 * (undefined when not loaded: a ticked directory whose listing is unknown
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

/** A root of the picker: the project directory or one volume's data. */
export interface PickerRoot {
	path: string;
	label: string;
	kind: 'project' | 'volume';
}

/**
 * The roots a backup's picker shows: the stack's project directory and
 * its volumes, or only `volume` (a volume's page).
 */
export function pickerRoots(
	b: {
		kind?: string;
		stackName?: string;
		volume?: string;
		projectPath?: string;
		volumePaths?: Record<string, string>;
		paths?: string[];
	},
	volume?: string
): PickerRoot[] {
	const vols = Object.entries(b.volumePaths ?? {}).sort(([a], [c]) => a.localeCompare(c));
	const out: PickerRoot[] = [];
	if (!volume && b.kind === 'stack' && b.projectPath)
		out.push({
			path: b.projectPath,
			label: `${b.stackName ?? 'Stack'} project files`,
			kind: 'project'
		});
	for (const [name, path] of vols) {
		if (!volume || name === volume) out.push({ path, label: `Volume ${name}`, kind: 'volume' });
	}
	if (!out.length && b.kind === 'volume' && b.paths?.length) {
		out.push({ path: b.paths[0], label: `Volume ${b.volume ?? ''}`.trim(), kind: 'volume' });
	}
	return out;
}
