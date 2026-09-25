// A small, safe Markdown subset for the editor's README preview (#22: the
// mockup's "Preview" belongs to the file editor). It parses into a tree
// that MarkdownView renders with Svelte elements, never as HTML, so file
// contents cannot inject markup or scripts. Supported: ATX headings,
// paragraphs, bullet and numbered lists, fenced code, block quotes,
// horizontal rules, inline code, **strong**, *emphasis* and
// [links](https://…) (http, https and mailto only).

export type Inline =
	| { type: 'text'; text: string }
	| { type: 'code'; text: string }
	| { type: 'strong'; children: Inline[] }
	| { type: 'em'; children: Inline[] }
	| { type: 'link'; href: string; children: Inline[] };

export type Block =
	| { type: 'heading'; level: 1 | 2 | 3 | 4 | 5 | 6; children: Inline[] }
	| { type: 'paragraph'; children: Inline[] }
	| { type: 'list'; ordered: boolean; items: Inline[][] }
	| { type: 'code'; language: string; text: string }
	| { type: 'quote'; children: Inline[] }
	| { type: 'rule' };

/** Only these link targets are rendered as links. */
export function safeHref(href: string): string | null {
	const h = href.trim();
	if (/^(https?:|mailto:)/i.test(h)) return h;
	return null;
}

/** Parses inline markup of one block's text. */
export function parseInline(src: string): Inline[] {
	const out: Inline[] = [];
	let text = '';
	const flush = () => {
		if (text) out.push({ type: 'text', text });
		text = '';
	};
	let i = 0;
	while (i < src.length) {
		const c = src[i];
		if (c === '\\' && i + 1 < src.length && /[\\`*_[\]()#+\-.!]/.test(src[i + 1])) {
			text += src[i + 1];
			i += 2;
			continue;
		}
		if (c === '`') {
			const end = src.indexOf('`', i + 1);
			if (end > i) {
				flush();
				out.push({ type: 'code', text: src.slice(i + 1, end) });
				i = end + 1;
				continue;
			}
		}
		if ((c === '*' || c === '_') && src[i + 1] === c) {
			const end = src.indexOf(c + c, i + 2);
			if (end > i + 2) {
				flush();
				out.push({ type: 'strong', children: parseInline(src.slice(i + 2, end)) });
				i = end + 2;
				continue;
			}
		}
		if (c === '*' || c === '_') {
			const end = src.indexOf(c, i + 1);
			if (end > i + 1 && src[i + 1] !== ' ') {
				flush();
				out.push({ type: 'em', children: parseInline(src.slice(i + 1, end)) });
				i = end + 1;
				continue;
			}
		}
		if (c === '[') {
			const close = src.indexOf('](', i + 1);
			const end = close > 0 ? src.indexOf(')', close + 2) : -1;
			if (close > 0 && end > 0) {
				const label = src.slice(i + 1, close);
				const href = safeHref(src.slice(close + 2, end));
				flush();
				out.push(
					href
						? { type: 'link', href, children: parseInline(label) }
						: { type: 'text', text: label }
				);
				i = end + 1;
				continue;
			}
		}
		text += c;
		i++;
	}
	flush();
	return out;
}

/** Parses a document into blocks. */
export function parseMarkdown(src: string): Block[] {
	const lines = src.replace(/\r\n?/g, '\n').split('\n');
	const blocks: Block[] = [];
	let para: string[] = [];
	const flushPara = () => {
		if (para.length) blocks.push({ type: 'paragraph', children: parseInline(para.join(' ')) });
		para = [];
	};
	for (let i = 0; i < lines.length; i++) {
		const line = lines[i];
		const fence = /^\s*(```|~~~)\s*([\w+-]*)\s*$/.exec(line);
		if (fence) {
			flushPara();
			const body: string[] = [];
			i++;
			while (i < lines.length && !lines[i].trim().startsWith(fence[1])) body.push(lines[i++]);
			blocks.push({ type: 'code', language: fence[2], text: body.join('\n') });
			continue;
		}
		const heading = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
		if (heading) {
			flushPara();
			blocks.push({
				type: 'heading',
				level: heading[1].length as 1 | 2 | 3 | 4 | 5 | 6,
				children: parseInline(heading[2])
			});
			continue;
		}
		if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
			flushPara();
			blocks.push({ type: 'rule' });
			continue;
		}
		const item = /^\s*([-*+]|\d+[.)])\s+(.*)$/.exec(line);
		if (item) {
			flushPara();
			const ordered = /\d/.test(item[1]);
			const items: Inline[][] = [parseInline(item[2])];
			while (i + 1 < lines.length) {
				const next = /^\s*([-*+]|\d+[.)])\s+(.*)$/.exec(lines[i + 1]);
				if (!next || /\d/.test(next[1]) !== ordered) break;
				items.push(parseInline(next[2]));
				i++;
			}
			blocks.push({ type: 'list', ordered, items });
			continue;
		}
		const quote = /^\s*>\s?(.*)$/.exec(line);
		if (quote) {
			flushPara();
			const body = [quote[1]];
			while (i + 1 < lines.length) {
				const q = /^\s*>\s?(.*)$/.exec(lines[i + 1]);
				if (!q) break;
				body.push(q[1]);
				i++;
			}
			blocks.push({ type: 'quote', children: parseInline(body.join(' ')) });
			continue;
		}
		if (line.trim() === '') {
			flushPara();
			continue;
		}
		para.push(line.trim());
	}
	flushPara();
	return blocks;
}
