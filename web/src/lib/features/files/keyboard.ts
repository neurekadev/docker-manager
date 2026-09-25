// Keyboard shortcuts of the file list (#15). They apply only while the
// list owns the focus: the list's own keydown handler calls commandFor, and
// events from anything the user types into (the editor, forms, the filter
// field, dialogs) are never interpreted (isTypingTarget).
import type { MoveMode } from './selection';

export type FileCommand =
	| { kind: 'move'; delta: number | 'first' | 'last' | 'pageUp' | 'pageDown'; mode: MoveMode }
	| { kind: 'toggle' }
	| { kind: 'open' }
	| { kind: 'parent' }
	| { kind: 'selectAll' }
	| { kind: 'copy' }
	| { kind: 'cut' }
	| { kind: 'paste' }
	| { kind: 'rename' }
	| { kind: 'delete' }
	| { kind: 'escape' };

export interface KeyLike {
	key: string;
	ctrlKey?: boolean;
	metaKey?: boolean;
	shiftKey?: boolean;
	altKey?: boolean;
}

/** The command of a key press in the file list, or null (not ours). */
export function commandFor(e: KeyLike): FileCommand | null {
	const mod = !!(e.ctrlKey || e.metaKey);
	const shift = !!e.shiftKey;
	const alt = !!e.altKey;
	const mode: MoveMode = shift ? 'extend' : mod ? 'cursor' : 'select';
	switch (e.key) {
		case 'ArrowDown':
			return alt ? null : { kind: 'move', delta: 1, mode };
		case 'ArrowUp':
			return alt ? { kind: 'parent' } : { kind: 'move', delta: -1, mode };
		case 'Home':
			return { kind: 'move', delta: 'first', mode };
		case 'End':
			return { kind: 'move', delta: 'last', mode };
		case 'PageDown':
			return { kind: 'move', delta: 'pageDown', mode };
		case 'PageUp':
			return { kind: 'move', delta: 'pageUp', mode };
		case ' ':
		case 'Spacebar':
			return shift ? null : { kind: 'toggle' };
		case 'Enter':
			return mod || alt ? null : { kind: 'open' };
		case 'Backspace':
			// Cmd+Backspace is "move to trash" on macOS: delete (confirmed).
			return e.metaKey ? { kind: 'delete' } : mod || alt ? null : { kind: 'parent' };
		case 'F2':
			return { kind: 'rename' };
		case 'Delete':
			return { kind: 'delete' };
		case 'Escape':
			return { kind: 'escape' };
	}
	if (mod && !alt && e.key.length === 1) {
		switch (e.key.toLowerCase()) {
			case 'a':
				return shift ? null : { kind: 'selectAll' };
			case 'c':
				return shift ? null : { kind: 'copy' };
			case 'x':
				return shift ? null : { kind: 'cut' };
			case 'v':
				return shift ? null : { kind: 'paste' };
		}
	}
	return null;
}

/**
 * True when the event comes from something the user types into: inputs,
 * text areas, selects, contenteditable (CodeMirror), or any element inside
 * a dialog or menu. File shortcuts never fire there.
 */
export function isTypingTarget(target: EventTarget | null): boolean {
	if (!target || typeof (target as Element).closest !== 'function') return false;
	const el = target as HTMLElement;
	if (el.isContentEditable) return true;
	const tag = el.tagName;
	if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true;
	return !!el.closest(
		'[contenteditable="true"], .cm-editor, [role="dialog"], [role="alertdialog"], [role="menu"]'
	);
}
