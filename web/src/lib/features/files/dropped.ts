// Files and folders dropped from the operating system or picked with the
// folder picker (#15): flattened to files with their directory relative to
// the upload target, so the file manager can create the folders first.

export interface PickedFile {
	file: File;
	/** Directory below the upload target ('' for the target itself). */
	relDir: string;
}

/** Upper bound of files one drop may add (a mistaken drop of a whole disk). */
export const MAX_DROPPED_FILES = 5000;

/** From <input type="file" webkitdirectory> or a plain multi-file input. */
export function filesFromInput(list: FileList | File[]): PickedFile[] {
	return Array.from(list).map((file) => {
		const rel = (file as File & { webkitRelativePath?: string }).webkitRelativePath ?? '';
		const i = rel.lastIndexOf('/');
		return { file, relDir: i > 0 ? rel.slice(0, i) : '' };
	});
}

interface EntryLike {
	isFile: boolean;
	isDirectory: boolean;
	name: string;
	file?: (ok: (f: File) => void, fail: (e: unknown) => void) => void;
	createReader?: () => {
		readEntries: (ok: (e: EntryLike[]) => void, fail: (e: unknown) => void) => void;
	};
}

async function walk(entry: EntryLike, dir: string, out: PickedFile[]): Promise<void> {
	if (out.length >= MAX_DROPPED_FILES) return;
	if (entry.isFile && entry.file) {
		const file = await new Promise<File>((ok, fail) => entry.file!(ok, fail));
		out.push({ file, relDir: dir });
		return;
	}
	if (entry.isDirectory && entry.createReader) {
		const sub = dir ? `${dir}/${entry.name}` : entry.name;
		const reader = entry.createReader();
		// readEntries answers in batches until it returns an empty one.
		for (;;) {
			const batch = await new Promise<EntryLike[]>((ok, fail) =>
				reader.readEntries(ok, fail)
			);
			if (batch.length === 0) break;
			for (const e of batch) await walk(e, sub, out);
		}
	}
}

/** Whether a drag carries files from the operating system. */
export function hasOsFiles(dt: DataTransfer | null): boolean {
	return !!dt && Array.from(dt.types).includes('Files');
}

/** Everything dropped: files, and folders walked recursively. */
export async function filesFromDrop(dt: DataTransfer): Promise<PickedFile[]> {
	const out: PickedFile[] = [];
	const entries: EntryLike[] = [];
	for (const item of Array.from(dt.items ?? [])) {
		if (item.kind !== 'file') continue;
		const entry = (
			item as DataTransferItem & { webkitGetAsEntry?: () => EntryLike | null }
		).webkitGetAsEntry?.();
		if (entry) entries.push(entry);
		else {
			const f = item.getAsFile();
			if (f) out.push({ file: f, relDir: '' });
		}
	}
	for (const e of entries) await walk(e, '', out);
	if (entries.length === 0 && out.length === 0)
		for (const f of Array.from(dt.files)) out.push({ file: f, relDir: '' });
	return out.slice(0, MAX_DROPPED_FILES);
}

/** Directories to create before the files, parents first. */
export function directoriesOf(files: readonly PickedFile[]): string[] {
	const dirs = new Set<string>();
	for (const f of files) {
		if (!f.relDir) continue;
		const parts = f.relDir.split('/');
		for (let i = 1; i <= parts.length; i++) dirs.add(parts.slice(0, i).join('/'));
	}
	return [...dirs].sort(
		(a, b) => a.split('/').length - b.split('/').length || a.localeCompare(b)
	);
}
