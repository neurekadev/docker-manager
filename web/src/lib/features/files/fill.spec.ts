// Full-height work surfaces: the height fills the viewport below the
// element's top, keeping the page's bottom padding, never below the minimum.
import { describe, expect, it } from 'vitest';
import { fillHeight } from './fill';

describe('fillHeight', () => {
	it('fills the viewport below the top without a page scroll', () => {
		// 1000 px window, surface starting at 260 px, main's 32 px bottom padding.
		expect(fillHeight(1000, 260, 32, 520)).toBe(708);
		expect(260 + fillHeight(1000, 260, 32, 520) + 32).toBe(1000);
	});

	it('grows when content above it goes away', () => {
		expect(fillHeight(1000, 180, 32, 520)).toBeGreaterThan(fillHeight(1000, 260, 32, 520));
	});

	it('never goes below the minimum and rounds down', () => {
		expect(fillHeight(600, 300, 32, 520)).toBe(520);
		expect(fillHeight(1000.6, 260.2, 32, 100)).toBe(708);
	});
});
