// The file manager's clipboard (#15): Copy / Cut keep root-relative paths
// of one scope until Paste (or Escape). It lives for the tab, survives
// directory changes and never touches the system clipboard: copying between
// roots or hosts needs a dedicated authorized workflow (#15), so Paste only
// works in the scope the paths came from.

export type ClipboardMode = 'copy' | 'cut';

export interface ClipboardContent {
	mode: ClipboardMode;
	/** scopeKey() of the source scope. */
	scope: string;
	paths: string[];
}

export class FileClipboard {
	content = $state<ClipboardContent | null>(null);

	set(mode: ClipboardMode, scope: string, paths: string[]) {
		this.content = paths.length ? { mode, scope, paths: [...paths] } : null;
	}

	clear() {
		this.content = null;
	}

	/** Paths pending a cut in `scope` (drawn dimmed). */
	cutPaths(scope: string): ReadonlySet<string> {
		const c = this.content;
		// A fresh read-only snapshot per call (derived by the view), never mutated.
		// eslint-disable-next-line svelte/prefer-svelte-reactivity
		return new Set(c && c.mode === 'cut' && c.scope === scope ? c.paths : []);
	}

	canPaste(scope: string): boolean {
		return !!this.content && this.content.scope === scope;
	}
}

export const fileClipboard = new FileClipboard();
