import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { IOS_VIEWPORT, isIOS, preventFocusZoom, VIEWPORT } from './viewport';

const IPHONE =
	'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1';
const IPAD_DESKTOP_MODE =
	'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15';
const ANDROID =
	'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36';

function fakeDoc(content = VIEWPORT) {
	const meta = {
		content,
		setAttribute(name: string, value: string) {
			if (name === 'content') meta.content = value;
		}
	};
	return {
		meta,
		doc: {
			querySelector: (sel: string) => (sel === 'meta[name="viewport"]' ? meta : null)
		} as unknown as Pick<Document, 'querySelector'>
	};
}

describe('isIOS', () => {
	it('knows iPhones and iPads, also iPads that present themselves as a Mac', () => {
		expect(isIOS(IPHONE, 5)).toBe(true);
		expect(isIOS(IPAD_DESKTOP_MODE, 5)).toBe(true);
	});

	it('leaves Macs without touch and Android alone', () => {
		expect(isIOS(IPAD_DESKTOP_MODE, 0)).toBe(false);
		expect(isIOS(ANDROID, 5)).toBe(false);
	});
});

describe('preventFocusZoom', () => {
	it('limits the scale on iOS', () => {
		const { meta, doc } = fakeDoc();
		preventFocusZoom(doc, { userAgent: IPHONE, maxTouchPoints: 5 });
		expect(meta.content).toBe(IOS_VIEWPORT);
		expect(IOS_VIEWPORT).toBe('width=device-width, initial-scale=1, maximum-scale=1');
	});

	it('keeps pinch zoom elsewhere: the viewport stays as it is', () => {
		const { meta, doc } = fakeDoc();
		preventFocusZoom(doc, { userAgent: ANDROID, maxTouchPoints: 5 });
		expect(meta.content).toBe(VIEWPORT);
	});

	it('does nothing without a viewport tag', () => {
		const doc = { querySelector: () => null } as unknown as Pick<Document, 'querySelector'>;
		expect(() => preventFocusZoom(doc, { userAgent: IPHONE, maxTouchPoints: 5 })).not.toThrow();
	});
});

describe('app.html', () => {
	it('starts with VIEWPORT, the viewport iOS extends', () => {
		const html = readFileSync(new URL('../../app.html', import.meta.url), 'utf8');
		expect(html).toContain(`<meta name="viewport" content="${VIEWPORT}" />`);
	});
});
