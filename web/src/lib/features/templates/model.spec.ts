import { describe, expect, it } from 'vitest';
import { applyListFilters, emptyFilterState } from '$lib/features/resources/filters';
import {
	catalogFilters,
	catalogHref,
	catalogSearch,
	composeFiles,
	composeServices,
	contentsSummary,
	envKeys,
	nextVersionLabel,
	parseTags,
	portText,
	projectNameFor,
	tagCounts,
	tagProblem,
	templateFilters,
	templateSearch,
	versionTitle
} from './model';
import type { Template, TemplateCatalogItem } from './queries';

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
		// "Status" (has a version or not) is not confused with the visibility.
		expect(defs.map((d) => d.label)).toEqual(['Tag', 'Visibility', 'Status']);
		expect(defs[2].options?.map((o) => o.label)).toEqual(['Ready to Use', 'Draft Only']);
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

	it('suggests project names', () => {
		expect(projectNameFor('Nextcloud AIO')).toBe('nextcloud-aio');
		expect(projectNameFor('  _Café & Bar!')).toBe('cafe-bar');
		expect(projectNameFor('x'.repeat(80))).toHaveLength(63);
	});

	it('filters the catalog by registry, tag and publication', () => {
		const item = (id: string, instanceId: string, extra: Partial<TemplateCatalogItem> = {}) =>
			({
				instanceId,
				registryName: instanceId === 'self' ? 'Home' : 'Friend',
				own: instanceId === 'self',
				templateId: id,
				name: `T ${id}`,
				tags: [],
				actions: [],
				versions: [],
				...extra
			}) as TemplateCatalogItem;
		const items = [
			item('a', 'self', { tags: ['web'] }),
			item('b', 'remote-1', {
				tags: ['web'],
				versions: [{ number: 1, label: '1', publishedAt: '', contentSize: 1 }]
			})
		];
		const defs = catalogFilters(items);
		const run = (values: Record<string, string>, q = '') =>
			applyListFilters(items, defs, { ...emptyFilterState(), q, values }, catalogSearch).map(
				(t) => t.templateId
			);
		expect(defs.map((d) => d.label)).toEqual(['Source', 'Tag', 'Status']);
		expect(defs[0].all).toBe('All Sources');
		expect(defs[0].options?.map((o) => o.label)).toEqual(['Friend', 'Home']);
		expect(run({ registry: 'remote-1' })).toEqual(['b']);
		expect(run({ published: 'no' })).toEqual(['a']);
		expect(run({}, 'friend')).toEqual(['b']);
		expect(catalogHref(items[0])).toBe('/templates/a');
		expect(catalogHref(items[1])).toBe('/templates/remote/remote-1/b');
	});

	it('labels versions one way', () => {
		expect(versionTitle('1.2.0')).toBe('Version 1.2.0');
		expect(versionTitle(undefined)).toBe('Draft Only');
	});

	it('counts contents like the Files tab', () => {
		expect(contentsSummary(2)).toBe('2 items');
		expect(contentsSummary(1)).toBe('1 item');
	});

	it('orders Compose files as Compose reads them and leaves out .env', () => {
		const def = [{ path: '.env' }, { path: 'compose.override.yaml' }, { path: 'compose.yaml' }];
		expect(composeFiles(def).map((f) => f.path)).toEqual([
			'compose.yaml',
			'compose.override.yaml'
		]);
	});

	it('writes ports as Compose does', () => {
		expect(portText('8080:80')).toBe('8080:80');
		expect(portText(53)).toBe('53');
		expect(portText({ target: 80, published: 8080 })).toBe('8080:80');
		expect(
			portText({ target: 53, published: '53', protocol: 'udp', host_ip: '127.0.0.1' })
		).toBe('127.0.0.1:53:53/udp');
		expect(portText({ target: 443, protocol: 'tcp' })).toBe('443');
		expect(portText({ published: 1 })).toBe('');
		expect(portText(null)).toBe('');
	});

	it('lists the services of Compose files, overrides merged', () => {
		const base = {
			services: {
				web: { image: 'nginx:1.27', ports: ['8080:80'] },
				app: { build: '.' },
				db: { image: 'postgres:16' },
				broken: null
			}
		};
		const override = { services: { web: { image: 'nginx:1.28', ports: ['8080:80', 8443] } } };
		expect(composeServices([base, override, 'not a document', null])).toEqual([
			{ name: 'app', built: true, ports: [] },
			{ name: 'broken', built: false, ports: [] },
			{ name: 'db', image: 'postgres:16', built: false, ports: [] },
			{ name: 'web', image: 'nginx:1.28', built: false, ports: ['8080:80', '8443'] }
		]);
		expect(composeServices([{ name: 'x' }])).toEqual([]);
	});

	it('reads the names of .env settings, never their values', () => {
		const keys = envKeys(
			[
				'# comment',
				'DB_PASSWORD=',
				'export TZ=Europe/Berlin',
				'EMPTY=""',
				'',
				'not a setting',
				'TZ=UTC',
				'FLAG',
				''
			].join('\n')
		);
		expect(keys).toEqual([
			{ name: 'DB_PASSWORD', empty: true },
			{ name: 'TZ', empty: false },
			{ name: 'EMPTY', empty: true },
			{ name: 'FLAG', empty: true }
		]);
		expect(JSON.stringify(keys)).not.toContain('Europe');
	});
});
