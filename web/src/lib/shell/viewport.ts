// Why this exists: iOS (Safari and every other iOS browser, all WebKit)
// zooms the page into any field whose text is smaller than 16 px when it
// gets focus, and does not zoom back out. Docker Manager's fields use 14 px
// (13 px monospace, the code editor's search 12 px) on purpose, so instead
// of enlarging every field on touch screens:
//
// - On iOS only, maximum-scale=1 in the viewport stops that automatic zoom.
//   Since iOS 10, iOS ignores maximum-scale for pinch zoom, so people can
//   still zoom by hand (accessibility is unchanged).
// - Everywhere else the viewport stays as in app.html: other browsers
//   honour maximum-scale and would lose pinch zoom, and they never zoom
//   into focused fields anyway.
//
// Do not raise field font sizes to 16 px to fix the zoom; if iOS ever stops
// honouring maximum-scale for it, revisit this file first.

/** The viewport of app.html. */
export const VIEWPORT = 'width=device-width, initial-scale=1';

/** The viewport on iOS: no zoom into focused fields. */
export const IOS_VIEWPORT = `${VIEWPORT}, maximum-scale=1`;

/** iPhone, iPod or iPad (iPadOS presents itself as a Mac with a touch screen). */
export function isIOS(userAgent: string, maxTouchPoints: number): boolean {
	return (
		/iPhone|iPad|iPod/.test(userAgent) || (/Macintosh/.test(userAgent) && maxTouchPoints > 1)
	);
}

/** On iOS, sets the page's viewport to IOS_VIEWPORT. */
export function preventFocusZoom(
	doc: Pick<Document, 'querySelector'>,
	nav: Pick<Navigator, 'userAgent' | 'maxTouchPoints'>
): void {
	if (!isIOS(nav.userAgent, nav.maxTouchPoints)) return;
	doc.querySelector('meta[name="viewport"]')?.setAttribute('content', IOS_VIEWPORT);
}
