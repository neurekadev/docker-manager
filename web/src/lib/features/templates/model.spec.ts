import { describe, expect, it } from 'vitest';
import { applyListFilters, emptyFilterState } from '$lib/features/resources/filters';
import {
	nextVersionLabel,
	parseTags,
	tagCounts,
	tagProblem,
	templateFilters,
	templateSearch
} from './model';
import type { Template } from './queries';

const tpl = (id: string, tags: string[], extra: Partial<Template> = {}): Template =>
	({
		id,
		name: `Template ${id}`,
		visibility: 'private',
		view: 'full',
		actions: [],
		versions: 0,
		tags,
		...extra
	}) as Template;

describe('templates model', () => {
	const all = [
		tpl('a', ['web', 'proxy'], { visibility: 'public', latest: { label: '1.0.0' } as never }),
		tpl('b', ['web']),
		tpl('c', ['db'], { description: 'Postgres with backups' })
	];

	it('counts tags, most used first', () => {
		expect(tagCounts(all)).toEqual([
			{ tag: 'web', count: 2 },
			{ tag: 'db', count: 1 },
			{ tag: 'proxy', count: 1 }
		]);
	});

	it('filters by tag, visibility and publication', () => {
		const defs = templateFilters(all);
		const run = (values: Record<string, string>, q = '') =>
			applyListFilters(all, defs, { ...emptyFilterState(), q, values }, templateSearch).map(
				(t) => t.id
			);
		expect(run({ tag: 'web' })).toEqual(['a', 'b']);
		expect(run({ visibility: 'public' })).toEqual(['a']);
		expect(run({ published: 'no' })).toEqual(['b', 'c']);
		expect(run({}, 'postgres')).toEqual(['c']);
		expect(run({}, 'proxy')).toEqual(['a']);
	});

	it('suggests the next version label', () => {
		expect(nextVersionLabel(undefined)).toBe('1.0.0');
		expect(nextVersionLabel('1.2.0')).toBe('1.2.1');
		expect(nextVersionLabel('v3')).toBe('v4');
		expect(nextVersionLabel('2024-06')).toBe('2024-07');
		expect(nextVersionLabel('beta')).toBe('beta.1');
	});

	it('parses and checks tags', () => {
		expect(parseTags(' Web, reverse proxy,web\nDB ')).toEqual(['db', 'reverse-proxy', 'web']);
		expect(tagProblem('web')).toBe('');
		expect(tagProblem('-web')).not.toBe('');
		expect(tagProblem('a'.repeat(33))).not.toBe('');
	});
});
