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
