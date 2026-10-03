import { afterEach, describe, expect, it, vi } from 'vitest';
import { Sortable } from './sortable.svelte';

// Two rows with their grip handles, attached the way DragHandle does.
function rows(sort: Sortable) {
	const cleanups: (() => void)[] = [];
	const handles = [0, 1].map((i) => {
		const item = document.createElement('li');
		const handle = document.createElement('button');
		item.append(handle);
		document.body.append(item);
		cleanups.push(
			sort.item(i)(item) as () => void,
			sort.handle(i, `Row ${i + 1}`)(handle) as () => void
		);
		return handle;
	});
	return { handles, cleanup: () => cleanups.forEach((c) => c()) };
}

describe('Sortable', () => {
	afterEach(() => document.body.replaceChildren());

	it('moves a row with the arrow keys on its handle', () => {
		const onmove = vi.fn();
		const { handles, cleanup } = rows(new Sortable({ onmove }));
		handles[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
		expect(onmove).toHaveBeenCalledWith(0, 1);
		cleanup();
	});

	it('ignores moves while canMove is false, without announcing one', () => {
		const onmove = vi.fn();
		let busy = true;
		const { handles, cleanup } = rows(new Sortable({ onmove, canMove: () => !busy }));
		const key = new KeyboardEvent('keydown', {
			key: 'ArrowDown',
			bubbles: true,
			cancelable: true
		});
		handles[0].dispatchEvent(key);
		expect(onmove).not.toHaveBeenCalled();
		expect(document.querySelector('[data-announcer]')?.textContent ?? '').toBe('');
		// The key is still taken, so the page does not scroll.
		expect(key.defaultPrevented).toBe(true);
		busy = false;
		handles[1].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }));
		expect(onmove).toHaveBeenCalledWith(1, 0);
		cleanup();
	});
});
