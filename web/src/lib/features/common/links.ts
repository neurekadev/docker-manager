// Links of stacks and templates (documentation, website, repository): the
// server's rules (domain.NormalizeLinks) checked in the browser so the
// editor can say what is wrong before saving, and the text a link shows.
import type { Schema } from '$lib/api/client';

export type WebLink = Schema<'WebLink'>;

export const MAX_LINKS = 10;
export const MAX_LINK_URL = 2048;
export const MAX_LINK_LABEL = 60;

/** A row of the links editor (both fields as typed). */
export interface LinkRow {
	label: string;
	url: string;
	/** Identifies the row while the list changes (newLinkRow, linkRows). */
	key?: number;
}

let lastKey = 0;

/** A new, empty editor row. */
export function newLinkRow(label = '', url = ''): LinkRow {
	return { label, url, key: ++lastKey };
}

/** What is wrong with a row (absent: nothing). */
export interface LinkRowProblem {
	label?: string;
	url?: string;
}

const chars = (s: string) => [...s].length;
// Whitespace or control characters anywhere in an address.
// eslint-disable-next-line no-control-regex
const SPACE_OR_CONTROL = /[\s\u0000-\u001f\u007f-\u009f]/;
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

/** What is wrong with a (trimmed) link address, or null. */
export function linkUrlProblem(url: string): string | null {
	if (!url) return 'Enter the address of the link.';
	if (chars(url) > MAX_LINK_URL) return `Use at most ${MAX_LINK_URL} characters.`;
	const scheme = 'Use a web address that starts with http:// or https://.';
	// Browsers accept "https:example.com" and "https:///host"; the server
	// does not.
	if (SPACE_OR_CONTROL.test(url) || !/^https?:\/\/[^/\\]/i.test(url)) return scheme;
	let u: URL;
	try {
		u = new URL(url);
	} catch {
		return scheme;
	}
	if ((u.protocol !== 'http:' && u.protocol !== 'https:') || !u.hostname) return scheme;
	if (u.username || u.password || /^https?:\/\/[^/?#]*@/i.test(url))
		return 'Remove the user name and password from the address.';
	return null;
}

/** What is wrong with a (trimmed) label, or null. */
export function linkLabelProblem(label: string): string | null {
	if (CONTROL.test(label)) return 'Remove line breaks from the label.';
	if (chars(label) > MAX_LINK_LABEL) return `Use at most ${MAX_LINK_LABEL} characters.`;
	return null;
}

const blank = (r: LinkRow) => !r.label.trim() && !r.url.trim();

/** Trimmed links of the rows, leaving out rows with neither label nor address. */
export function cleanLinks(rows: LinkRow[]): WebLink[] {
	return rows
		.filter((r) => !blank(r))
		.map((r) => {
			const label = r.label.trim();
			const url = r.url.trim();
			return label ? { label, url } : { url };
		});
}

/**
 * The problems of every row (same index as rows; blank rows have none) and
 * of the list as a whole (too many links).
 */
export function linkProblems(rows: LinkRow[]): { rows: LinkRowProblem[]; list: string | null } {
	const seen = new Set<string>();
	let count = 0;
	const out = rows.map((r): LinkRowProblem => {
		if (blank(r)) return {};
		count++;
		const label = r.label.trim();
		const url = r.url.trim();
		const p: LinkRowProblem = {};
		const urlProblem = linkUrlProblem(url);
		if (urlProblem) p.url = urlProblem;
		else if (seen.has(url)) p.url = 'This address is already listed.';
		seen.add(url);
		const labelProblem = linkLabelProblem(label);
		if (labelProblem) p.label = labelProblem;
		return p;
	});
	return { rows: out, list: count > MAX_LINKS ? `Keep at most ${MAX_LINKS} links.` : null };
}

/** Whether the rows can be saved. */
export function linksValid(rows: LinkRow[]): boolean {
	const p = linkProblems(rows);
	return !p.list && p.rows.every((r) => !r.label && !r.url);
}

/** Editor rows from saved links. */
export function linkRows(links: WebLink[] | undefined): LinkRow[] {
	return (links ?? []).map((l) => newLinkRow(l.label ?? '', l.url));
}

/** Whether two link lists are the same (order included). */
export function sameLinks(a: WebLink[] | undefined, b: WebLink[] | undefined): boolean {
	const x = a ?? [];
	const y = b ?? [];
	return (
		x.length === y.length &&
		x.every((l, i) => (l.label ?? '') === (y[i].label ?? '') && l.url === y[i].url)
	);
}

/** The text a link shows: its label, else the address's host. */
export function linkText(link: WebLink): string {
	if (link.label) return link.label;
	try {
		return new URL(link.url).host || link.url;
	} catch {
		return link.url;
	}
}

/**
 * The server's problems with submitted links (fields body.links,
 * body.links[i].url, body.links[i].label) placed on the editor's rows: the
 * server counts the submitted links, which leave out blank rows.
 */
export function serverLinkProblems(
	rows: LinkRow[],
	fields: { field: string; message: string }[]
): { rows: LinkRowProblem[]; list: string | null } {
	const submitted: number[] = [];
	rows.forEach((r, i) => {
		if (!blank(r)) submitted.push(i);
	});
	const out: LinkRowProblem[] = rows.map(() => ({}));
	let list: string | null = null;
	for (const f of fields) {
		const m = /^body\.links(?:\[(\d+)\](?:\.(url|label))?)?$/.exec(f.field);
		if (!m) continue;
		const message = sentence(f.message);
		const row = m[1] === undefined ? undefined : submitted[Number(m[1])];
		if (row === undefined) list = message;
		else out[row][(m[2] as 'url' | 'label' | undefined) ?? 'url'] = message;
	}
	return { rows: out, list };
}

function sentence(s: string): string {
	const t = s.trim();
	if (!t) return t;
	const cap = t[0].toUpperCase() + t.slice(1);
	return /[.!?]$/.test(cap) ? cap : `${cap}.`;
}
