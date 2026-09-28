// The file editor (CodeMirror): Tab and Shift-Tab indent and outdent the
// line instead of moving focus (Escape, then Tab, still leaves the editor).
import { afterEach, describe, expect, it } from 'vitest';
import { mountCodeEditor, type CodeEditorHandle } from './index';

let handle: CodeEditorHandle | undefined;
afterEach(() => {
	handle?.destroy();
	document.body.innerHTML = '';
});

function press(el: Element, shiftKey = false) {
	el.dispatchEvent(
		new KeyboardEvent('keydown', {
			key: 'Tab',
			keyCode: 9,
			shiftKey,
			bubbles: true,
			cancelable: true
		})
	);
}

describe('mountCodeEditor', () => {
	it('indents with Tab and outdents with Shift-Tab', async () => {
		const parent = document.createElement('div');
		document.body.append(parent);
		handle = await mountCodeEditor(parent, 'a: 1', { language: 'yaml' });
		const content = parent.querySelector('.cm-content');
		expect(content).not.toBeNull();
		handle.focus();

		press(content!);
		expect(handle.text()).toMatch(/^\s+a: 1$/);
		press(content!, true);
		expect(handle.text()).toBe('a: 1');
	});
});
