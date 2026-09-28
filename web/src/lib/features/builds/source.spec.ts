import { describe, expect, it } from 'vitest';
import type { BuildDefinition, ImageBuild } from '$lib/api/queries';
import { applyListFilters } from '$lib/features/resources/filters';
import { checkLabel, maskFingerprint } from '$lib/features/registries/model';
import { buildFilters, buildSearch, definitionFilters, definitionSearch } from './filters';
import {
	clockTime,
	emptyForm,
	formFromSource,
	isComplete,
	repoLabel,
	sourceOfBuild,
	toSource,
	validateSource
} from './source';

describe('Git build form (#33)', () => {
	const form = () => ({
		...emptyForm(),
		gitUrl: ' https://github.com/acme/app.git ',
		ref: 'main',
		tags: 'registry.example.com/acme/app:1.4\n\nghcr.io/acme/app:latest',
		buildArgs: 'NODE_VERSION=22\n# comment\nAPI_URL=http://api:8080'
	});

	it('builds the API source from the form', () => {
		expect(toSource(form())).toEqual({
			gitUrl: 'https://github.com/acme/app.git',
			ref: 'main',
			contextPath: undefined,
			dockerfile: undefined,
			target: undefined,
			buildArgs: { NODE_VERSION: '22', API_URL: 'http://api:8080' },
			tags: ['registry.example.com/acme/app:1.4', 'ghcr.io/acme/app:latest'],
			platform: undefined,
			noCache: undefined,
			pull: undefined,
			gitCredentialId: undefined
		});
		expect(isComplete(form())).toBe(true);
		expect(isComplete({ ...form(), tags: '' })).toBe(false);
		expect(validateSource(form())).toEqual({});
	});

	it('round-trips a saved definition', () => {
		const src = toSource({ ...form(), noCache: true, target: 'runtime' });
		expect(toSource(formFromSource(src))).toEqual(src);
		expect(formFromSource(undefined)).toEqual(emptyForm());
	});

	it('refuses SSH URLs, credentials in URLs, bad names, bad argument keys and escaping Dockerfiles', () => {
		expect(
			validateSource({ ...form(), gitUrl: 'git@github.com:acme/app.git' }).gitUrl
		).toContain('https://');
		expect(
			validateSource({ ...form(), gitUrl: 'https://user:tok@github.com/a/b' }).gitUrl
		).toContain('credentials');
		expect(validateSource({ ...form(), tags: 'Acme/App:1' }).tags).toContain('Acme/App:1');
		expect(validateSource({ ...form(), buildArgs: '1BAD=x' }).buildArgs).toContain('1BAD');
		expect(validateSource({ ...form(), buildArgs: 'novalue' }).buildArgs).toContain('Line 1');
		expect(validateSource({ ...form(), dockerfile: '../Dockerfile' }).dockerfile).toBeDefined();
		expect(validateSource({ ...form(), tags: 'localhost:5000/app:dev' })).toEqual({});
	});

	it('labels repositories without scheme and .git', () => {
		expect(repoLabel('https://github.com/silo/web.git')).toBe('github.com/silo/web');
	});
});

const build = (x: Partial<ImageBuild>): ImageBuild =>
	({
		id: 'b1',
		environmentId: 'e1',
		jobId: 'b1',
		gitUrl: 'https://github.com/silo/web.git',
		tags: ['silo/web:1'],
		noCache: false,
		pull: true,
		buildArgNames: [],
		registryIds: [],
		status: 'succeeded',
		createdAt: '2026-09-27T10:00:00Z',
		...x
	}) as ImageBuild;

describe('builds again and lists (#33)', () => {
	it('rebuilds from a build record; argument values are not kept', () => {
		expect(sourceOfBuild(build({ ref: 'main' }))).toEqual({
			source: {
				gitUrl: 'https://github.com/silo/web.git',
				ref: 'main',
				contextPath: undefined,
				dockerfile: undefined,
				target: undefined,
				buildArgs: undefined,
				tags: ['silo/web:1'],
				platform: undefined,
				noCache: undefined,
				pull: true,
				gitCredentialId: undefined
			},
			argsMissing: false
		});
		const again = sourceOfBuild(build({ buildArgNames: ['NODE_VERSION'] }));
		expect(again.argsMissing).toBe(true);
		expect(again.source.buildArgs).toEqual({ NODE_VERSION: '' });
	});

	it('times log lines to the second', () => {
		expect(clockTime('2026-09-27T16:54:03Z', 'UTC')).toBe('16:54:03');
		expect(clockTime('nope')).toBe('');
	});

	it('filters builds by result and environment, and searches them', () => {
		const rows = [
			build({ id: 'a', status: 'running' }),
			build({ id: 'b', status: 'interrupted', environmentId: 'e2' }),
			build({ id: 'c', tags: ['acme/api:2'], resolvedCommit: 'abc123' })
		];
		const envs = [
			{ id: 'e1', name: 'homelab' },
			{ id: 'e2', name: 'nas' }
		];
		const names = new Map(envs.map((e) => [e.id, e.name]));
		const run = (values: Record<string, string>, q = '') =>
			applyListFilters(
				rows,
				buildFilters({ envs }),
				{ q, values },
				buildSearch((id) => names.get(id))
			).map((b) => b.id);
		expect(run({ status: 'running' })).toEqual(['a']);
		expect(run({ status: 'failed' })).toEqual(['b']);
		expect(run({ environment: 'e2' })).toEqual(['b']);
		expect(run({}, 'acme/api')).toEqual(['c']);
		expect(run({}, 'abc123')).toEqual(['c']);
		expect(buildFilters({ envs: [] }).map((f) => f.id)).toEqual(['status']);
	});

	it('searches definitions and filters them by environment only with several', () => {
		const defs = [
			{
				id: 'd1',
				name: 'silo-web',
				environmentId: 'e1',
				source: { gitUrl: 'https://github.com/silo/web.git', tags: ['silo/web:1'] }
			},
			{ id: 'd2', name: 'api', environmentId: 'e2', description: 'Backend' }
		] as BuildDefinition[];
		expect(definitionFilters({ envs: [] })).toEqual([]);
		expect(
			applyListFilters(
				defs,
				definitionFilters({ envs: [] }),
				{ q: 'backend', values: {} },
				definitionSearch(() => undefined)
			).map((d) => d.id)
		).toEqual(['d2']);
		expect(
			applyListFilters(
				defs,
				definitionFilters({ envs: [] }),
				{ q: 'github.com/silo', values: {} },
				definitionSearch(() => undefined)
			).map((d) => d.id)
		).toEqual(['d1']);
	});
});

describe('credential display (#19)', () => {
	it('masks fingerprints and names check results', () => {
		expect(maskFingerprint('fp_3f2a9c0d1e4b5a67')).toBe('fp_3f2a…5a67');
		expect(maskFingerprint('short')).toBe('short');
		expect(maskFingerprint(undefined)).toBe('—');
		expect(checkLabel('rate_limited')).toBe('Rate limited');
		expect(checkLabel('something_new')).toBe('something new');
	});
});
