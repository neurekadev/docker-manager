import { describe, expect, it } from 'vitest';
import { dropIndex, keyTarget, moveItem, movedMessage, shiftOf } from './sortable';

describe('moveItem', () => {
	it('moves an item down and up without changing the input', () => {
		const items = ['a', 'b', 'c', 'd'];
		expect(moveItem(items, 0, 2)).toEqual(['b', 'c', 'a', 'd']);
		expect(moveItem(items, 3, 1)).toEqual(['a', 'd', 'b', 'c']);
		expect(items).toEqual(['a', 'b', 'c', 'd']);
	});

	it('returns an unchanged copy for the same or an out-of-range index', () => {
		const items = ['a', 'b'];
		expect(moveItem(items, 1, 1)).toEqual(['a', 'b']);
		expect(moveItem(items, -1, 0)).toEqual(['a', 'b']);
		expect(moveItem(items, 0, 2)).toEqual(['a', 'b']);
		expect(moveItem(items, 0, 0)).not.toBe(items);
	});
});

describe('dropIndex', () => {
	// Four rows 40 px tall, 8 px apart: tops 0, 48, 96, 144; middles 20, 68, 116, 164.
	const mids = [20, 68, 116, 164];
	// Row 1 (48 to 88) moved by dy.
	const row1 = (dy: number) => dropIndex(mids, 1, 48 + dy, 88 + dy);

	it('stays in place until an edge passes a neighbour’s middle', () => {
		expect(row1(0)).toBe(1);
		expect(row1(20)).toBe(1);
		expect(row1(-20)).toBe(1);
	});

	it('passes every middle an edge crossed, both ways', () => {
		expect(row1(30)).toBe(2);
		expect(row1(200)).toBe(3);
		expect(row1(-30)).toBe(0);
		// Row 3 (144 to 184) raised by 80: its top (64) is above rows 1 and 2.
		expect(dropIndex(mids, 3, 64, 104)).toBe(1);
	});

	it('reaches the last place when held at the end of the list', () => {
		// Row 0 (0 to 40) clamped to the list's bottom (144 to 184).
		expect(dropIndex(mids, 0, 144, 184)).toBe(3);
	});
});

describe('shiftOf', () => {
	it('moves the rows between the start and the target the other way', () => {
		// Row 0 held over position 2: rows 1 and 2 move up.
		expect([0, 1, 2, 3].map((i) => shiftOf(i, 0, 2))).toEqual([0, -1, -1, 0]);
		// Row 3 held over position 1: rows 1 and 2 move down.
		expect([0, 1, 2, 3].map((i) => shiftOf(i, 3, 1))).toEqual([0, 1, 1, 0]);
		expect([0, 1, 2].map((i) => shiftOf(i, 1, 1))).toEqual([0, 0, 0]);
	});
});

describe('keyTarget', () => {
	it('moves one place with the arrows and to the ends with Home and End', () => {
		expect(keyTarget('ArrowUp', 2, 4)).toBe(1);
		expect(keyTarget('ArrowDown', 2, 4)).toBe(3);
		expect(keyTarget('Home', 2, 4)).toBe(0);
		expect(keyTarget('End', 1, 4)).toBe(3);
	});

	it('is null past the ends, in place and for other keys', () => {
		expect(keyTarget('ArrowUp', 0, 4)).toBeNull();
		expect(keyTarget('ArrowDown', 3, 4)).toBeNull();
		expect(keyTarget('Home', 0, 4)).toBeNull();
		expect(keyTarget('Enter', 1, 4)).toBeNull();
	});
});

describe('movedMessage', () => {
	it('names the row and its new place counted from one', () => {
		expect(movedMessage('Link 2', 0, 3)).toBe('Moved Link 2 to position 1 of 3.');
	});
});
