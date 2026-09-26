// File manager logic (#15): paths, selection semantics, keyboard mapping and
// the typing guard, per-item conflict grouping, diff, Markdown safety,
// language detection and Compose sources.
import { describe, expect, it } from 'vitest';
import { ApiRequestError } from '$lib/api/client';
import { groupRequests, ConflictQueue, decisionSummary, type ConflictItem } from './conflicts';
import { definitionRefusal, isDefinitionFile } from './definition';
import { diffLines, diffRows } from './diff';
import { directoriesOf } from './dropped';
import { modeString } from './icons';
import { commandFor, isTypingTarget } from './keyboard';
import { archiveFormat, detectLanguage, formattable } from './language';
import { parseInline, parseMarkdown, safeHref } from './markdown';
import * as p from './paths';
import * as sel from './selection';
import { nextSort, sortColumn, sortDirection } from './sort';

describe('paths', () => {
	it('normalizes, joins and splits root-relative paths', () => {
		expect(p.normalize('')).toBe('.');
		expect(p.normalize('/config/')).toBe('config');
		expect(p.join('.', 'a.txt')).toBe('a.txt');
		expect(p.join('config', 'a.txt')).toBe('config/a.txt');
		expect(p.parent('config/a.txt')).toBe('config');
		expect(p.parent('a.txt')).toBe('.');
		expect(p.parent('.')).toBe('.');
		expect(p.basename('config/sub/a.txt')).toBe('a.txt');
		expect(p.extension('archive.tar.gz')).toBe('gz');
		expect(p.extension('.env')).toBe('');
		expect(p.isHidden('.env')).toBe(true);
		expect(p.isHidden('..')).toBe(false);
		expect(p.within('config/sub', 'config')).toBe(true);
		expect(p.within('configs', 'config')).toBe(false);
		expect(p.within('anything', '.')).toBe(true);
	});

	it('builds breadcrumbs from the root', () => {
		expect(p.crumbs('.', 'silo')).toEqual([{ label: 'silo', path: '.' }]);
		expect(p.crumbs('data/thumbnails', 'silo')).toEqual([
			{ label: 'silo', path: '.' },
			{ label: 'data', path: 'data' },
			{ label: 'thumbnails', path: 'data/thumbnails' }
		]);
	});

	it('validates new names like the API and predicts keep-both names', () => {
		expect(p.nameProblem('compose.yaml')).toBeNull();
		expect(p.nameProblem('')).toMatch(/Enter a name/);
		expect(p.nameProblem('..')).toMatch(/other than/);
		expect(p.nameProblem('a/b')).toMatch(/cannot contain/);
		expect(p.nameProblem('a\u0001')).toMatch(/control/);
		expect(p.nameProblem('é'.repeat(128))).toMatch(/255 bytes/);
		expect(p.keepBothName('app.yaml')).toBe('app (1).yaml');
		expect(p.keepBothName('Makefile', 2)).toBe('Makefile (2)');
	});
});

describe('selection', () => {
	const keys = ['a', 'b', 'c', 'd', 'e'];

	it('click selects one, Ctrl/Cmd-click toggles, Shift-click selects a range', () => {
		let s = sel.click(sel.EMPTY, keys, 'b');
		expect(s).toEqual({ selected: ['b'], anchor: 'b', cursor: 'b' });
		s = sel.click(s, keys, 'd', { toggle: true });
		expect(s.selected).toEqual(['b', 'd']);
		s = sel.click(s, keys, 'd', { toggle: true });
		expect(s.selected).toEqual(['b']);
		// The anchor moved to d with the toggle: the range is d..a.
		s = sel.click(s, keys, 'a', { range: true });
		expect(s.selected).toEqual(['a', 'b', 'c', 'd']);
		expect(s.anchor).toBe('d');
		s = sel.click(sel.click(sel.EMPTY, keys, 'a'), keys, 'e', { toggle: true });
		s = sel.click(s, keys, 'c', { range: true, toggle: true });
		expect(new Set(s.selected)).toEqual(new Set(['a', 'c', 'd', 'e']));
		expect(sel.click(s, keys, 'zzz')).toBe(s);
	});

	it('arrows move and select, Shift extends, Ctrl moves only the cursor', () => {
		let s = sel.move(sel.EMPTY, keys, 1);
		expect(s.selected).toEqual(['a']);
		s = sel.move(s, keys, 1);
		expect(s).toEqual({ selected: ['b'], anchor: 'b', cursor: 'b' });
		s = sel.move(s, keys, 2, 'extend');
		expect(s.selected).toEqual(['b', 'c', 'd']);
		s = sel.move(s, keys, 'first', 'extend');
		expect(s.selected).toEqual(['a', 'b']);
		s = sel.move(s, keys, 'last', 'cursor');
		expect(s.cursor).toBe('e');
		expect(s.selected).toEqual(['a', 'b']);
		s = sel.toggleCursor(s);
		expect(s.selected).toEqual(['a', 'b', 'e']);
		expect(sel.move(s, keys, 99).cursor).toBe('e');
		expect(sel.move(sel.EMPTY, keys, -1).cursor).toBe('e');
	});

	it('select all, clear, retain after refresh and targets in display order', () => {
		let s = sel.selectAll(sel.EMPTY, keys);
		expect(s.selected).toEqual(keys);
		s = sel.retain({ selected: ['e', 'b', 'x'], anchor: 'x', cursor: 'x' }, keys);
		expect(s).toEqual({ selected: ['e', 'b'], anchor: null, cursor: null });
		expect(sel.targets(s, keys)).toEqual(['b', 'e']);
		const same = { selected: ['a'], anchor: 'a', cursor: 'a' };
		expect(sel.retain(same, keys)).toBe(same);
		expect(sel.clear({ selected: ['a'], anchor: 'a', cursor: 'c' })).toEqual({
			selected: [],
			anchor: 'c',
			cursor: 'c'
		});
		// Actions never apply to an unselected cursor row.
		expect(sel.targets({ selected: [], anchor: 'a', cursor: 'a' }, keys)).toEqual([]);
	});
});

describe('keyboard', () => {
	it('maps the browser shortcuts', () => {
		expect(commandFor({ key: 'ArrowDown' })).toEqual({
			kind: 'move',
			delta: 1,
			mode: 'select'
		});
		expect(commandFor({ key: 'ArrowUp', shiftKey: true })).toEqual({
			kind: 'move',
			delta: -1,
			mode: 'extend'
		});
		expect(commandFor({ key: 'ArrowDown', ctrlKey: true })).toMatchObject({ mode: 'cursor' });
		expect(commandFor({ key: 'ArrowUp', altKey: true })).toEqual({ kind: 'parent' });
		expect(commandFor({ key: 'Backspace' })).toEqual({ kind: 'parent' });
		expect(commandFor({ key: 'Backspace', metaKey: true })).toEqual({ kind: 'delete' });
		expect(commandFor({ key: 'Delete' })).toEqual({ kind: 'delete' });
		expect(commandFor({ key: 'F2' })).toEqual({ kind: 'rename' });
		expect(commandFor({ key: 'Enter' })).toEqual({ kind: 'open' });
		expect(commandFor({ key: 'Escape' })).toEqual({ kind: 'escape' });
		expect(commandFor({ key: ' ' })).toEqual({ kind: 'toggle' });
		expect(commandFor({ key: 'a', ctrlKey: true })).toEqual({ kind: 'selectAll' });
		expect(commandFor({ key: 'A', metaKey: true })).toEqual({ kind: 'selectAll' });
		expect(commandFor({ key: 'c', ctrlKey: true })).toEqual({ kind: 'copy' });
		expect(commandFor({ key: 'x', metaKey: true })).toEqual({ kind: 'cut' });
		expect(commandFor({ key: 'v', ctrlKey: true })).toEqual({ kind: 'paste' });
		expect(commandFor({ key: 'PageDown' })).toMatchObject({ delta: 'pageDown' });
		// Not ours: plain letters, Ctrl+Shift+C, Ctrl+Enter.
		expect(commandFor({ key: 'a' })).toBeNull();
		expect(commandFor({ key: 'c', ctrlKey: true, shiftKey: true })).toBeNull();
		expect(commandFor({ key: 'Enter', ctrlKey: true })).toBeNull();
	});

	it('never fires while the user types in the editor, a form or a dialog', () => {
		const el = (
			tag: string,
			props: Record<string, unknown> = {},
			closest: string | null = null
		) =>
			({
				tagName: tag,
				isContentEditable: false,
				...props,
				closest: (q: string) => (closest && q.includes(closest) ? {} : null)
			}) as unknown as EventTarget;
		expect(isTypingTarget(el('INPUT'))).toBe(true);
		expect(isTypingTarget(el('TEXTAREA'))).toBe(true);
		expect(isTypingTarget(el('SELECT'))).toBe(true);
		expect(isTypingTarget(el('DIV', { isContentEditable: true }))).toBe(true);
		expect(isTypingTarget(el('DIV', {}, '.cm-editor'))).toBe(true);
		expect(isTypingTarget(el('BUTTON', {}, '[role="dialog"]'))).toBe(true);
		expect(isTypingTarget(el('DIV'))).toBe(false);
		expect(isTypingTarget(null)).toBe(false);
	});
});

describe('conflicts', () => {
	const existing = { name: 'x', type: 'file', size: 1, modifiedAt: '2026-09-25T10:00:00Z' };
	const conflicts: ConflictItem[] = [
		{ source: 'a.txt', destination: 'dst/a.txt', existing },
		{ source: 'b.txt', destination: 'dst/b.txt', existing },
		{ source: 'c.txt', destination: 'dst/c.txt', existing }
	];

	it('asks one conflict at a time; apply to all is explicit', () => {
		const q = new ConflictQueue(conflicts);
		expect(q.current?.source).toBe('a.txt');
		expect(q.position).toBe(1);
		q.decide('overwrite');
		expect(q.current?.source).toBe('b.txt');
		expect(q.remaining).toBe(2);
		q.decide('skip', true);
		expect(q.done).toBe(true);
		expect([...q.decisions]).toEqual([
			['a.txt', 'overwrite'],
			['b.txt', 'skip'],
			['c.txt', 'skip']
		]);
		q.decide('keep_both');
		expect(q.decisions.get('c.txt')).toBe('skip');
		expect(decisionSummary(q.decisions)).toBe('Replace 1, skip 2');
	});

	it('sends one request per decision; free names fail instead of overwriting', () => {
		const decisions = new Map([
			['a.txt', 'overwrite' as const],
			['b.txt', 'keep_both' as const],
			['c.txt', 'skip' as const]
		]);
		const out = groupRequests(
			['a.txt', 'b.txt', 'c.txt', 'd.txt', 'e.txt'],
			conflicts,
			decisions
		);
		expect(out.groups).toEqual([
			{ conflict: 'fail', paths: ['d.txt', 'e.txt'] },
			{ conflict: 'overwrite', paths: ['a.txt'] },
			{ conflict: 'keep_both', paths: ['b.txt'] }
		]);
		expect(out.skipped).toEqual(['c.txt']);
		expect(() => groupRequests(['a.txt'], conflicts, new Map())).toThrow(/no decision/);
		expect(groupRequests(['z'], [], new Map()).groups).toEqual([
			{ conflict: 'fail', paths: ['z'] }
		]);
	});
});

describe('diff', () => {
	it('computes a minimal line diff with numbered rows and context', () => {
		const d = diffLines('a\nb\nc\nd\n', 'a\nB\nc\nd\ne\n');
		expect(d.tooLarge).toBe(false);
		expect(d.added).toBe(2);
		expect(d.removed).toBe(1);
		expect(d.ops.map((o) => `${o.type[0]}${o.text}`)).toEqual([
			'ea',
			'rb',
			'aB',
			'ec',
			'ed',
			'ae'
		]);
		const rows = diffRows(d.ops, 0);
		expect(rows[0]).toEqual({ type: 'skip', text: '1 unchanged lines', a: null, b: null });
		expect(rows.find((r) => r.type === 'add' && r.text === 'e')).toEqual({
			type: 'add',
			text: 'e',
			a: null,
			b: 5
		});
		expect(diffLines('', '').ops).toEqual([]);
		expect(diffLines('same\n', 'same\n').added).toBe(0);
	});

	it('gives up instead of freezing on huge differences', () => {
		const a = Array.from({ length: 300 }, (_, i) => `a${i}`).join('\n');
		const b = Array.from({ length: 300 }, (_, i) => `b${i}`).join('\n');
		expect(diffLines(a, b, 100).tooLarge).toBe(true);
		expect(diffLines(a, b).tooLarge).toBe(false);
	});
});

describe('markdown preview', () => {
	it('parses the supported blocks and inlines', () => {
		const blocks = parseMarkdown(
			'# Silo\n\nPersonal **cloud** and *media*.\n\n- one\n- `two`\n\n1. first\n2. second\n\n```yaml\na: 1\n```\n\n> quote\n\n---\n'
		);
		expect(blocks.map((b) => b.type)).toEqual([
			'heading',
			'paragraph',
			'list',
			'list',
			'code',
			'quote',
			'rule'
		]);
		expect(blocks[4]).toEqual({ type: 'code', language: 'yaml', text: 'a: 1' });
		expect(parseInline('a **b** `c`')).toEqual([
			{ type: 'text', text: 'a ' },
			{ type: 'strong', children: [{ type: 'text', text: 'b' }] },
			{ type: 'text', text: ' ' },
			{ type: 'code', text: 'c' }
		]);
	});

	it('never produces script links or raw HTML', () => {
		expect(safeHref('javascript:alert(1)')).toBeNull();
		expect(safeHref('https://docs.docker.com')).toBe('https://docs.docker.com');
		const nodes = parseInline('[x](javascript:alert(1))');
		expect(nodes.some((n) => n.type === 'link')).toBe(false);
		expect(nodes[0]).toEqual({ type: 'text', text: 'x' });
		// HTML stays text (rendered as text nodes by MarkdownView).
		expect(parseInline('<img src=x onerror=alert(1)>')).toEqual([
			{ type: 'text', text: '<img src=x onerror=alert(1)>' }
		]);
	});
});

describe('languages, Compose sources, modes, folders', () => {
	it('detects editor languages and archive formats', () => {
		expect(detectLanguage('compose.yaml')).toBe('yaml');
		expect(detectLanguage('config/appsettings.json')).toBe('json');
		expect(detectLanguage('.env')).toBe('properties');
		expect(detectLanguage('.env.production')).toBe('properties');
		expect(detectLanguage('Dockerfile')).toBe('dockerfile');
		expect(detectLanguage('nginx.conf')).toBe('nginx');
		expect(detectLanguage('scripts/run.sh')).toBe('shell');
		expect(detectLanguage('README.md')).toBe('markdown');
		expect(detectLanguage('LICENSE')).toBe('text');
		expect(formattable('yaml')).toBe(true);
		expect(formattable('shell')).toBe(false);
		expect(archiveFormat('backup.TGZ')).toBe('tar.gz');
		expect(archiveFormat('site.zip')).toBe('zip');
		expect(archiveFormat('notes.gz')).toBeNull();
	});

	it('knows the Compose sources of a stack', () => {
		expect(isDefinitionFile('compose.yaml')).toBe(true);
		expect(isDefinitionFile('.env')).toBe(true);
		expect(isDefinitionFile('docker-compose.override.yml')).toBe(true);
		expect(isDefinitionFile('config/compose.yaml')).toBe(false);
		expect(isDefinitionFile('prod.yaml', ['prod.yaml'])).toBe(true);
	});

	it('renders modes and orders folders to create', () => {
		expect(modeString('0644')).toBe('rw-r--r--');
		expect(modeString('0755')).toBe('rwxr-xr-x');
		const f = (relDir: string) => ({ file: new File([], 'x'), relDir });
		expect(directoriesOf([f('a/b/c'), f(''), f('a/d')])).toEqual(['a', 'a/b', 'a/d', 'a/b/c']);
	});
});

describe('column sorting', () => {
	it('maps headers to API sorts, folders first by default', () => {
		expect(sortColumn('type')).toBe('name');
		expect(sortDirection('type')).toBe('ascending');
		expect(nextSort('type', 'name')).toBe('-name');
		expect(nextSort('-name', 'name')).toBe('type');
		expect(nextSort('type', 'size')).toBe('-size');
		expect(nextSort('-size', 'size')).toBe('size');
		expect(sortDirection('-modified')).toBe('descending');
	});
});

describe('definitionRefusal', () => {
	it('lists the findings of a save refused as an invalid definition', () => {
		const e = new ApiRequestError('invalid', 422, {
			code: 'invalid_definition',
			message: 'not saved: the Compose definition would be invalid',
			details: [{ field: 'body.content', message: 'invalid_project: yaml: line 2' }]
		} as never);
		expect(definitionRefusal(e)).toBe(
			'Nothing was written: the Compose definition would be invalid: invalid_project: yaml: line 2. Your edits are kept; fix them and save again.'
		);
		expect(definitionRefusal(new ApiRequestError('gone', 404))).toBeNull();
		expect(definitionRefusal(new Error('x'))).toBeNull();
	});
});
