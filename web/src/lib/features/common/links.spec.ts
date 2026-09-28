import { describe, expect, it } from 'vitest';
import {
	MAX_LINKS,
	cleanLinks,
	linkLabelProblem,
	linkProblems,
	linkRows,
	linkText,
	linkUrlProblem,
	linksValid,
	newLinkRow,
	sameLinks,
	serverLinkProblems
} from './links';

const SCHEME = 'Use a web address that starts with http:// or https://.';

describe('link rules (the server’s, checked in the browser)', () => {
	it('accepts absolute http and https addresses', () => {
		for (const url of [
			'https://docs.example.com/guide?page=2#install',
			'http://192.168.1.10:8080/',
			'HTTPS://Example.com',
			'https://[::1]:8443/x',
			'https://example.com/' + 'a'.repeat(2048 - 20)
		])
			expect(linkUrlProblem(url), url).toBeNull();
	});

	it('refuses other schemes, relative addresses, spaces and credentials', () => {
		for (const url of [
			'javascript:alert(1)',
			'data:text/html,<b>x</b>',
			'file:///etc/passwd',
			'ftp://example.com/',
			'//example.com/path',
			'example.com',
			'https:example.com',
			'https:///path',
			'https://exa mple.com',
			'https://example.com/\npath'
		])
			expect(linkUrlProblem(url), url).toBe(SCHEME);
		expect(linkUrlProblem('https://user:secret@example.com/')).toBe(
			'Remove the user name and password from the address.'
		);
		expect(linkUrlProblem('https://token@example.com/')).toBe(
			'Remove the user name and password from the address.'
		);
		expect(linkUrlProblem('')).toBe('Enter the address of the link.');
		expect(linkUrlProblem('https://example.com/' + 'a'.repeat(2048))).toBe(
			'Use at most 2048 characters.'
		);
	});

	it('bounds labels to 60 characters on one line', () => {
		expect(linkLabelProblem('')).toBeNull();
		expect(linkLabelProblem('é'.repeat(60))).toBeNull();
		expect(linkLabelProblem('é'.repeat(61))).toBe('Use at most 60 characters.');
		expect(linkLabelProblem('Docs\nmore')).toBe('Remove line breaks from the label.');
	});

	it('checks every row, leaving blank rows alone, and finds repeated addresses', () => {
		const rows = [
			newLinkRow('Docs', ' https://docs.example.com '),
			newLinkRow(),
			newLinkRow('', 'javascript:alert(1)'),
			newLinkRow('Again', 'https://docs.example.com'),
			newLinkRow('a'.repeat(61), 'https://ok.example')
		];
		const p = linkProblems(rows);
		expect(p.rows).toEqual([
			{},
			{},
			{ url: SCHEME },
			{ url: 'This address is already listed.' },
			{ label: 'Use at most 60 characters.' }
		]);
		expect(p.list).toBeNull();
		expect(linksValid(rows)).toBe(false);
		expect(linksValid([rows[0], rows[1]])).toBe(true);
	});

	it('allows at most 10 links', () => {
		const rows = Array.from({ length: MAX_LINKS + 1 }, (_, i) =>
			newLinkRow('', `https://example.com/${i}`)
		);
		expect(linkProblems(rows).list).toBe('Keep at most 10 links.');
		expect(linksValid(rows.slice(0, MAX_LINKS))).toBe(true);
	});
});

describe('link rows and display', () => {
	it('saves trimmed links without blank rows and without empty labels', () => {
		expect(
			cleanLinks([
				newLinkRow(' Docs ', ' https://docs.example.com '),
				newLinkRow(' ', ''),
				newLinkRow('', 'https://git.example.com/app')
			])
		).toEqual([
			{ label: 'Docs', url: 'https://docs.example.com' },
			{ url: 'https://git.example.com/app' }
		]);
	});

	it('round-trips saved links through editor rows with a key each', () => {
		const rows = linkRows([
			{ label: 'Docs', url: 'https://docs.example.com' },
			{ url: 'https://x.example' }
		]);
		expect(rows.map((r) => [r.label, r.url])).toEqual([
			['Docs', 'https://docs.example.com'],
			['', 'https://x.example']
		]);
		expect(new Set(rows.map((r) => r.key)).size).toBe(2);
		expect(linkRows(undefined)).toEqual([]);
		expect(
			sameLinks(cleanLinks(rows), [
				{ label: 'Docs', url: 'https://docs.example.com' },
				{ url: 'https://x.example' }
			])
		).toBe(true);
		expect(
			sameLinks(cleanLinks(rows), [
				{ url: 'https://x.example' },
				{ label: 'Docs', url: 'https://docs.example.com' }
			])
		).toBe(false);
		expect(sameLinks(undefined, [])).toBe(true);
	});

	it('shows the label, or else the host', () => {
		expect(linkText({ label: 'Docs', url: 'https://docs.example.com/x' })).toBe('Docs');
		expect(linkText({ url: 'https://git.example.com:8443/app?x=1' })).toBe(
			'git.example.com:8443'
		);
		expect(linkText({ url: 'not a url' })).toBe('not a url');
	});

	it('places the server’s problems on the rows that were sent', () => {
		const rows = [
			newLinkRow('Docs', 'https://docs.example.com'),
			newLinkRow(),
			newLinkRow('', 'https://bad.example')
		];
		const p = serverLinkProblems(rows, [
			{ field: 'body.links[1].url', message: 'must not contain a user name or password' },
			{ field: 'body.name', message: 'other' }
		]);
		expect(p.rows).toEqual([{}, {}, { url: 'Must not contain a user name or password.' }]);
		expect(p.list).toBeNull();
		expect(
			serverLinkProblems(rows, [
				{ field: 'body.links', message: 'expected at most 10 items' }
			]).list
		).toBe('Expected at most 10 items.');
	});
});
