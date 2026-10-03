// Polite screen reader announcements (#22) for changes that have no
// visible text of their own (a row moved by keyboard). One shared,
// visually hidden live region at the end of the body, created on first use.

let region: HTMLElement | null = null;

// Written as a code point: the literal character trips no-irregular-whitespace.
const NO_BREAK_SPACE = String.fromCharCode(0xa0);

/** Announces `message` politely (role="status"). */
export function announce(message: string): void {
	if (typeof document === 'undefined') return;
	if (!region || !region.isConnected) {
		region = document.createElement('div');
		region.className = 'sr-only';
		region.setAttribute('role', 'status');
		region.setAttribute('aria-live', 'polite');
		region.setAttribute('data-announcer', '');
		document.body.append(region);
	}
	// The same words again still change the text (a trailing no-break
	// space), so they are read again.
	region.textContent = region.textContent === message ? `${message}${NO_BREAK_SPACE}` : message;
}
