// Setup of the jsdom component tests (*.test.ts): jest-dom matchers and the
// browser APIs jsdom lacks but Bits UI and the shell use.
import '@testing-library/jest-dom/vitest';

class ResizeObserverStub {
	observe() {}
	unobserve() {}
	disconnect() {}
}

const g = globalThis as unknown as Record<string, unknown>;
g.ResizeObserver ??= ResizeObserverStub;

if (!window.matchMedia) {
	window.matchMedia = (query: string) =>
		({
			matches: false,
			media: query,
			onchange: null,
			addEventListener() {},
			removeEventListener() {},
			addListener() {},
			removeListener() {},
			dispatchEvent: () => false
		}) as MediaQueryList;
}

Element.prototype.scrollIntoView ??= function () {};

// jsdom has no layout: every element reports no client rects, so focus
// management (tabbable, used by Bits UI focus scopes) would treat all of
// them as hidden. Report one rect for rendered elements, none for
// display:none subtrees, as a browser would.
function renderedInTree(start: Element): boolean {
	for (let el: Element | null = start; el; el = el.parentElement) {
		if (getComputedStyle(el).display === 'none' || (el as HTMLElement).hidden) return false;
	}
	return true;
}

Element.prototype.getClientRects = function (this: Element) {
	return (renderedInTree(this) ? [new DOMRect(0, 0, 10, 10)] : []) as unknown as DOMRectList;
};

if (!('requestAnimationFrame' in window)) {
	g.requestAnimationFrame = (cb: FrameRequestCallback) =>
		setTimeout(() => cb(performance.now()), 0);
	g.cancelAnimationFrame = (id: number) => clearTimeout(id);
}
Element.prototype.hasPointerCapture ??= () => false;
Element.prototype.releasePointerCapture ??= () => {};
Element.prototype.setPointerCapture ??= () => {};

if (!('PointerEvent' in window)) {
	class PointerEventStub extends MouseEvent {
		pointerId: number;
		pointerType: string;
		constructor(type: string, init: PointerEventInit = {}) {
			super(type, init);
			this.pointerId = init.pointerId ?? 1;
			this.pointerType = init.pointerType ?? 'mouse';
		}
	}
	g.PointerEvent = PointerEventStub;
}

// Clipboard for copy buttons (tests inspect navigator.clipboard.writeText).
if (!navigator.clipboard) {
	let text = '';
	Object.defineProperty(navigator, 'clipboard', {
		configurable: true,
		value: {
			writeText: async (t: string) => {
				text = t;
			},
			readText: async () => text
		}
	});
}
