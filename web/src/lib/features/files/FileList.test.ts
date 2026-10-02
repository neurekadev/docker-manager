// The file list's mouse and keyboard paths (#15): click, Ctrl/Cmd-click,
// Shift-click, checkboxes (touch), the ".." row, arrows, Space, Enter,
// Ctrl/Cmd+A/C/X/V, F2, Delete, Escape, sorting headers, and no shortcuts
// while typing elsewhere.
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import FileListHarness from '../../../test/FileListHarness.svelte';
import type { FileEntry } from './api';

function e(name: string, type: FileEntry['type'] = 'file'): FileEntry {
	return {
		name,
		path: `config/${name}`,
		type,
		size: 10,
		mode: type === 'dir' ? '0755' : '0644',
		uid: 0,
		gid: 0,
		modifiedAt: '2026-09-25T10:00:00Z'
	};
}

const rows = [e('certs', 'dir'), e('app.yaml'), e('b.json'), e('c.env'), e('d.txt')];

function setup(extra: Record<string, unknown> = {}) {
	const events: string[] = [];
	const user = userEvent.setup({ pointerEventsCheck: 0 });
	render(FileListHarness, { props: { rows, events, ...extra } });
	const selected = () => screen.getByTestId('selected').textContent;
	const row = (name: RegExp) => screen.getByRole('row', { name });
	return {
		events,
		user,
		selected,
		row,
		grid: screen.getByRole('grid', { name: 'Files in config' })
	};
}

describe('FileList', () => {
	it('selects with click, Ctrl/Cmd-click and Shift-click; opens files on click, folders on double-click', async () => {
		const { events, user, selected, row } = setup();
		await user.click(row(/app\.yaml/));
		// Opening a file is not selecting it: no selection actions appear.
		expect(selected()).toBe('');
		expect(screen.getByTestId('cursor').textContent).toBe('config/app.yaml');
		expect(row(/app\.yaml/)).toHaveAttribute('aria-selected', 'false');
		expect(events).toContain('open:config/app.yaml:pointer');
		await user.keyboard('{Control>}');
		await user.click(row(/c\.env/));
		await user.keyboard('{/Control}');
		expect(selected()).toBe('config/c.env');
		await user.keyboard('{Shift>}');
		await user.click(row(/d\.txt/));
		await user.keyboard('{/Shift}');
		expect(selected()).toBe('config/c.env,config/d.txt');
		expect(row(/d\.txt/)).toHaveAttribute('aria-selected', 'true');
		// Modified clicks only select.
		expect(events.filter((x) => x.startsWith('open:'))).toEqual([
			'open:config/app.yaml:pointer'
		]);
		await user.click(row(/certs/));
		expect(events).not.toContain('open:config/certs:pointer');
		expect(selected()).toBe('config/certs');
		await user.dblClick(row(/certs/));
		expect(events).toContain('open:config/certs:pointer');
	});

	it('checkboxes toggle rows without touching the rest; the header selects all', async () => {
		const { user, selected } = setup();
		await user.click(screen.getByRole('checkbox', { name: 'Select b.json' }));
		await user.click(screen.getByRole('checkbox', { name: 'Select d.txt' }));
		expect(selected()).toBe('config/b.json,config/d.txt');
		await user.click(screen.getByRole('checkbox', { name: 'Select All Entries' }));
		expect(selected()?.split(',')).toHaveLength(5);
		await user.click(screen.getByRole('checkbox', { name: 'Select All Entries' }));
		expect(selected()).toBe('');
	});

	it('shows the ".." row below the root; it opens the parent', async () => {
		const { events, user } = setup();
		const up = screen.getByRole('row', { name: /Parent Folder/ });
		await user.dblClick(up);
		expect(events).toContain('parent');
		expect(up).not.toHaveAttribute('aria-selected');
	});

	it('has no ".." row at the root', () => {
		setup({ dir: '.' });
		expect(screen.queryByRole('row', { name: /Parent Folder/ })).toBeNull();
	});

	it('drives selection and commands from the keyboard while it has focus', async () => {
		const { events, user, selected, grid } = setup();
		grid.focus();
		await user.keyboard('{ArrowDown}'); // the ".." row: never selected
		expect(selected()).toBe('');
		expect(screen.getByTestId('cursor').textContent).toBe('..');
		await user.keyboard('{Enter}');
		expect(events).toContain('parent');
		await user.keyboard('{ArrowDown}{ArrowDown}');
		expect(selected()).toBe('config/app.yaml');
		expect(grid).toHaveAttribute(
			'aria-activedescendant',
			screen.getByRole('row', { name: /app\.yaml/ }).id
		);
		await user.keyboard('{Shift>}{ArrowDown}{ArrowDown}{/Shift}');
		expect(selected()).toBe('config/app.yaml,config/b.json,config/c.env');
		await user.keyboard('{Control>}{ArrowDown}{/Control} ');
		expect(selected()).toBe('config/app.yaml,config/b.json,config/c.env,config/d.txt');
		await user.keyboard('{Enter}');
		expect(events).toContain('open:config/d.txt:keyboard');
		await user.keyboard('{Control>}a{/Control}');
		expect(selected()?.split(',')).toHaveLength(5);
		await user.keyboard(
			'{Control>}c{/Control}{Control>}x{/Control}{Control>}v{/Control}{F2}{Delete}{Escape}'
		);
		expect(events.filter((x) => x.startsWith('command:'))).toEqual([
			'command:copy',
			'command:cut',
			'command:paste',
			'command:rename',
			'command:delete',
			'command:escape'
		]);
		await user.keyboard('{Backspace}');
		expect(events.filter((x) => x === 'parent')).toHaveLength(2);
	});

	it('ignores the shortcuts while the user types elsewhere', async () => {
		const { events, user, selected } = setup();
		await user.click(screen.getByRole('textbox', { name: 'Other field' }));
		await user.keyboard('{Control>}a{/Control}{Delete}{F2}{ArrowDown}');
		expect(selected()).toBe('');
		expect(events).toEqual([]);
	});

	it('shows permissions and owners only in the details view', () => {
		setup();
		expect(screen.queryByRole('columnheader', { name: 'Permissions' })).toBeNull();
		expect(screen.queryByRole('columnheader', { name: 'Owner' })).toBeNull();
		expect(screen.getByRole('row', { name: /b\.json/ })).not.toHaveTextContent('rw-r--r--');
	});

	it('the details view reads modes and owners in words', () => {
		setup({ details: true });
		expect(screen.getByRole('columnheader', { name: 'Permissions' })).toBeInTheDocument();
		expect(screen.getByRole('columnheader', { name: 'Owner' })).toBeInTheDocument();
		const r = screen.getByRole('row', { name: /b\.json/ });
		expect(r).toHaveTextContent('rw-r--r--');
		expect(r).toHaveTextContent('root');
		expect(r).not.toHaveTextContent('0:0');
	});

	it('sorts by header and marks cut entries', async () => {
		const { events, user } = setup({ cut: ['config/b.json'] });
		const name = screen.getByRole('columnheader', { name: /Name/ });
		expect(name).toHaveAttribute('aria-sort', 'ascending');
		await user.click(screen.getByRole('button', { name: /Name/ }));
		expect(events).toContain('sort:-name');
		await user.click(screen.getByRole('button', { name: /Size/ }));
		expect(events).toContain('sort:-size');
		expect(screen.getByRole('row', { name: /b\.json/ })).toHaveTextContent(/cut/);
	});
});
