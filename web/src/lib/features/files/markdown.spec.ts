// The editor's Markdown preview (#22): GitHub Flavored Markdown from
// marked's lexer turned into a tree MarkdownView renders as elements, with
// GitHub's alerts, heading slugs and no way to inject markup or scripts.
import { describe, expect, it } from 'vitest';
import { Lexer } from 'marked';
import { decodeEntities, safeHref, slugify, toBlocks, type Block, type Inline } from './markdown';

const parse = (src: string) => toBlocks(Lexer.lex(src, { gfm: true }));
const para = (src: string): Inline[] => {
	const [b] = parse(src);
	if (b?.type !== 'paragraph') throw new Error(`not a paragraph: ${b?.type}`);
	return b.children;
};

describe('markdown preview', () => {
	it('parses blocks and inlines', () => {
		const blocks = parse(
			'# Silo\n\nPersonal **cloud**, *media* and ~~cruft~~.\n\n- one\n- `two`\n\n3. third\n4. fourth\n\n```yaml\na: 1\n```\n\n> quote\n\n---\n'
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
		expect(blocks[1]).toEqual({
			type: 'paragraph',
			children: [
				{ type: 'text', text: 'Personal ' },
				{ type: 'strong', children: [{ type: 'text', text: 'cloud' }] },
				{ type: 'text', text: ', ' },
				{ type: 'em', children: [{ type: 'text', text: 'media' }] },
				{ type: 'text', text: ' and ' },
				{ type: 'del', children: [{ type: 'text', text: 'cruft' }] },
				{ type: 'text', text: '.' }
			]
		});
		expect(blocks[3]).toMatchObject({ type: 'list', ordered: true, start: 3 });
		expect(blocks[4]).toEqual({ type: 'code', language: 'yaml', text: 'a: 1' });
	});

	it('reads task lists, nested lists and tables', () => {
		const [list, table] = parse(
			'- [x] done\n- [ ] todo\n  - nested\n\n| Name | Size |\n|:-----|-----:|\n| a | `1` |\n'
		);
		expect(list).toMatchObject({
			type: 'list',
			items: [
				{
					checked: true,
					blocks: [{ type: 'text', children: [{ type: 'text', text: 'done' }] }]
				},
				{
					checked: false,
					blocks: [{ type: 'text' }, { type: 'list', items: [{ checked: null }] }]
				}
			]
		});
		expect(table).toEqual({
			type: 'table',
			align: ['left', 'right'],
			header: [[{ type: 'text', text: 'Name' }], [{ type: 'text', text: 'Size' }]],
			rows: [[[{ type: 'text', text: 'a' }], [{ type: 'code', text: '1' }]]]
		});
	});

	it("turns GitHub's alerts into alerts and keeps other quotes", () => {
		const blocks = parse(
			'> [!NOTE]\n> Read this.\n\n> [!caution]\n>\n> Second paragraph.\n\n> [!TIP] not alone\n'
		);
		expect(blocks[0]).toEqual({
			type: 'alert',
			kind: 'note',
			blocks: [{ type: 'paragraph', children: [{ type: 'text', text: 'Read this.' }] }]
		});
		expect(blocks[1]).toEqual({
			type: 'alert',
			kind: 'caution',
			blocks: [{ type: 'paragraph', children: [{ type: 'text', text: 'Second paragraph.' }] }]
		});
		// The marker must be alone on its line.
		expect(blocks[2].type).toBe('quote');
	});

	it('gives headings GitHub slugs and links them', () => {
		const blocks = parse(
			'# Getting Started!\n\n## Getting Started\n\nSee [setup](#Getting-Started-1).\n'
		);
		expect(
			blocks.slice(0, 2).map((b) => (b as Extract<Block, { type: 'heading' }>).slug)
		).toEqual(['getting-started', 'getting-started-1']);
		expect((blocks[2] as Extract<Block, { type: 'paragraph' }>).children[1]).toEqual({
			type: 'anchor',
			target: 'getting-started-1',
			children: [{ type: 'text', text: 'setup' }]
		});
		expect(slugify('Ünïcode & Co. v2')).toBe('ünïcode--co-v2');
	});

	it('links only safe targets and autolinks', () => {
		expect(safeHref('javascript:alert(1)')).toBeNull();
		expect(safeHref('https://docs.docker.com')).toBe('https://docs.docker.com');
		expect(para('[x](javascript:alert(1)) [y](docs/a.md)')).toEqual([
			{ type: 'text', text: 'x ' },
			{ type: 'text', text: 'y' }
		]);
		expect(para('Visit www.example.com')[1]).toEqual({
			type: 'link',
			href: 'http://www.example.com',
			children: [{ type: 'text', text: 'www.example.com' }]
		});
	});

	it('never produces markup from HTML and shows images as links', () => {
		expect(para('a <img src=x onerror=alert(1)> b<br>c')).toEqual([
			{ type: 'text', text: 'a ' },
			{ type: 'image', alt: '', href: null },
			{ type: 'text', text: ' b' },
			{ type: 'break' },
			{ type: 'text', text: 'c' }
		]);
		expect(
			parse(
				'<p align="center"><img src="https://x.test/logo.png" alt="Logo"><br>Hi &amp; bye</p>\n'
			)
		).toEqual([
			{
				type: 'paragraph',
				children: [
					{ type: 'image', alt: 'Logo', href: 'https://x.test/logo.png' },
					{ type: 'break' },
					{ type: 'text', text: 'Hi & bye' }
				]
			}
		]);
		expect(parse('<!-- hidden -->\n')).toEqual([]);
		expect(para('![Build](https://ci.test/badge.svg)')).toEqual([
			{ type: 'image', alt: 'Build', href: 'https://ci.test/badge.svg' }
		]);
	});

	it('decodes character references in text, not in code', () => {
		expect(decodeEntities('&copy; &#169; &#xA9; &unknown;')).toBe('© © © &unknown;');
		expect(para('&copy; `&copy;`')).toEqual([
			{ type: 'text', text: '© ' },
			{ type: 'code', text: '&copy;' }
		]);
	});
});
