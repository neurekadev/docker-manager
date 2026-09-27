// Root-relative path helpers of the file manager (#15, docs/internal/api/files.md
// "Paths"): slash-separated, no leading "/", "." is the root.

export const ROOT = '.';

/** Normalizes '' and '/' to the root; strips a trailing slash. */
export function normalize(path: string): string {
	const p = path.replace(/^\/+/, '').replace(/\/+$/, '');
	return p === '' ? ROOT : p;
}

export function isRoot(path: string): boolean {
	return normalize(path) === ROOT;
}

/** Joins a directory and a name (root-aware). */
export function join(dir: string, name: string): string {
	const d = normalize(dir);
	return d === ROOT ? name : `${d}/${name}`;
}

/** The parent directory ('.' for top-level entries and the root). */
export function parent(path: string): string {
	const p = normalize(path);
	const i = p.lastIndexOf('/');
	return i < 0 ? ROOT : p.slice(0, i);
}

export function basename(path: string): string {
	const p = normalize(path);
	if (p === ROOT) return ROOT;
	return p.slice(p.lastIndexOf('/') + 1);
}

/** Lower-case extension without the dot ('' for none and dot files). */
export function extension(name: string): string {
	const i = name.lastIndexOf('.');
	return i <= 0 ? '' : name.slice(i + 1).toLowerCase();
}

export function isHidden(name: string): boolean {
	return name.startsWith('.') && name !== '.' && name !== '..';
}

/** Whether `path` is `dir` itself or inside it. */
export function within(path: string, dir: string): boolean {
	const p = normalize(path);
	const d = normalize(dir);
	return d === ROOT || p === d || p.startsWith(d + '/');
}

export interface PathCrumb {
	label: string;
	path: string;
}

/** Breadcrumbs from the root ("rootLabel") to `path`. */
export function crumbs(path: string, rootLabel: string): PathCrumb[] {
	const out: PathCrumb[] = [{ label: rootLabel, path: ROOT }];
	const p = normalize(path);
	if (p === ROOT) return out;
	let acc = '';
	for (const seg of p.split('/')) {
		acc = acc ? `${acc}/${seg}` : seg;
		out.push({ label: seg, path: acc });
	}
	return out;
}

/**
 * Validates a new name (one path component, docs/internal/api/files.md): returns a
 * user-facing problem or null.
 */
export function nameProblem(name: string): string | null {
	if (name.trim() === '') return 'Enter a name.';
	if (name === '.' || name === '..') return 'Use a name other than . or ..';
	if (name.includes('/') || name.includes('\\')) return 'A name cannot contain / or \\.';
	// eslint-disable-next-line no-control-regex
	if (/[\u0000-\u001f\u007f]/.test(name)) return 'A name cannot contain control characters.';
	if (new TextEncoder().encode(name).length > 255) return 'Use a name of at most 255 bytes.';
	return null;
}

/**
 * The "keep both" name the agent picks for `name` (name (1).ext), used to
 * describe the outcome before the request.
 */
export function keepBothName(name: string, n = 1): string {
	const ext = extension(name);
	if (!ext) return `${name} (${n})`;
	const stem = name.slice(0, name.length - ext.length - 1);
	return `${stem} (${n}).${ext}`;
}
