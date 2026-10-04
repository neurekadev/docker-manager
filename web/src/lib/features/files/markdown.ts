// The editor's Markdown preview (#22: the mockup's "Preview" belongs to the
// file editor), rendered like GitHub: GitHub Flavored Markdown from
// marked's lexer ($lib/lazy lexMarkdown: tables, task lists,
// strikethrough, autolinks, nested lists, fenced code) and GitHub's alerts
// (`> [!NOTE]`, TIP, IMPORTANT, WARNING, CAUTION). The tokens become the
// tree below, which MarkdownView renders with Svelte elements, never as
// HTML, so file contents cannot inject markup or scripts:
//
//   - links open http, https and mailto targets only; `#heading` links
//     scroll to the heading (GitHub's slugs); other targets stay text;
//   - images show as a link with their alt text (the page loads no image
//     from elsewhere);
//   - raw HTML is reduced to its text, line breaks and images.
import type { Token, Tokens } from 'marked';

export type Inline =
	| { type: 'text'; text: string }
	| { type: 'code'; text: string }
	| { type: 'strong' | 'em' | 'del'; children: Inline[] }
	| { type: 'link'; href: string; children: Inline[] }
	/** A link to a heading of the document (its slug). */
	| { type: 'anchor'; target: string; children: Inline[] }
	| { type: 'image'; alt: string; href: string | null }
	| { type: 'break' };

export type AlertKind = 'note' | 'tip' | 'important' | 'warning' | 'caution';

export interface ListItem {
	/** A task list item's box; null for a plain item. */
	checked: boolean | null;
	blocks: Block[];
}

export type Align = 'left' | 'center' | 'right' | null;

export type Block =
	| { type: 'heading'; level: 1 | 2 | 3 | 4 | 5 | 6; slug: string; children: Inline[] }
	| { type: 'paragraph'; children: Inline[] }
	/** The text of a tight list item (no paragraph spacing). */
	| { type: 'text'; children: Inline[] }
	| { type: 'list'; ordered: boolean; start: number; items: ListItem[] }
	| { type: 'code'; language: string; text: string }
	| { type: 'quote'; blocks: Block[] }
	| { type: 'alert'; kind: AlertKind; blocks: Block[] }
	| { type: 'table'; align: Align[]; header: Inline[][]; rows: Inline[][][] }
	| { type: 'rule' };

/** The titles GitHub gives its alerts. */
export const ALERT_TITLES: Record<AlertKind, string> = {
	note: 'Note',
	tip: 'Tip',
	important: 'Important',
	warning: 'Warning',
	caution: 'Caution'
};

/** Only these link targets are rendered as links. */
export function safeHref(href: string): string | null {
	const h = href.trim();
	if (/^(https?:|mailto:)/i.test(h)) return h;
	return null;
}

const ENTITIES: Record<string, string> = {
	amp: '&',
	lt: '<',
	gt: '>',
	quot: '"',
	apos: "'",
	nbsp: ' ',
	copy: '©',
	reg: '®',
	trade: '™',
	hellip: '…',
	mdash: '—',
	ndash: '–',
	laquo: '«',
	raquo: '»',
	middot: '·',
	bull: '•',
	times: '×',
	larr: '←',
	rarr: '→',
	uarr: '↑',
	darr: '↓',
	check: '✓',
	deg: '°'
};

/** Decodes HTML character references in text (the browser does it on GitHub). */
export function decodeEntities(text: string): string {
	return text.replace(/&(#x[\da-f]+|#\d+|[a-z]+);/gi, (m, ref: string) => {
		if (ref[0] === '#') {
			const code =
				ref[1] === 'x' || ref[1] === 'X' ? parseInt(ref.slice(2), 16) : +ref.slice(1);
			return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : m;
		}
		return ENTITIES[ref.toLowerCase()] ?? m;
	});
}

/** The plain text of inline nodes (heading slugs, image alt text). */
export function plainText(nodes: Inline[]): string {
	return nodes
		.map((n) => {
			switch (n.type) {
				case 'text':
				case 'code':
					return n.text;
				case 'image':
					return n.alt;
				case 'break':
					return ' ';
				default:
					return plainText(n.children);
			}
		})
		.join('');
}

/** GitHub's heading anchor: lower case, punctuation removed, spaces as dashes. */
export function slugify(text: string): string {
	return text
		.trim()
		.toLowerCase()
		.replace(/[^\p{L}\p{M}\p{N}\p{Pc}\- ]/gu, '')
		.replace(/ /g, '-');
}

/** Turns marked's tokens into the preview's tree. */
export function toBlocks(tokens: Token[]): Block[] {
	const slugs = new Map<string, number>();
	const slug = (text: string) => {
		const base = slugify(text);
		const n = slugs.get(base) ?? 0;
		slugs.set(base, n + 1);
		return n ? `${base}-${n}` : base;
	};
	return blocks(tokens, slug);
}

const ALERT = /^\[!(note|tip|important|warning|caution)\][ \t]*(\n|$)/i;

function blocks(tokens: Token[], slug: (text: string) => string): Block[] {
	const out: Block[] = [];
	for (const t of tokens) {
		switch (t.type) {
			case 'heading': {
				const h = t as Tokens.Heading;
				const children = inline(h.tokens);
				out.push({
					type: 'heading',
					level: Math.min(6, Math.max(1, h.depth)) as 1 | 2 | 3 | 4 | 5 | 6,
					slug: slug(plainText(children)),
					children
				});
				break;
			}
			case 'paragraph':
				out.push({ type: 'paragraph', children: inline((t as Tokens.Paragraph).tokens) });
				break;
			case 'text': {
				const x = t as Tokens.Text;
				out.push({
					type: 'text',
					children: x.tokens
						? inline(x.tokens)
						: [{ type: 'text', text: decodeEntities(x.text) }]
				});
				break;
			}
			case 'list': {
				const l = t as Tokens.List;
				out.push({
					type: 'list',
					ordered: l.ordered,
					start: typeof l.start === 'number' ? l.start : 1,
					items: l.items.map((item) => ({
						checked: item.task ? !!item.checked : null,
						blocks: blocks(
							item.tokens.filter((x) => x.type !== 'checkbox'),
							slug
						)
					}))
				});
				break;
			}
			case 'code': {
				const c = t as Tokens.Code;
				out.push({ type: 'code', language: (c.lang ?? '').split(/\s/)[0], text: c.text });
				break;
			}
			case 'blockquote':
				out.push(quote(t as Tokens.Blockquote, slug));
				break;
			case 'table': {
				const tb = t as Tokens.Table;
				out.push({
					type: 'table',
					align: tb.align.map((a) => a ?? null),
					header: tb.header.map((cell) => inline(cell.tokens)),
					rows: tb.rows.map((row) => row.map((cell) => inline(cell.tokens)))
				});
				break;
			}
			case 'hr':
				out.push({ type: 'rule' });
				break;
			case 'html': {
				const children = htmlInline((t as Tokens.HTML).text);
				if (children.some((c) => c.type !== 'break'))
					out.push({ type: 'paragraph', children });
				break;
			}
			// space, def (link definitions are applied by the lexer)
		}
	}
	return out;
}

/** A block quote, or a GitHub alert when its first line is "[!NOTE]" etc. */
function quote(q: Tokens.Blockquote, slug: (text: string) => string): Block {
	const first = q.tokens[0];
	const lead = first?.type === 'paragraph' ? (first as Tokens.Paragraph).tokens[0] : undefined;
	const m = lead?.type === 'text' ? ALERT.exec((lead as Tokens.Text).text) : null;
	if (!m) return { type: 'quote', blocks: blocks(q.tokens, slug) };
	const p = first as Tokens.Paragraph;
	const rest = (lead as Tokens.Text).text.slice(m[0].length);
	const head: Token[] = rest ? [{ ...lead!, text: rest, raw: rest } as Tokens.Text] : [];
	const tokens = [...head, ...p.tokens.slice(1)];
	return {
		type: 'alert',
		kind: m[1].toLowerCase() as AlertKind,
		blocks: blocks([...(tokens.length ? [{ ...p, tokens }] : []), ...q.tokens.slice(1)], slug)
	};
}

function inline(tokens: Token[] | undefined): Inline[] {
	const out: Inline[] = [];
	const text = (s: string) => {
		const last = out[out.length - 1];
		if (last?.type === 'text') out[out.length - 1] = { type: 'text', text: last.text + s };
		else if (s) out.push({ type: 'text', text: s });
	};
	for (const t of tokens ?? []) {
		switch (t.type) {
			case 'text': {
				const x = t as Tokens.Text;
				if (x.tokens?.length) out.push(...inline(x.tokens));
				else text(decodeEntities(x.text));
				break;
			}
			case 'escape':
				text((t as Tokens.Escape).text);
				break;
			case 'codespan':
				out.push({ type: 'code', text: (t as Tokens.Codespan).text });
				break;
			case 'strong':
			case 'em':
			case 'del':
				out.push({
					type: t.type as 'strong' | 'em' | 'del',
					children: inline((t as Tokens.Strong).tokens)
				});
				break;
			case 'br':
				out.push({ type: 'break' });
				break;
			case 'link': {
				const l = t as Tokens.Link;
				const children = inline(l.tokens);
				const href = safeHref(l.href);
				if (href) out.push({ type: 'link', href, children });
				else if (l.href.startsWith('#') && l.href.length > 1)
					out.push({ type: 'anchor', target: anchorTarget(l.href), children });
				else out.push(...children);
				break;
			}
			case 'image': {
				const i = t as Tokens.Image;
				out.push({ type: 'image', alt: decodeEntities(i.text), href: safeHref(i.href) });
				break;
			}
			case 'html':
				out.push(...htmlInline((t as Tokens.Tag).text));
				break;
			default:
				if ('text' in t && typeof t.text === 'string') text(decodeEntities(t.text));
		}
	}
	return out;
}

function anchorTarget(href: string): string {
	let target = href.slice(1);
	try {
		target = decodeURIComponent(target);
	} catch {
		// keep it as written
	}
	return target.toLowerCase();
}

function attribute(tag: string, name: string): string | null {
	const m = new RegExp(`\\s${name}\\s*=\\s*("([^"]*)"|'([^']*)'|([^\\s>]+))`, 'i').exec(tag);
	return m ? (m[2] ?? m[3] ?? m[4] ?? '') : null;
}

/** Raw HTML as text: tags and comments removed, <br> a line break, <img> an image. */
function htmlInline(html: string): Inline[] {
	const out: Inline[] = [];
	const text = (s: string) => {
		const clean = decodeEntities(s.replace(/\s+/g, ' '));
		const last = out[out.length - 1];
		if (last?.type === 'text') last.text += clean;
		else if (clean.trim()) out.push({ type: 'text', text: clean });
	};
	const re = /<!--[\s\S]*?(-->|$)|<\/?([a-z][\w-]*)\b[^>]*>/gi;
	let at = 0;
	for (let m = re.exec(html); m; m = re.exec(html)) {
		text(html.slice(at, m.index));
		at = m.index + m[0].length;
		const tag = m[2]?.toLowerCase();
		if (tag === 'br') out.push({ type: 'break' });
		else if (tag === 'img' && !m[0].startsWith('</')) {
			const src = attribute(m[0], 'src');
			out.push({
				type: 'image',
				alt: decodeEntities(attribute(m[0], 'alt') ?? ''),
				href: src ? safeHref(src) : null
			});
		}
	}
	text(html.slice(at));
	const last = out[out.length - 1];
	if (last?.type === 'text') last.text = last.text.trimEnd();
	const first = out[0];
	if (first?.type === 'text') first.text = first.text.trimStart();
	return out.filter((n) => n.type !== 'text' || n.text);
}
