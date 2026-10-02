// Template list helpers (template registry): search, filters, tag counts
// for discovery, the suggested next version label, version labels and
// what a version runs (services, images, ports and .env names, read from
// its Compose files). Pure; tested in model.spec.ts.
import type { ListFilter } from '$lib/features/resources/filters';
import { routes } from '$lib/routes';
import type { Template, TemplateCatalogItem } from './queries';

/** Search texts of a template: name, description and tags. */
export const templateSearch = (t: Template) => [t.name, t.description, ...(t.tags ?? [])];

/** Tags with the number of templates carrying each, most used first. */
export function tagCounts(
	templates: readonly { tags?: string[] }[]
): { tag: string; count: number }[] {
	const counts = new Map<string, number>();
	for (const t of templates)
		for (const tag of t.tags ?? []) counts.set(tag, (counts.get(tag) ?? 0) + 1);
	return [...counts]
		.map(([tag, count]) => ({ tag, count }))
		.sort((a, b) => b.count - a.count || a.tag.localeCompare(b.tag));
}

/**
 * Whether a template has a published version ("Ready to Use") or only its
 * draft. Named "Status" so it is not confused with the visibility (Public,
 * Private). The stored ID stays "published".
 */
const READY_FILTER = {
	id: 'published',
	label: 'Status',
	all: 'All Statuses',
	options: [
		{ value: 'yes', label: 'Ready to Use' },
		{ value: 'no', label: 'Draft Only' }
	]
};

/** The filters of the template list: tag, visibility and status. */
export function templateFilters(templates: readonly Template[]): ListFilter<Template>[] {
	return [
		{
			id: 'tag',
			label: 'Tag',
			all: 'All Tags',
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
			all: 'Public and Private',
			options: [
				{ value: 'public', label: 'Public' },
				{ value: 'private', label: 'Private' }
			],
			match: (t, v) => t.visibility === v
		},
		{
			...READY_FILTER,
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

/** A Compose project name suggested for a stack from a template's name. */
export function projectNameFor(name: string): string {
	return name
		.toLowerCase()
		.normalize('NFKD')
		.replace(/[^a-z0-9_-]+/g, '-')
		.replace(/^[^a-z0-9]+/, '')
		.replace(/-+$/, '')
		.slice(0, 63);
}

/** Search texts of a catalog item. */
export const catalogSearch = (t: TemplateCatalogItem) => [
	t.name,
	t.description,
	t.registryName,
	...(t.tags ?? [])
];

/** The filters of the catalog: source (registry), tag and status. */
export function catalogFilters(
	items: readonly TemplateCatalogItem[]
): ListFilter<TemplateCatalogItem>[] {
	const registries = new Map<string, string>();
	for (const t of items) registries.set(t.instanceId, t.registryName);
	return [
		{
			id: 'registry',
			label: 'Source',
			all: 'All Sources',
			dynamic: true,
			options: [...registries]
				.map(([value, label]) => ({ value, label }))
				.sort((a, b) => a.label.localeCompare(b.label)),
			match: (t, v) => t.instanceId === v
		},
		{
			id: 'tag',
			label: 'Tag',
			all: 'All Tags',
			dynamic: true,
			options: tagCounts(items).map((t) => ({
				value: t.tag,
				label: `${t.tag} (${t.count})`
			})),
			match: (t, v) => (t.tags ?? []).includes(v)
		},
		{
			...READY_FILTER,
			match: (t, v) => (v === 'yes') === t.versions.length > 0
		}
	];
}

/** The page of a catalog item: own templates open their management pages. */
export function catalogHref(
	t: Pick<TemplateCatalogItem, 'own' | 'instanceId' | 'templateId'>
): string {
	return t.own
		? routes.template(t.templateId)
		: routes.remoteTemplate(t.instanceId, t.templateId);
}

/**
 * A version as a label ("Version 1.2.0"), or "Draft Only" before the first
 * one. Values in tables and selects stay the bare label ("1.2.0").
 */
export function versionTitle(label: string | undefined): string {
	return label ? `Version ${label}` : 'Draft Only';
}

/** The contents of a version in the file manager's words: "2 items". */
export function contentsSummary(entries: number): string {
	return `${entries} ${entries === 1 ? 'item' : 'items'}`;
}

/**
 * The Compose files of a definition (every file but .env) in the order
 * Compose reads them: the default file first, override files after.
 */
export function composeFiles<T extends { path: string }>(definition: readonly T[]): T[] {
	const override = (f: T) => (f.path.includes('override') ? 1 : 0);
	return definition.filter((f) => f.path !== '.env').sort((a, b) => override(a) - override(b));
}

/** One service of a template: its name, image (absent when built) and ports. */
export interface ServiceSummary {
	name: string;
	image?: string;
	/** Built from the template's own files (a build: section, no image). */
	built: boolean;
	ports: string[];
}

const isRecord = (v: unknown): v is Record<string, unknown> =>
	typeof v === 'object' && v !== null && !Array.isArray(v);

/** A Compose port entry as written: "8080:80", "53/udp", "127.0.0.1:8443:443". */
export function portText(p: unknown): string {
	if (typeof p === 'string') return p.trim();
	if (typeof p === 'number') return String(p);
	if (!isRecord(p) || p.target === undefined) return '';
	const host = p.host_ip ? `${p.host_ip}:` : '';
	const published = p.published !== undefined && p.published !== '' ? `${p.published}:` : '';
	const protocol = p.protocol && p.protocol !== 'tcp' ? `/${p.protocol}` : '';
	return `${host}${published}${p.target}${protocol}`;
}

/**
 * The services of a template's Compose files (parsed YAML documents, the
 * default file first, overrides after): later files override a service's
 * image and add ports, as Compose merges them. Sorted by name.
 */
export function composeServices(docs: readonly unknown[]): ServiceSummary[] {
	const byName = new Map<string, ServiceSummary>();
	for (const doc of docs) {
		if (!isRecord(doc) || !isRecord(doc.services)) continue;
		for (const [name, raw] of Object.entries(doc.services)) {
			const svc = isRecord(raw) ? raw : {};
			const cur: ServiceSummary = byName.get(name) ?? { name, built: false, ports: [] };
			if (typeof svc.image === 'string' && svc.image.trim()) cur.image = svc.image.trim();
			if (svc.build !== undefined && svc.build !== null) cur.built = true;
			if (Array.isArray(svc.ports))
				for (const p of svc.ports) {
					const text = portText(p);
					if (text && !cur.ports.includes(text)) cur.ports.push(text);
				}
			byName.set(name, cur);
		}
	}
	return [...byName.values()]
		.map((s) => ({ ...s, built: s.built && !s.image }))
		.sort((a, b) => a.name.localeCompare(b.name));
}

/** A setting of a .env file: its name and whether it has a value yet. */
export interface EnvKey {
	name: string;
	empty: boolean;
}

/**
 * The names in a .env file, in file order and unique; never the values
 * (only whether one is set, so people see what they must fill in).
 */
export function envKeys(text: string): EnvKey[] {
	const out = new Map<string, EnvKey>();
	for (const raw of text.split(/\r?\n/)) {
		const line = raw.trim();
		if (!line || line.startsWith('#')) continue;
		const m = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_.-]*)\s*(=(.*))?$/.exec(line);
		if (!m) continue;
		const value = (m[3] ?? '').trim().replace(/^(["'])(.*)\1$/, '$2');
		out.set(m[1], { name: m[1], empty: value === '' });
	}
	return [...out.values()];
}
