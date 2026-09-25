// One-time codes arrive in the URL fragment (#code=…), which browsers never
// send to a server or put in Referer. Read it once, then remove it from the
// address bar so it does not stay in history or screenshots.

export function takeCodeFromFragment(
	loc: Pick<Location, 'hash' | 'pathname' | 'search'> = location
): string {
	const params = new URLSearchParams(loc.hash.replace(/^#/, ''));
	const code = params.get('code') ?? '';
	if (code && typeof history !== 'undefined') {
		history.replaceState(history.state, '', loc.pathname + loc.search);
	}
	return code;
}

/** The code from a pasted link (…#code=…) or a pasted code itself. */
export function codeFromPasted(input: string): string {
	const s = input.trim();
	const i = s.indexOf('#');
	if (i < 0) return s;
	return new URLSearchParams(s.slice(i + 1)).get('code') ?? '';
}
