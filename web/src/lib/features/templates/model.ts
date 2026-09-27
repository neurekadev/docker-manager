// Template list helpers (template registry): search, filters, tag counts
// for discovery and the suggested next version label. Pure; tested in
// model.spec.ts.
import type { ListFilter } from '$lib/features/resources/filters';
import type { Template } from './queries';

/** Search texts of a template: name, description and tags. */
export const templateSearch = (t: Template) => [t.name, t.description, ...(t.tags ?? [])];

/** Tags with the number of templates carrying each, most used first. */
export function tagCounts(templates: readonly Template[]): { tag: string; count: number }[] {
	const counts = new Map<string, number>();
	for (const t of templates)
		for (const tag of t.tags ?? []) counts.set(tag, (counts.get(tag) ?? 0) + 1);
	return [...counts]
		.map(([tag, count]) => ({ tag, count }))
		.sort((a, b) => b.count - a.count || a.tag.localeCompare(b.tag));
}

/** The filters of the template list: tag, visibility and publication. */
export function templateFilters(templates: readonly Template[]): ListFilter<Template>[] {
	return [
		{
			id: 'tag',
			label: 'Tag',
			all: 'All tags',
			dynamic: true,
			options: tagCounts(templates).map((t) => ({
				value: t.tag,
				label: `${t.tag} (${t.count})`
			})),
			match: (t, v) => (t.tags ?? []).includes(v)
		},
		{
			id: 'visibility',
			label: 'Visibility',
			all: 'Public and private',
			options: [
				{ value: 'public', label: 'Public' },
				{ value: 'private', label: 'Private' }
			],
			match: (t, v) => t.visibility === v
		},
		{
			id: 'published',
			label: 'Published',
			all: 'Published or not',
			options: [
				{ value: 'yes', label: 'Has a version' },
				{ value: 'no', label: 'Draft only' }
			],
			match: (t, v) => (v === 'yes') === !!t.latest
		}
	];
}

/**
 * Suggests the label of the next version: the last label with its last
 * number raised (1.2.0 -> 1.2.1, v3 -> v4), or 1.0.0 for the first.
 */
export function nextVersionLabel(last: string | undefined): string {
	if (!last) return '1.0.0';
	const m = /^(.*?)(\d+)(\D*)$/.exec(last);
	if (!m) return `${last}.1`;
	const n = String(Number(m[2]) + 1).padStart(m[2].length, '0');
	return `${m[1]}${n}${m[3]}`;
}

/** Normalizes typed tags: lowercase, dashes for spaces, unique, sorted. */
export function parseTags(input: string): string[] {
	const out = new Set<string>();
	for (const raw of input.split(/[,\n]/)) {
		const t = raw.trim().toLowerCase().replace(/\s+/g, '-');
		if (t) out.add(t);
	}
	return [...out].sort();
}

/** Why a tag is refused ('' when valid). */
export function tagProblem(tag: string): string {
	if (tag.length > 32) return `${tag} is longer than 32 characters.`;
	if (!/^[a-z0-9][a-z0-9-]*$/.test(tag))
		return `${tag}: use lowercase letters, digits and dashes.`;
	return '';
}
