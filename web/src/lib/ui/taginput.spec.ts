import { describe, expect, it } from 'vitest';
import { addTags, isTagKey, splitTagText } from './taginput';

describe('splitTagText', () => {
	it('splits at commas and whitespace and drops empty parts', () => {
		expect(splitTagText(' a, b c,,\td\n')).toEqual(['a', 'b', 'c', 'd']);
		expect(splitTagText('  ')).toEqual([]);
	});
});

describe('isTagKey', () => {
	it('ends a tag on Space, Enter and a comma only', () => {
		expect([' ', 'Enter', ','].every(isTagKey)).toBe(true);
		expect(['a', 'Tab', 'Backspace', '-'].some(isTagKey)).toBe(false);
	});
});

describe('addTags', () => {
	it('normalizes, skips empty ones and duplicates, keeps the order', () => {
		const lower = (s: string) => s.trim().toLowerCase();
		expect(addTags(['web'], ['DB', 'Web', ' ', 'db', 'cache'], lower)).toEqual({
			values: ['web', 'db', 'cache'],
			overflow: []
		});
	});

	it('trims by default', () => {
		expect(addTags([], [' api '])).toEqual({ values: ['api'], overflow: [] });
	});

	it('keeps what does not fit below max as overflow', () => {
		expect(addTags(['a'], ['b', 'c', 'a', 'd'], undefined, 2)).toEqual({
			values: ['a', 'b'],
			overflow: ['c', 'd']
		});
	});
});
